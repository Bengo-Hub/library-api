package circulation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/finepayment"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/modules/fines"
)

// feeFixture builds a tenant with an Adult tier (fee 20) and a Junior tier (fee 10), both
// with the late fee tied to the borrowing fee, plus a few copies to lend.
type feeFixture struct {
	t        *testing.T
	ctx      context.Context
	client   *ent.Client
	svc      *Service
	tenantID uuid.UUID
	adult    *ent.MemberTier
	junior   *ent.MemberTier
	branchID uuid.UUID
	bibID    uuid.UUID
}

func newFeeFixture(t *testing.T) *feeFixture {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("no Postgres available, skipping circulation integration test")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	ctx := context.Background()
	f := &feeFixture{t: t, ctx: ctx, client: client, svc: NewService(client, nil, zap.NewNop()), tenantID: uuid.New()}

	br := must(client.Branch.Create().SetTenantID(f.tenantID).SetName("Main").SetCode("B-" + short()).Save(ctx))
	f.branchID = br.ID
	f.adult = must(client.MemberTier.Create().SetTenantID(f.tenantID).SetName("Adult").SetIsDefault(true).
		SetMaxConcurrentLoans(2).SetLoanPeriodDays(14).Save(ctx))
	f.junior = must(client.MemberTier.Create().SetTenantID(f.tenantID).SetName("Junior").
		SetMaxConcurrentLoans(5).SetLoanPeriodDays(14).Save(ctx))
	for tier, fee := range map[*ent.MemberTier]string{f.adult: "20", f.junior: "10"} {
		must(client.CirculationRule.Create().SetTenantID(f.tenantID).SetTierID(tier.ID).
			SetRentalCharge(decimal.RequireFromString(fee)).SetLateFeeUsesRentalCharge(true).
			SetLoanPeriodDays(14).SetMaxRenewals(1).Save(ctx))
	}
	f.bibID = must(client.BibRecord.Create().SetTenantID(f.tenantID).SetTitle("Dune").Save(ctx)).ID
	return f
}

func (f *feeFixture) member(tier *ent.MemberTier) *ent.Member {
	return must(f.client.Member.Create().SetTenantID(f.tenantID).SetMembershipNo("M-" + short()).
		SetTierID(tier.ID).SetDisplayName("Reader").SetContactEmail("reader@example.test").Save(f.ctx))
}

func (f *feeFixture) copy() *ent.BookCopy {
	return must(f.client.BookCopy.Create().SetTenantID(f.tenantID).SetBibRecordID(f.bibID).
		SetBranchID(f.branchID).SetBarcode("BC-" + short()).Save(f.ctx))
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestBorrowingFeeCollectedAtCheckout(t *testing.T) {
	f := newFeeFixture(t)
	adult, junior := f.member(f.adult), f.member(f.junior)

	// The quote shows the fee before anything is charged.
	cp := f.copy()
	q, err := f.svc.QuoteCheckout(f.ctx, f.tenantID, adult.ID, cp.ID, false)
	if err != nil || !q.BorrowingFee.Equal(dec("20")) || !q.LateFeePerDay.Equal(dec("20")) {
		t.Fatalf("adult quote: %+v err=%v", q, err)
	}
	if q, _ := f.svc.QuoteCheckout(f.ctx, f.tenantID, junior.ID, cp.ID, false); !q.BorrowingFee.Equal(dec("10")) {
		t.Fatalf("junior fee should be 10, got %s", q.BorrowingFee)
	}
	if q, _ := f.svc.QuoteCheckout(f.ctx, f.tenantID, adult.ID, cp.ID, true); !q.BorrowingFee.IsZero() {
		t.Fatalf("in-house reading must not charge a fee, got %s", q.BorrowingFee)
	}
	if got := must(f.client.BookCopy.Get(f.ctx, cp.ID)); got.Status != "AVAILABLE" {
		t.Fatalf("a quote must not change the copy, status %s", got.Status)
	}

	// Collected at the desk: loan and a PAID fee in one step.
	out, err := f.svc.Checkout(f.ctx, f.tenantID, adult.ID, cp.ID, CheckoutOptions{
		StaffID: "staff-1", ClientRef: "ref-" + short(),
		Payment: &FeePayment{Method: finepayment.MethodMPESA, Reference: "QWE123"},
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if out.Fee == nil || out.Fee.Status != fine.StatusPAID || !out.Fee.Amount.Equal(dec("20")) || !out.Fee.AmountPaid.Equal(dec("20")) {
		t.Fatalf("expected a paid fee of 20, got %+v", out.Fee)
	}
	pays := must(f.client.FinePayment.Query().Where(finepayment.FineID(out.Fee.ID)).All(f.ctx))
	if len(pays) != 1 || pays[0].Method != finepayment.MethodMPESA || pays[0].Reference != "QWE123" || pays[0].ReceivedBy != "staff-1" {
		t.Fatalf("expected one M-Pesa ledger row, got %+v", pays)
	}

	// A replayed offline checkout is exactly-once: same loan, same fee, no second payment.
	ref := "offline-" + short()
	cp2 := f.copy()
	first := must(f.svc.Checkout(f.ctx, f.tenantID, junior.ID, cp2.ID, CheckoutOptions{ClientRef: ref, Payment: &FeePayment{Method: finepayment.MethodCASH}}))
	again := must(f.svc.Checkout(f.ctx, f.tenantID, junior.ID, cp2.ID, CheckoutOptions{ClientRef: ref, Payment: &FeePayment{Method: finepayment.MethodCASH}}))
	if first.Loan.ID != again.Loan.ID || again.Fee == nil || first.Fee.ID != again.Fee.ID || !again.Fee.Amount.Equal(dec("10")) {
		t.Fatalf("replay must return the original loan and fee")
	}
	if n := must(f.client.FinePayment.Query().Where(finepayment.FineID(first.Fee.ID)).Count(f.ctx)); n != 1 {
		t.Fatalf("replay recorded %d payments, want 1", n)
	}
}

func TestBorrowingFeeLeftUnpaidThenPaidInParts(t *testing.T) {
	f := newFeeFixture(t)
	m := f.member(f.adult)
	out := must(f.svc.Checkout(f.ctx, f.tenantID, m.ID, f.copy().ID, CheckoutOptions{StaffID: "staff-1"}))
	if out.Fee == nil || out.Fee.Status != fine.StatusUNPAID || !out.Fee.AmountPaid.IsZero() {
		t.Fatalf("an uncollected fee must stay unpaid: %+v", out.Fee)
	}
	owed := must(fines.Outstanding(f.ctx, f.client, f.tenantID, m.ID))
	if !owed.Equal(dec("20")) {
		t.Fatalf("outstanding = %s, want 20", owed)
	}

	pay := func(amount string, key string) (*fines.Applied, error) {
		tx := must(f.client.Tx(f.ctx))
		a, err := fines.ApplyPayment(f.ctx, tx, f.tenantID, out.Fee.ID, fines.PaymentInput{
			Amount: dec(amount), Method: finepayment.MethodCASH, ReceivedBy: "staff-2", IdempotencyKey: key,
		})
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		return a, tx.Commit()
	}
	a, err := pay("5", "k1")
	if err != nil || a.Fine.Status != fine.StatusPARTIAL || !a.Fine.AmountPaid.Equal(dec("5")) {
		t.Fatalf("part payment: %+v err=%v", a, err)
	}
	if r, err := pay("5", "k1"); err != nil || !r.Replayed || !r.Fine.AmountPaid.Equal(dec("5")) {
		t.Fatalf("same key must replay, got %+v err=%v", r, err)
	}
	if _, err := pay("100", "k2"); !errors.Is(err, fines.ErrOverpayment) {
		t.Fatalf("overpayment must be rejected, got %v", err)
	}
	if _, err := pay("-1", "k3"); !errors.Is(err, fines.ErrInvalidAmount) {
		t.Fatalf("negative payment must be rejected, got %v", err)
	}
	a, err = pay("15", "k4")
	if err != nil || a.Fine.Status != fine.StatusPAID || a.Fine.PaidAt == nil {
		t.Fatalf("final payment should settle the fee: %+v err=%v", a, err)
	}
	if _, err := pay("1", "k5"); !errors.Is(err, fines.ErrSettled) {
		t.Fatalf("a settled fee takes no more payments, got %v", err)
	}
	if owed := must(fines.Outstanding(f.ctx, f.client, f.tenantID, m.ID)); !owed.IsZero() {
		t.Fatalf("nothing should be owed now, got %s", owed)
	}
}

func TestLateFeeChargedAtTheBorrowingFeePerDay(t *testing.T) {
	f := newFeeFixture(t)
	adult, junior := f.member(f.adult), f.member(f.junior)

	for _, c := range []struct {
		m    *ent.Member
		want string
	}{{adult, "80"}, {junior, "40"}} {
		cp := f.copy()
		out := must(f.svc.Checkout(f.ctx, f.tenantID, c.m.ID, cp.ID, CheckoutOptions{}))
		// Three full days late counts as the fourth started day: 4 x the borrowing fee.
		must(f.client.Loan.UpdateOneID(out.Loan.ID).SetDueAt(time.Now().Add(-73 * time.Hour)).Save(f.ctx))
		res, err := f.svc.Return(f.ctx, f.tenantID, cp.ID, "staff-1")
		if err != nil || res.Fine == nil {
			t.Fatalf("return: fine=%v err=%v", res, err)
		}
		if !res.Fine.Amount.Equal(dec(c.want)) {
			t.Fatalf("late fee = %s, want %s", res.Fine.Amount, c.want)
		}
	}
}

func TestOverdueLoansStillCountAgainstTheLoanLimit(t *testing.T) {
	f := newFeeFixture(t)
	m := f.member(f.adult) // adult tier allows 2 loans
	a := must(f.svc.Checkout(f.ctx, f.tenantID, m.ID, f.copy().ID, CheckoutOptions{}))
	must(f.svc.Checkout(f.ctx, f.tenantID, m.ID, f.copy().ID, CheckoutOptions{}))
	// The hourly sweep flips late loans to OVERDUE; they are still out.
	must(f.client.Loan.UpdateOneID(a.Loan.ID).SetStatus(loan.StatusOVERDUE).Save(f.ctx))
	if _, err := f.svc.Checkout(f.ctx, f.tenantID, m.ID, f.copy().ID, CheckoutOptions{}); !errors.Is(err, ErrLoanLimit) {
		t.Fatalf("overdue loans must count toward the limit, got %v", err)
	}
}

func TestRuleResolutionFallsBackToTheTier(t *testing.T) {
	f := newFeeFixture(t)
	// A tier with no matching rule uses its own loan period, renewals and daily fine rate.
	tier := must(f.client.MemberTier.Create().SetTenantID(f.tenantID).SetName("Staff").
		SetLoanPeriodDays(30).SetMaxRenewals(4).SetDailyFineRate(dec("3")).Save(f.ctx))
	r := f.svc.ResolveRule(f.ctx, f.tenantID, f.branchID, tier, "")
	if r.LoanPeriodDays != 30 || r.MaxRenewals != 4 || !r.LateRate(tier.DailyFineRate).Equal(dec("3")) || !r.BorrowingFee().IsZero() {
		t.Fatalf("tier baseline wrong: %+v", r.CirculationRule)
	}
	// The branch-specific rule beats the tier-wide one.
	special := must(f.client.CirculationRule.Create().SetTenantID(f.tenantID).SetTierID(f.adult.ID).SetBranchID(f.branchID).
		SetRentalCharge(dec("50")).SetLoanPeriodDays(7).Save(f.ctx))
	if got := f.svc.ResolveRule(f.ctx, f.tenantID, f.branchID, f.adult, ""); got.ID != special.ID {
		t.Fatalf("most specific rule should win")
	}
}
