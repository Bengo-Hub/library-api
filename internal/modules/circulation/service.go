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

	"github.com/redis/go-redis/v9"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/ent/recallrequest"
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
	db    *ent.Client
	cache *redis.Client
	log   *zap.Logger
}

// NewService builds the circulation service.
func NewService(db *ent.Client, cache *redis.Client, log *zap.Logger) *Service {
	return &Service{db: db, cache: cache, log: log}
}

// Errors surfaced to the handler (mapped to 4xx).
var (
	ErrMemberNotActive  = errors.New("member is not active")
	ErrMemberBlocked    = errors.New("member is blocked by outstanding fines")
	ErrLoanLimit        = errors.New("member has reached their concurrent-loan limit")
	ErrCopyUnavailable  = errors.New("copy is not available for checkout")
	ErrNoActiveLoan     = errors.New("no active loan for that copy")
	ErrLoanNotLostable  = errors.New("loan is not in a lostable state")
	ErrRenewLimit       = errors.New("renewal limit reached")
	ErrRenewHeld        = errors.New("cannot renew — another member is waiting")
	ErrRenewRecalled    = errors.New("cannot renew — this item has been recalled")
	ErrNoWaitingHolder  = errors.New("recall requires a waiter in the hold queue")
	ErrAlreadyRecalled  = errors.New("this loan has already been recalled")
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
	// Block checkout if this specific copy has a WAITING hold for a different member (item-level hold).
	if blocked := s.copyHeldForOther(ctx, tenantID, copyID, memberID); blocked {
		return nil, ErrCopyUnavailable
	}

	// Resolve the effective circulation rule for this (branch, tier, format) triple.
	bib, bibErr := s.db.BibRecord.Query().Where(bibrecord.IDEQ(c.BibRecordID)).Only(ctx)
	bibFormat := ""
	if bibErr == nil {
		bibFormat = string(bib.Format)
	}
	rule := s.ResolveRule(ctx, tenantID, c.BranchID, m.TierID, bibFormat)

	now := time.Now()
	due := now.Add(time.Duration(rule.LoanPeriodDays) * 24 * time.Hour)

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
	// Rental charge: create RENTAL fine immediately if the rule specifies a flat fee.
	if !inHouse && rule.RentalCharge.IsPositive() {
		_, _ = tx.Fine.Create().
			SetTenantID(tenantID).SetMemberID(memberID).SetLoanID(l.ID).
			SetReason(fine.ReasonRENTAL).SetAmount(rule.RentalCharge).
			SetDescription("Rental charge").SetAssessedAt(now).Save(ctx)
	}
	_ = events.Publish(ctx, tx.OutboxEvent, tenantID, l.ID.String(), events.EventLoanCreated, map[string]any{
		"loan_id": l.ID, "member_id": memberID, "copy_id": copyID, "due_at": due, "in_house": inHouse,
	})
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return l, nil
}

// MarkLost marks an active or overdue loan as LOST, sets the copy status to LOST, and
// creates a REPLACEMENT fine (replacement_cost + processing_fee from the effective rule).
func (s *Service) MarkLost(ctx context.Context, tenantID, loanID uuid.UUID, staffID string) error {
	l, err := s.db.Loan.Query().
		Where(loan.IDEQ(loanID), loan.TenantID(tenantID)).
		First(ctx)
	if ent.IsNotFound(err) {
		return ErrNoActiveLoan
	} else if err != nil {
		return err
	}
	if l.Status != loan.StatusACTIVE && l.Status != loan.StatusOVERDUE {
		return ErrLoanNotLostable
	}

	m, err := s.db.Member.Query().Where(member.IDEQ(l.MemberID)).Only(ctx)
	if err != nil {
		return err
	}
	c, err := s.db.BookCopy.Query().Where(bookcopy.IDEQ(l.CopyID), bookcopy.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return err
	}
	bib, bibErr := s.db.BibRecord.Query().Where(bibrecord.IDEQ(c.BibRecordID)).Only(ctx)
	bibFormat := ""
	if bibErr == nil {
		bibFormat = string(bib.Format)
	}
	rule := s.ResolveRule(ctx, tenantID, c.BranchID, m.TierID, bibFormat)

	now := time.Now()
	tx, txErr := s.db.Tx(ctx)
	if txErr != nil {
		return txErr
	}
	if _, err := tx.Loan.UpdateOneID(loanID).SetStatus(loan.StatusLOST).SetReturnedBy(staffID).Save(ctx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.BookCopy.UpdateOneID(l.CopyID).SetStatus(bookcopy.StatusLOST).Save(ctx); err != nil {
		_ = tx.Rollback()
		return err
	}

	replacementAmt := rule.ReplacementCost.Add(rule.ProcessingFee)
	if replacementAmt.IsPositive() {
		desc := fmt.Sprintf("Lost item — replacement cost + processing fee")
		if rule.CapFineAtReplacementPrice && rule.ReplacementCost.IsPositive() {
			if replacementAmt.GreaterThan(rule.ReplacementCost) {
				replacementAmt = rule.ReplacementCost
			}
		}
		_, _ = tx.Fine.Create().
			SetTenantID(tenantID).SetMemberID(l.MemberID).SetLoanID(loanID).
			SetReason(fine.ReasonREPLACEMENT).SetAmount(replacementAmt).
			SetDescription(desc).SetAssessedAt(now).Save(ctx)
	}

	_ = events.Publish(ctx, tx.OutboxEvent, tenantID, loanID.String(), events.EventLoanReturned, map[string]any{
		"loan_id": loanID, "member_id": l.MemberID, "copy_id": l.CopyID, "lost": true,
	})
	return tx.Commit()
}

// Return checks a copy back in, assessing an overdue fine and promoting the next hold.
func (s *Service) Return(ctx context.Context, tenantID, copyID uuid.UUID, staffID string) (*Result, error) {
	l, err := s.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.CopyID(copyID), loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE)).
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

	// Resolve rule for grace-period and fine-cap logic in assessOverdueFine.
	var rule *ResolvedRule
	if m, merr := s.db.Member.Query().Where(member.IDEQ(l.MemberID)).Only(ctx); merr == nil {
		bibFormat := ""
		if bib, berr := s.db.BibRecord.Query().Where(bibrecord.IDEQ(c.BibRecordID)).Only(ctx); berr == nil {
			bibFormat = string(bib.Format)
		}
		rule = s.ResolveRule(ctx, tenantID, c.BranchID, m.TierID, bibFormat)
	}
	if rule == nil {
		rule = DefaultRule()
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

	// Overdue fine (with grace period and cap from the resolved rule).
	if now.After(l.DueAt) {
		if f, ferr := s.assessOverdueFine(ctx, tx, tenantID, l, now, rule); ferr == nil && f != nil {
			res.Fine = f
		}
	}

	// Promote the next waiting hold — prefer item-level hold for this exact copy, then any bib-level hold.
	next, _ := tx.Hold.Query().
		Where(hold.TenantID(tenantID), hold.BibRecordID(c.BibRecordID), hold.StatusEQ(hold.StatusWAITING), hold.CopyIDEQ(c.ID)).
		Order(ent.Asc(hold.FieldQueuePosition), ent.Asc(hold.FieldPlacedAt)).First(ctx)
	if next == nil {
		next, _ = tx.Hold.Query().
			Where(hold.TenantID(tenantID), hold.BibRecordID(c.BibRecordID), hold.StatusEQ(hold.StatusWAITING), hold.CopyIDIsNil()).
			Order(ent.Asc(hold.FieldQueuePosition), ent.Asc(hold.FieldPlacedAt)).First(ctx)
	}
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
	// Block renewal if there is an active recall on this loan.
	recalled, _ := s.db.RecallRequest.Query().
		Where(recallrequest.TenantIDEQ(tenantID), recallrequest.LoanIDEQ(loanID), recallrequest.StatusEQ(recallrequest.StatusPENDING)).
		Exist(ctx)
	if recalled {
		return nil, ErrRenewRecalled
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

// Recall shortens an active loan's due date so a waiting hold requester gets the item sooner.
// New due date defaults to 7 days from now (or the current due date if already sooner).
// Publishes library.loan.recalled so the borrower gets notified to return early.
func (s *Service) Recall(ctx context.Context, tenantID, loanID uuid.UUID, requestedByMemberID uuid.UUID, holdID *uuid.UUID) error {
	l, err := s.db.Loan.Query().
		Where(loan.IDEQ(loanID), loan.TenantID(tenantID), loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE)).
		First(ctx)
	if ent.IsNotFound(err) {
		return ErrNoActiveLoan
	} else if err != nil {
		return err
	}
	// Ensure there's actually a waiter (either the specified hold or any WAITING hold on the bib copy).
	c, err := s.db.BookCopy.Query().Where(bookcopy.IDEQ(l.CopyID), bookcopy.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return err
	}
	waitCount, _ := s.db.Hold.Query().Where(hold.TenantID(tenantID), hold.BibRecordID(c.BibRecordID), hold.StatusEQ(hold.StatusWAITING)).Count(ctx)
	if waitCount == 0 {
		return ErrNoWaitingHolder
	}
	// Prevent duplicate active recall on the same loan.
	alreadyRecalled, _ := s.db.RecallRequest.Query().
		Where(recallrequest.TenantIDEQ(tenantID), recallrequest.LoanIDEQ(loanID), recallrequest.StatusEQ(recallrequest.StatusPENDING)).
		Exist(ctx)
	if alreadyRecalled {
		return ErrAlreadyRecalled
	}

	now := time.Now()
	newDue := now.Add(7 * 24 * time.Hour)
	if l.DueAt.Before(newDue) {
		newDue = l.DueAt // don't extend — only shorten
	}

	tx, txErr := s.db.Tx(ctx)
	if txErr != nil {
		return txErr
	}
	create := tx.RecallRequest.Create().
		SetTenantID(tenantID).SetLoanID(loanID).SetRequestedByMemberID(requestedByMemberID).
		SetNewDueAt(newDue).SetStatus(recallrequest.StatusPENDING)
	if holdID != nil {
		create = create.SetHoldID(*holdID)
	}
	rr, err := create.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Loan.UpdateOneID(loanID).SetDueAt(newDue).Save(ctx); err != nil {
		_ = tx.Rollback()
		return err
	}
	borrowerEmail, borrowerName := s.MemberContact(ctx, l.MemberID)
	_ = events.Publish(ctx, tx.OutboxEvent, tenantID, rr.ID.String(), events.EventLoanRecalled, map[string]any{
		"recall_id": rr.ID, "loan_id": loanID, "member_id": l.MemberID, "new_due_at": newDue,
		"email": borrowerEmail, "name": borrowerName,
	})
	return tx.Commit()
}

// assessOverdueFine creates an OVERDUE fine applying the rule's grace period and fine cap.
func (s *Service) assessOverdueFine(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, l *ent.Loan, now time.Time, rule *ResolvedRule) (*ent.Fine, error) {
	m, err := s.db.Member.Query().Where(member.IDEQ(l.MemberID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	tier, err := s.db.MemberTier.Query().Where(membertier.IDEQ(m.TierID)).Only(ctx)
	if err != nil {
		return nil, err
	}

	rawDays := int(now.Sub(l.DueAt).Hours()/24) + 1
	graceDays := rule.GraceDays
	days := rawDays - graceDays
	if days <= 0 {
		return nil, nil
	}

	// Use the rule's per-day rate if configured, otherwise fall back to the tier rate.
	dailyRate := rule.FinePerDay
	if dailyRate.IsZero() {
		dailyRate = tier.DailyFineRate
	}
	if dailyRate.IsZero() {
		return nil, nil
	}

	amount := dailyRate.Mul(decimal.NewFromInt(int64(days)))

	// Apply max_fine_cap from the rule.
	if !rule.MaxFineCap.IsZero() && amount.GreaterThan(rule.MaxFineCap) {
		amount = rule.MaxFineCap
	}
	// Also cap at replacement cost if the rule says so.
	if rule.CapFineAtReplacementPrice && !rule.ReplacementCost.IsZero() && amount.GreaterThan(rule.ReplacementCost) {
		amount = rule.ReplacementCost
	}

	desc := fmt.Sprintf("Overdue by %d day(s)", rawDays)
	if graceDays > 0 {
		desc = fmt.Sprintf("Overdue by %d day(s) (grace: %d day(s))", rawDays, graceDays)
	}
	f, err := tx.Fine.Create().
		SetTenantID(tenantID).SetMemberID(l.MemberID).SetLoanID(l.ID).
		SetReason(fine.ReasonOVERDUE).SetAmount(amount).
		SetDescription(desc).SetAssessedAt(now).Save(ctx)
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

// copyHeldForOther returns true when this specific copy has a WAITING item-level hold
// for a different member — preventing checkout by someone who isn't the designated waiter.
func (s *Service) copyHeldForOther(ctx context.Context, tenantID, copyID, memberID uuid.UUID) bool {
	h, err := s.db.Hold.Query().
		Where(hold.TenantID(tenantID), hold.CopyIDEQ(copyID), hold.StatusEQ(hold.StatusWAITING)).
		First(ctx)
	if err != nil || h == nil {
		return false
	}
	return h.MemberID != memberID
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
