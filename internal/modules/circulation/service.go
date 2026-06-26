// Package circulation is the circulation rules engine: checkout, return, renew, in-house
// reading, hold fulfillment on return, and overdue-fine accrual. Loan/policy resolution
// precedence is copy → bib → tier → tenant default (tier is the implemented baseline).
package circulation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/events"
)

// Result bundles the outcome of a return (loan + any assessed fine + promoted hold).
type Result struct {
	Loan        *ent.Loan `json:"loan"`
	Fine        *ent.Fine `json:"fine,omitempty"`
	PromotedHld *ent.Hold `json:"promoted_hold,omitempty"`
}

// Service implements the circulation workflows.
type Service struct {
	db  *ent.Client
	log *zap.Logger
}

// NewService builds the circulation service.
func NewService(db *ent.Client, log *zap.Logger) *Service {
	return &Service{db: db, log: log}
}

// Errors surfaced to the handler (mapped to 4xx).
var (
	ErrMemberNotActive = errors.New("member is not active")
	ErrMemberBlocked   = errors.New("member is blocked by outstanding fines")
	ErrLoanLimit       = errors.New("member has reached their concurrent-loan limit")
	ErrCopyUnavailable = errors.New("copy is not available for checkout")
	ErrNoActiveLoan    = errors.New("no active loan for that copy")
	ErrRenewLimit      = errors.New("renewal limit reached")
	ErrRenewHeld       = errors.New("cannot renew — another member is waiting")
)

// Checkout lends a copy to a member. in_house marks a reference/reading-room session.
// clientRef is an optional client-generated idempotency key: if a loan already exists with
// it, the existing loan is returned (get-or-create) so a replayed offline checkout is
// exactly-once.
func (s *Service) Checkout(ctx context.Context, tenantID, memberID, copyID uuid.UUID, inHouse bool, staffID, clientRef string) (*ent.Loan, error) {
	if clientRef != "" {
		if existing, err := s.db.Loan.Query().
			Where(loan.TenantID(tenantID), loan.ClientReference(clientRef)).First(ctx); err == nil {
			return existing, nil // idempotent replay
		}
	}
	m, err := s.db.Member.Query().Where(member.IDEQ(memberID), member.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("member: %w", err)
	}
	if m.Status != member.StatusACTIVE {
		return nil, ErrMemberNotActive
	}
	tier, err := s.db.MemberTier.Query().Where(membertier.IDEQ(m.TierID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("tier: %w", err)
	}
	// Fine-block check.
	if blocked, _ := s.fineBlocked(ctx, tenantID, memberID, tier); blocked {
		return nil, ErrMemberBlocked
	}
	// Loan-limit check (in-house sessions do not count against the take-home limit).
	if !inHouse {
		active, _ := s.db.Loan.Query().Where(loan.TenantID(tenantID), loan.MemberID(memberID), loan.StatusEQ(loan.StatusACTIVE), loan.InHouse(false)).Count(ctx)
		if active >= tier.MaxConcurrentLoans {
			return nil, ErrLoanLimit
		}
	}
	c, err := s.db.BookCopy.Query().Where(bookcopy.IDEQ(copyID), bookcopy.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("copy: %w", err)
	}
	readyHold := s.readyHoldFor(ctx, tenantID, c.BibRecordID, memberID)
	if c.Status != bookcopy.StatusAVAILABLE && !(c.Status == bookcopy.StatusRESERVED && readyHold != nil) {
		return nil, ErrCopyUnavailable
	}
	if inHouse && c.IsReferenceOnly {
		// reference-only copies are valid for in-house, fine
	}

	now := time.Now()
	due := now.Add(time.Duration(tier.LoanPeriodDays) * 24 * time.Hour)

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	newStatus := bookcopy.StatusON_LOAN
	if inHouse {
		newStatus = bookcopy.StatusIN_HOUSE
	}
	if _, err := tx.BookCopy.UpdateOneID(copyID).SetStatus(newStatus).Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	lc := tx.Loan.Create().
		SetTenantID(tenantID).SetCopyID(copyID).SetMemberID(memberID).SetBranchID(c.BranchID).
		SetCheckoutAt(now).SetDueAt(due).SetInHouse(inHouse).SetCheckedOutBy(staffID).
		SetClientReference(clientRef)
	l, err := lc.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if readyHold != nil {
		_, _ = tx.Hold.UpdateOneID(readyHold.ID).SetStatus(hold.StatusFULFILLED).Save(ctx)
	}
	_ = events.Publish(ctx, tx.OutboxEvent, tenantID, l.ID.String(), events.EventLoanCreated, map[string]any{
		"loan_id": l.ID, "member_id": memberID, "copy_id": copyID, "due_at": due, "in_house": inHouse,
	})
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return l, nil
}

// Return checks a copy back in, assessing an overdue fine and promoting the next hold.
func (s *Service) Return(ctx context.Context, tenantID, copyID uuid.UUID, staffID string) (*Result, error) {
	l, err := s.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.CopyID(copyID), loan.StatusEQ(loan.StatusACTIVE)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNoActiveLoan
	} else if err != nil {
		return nil, err
	}
	c, err := s.db.BookCopy.Query().Where(bookcopy.IDEQ(copyID), bookcopy.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	updatedLoan, err := tx.Loan.UpdateOneID(l.ID).SetReturnedAt(now).SetStatus(loan.StatusRETURNED).SetReturnedBy(staffID).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	res := &Result{Loan: updatedLoan}

	// Overdue fine.
	if now.After(l.DueAt) {
		if f, ferr := s.assessOverdueFine(ctx, tx, tenantID, l, now); ferr == nil && f != nil {
			res.Fine = f
		}
	}

	// Promote the next waiting hold for this bib (else free the copy).
	next, _ := tx.Hold.Query().
		Where(hold.TenantID(tenantID), hold.BibRecordID(c.BibRecordID), hold.StatusEQ(hold.StatusWAITING)).
		Order(ent.Asc(hold.FieldQueuePosition), ent.Asc(hold.FieldPlacedAt)).First(ctx)
	if next != nil {
		expires := now.Add(48 * time.Hour)
		promoted, _ := tx.Hold.UpdateOneID(next.ID).SetStatus(hold.StatusREADY).SetCopyID(copyID).SetReadyAt(now).SetExpiresAt(expires).Save(ctx)
		_, _ = tx.BookCopy.UpdateOneID(copyID).SetStatus(bookcopy.StatusRESERVED).Save(ctx)
		res.PromotedHld = promoted
		hEmail, hName := s.MemberContact(ctx, next.MemberID)
		_ = events.Publish(ctx, tx.OutboxEvent, tenantID, next.ID.String(), events.EventHoldReady, map[string]any{
			"hold_id": next.ID, "member_id": next.MemberID, "bib_record_id": c.BibRecordID, "expires_at": expires,
			"email": hEmail, "name": hName,
		})
	} else {
		_, _ = tx.BookCopy.UpdateOneID(copyID).SetStatus(bookcopy.StatusAVAILABLE).Save(ctx)
	}

	_ = events.Publish(ctx, tx.OutboxEvent, tenantID, l.ID.String(), events.EventLoanReturned, map[string]any{
		"loan_id": l.ID, "member_id": l.MemberID, "copy_id": copyID,
	})
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// Renew extends an active loan, blocked by the renewal cap or a waiting hold.
func (s *Service) Renew(ctx context.Context, tenantID, loanID uuid.UUID) (*ent.Loan, error) {
	l, err := s.db.Loan.Query().Where(loan.IDEQ(loanID), loan.TenantID(tenantID), loan.StatusEQ(loan.StatusACTIVE)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNoActiveLoan
	} else if err != nil {
		return nil, err
	}
	m, err := s.db.Member.Query().Where(member.IDEQ(l.MemberID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	tier, err := s.db.MemberTier.Query().Where(membertier.IDEQ(m.TierID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if l.RenewalsCount >= tier.MaxRenewals {
		return nil, ErrRenewLimit
	}
	c, err := s.db.BookCopy.Query().Where(bookcopy.IDEQ(l.CopyID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	waiting, _ := s.db.Hold.Query().Where(hold.TenantID(tenantID), hold.BibRecordID(c.BibRecordID), hold.StatusEQ(hold.StatusWAITING)).Count(ctx)
	if waiting > 0 {
		return nil, ErrRenewHeld
	}
	newDue := l.DueAt.Add(time.Duration(tier.LoanPeriodDays) * 24 * time.Hour)
	updated, err := s.db.Loan.UpdateOneID(loanID).SetDueAt(newDue).AddRenewalsCount(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	_ = events.Publish(ctx, s.db.OutboxEvent, tenantID, loanID.String(), events.EventLoanRenewed, map[string]any{
		"loan_id": loanID, "new_due_at": newDue, "renewals": updated.RenewalsCount,
	})
	return updated, nil
}

// assessOverdueFine creates an OVERDUE fine = days_overdue × tier daily rate.
func (s *Service) assessOverdueFine(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, l *ent.Loan, now time.Time) (*ent.Fine, error) {
	m, err := s.db.Member.Query().Where(member.IDEQ(l.MemberID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	tier, err := s.db.MemberTier.Query().Where(membertier.IDEQ(m.TierID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	days := int(now.Sub(l.DueAt).Hours()/24) + 1
	if days <= 0 || tier.DailyFineRate.IsZero() {
		return nil, nil
	}
	amount := tier.DailyFineRate.Mul(decimal.NewFromInt(int64(days)))
	f, err := tx.Fine.Create().
		SetTenantID(tenantID).SetMemberID(l.MemberID).SetLoanID(l.ID).
		SetReason(fine.ReasonOVERDUE).SetAmount(amount).
		SetDescription(fmt.Sprintf("Overdue by %d day(s)", days)).
		SetAssessedAt(now).Save(ctx)
	if err != nil {
		return nil, err
	}
	_ = events.Publish(ctx, tx.OutboxEvent, tenantID, f.ID.String(), events.EventFineAssessed, map[string]any{
		"fine_id": f.ID, "member_id": l.MemberID, "amount": amount.String(), "reason": "OVERDUE",
		"email": m.ContactEmail, "name": m.DisplayName,
	})
	return f, nil
}

// fineBlocked reports whether the member's unpaid fines meet/exceed the tier block threshold.
func (s *Service) fineBlocked(ctx context.Context, tenantID, memberID uuid.UUID, tier *ent.MemberTier) (bool, error) {
	if tier.MaxFineBeforeBlock.IsZero() {
		return false, nil
	}
	rows, err := s.db.Fine.Query().
		Where(fine.TenantID(tenantID), fine.MemberID(memberID), fine.StatusIn(fine.StatusUNPAID, fine.StatusPARTIAL)).
		All(ctx)
	if err != nil {
		return false, err
	}
	outstanding := decimal.Zero
	for _, f := range rows {
		outstanding = outstanding.Add(f.Amount.Sub(f.AmountPaid))
	}
	return outstanding.GreaterThanOrEqual(tier.MaxFineBeforeBlock), nil
}

// readyHoldFor returns this member's READY hold on the bib, if any.
func (s *Service) readyHoldFor(ctx context.Context, tenantID, bibID, memberID uuid.UUID) *ent.Hold {
	h, _ := s.db.Hold.Query().
		Where(hold.TenantID(tenantID), hold.BibRecordID(bibID), hold.MemberID(memberID), hold.StatusEQ(hold.StatusREADY)).
		First(ctx)
	return h
}

// MemberContact returns a member's contact email + display name for notification routing
// (so patron-facing notices reach the member, not just the librarian).
func (s *Service) MemberContact(ctx context.Context, memberID uuid.UUID) (email, name string) {
	m, err := s.db.Member.Query().Where(member.IDEQ(memberID)).Only(ctx)
	if err != nil {
		return "", ""
	}
	return m.ContactEmail, m.DisplayName
}
