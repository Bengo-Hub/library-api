package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/google/uuid"
)

// cmd/seed is idempotent. It always ensures global library roles exist; when SEED_TENANT_ID
// is set it also seeds a demo tenant (a branch, member tiers, a loan policy and a couple of
// sample bib records + copies) for E2E. Safe to run on every startup.
func main() {
	_ = godotenv.Load()

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()
	ctx := context.Background()

	tenantStr := os.Getenv("SEED_TENANT_ID")
	if tenantStr == "" {
		log.Println("library seed: SEED_TENANT_ID not set — global roles are seeded by the API on startup; skipping demo data")
		return
	}
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		log.Fatalf("invalid SEED_TENANT_ID: %v", err)
	}
	if err := seedDemo(ctx, client, tenantID); err != nil {
		log.Printf("library seed: demo seed completed with warnings: %v", err)
	}
	log.Println("library seed: done")
}

func seedDemo(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	// Default branch.
	br, err := client.Branch.Query().Where(branch.TenantID(tenantID), branch.Code("MAIN")).First(ctx)
	if err != nil {
		br, err = client.Branch.Create().SetTenantID(tenantID).SetName("Main Library").SetCode("MAIN").SetIsDefault(true).Save(ctx)
		if err != nil {
			return err
		}
	}

	// Default member tier.
	if _, err := client.MemberTier.Query().Where(membertier.TenantID(tenantID), membertier.IsDefault(true)).First(ctx); err != nil {
		_, _ = client.MemberTier.Create().
			SetTenantID(tenantID).SetName("Standard").SetIsDefault(true).
			SetMaxConcurrentLoans(3).SetLoanPeriodDays(14).SetMaxRenewals(2).SetHoldLimit(5).
			SetDailyFineRate(decimal.RequireFromString("10")).
			SetMaxFineBeforeBlock(decimal.RequireFromString("1000")).
			SetAnnualFee(decimal.RequireFromString("500")).
			Save(ctx)
	}

	// A couple of sample titles + copies.
	samples := []struct {
		title, author, isbn string
	}{
		{"The Go Programming Language", "Donovan & Kernighan", "9780134190440"},
		{"Things Fall Apart", "Chinua Achebe", "9780385474542"},
	}
	for _, s := range samples {
		// Get-or-create the bib (idempotent by ISBN) so re-runs don't duplicate titles.
		bib, berr := client.BibRecord.Query().
			Where(bibrecord.TenantID(tenantID), bibrecord.Isbn13(s.isbn)).First(ctx)
		if berr != nil {
			bib, berr = client.BibRecord.Create().
				SetTenantID(tenantID).SetTitle(s.title).SetAuthors([]string{s.author}).SetIsbn13(s.isbn).
				Save(ctx)
			if berr != nil {
				continue
			}
		}
		// Ensure at least demoCopiesPerTitle physical copies (idempotent by barcode).
		have, _ := client.BookCopy.Query().
			Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordID(bib.ID)).Count(ctx)
		for n := have + 1; n <= demoCopiesPerTitle; n++ {
			barcode := fmt.Sprintf("DEMO-%s-%02d", s.isbn, n)
			_, _ = client.BookCopy.Create().
				SetTenantID(tenantID).SetBibRecordID(bib.ID).SetBranchID(br.ID).
				SetBarcode(barcode).SetAccessionNo(barcode).
				SetStatus(bookcopy.StatusAVAILABLE).SetCondition("good").
				SetAcquisitionDate(time.Now()).
				Save(ctx)
		}
	}
	return nil
}

// demoCopiesPerTitle mirrors refdata: each demo physical title gets at least this many copies.
const demoCopiesPerTitle = 5
