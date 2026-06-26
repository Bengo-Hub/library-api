package circulation

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/loan"
)

// TestCheckoutReturnFlow exercises the core circulation engine against a real Postgres.
// Skips when POSTGRES_URL is unset/unreachable so it never blocks an offline build.
func TestCheckoutReturnFlow(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("no Postgres available — skipping circulation integration test")
	}
	defer sqlDB.Close()
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	defer client.Close()
	ctx := context.Background()
	svc := NewService(client, zap.NewNop())

	tenantID := uuid.New()
	// Fixtures.
	br := must(client.Branch.Create().SetTenantID(tenantID).SetName("T").SetCode("T-"+short()).Save(ctx))
	tier := must(client.MemberTier.Create().SetTenantID(tenantID).SetName("Std").SetIsDefault(true).
		SetMaxConcurrentLoans(2).SetLoanPeriodDays(14).SetMaxRenewals(1).
		SetDailyFineRate(decimal.RequireFromString("10")).Save(ctx))
	mem := must(client.Member.Create().SetTenantID(tenantID).SetMembershipNo("M-"+short()).SetTierID(tier.ID).SetDisplayName("Jane").Save(ctx))
	bib := must(client.BibRecord.Create().SetTenantID(tenantID).SetTitle("Dune").Save(ctx))
	cp := must(client.BookCopy.Create().SetTenantID(tenantID).SetBibRecordID(bib.ID).SetBranchID(br.ID).SetBarcode("BC-"+short()).Save(ctx))

	// Checkout.
	l, err := svc.Checkout(ctx, tenantID, mem.ID, cp.ID, false, "staff-1", "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got, _ := client.BookCopy.Query().Where(bookcopy.IDEQ(cp.ID)).Only(ctx); got.Status != bookcopy.StatusON_LOAN {
		t.Fatalf("expected copy ON_LOAN, got %s", got.Status)
	}

	// Force overdue, then return → fine assessed.
	_, _ = client.Loan.UpdateOneID(l.ID).SetDueAt(time.Now().Add(-72 * time.Hour)).Save(ctx)
	res, err := svc.Return(ctx, tenantID, cp.ID, "staff-1")
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if res.Loan.Status != loan.StatusRETURNED {
		t.Fatalf("expected loan RETURNED, got %s", res.Loan.Status)
	}
	if res.Fine == nil {
		t.Fatalf("expected an overdue fine to be assessed")
	}
	if !res.Fine.Amount.GreaterThan(decimal.Zero) {
		t.Fatalf("expected positive fine, got %s", res.Fine.Amount)
	}
	if got, _ := client.BookCopy.Query().Where(bookcopy.IDEQ(cp.ID)).Only(ctx); got.Status != bookcopy.StatusAVAILABLE {
		t.Fatalf("expected copy AVAILABLE after return, got %s", got.Status)
	}

	t.Logf("OK: checkout→overdue→return assessed fine of %s", res.Fine.Amount)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func short() string { return uuid.New().String()[:8] }
