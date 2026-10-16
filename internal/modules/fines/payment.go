// Package fines owns how money is received against a fine or fee. Every settlement path goes
// through ApplyPayment: the librarian recording cash at the desk, a borrowing fee collected
// during checkout, and the treasury payment consumer. That keeps fine.amount_paid, the status
// and the fine_payments ledger consistent no matter where the money came from.
package fines

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/finepayment"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/events"
)

// Errors surfaced to handlers.
var (
	ErrSettled       = errors.New("fine is already settled")
	ErrInvalidAmount = errors.New("payment amount must be greater than zero")
	ErrOverpayment   = errors.New("payment is more than the outstanding balance")
)

// PaymentInput describes one payment. A zero Amount settles the whole outstanding balance.
type PaymentInput struct {
	Amount         decimal.Decimal
	Method         finepayment.Method
	Reference      string
	Note           string
	ReceivedBy     string
	IdempotencyKey string
	At             time.Time
}

// Applied is the outcome of ApplyPayment.
type Applied struct {
	Fine     *ent.Fine
	Payment  *ent.FinePayment
	Replayed bool
}

// ParseMethod maps a request value to a payment method, defaulting to CASH.
func ParseMethod(s string) (finepayment.Method, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return finepayment.MethodCASH, true
	}
	m := finepayment.Method(s)
	if err := finepayment.MethodValidator(m); err != nil {
		return "", false
	}
	return m, true
}

// ApplyPayment records a payment against a fine inside tx. The fine row is locked for the
// duration of the transaction so two desks (or a desk and the treasury consumer) cannot both
// settle the same balance. When IdempotencyKey is set a repeat call returns the first result.
// A settled fine with an email on file also emits library.fine.paid for the receipt email.
func ApplyPayment(ctx context.Context, tx *ent.Tx, tenantID, fineID uuid.UUID, in PaymentInput) (*Applied, error) {
	if in.IdempotencyKey != "" {
		prev, err := tx.FinePayment.Query().
			Where(finepayment.TenantID(tenantID), finepayment.IdempotencyKey(in.IdempotencyKey)).
			Only(ctx)
		if err == nil {
			f, ferr := tx.Fine.Get(ctx, prev.FineID)
			if ferr != nil {
				return nil, ferr
			}
			return &Applied{Fine: f, Payment: prev, Replayed: true}, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}

	f, err := tx.Fine.Query().Where(fine.IDEQ(fineID), fine.TenantID(tenantID)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	if f.Status == fine.StatusPAID || f.Status == fine.StatusWAIVED {
		return nil, ErrSettled
	}

	outstanding := f.Amount.Sub(f.AmountPaid)
	amount := in.Amount
	switch {
	case amount.IsZero():
		amount = outstanding
	case amount.IsNegative():
		return nil, ErrInvalidAmount
	case amount.Round(2).GreaterThanOrEqual(outstanding.Round(2)):
		// Entered the balance as shown on screen (two decimals): settle it exactly.
		if amount.Round(2).GreaterThan(outstanding.Round(2)) {
			return nil, ErrOverpayment
		}
		amount = outstanding
	default:
		amount = amount.Round(2)
	}
	if !amount.IsPositive() {
		return nil, ErrInvalidAmount
	}

	method := in.Method
	if method == "" {
		method = finepayment.MethodCASH
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}

	pc := tx.FinePayment.Create().
		SetTenantID(tenantID).SetFineID(f.ID).SetMemberID(f.MemberID).
		SetAmount(amount).SetMethod(method).SetPaidAt(at).
		SetReference(in.Reference).SetNote(in.Note).SetReceivedBy(in.ReceivedBy)
	if in.IdempotencyKey != "" {
		pc.SetIdempotencyKey(in.IdempotencyKey)
	}
	pay, err := pc.Save(ctx)
	if err != nil {
		return nil, err
	}

	paid := f.AmountPaid.Add(amount)
	fu := tx.Fine.UpdateOneID(f.ID).SetAmountPaid(paid)
	settled := !paid.LessThan(f.Amount)
	if settled {
		fu.SetStatus(fine.StatusPAID).SetPaidAt(at)
	} else {
		fu.SetStatus(fine.StatusPARTIAL)
	}
	updated, err := fu.Save(ctx)
	if err != nil {
		return nil, err
	}

	if settled {
		if err := publishPaid(ctx, tx, updated, pay); err != nil {
			return nil, err
		}
	}
	return &Applied{Fine: updated, Payment: pay}, nil
}

// publishPaid emits library.fine.paid when the member has an email to receive the receipt.
// Walk-in members without an email would otherwise route the receipt to the librarian.
func publishPaid(ctx context.Context, tx *ent.Tx, f *ent.Fine, pay *ent.FinePayment) error {
	m, err := tx.Member.Query().Where(member.IDEQ(f.MemberID)).Only(ctx)
	if err != nil || m.ContactEmail == "" {
		return nil
	}
	return events.Publish(ctx, tx.OutboxEvent, f.TenantID, f.ID.String(), events.EventFinePaid, map[string]any{
		"fine_id":     f.ID,
		"member_id":   f.MemberID,
		"amount":      f.Amount.StringFixed(2),
		"method":      string(pay.Method),
		"reference":   pay.Reference,
		"reason":      string(f.Reason),
		"description": f.Description,
		"email":       m.ContactEmail,
		"name":        m.DisplayName,
	})
}

// outstandingExpr is sum(amount - amount_paid) aliased as "outstanding". It is written out
// instead of using ent.Sum so it can live next to other aggregates without the unaliased
// "sum" column name colliding.
func outstandingExpr(s *sql.Selector) string {
	return sql.As(fmt.Sprintf("COALESCE(SUM(%s - %s), 0)", s.C(fine.FieldAmount), s.C(fine.FieldAmountPaid)), "outstanding")
}

// Outstanding returns sum(amount - amount_paid) over the tenant's unpaid and partly paid
// fines, in one aggregate query. memberID narrows it to a single member when not Nil.
func Outstanding(ctx context.Context, db *ent.Client, tenantID, memberID uuid.UUID) (decimal.Decimal, error) {
	q := db.Fine.Query().Where(fine.TenantID(tenantID), fine.StatusIn(fine.StatusUNPAID, fine.StatusPARTIAL))
	if memberID != uuid.Nil {
		q = q.Where(fine.MemberID(memberID))
	}
	var rows []struct {
		Outstanding string `json:"outstanding"`
	}
	if err := q.Aggregate(outstandingExpr).Scan(ctx, &rows); err != nil || len(rows) == 0 {
		return decimal.Zero, err
	}
	return decimal.NewFromString(rows[0].Outstanding)
}

// OutstandingByMember returns each listed member's outstanding balance in one grouped query.
// Members with nothing owed are absent from the map.
func OutstandingByMember(ctx context.Context, db *ent.Client, tenantID uuid.UUID, memberIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	out := map[uuid.UUID]decimal.Decimal{}
	if len(memberIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		MemberID    uuid.UUID `json:"member_id"`
		Outstanding string    `json:"outstanding"`
	}
	err := db.Fine.Query().
		Where(fine.TenantID(tenantID), fine.MemberIDIn(memberIDs...), fine.StatusIn(fine.StatusUNPAID, fine.StatusPARTIAL)).
		GroupBy(fine.FieldMemberID).Aggregate(outstandingExpr).Scan(ctx, &rows)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		if d, derr := decimal.NewFromString(r.Outstanding); derr == nil {
			out[r.MemberID] = d
		}
	}
	return out, nil
}
