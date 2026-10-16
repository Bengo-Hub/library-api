package handlers

import (
	"context"
	"database/sql"
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
	"github.com/bengobox/library-service/internal/ent/finepayment"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/modules/fines"
)

// TestLoanAndFeeAnalytics runs the grouped dashboard queries against a real Postgres: loans
// that the sweep already flipped to OVERDUE still count, money owed nets part payments, and
// today's collections come from the payment ledger. Skips when no database is reachable.
func TestLoanAndFeeAnalytics(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("no Postgres available, skipping analytics integration test")
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { client.Close(); sqlDB.Close() })
	ctx := context.Background()
	h := NewReportsHandler(client, nil, zap.NewNop())

	tenantID := uuid.New()
	br, err := client.Branch.Create().SetTenantID(tenantID).SetName("Main").SetCode("B-" + uuid.NewString()[:8]).Save(ctx)
	must1(t, err)
	tier, err := client.MemberTier.Create().SetTenantID(tenantID).SetName("Adult").Save(ctx)
	must1(t, err)
	mem, err := client.Member.Create().SetTenantID(tenantID).SetMembershipNo("M-" + uuid.NewString()[:8]).SetTierID(tier.ID).SetDisplayName("Reader").Save(ctx)
	must1(t, err)
	bib, err := client.BibRecord.Create().SetTenantID(tenantID).SetTitle("Dune").Save(ctx)
	must1(t, err)
	cp, err := client.BookCopy.Create().SetTenantID(tenantID).SetBibRecordID(bib.ID).SetBranchID(br.ID).SetBarcode("BC-" + uuid.NewString()[:8]).Save(ctx)
	must1(t, err)

	now := time.Now()
	// One loan the sweep flipped to OVERDUE, one still ACTIVE and not yet due.
	_, err = client.Loan.Create().SetTenantID(tenantID).SetCopyID(cp.ID).SetMemberID(mem.ID).SetBranchID(br.ID).
		SetCheckoutAt(now.Add(-20 * 24 * time.Hour)).SetDueAt(now.Add(-6 * 24 * time.Hour)).SetStatus(loan.StatusOVERDUE).Save(ctx)
	must1(t, err)
	_, err = client.Loan.Create().SetTenantID(tenantID).SetCopyID(cp.ID).SetMemberID(mem.ID).SetBranchID(br.ID).
		SetCheckoutAt(now).SetDueAt(now.Add(14 * 24 * time.Hour)).Save(ctx)
	must1(t, err)

	// A 20 fee with 5 paid, and a settled 10 fee.
	f1, err := client.Fine.Create().SetTenantID(tenantID).SetMemberID(mem.ID).SetAmount(decimal.RequireFromString("20")).Save(ctx)
	must1(t, err)
	tx, err := client.Tx(ctx)
	must1(t, err)
	_, err = fines.ApplyPayment(ctx, tx, tenantID, f1.ID, fines.PaymentInput{Amount: decimal.RequireFromString("5"), Method: finepayment.MethodCASH})
	must1(t, err)
	must1(t, tx.Commit())

	sum := h.buildSummary(ctx, tenantID)
	if sum["active_loans"] != 2 || sum["overdue_loans"] != 1 {
		t.Fatalf("active=%v overdue=%v, want 2 and 1", sum["active_loans"], sum["overdue_loans"])
	}
	if sum["outstanding_fines"] != 15.0 || sum["collected_today"] != 5.0 {
		t.Fatalf("outstanding=%v collected=%v, want 15 and 5", sum["outstanding_fines"], sum["collected_today"])
	}
	owed, err := fines.OutstandingByMember(ctx, client, tenantID, []uuid.UUID{mem.ID})
	must1(t, err)
	if !owed[mem.ID].Equal(decimal.RequireFromString("15")) {
		t.Fatalf("member owes %s, want 15", owed[mem.ID])
	}

	titles := h.popularTitles(ctx, tenantID, now.AddDate(0, 0, -30), 5)
	if len(titles) != 1 || titles[0].Title != "Dune" || titles[0].Loans != 2 {
		t.Fatalf("popular titles in the 30 day window: %+v", titles)
	}
	trend := h.circulationTrend(ctx, tenantID, 14)
	if len(trend) != 14 || trend[13].Checkouts != 1 {
		t.Fatalf("trend should end with today's checkout: %+v", trend)
	}
}

func must1(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
