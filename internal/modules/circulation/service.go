// Package circulation is the circulation rules engine: checkout (with the borrowing fee),
// return, renew, in-house reading, hold fulfillment on return, and overdue-fine accrual.
// Rules resolve branch + tier + format first, then fall back to the member tier's own
// loan period, renewal cap and daily fine rate (see rule_resolver.go).
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
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/finepayment"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/ent/recallrequest"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/calendar"
	"github.com/bengobox/library-service/internal/modules/fines"
)

// Result bundles the outcome of a return (loan + any assessed fine + promoted hold).
type Result struct {
	Loan        *ent.Loan `json:"loan"`
	Fine        *ent.Fine `json:"fine,omitempty"`
	PromotedHld *ent.Hold `json:"promoted_hold,omitempty"`
}

// FeePayment records how the librarian collected the borrowing fee at the desk. Leaving it
// out leaves the fee unpaid on the member's account.
type FeePayment struct {
	Method    finepayment.Method
	Reference string
}

// CheckoutOptions carries the optional parts of a checkout.
type CheckoutOptions struct {
	InHouse bool
	StaffID string
	// ClientRef is a client-generated idempotency key: a replayed offline checkout returns
	// the original loan instead of creating a second one.
	ClientRef string
	Payment   *FeePayment
}

// CheckoutOutcome is the loan plus the borrowing fee it generated (nil when the rule
// charges none, or for in-house reading).
type CheckoutOutcome struct {
	Loan *ent.Loan
	Fee  *ent.Fine
}

// Quote previews what a checkout would charge and when it falls due, so the desk can show
// the borrowing fee before the librarian confirms it was collected.
type Quote struct {
	BorrowingFee  decimal.Decimal
	LateFeePerDay decimal.Decimal
	GraceDays     int
	MaxFineCap    decimal.Decimal
	DueAt         time.Time
}

// Service implements the circulation workflows.
type Service struct {
	db       *ent.Client
	calendar *calendar.Calculator // nil = plain day counting
	log      *zap.Logger
}

// NewService builds the circulation service. calc applies each rule's due-date mode against
// branch opening hours and holidays; pass nil to count raw days.
func NewService(db *ent.Client, calc *calendar.Calculator, log *zap.Logger) *Service {
	return &Service{db: db, calendar: calc, log: log}
}

// dueDate is when a loan starting at from falls due under the rule: hourly rules add hours,
// everything else goes through the calendar so closed days and holidays are honoured.
func (s *Service) dueDate(ctx context.Context, tenantID, branchID uuid.UUID, from time.Time, rule *ResolvedRule) time.Time {
	if rule.IsHourly && rule.LoanPeriodHours > 0 {
		return from.Add(rule.LoanDuration())
	}
	if s.calendar == nil {
		return from.AddDate(0, 0, rule.LoanPeriodDays)
	}
	return s.calendar.CalculateDueDate(ctx, tenantID, branchID, from, rule.LoanPeriodDays, calendar.Mode(rule.DueDateMode))
}

// Errors surfaced to the handler (mapped to 4xx).
var (
	ErrMemberNotActive = errors.New("member is not active")
	ErrMemberBlocked   = errors.New("member is blocked by outstanding fines")
	ErrLoanLimit       = errors.New("member has reached their concurrent-loan limit")
	ErrCopyUnavailable = errors.New("copy is not available for checkout")
	ErrNoActiveLoan    = errors.New("no active loan for that copy")
	ErrLoanNotLostable = errors.New("loan is not in a lostable state")
	ErrRenewLimit      = errors.New("renewal limit reached")
	ErrRenewHeld       = errors.New("cannot renew — another member is waiting")
	ErrRenewRecalled   = errors.New("cannot renew — this item has been recalled")
	ErrNoWaitingHolder = errors.New("recall requires a waiter in the hold queue")
	ErrAlreadyRecalled = errors.New("this loan has already been recalled")
)

// checkoutContext is everything a checkout, quote or return needs about the member, the copy
// and the circulation rule that applies to the pair.
type checkoutContext struct {
	member *ent.Member
	tier   *ent.MemberTier
	copy   *ent.BookCopy
	rule   *ResolvedRule
}

// ruleFor resolves the rule for a member and copy, reading the title's format.
func (s *Service) ruleFor(ctx context.Context, tenantID uuid.UUID, tier *ent.MemberTier, c *ent.BookCopy) *ResolvedRule {
	format := ""
	if bib, err := s.db.BibRecord.Query().Where(bibrecord.IDEQ(c.BibRecordID)).Only(ctx); err == nil {
		format = string(bib.Format)
	}
	return s.ResolveRule(ctx, tenantID, c.BranchID, tier, format)
}

func (s *Service) loadCheckoutContext(ctx context.Context, tenantID, memberID, copyID uuid.UUID) (*checkoutContext, error) {
	m, err := s.db.Member.Query().Where(member.IDEQ(memberID), member.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("member: %w", err)
	}
	tier, err := s.db.MemberTier.Query().Where(membertier.IDEQ(m.TierID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("tier: %w", err)
	}
	c, err := s.db.BookCopy.Query().Where(bookcopy.IDEQ(copyID), bookcopy.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("copy: %w", err)
	}
	return &checkoutContext{member: m, tier: tier, copy: c, rule: s.ruleFor(ctx, tenantID, tier, c)}, nil
}

// QuoteCheckout previews the borrowing fee, late-fee terms and due date for lending a copy to
// a member. It is read-only and applies the same rule resolution as Checkout.
func (s *Service) QuoteCheckout(ctx context.Context, tenantID, memberID, copyID uuid.UUID, inHouse bool) (*Quote, error) {
	cc, err := s.loadCheckoutContext(ctx, tenantID, memberID, copyID)
	if err != nil {
		return nil, err
	}
	q := &Quote{DueAt: s.dueDate(ctx, tenantID, cc.copy.BranchID, time.Now(), cc.rule)}
	if !inHouse {
		q.BorrowingFee = cc.rule.BorrowingFee()
		q.LateFeePerDay = cc.rule.LateRate(cc.tier.DailyFineRate)
		q.GraceDays = cc.rule.GraceDays
		q.MaxFineCap = cc.rule.MaxFineCap
	}
	return q, nil
}

// Checkout lends a copy to a member. in_house marks a reference/reading-room session.
// A replayed ClientRef returns the original outcome (get-or-create) so an offline checkout
// is exactly-once. When the matching rule charges a borrowing fee, an unpaid RENTAL fine is
// raised in the same transaction as the loan; with opts.Payment it is settled immediately,
// so the librarian marks the fee paid while checking the book out.
func (s *Service) Checkout(ctx context.Context, tenantID, memberID, copyID uuid.UUID, opts CheckoutOptions) (*CheckoutOutcome, error) {
	inHouse := opts.InHouse
	if opts.ClientRef != "" {
		if existing, err := s.db.Loan.Query().
			Where(loan.TenantID(tenantID), loan.ClientReference(opts.ClientRef)).First(ctx); err == nil {
			fee, _ := s.db.Fine.Query().
				Where(fine.TenantID(tenantID), fine.LoanID(existing.ID), fine.ReasonEQ(fine.ReasonRENTAL)).First(ctx)
			return &CheckoutOutcome{Loan: existing, Fee: fee}, nil // idempotent replay
		}
	}
	cc, err := s.loadCheckoutContext(ctx, tenantID, memberID, copyID)
	if err != nil {
		return nil, err
	}
	m, tier, c, rule := cc.member, cc.tier, cc.copy, cc.rule
	if m.Status != member.StatusACTIVE {
		return nil, ErrMemberNotActive
	}
	// Fine-block check.
	if blocked, _ := s.fineBlocked(ctx, tenantID, memberID, tier); blocked {
		return nil, ErrMemberBlocked
	}
	// Loan-limit check (in-house sessions do not count against the take-home limit). Overdue
	// loans still count: the member has not returned them.
	if !inHouse {
		active, _ := s.db.Loan.Query().Where(loan.TenantID(tenantID), loan.MemberID(memberID),
			loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE), loan.InHouse(false)).Count(ctx)
		if active >= tier.MaxConcurrentLoans {
			return nil, ErrLoanLimit
		}
	}
	readyHold := s.readyHoldFor(ctx, tenantID, c.BibRecordID, memberID)
	if !copyLendable(c, readyHold) {
		return nil, ErrCopyUnavailable
	}
	// Block checkout if this specific copy has a WAITING hold for a different member (item-level hold).
	if blocked := s.copyHeldForOther(ctx, tenantID, copyID, memberID); blocked {
		return nil, ErrCopyUnavailable
	}

	now := time.Now()
	due := s.dueDate(ctx, tenantID, c.BranchID, now, rule)

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	// Lock the copy and re-check it inside the transaction: two desks (or a double click)
	// must not both lend the same copy, and must never raise two borrowing fees for it.
	locked, err := tx.BookCopy.Query().Where(bookcopy.IDEQ(copyID), bookcopy.TenantID(tenantID)).ForUpdate().Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if !copyLendable(locked, readyHold) {
		_ = tx.Rollback()
		return nil, ErrCopyUnavailable
	}
	newStatus := bookcopy.StatusON_LOAN
	if inHouse {
		newStatus = bookcopy.StatusIN_HOUSE
	}
	if _, err := tx.BookCopy.UpdateOneID(copyID).SetStatus(newStatus).Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	l, err := tx.Loan.Create().
		SetTenantID(tenantID).SetCopyID(copyID).SetMemberID(memberID).SetBranchID(c.BranchID).
		SetCheckoutAt(now).SetDueAt(due).SetInHouse(inHouse).SetCheckedOutBy(opts.StaffID).
		SetClientReference(opts.ClientRef).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if readyHold != nil {
		if _, err := tx.Hold.UpdateOneID(readyHold.ID).SetStatus(hold.StatusFULFILLED).Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}

	// Borrowing fee: charged per book when the rule sets one. It is part of the checkout
	// transaction, so a failure here cancels the loan rather than lending the book for free.
	var feeRow *ent.Fine
	if !inHouse && rule.BorrowingFee().IsPositive() {
		feeRow, err = tx.Fine.Create().
			SetTenantID(tenantID).SetMemberID(memberID).SetLoanID(l.ID).
			SetReason(fine.ReasonRENTAL).SetAmount(rule.BorrowingFee()).
			SetDescription("Borrowing fee").SetAssessedAt(now).Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if opts.Payment != nil {
			applied, perr := fines.ApplyPayment(ctx, tx, tenantID, feeRow.ID, fines.PaymentInput{
				Method: opts.Payment.Method, Reference: opts.Payment.Reference,
				ReceivedBy: opts.StaffID, Note: "Collected at checkout",
				IdempotencyKey: "checkout:" + l.ID.String(), At: now,
			})
			if perr != nil {
				_ = tx.Rollback()
				return nil, perr
			}
			feeRow = applied.Fine
		}
	}

	payload := map[string]any{
		"loan_id": l.ID, "member_id": memberID, "copy_id": copyID, "due_at": due, "in_house": inHouse,
	}
	if feeRow != nil {
		payload["fee_id"] = feeRow.ID
		payload["fee_amount"] = feeRow.Amount.StringFixed(2)
		payload["fee_paid"] = feeRow.Status == fine.StatusPAID
	}
	if err := events.Publish(ctx, tx.OutboxEvent, tenantID, l.ID.String(), events.EventLoanCreated, payload); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &CheckoutOutcome{Loan: l, Fee: feeRow}, nil
}

// copyLendable reports whether a copy can go out: available, or reserved for the member whose
// hold is READY.
func copyLendable(c *ent.BookCopy, readyHold *ent.Hold) bool {
	return c.Status == bookcopy.StatusAVAILABLE || (c.Status == bookcopy.StatusRESERVED && readyHold != nil)
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

	cc, err := s.loadCheckoutContext(ctx, tenantID, l.MemberID, l.CopyID)
	if err != nil {
		return err
	}
	rule := cc.rule

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
		desc := "Lost item: replacement cost and processing fee"
		if rule.CapFineAtReplacementPrice && rule.ReplacementCost.IsPositive() {
			if replacementAmt.GreaterThan(rule.ReplacementCost) {
				replacementAmt = rule.ReplacementCost
			}
		}
		if _, err := tx.Fine.Create().
			SetTenantID(tenantID).SetMemberID(l.MemberID).SetLoanID(loanID).
			SetReason(fine.ReasonREPLACEMENT).SetAmount(replacementAmt).
			SetDescription(desc).SetAssessedAt(now).Save(ctx); err != nil {
			_ = tx.Rollback()
			return err
		}
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

	// Rule for grace period, daily rate and fine caps in assessOverdueFine.
	var m *ent.Member
	var tier *ent.MemberTier
	var rule *ResolvedRule
	if mem, merr := s.db.Member.Query().Where(member.IDEQ(l.MemberID)).Only(ctx); merr == nil {
		m = mem
		if t, terr := s.db.MemberTier.Query().Where(membertier.IDEQ(m.TierID)).Only(ctx); terr == nil {
			tier = t
		}
		rule = s.ruleFor(ctx, tenantID, tier, c)
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
	if now.After(l.DueAt) && m != nil {
		f, ferr := s.assessOverdueFine(ctx, tx, tenantID, l, now, rule, m, tier)
		if ferr != nil {
			_ = tx.Rollback()
			return nil, ferr
		}
		res.Fine = f
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

// Renew extends an active loan, blocked by the renewal cap or a waiting hold. The renewal
// cap and the extension length come from the same rule that priced the original checkout.
func (s *Service) Renew(ctx context.Context, tenantID, loanID uuid.UUID) (*ent.Loan, error) {
	l, err := s.db.Loan.Query().Where(loan.IDEQ(loanID), loan.TenantID(tenantID), loan.StatusEQ(loan.StatusACTIVE)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNoActiveLoan
	} else if err != nil {
		return nil, err
	}
	cc, err := s.loadCheckoutContext(ctx, tenantID, l.MemberID, l.CopyID)
	if err != nil {
		return nil, err
	}
	if l.RenewalsCount >= cc.rule.MaxRenewals {
		return nil, ErrRenewLimit
	}
	waiting, _ := s.db.Hold.Query().Where(hold.TenantID(tenantID), hold.BibRecordID(cc.copy.BibRecordID), hold.StatusEQ(hold.StatusWAITING)).Count(ctx)
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
	newDue := s.dueDate(ctx, tenantID, cc.copy.BranchID, l.DueAt, cc.rule)
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
// Each late day is charged at the rule's late rate: its own fine_per_day, the borrowing fee
// when the rule says so, or the member tier's daily rate.
func (s *Service) assessOverdueFine(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, l *ent.Loan, now time.Time, rule *ResolvedRule, m *ent.Member, tier *ent.MemberTier) (*ent.Fine, error) {
	rawDays := int(now.Sub(l.DueAt).Hours()/24) + 1
	graceDays := rule.GraceDays
	days := rawDays - graceDays
	if days <= 0 {
		return nil, nil
	}

	tierRate := decimal.Zero
	if tier != nil {
		tierRate = tier.DailyFineRate
	}
	dailyRate := rule.LateRate(tierRate)
	if !dailyRate.IsPositive() {
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

	desc := fmt.Sprintf("Overdue by %d day(s) at %s per day", rawDays, dailyRate.StringFixed(2))
	if graceDays > 0 {
		desc = fmt.Sprintf("Overdue by %d day(s), %d day(s) grace, at %s per day", rawDays, graceDays, dailyRate.StringFixed(2))
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
// The balance is one SUM query, not every fine row.
func (s *Service) fineBlocked(ctx context.Context, tenantID, memberID uuid.UUID, tier *ent.MemberTier) (bool, error) {
	if tier.MaxFineBeforeBlock.IsZero() {
		return false, nil
	}
	outstanding, err := fines.Outstanding(ctx, s.db, tenantID, memberID)
	if err != nil {
		return false, err
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
