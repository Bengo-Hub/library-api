package refdata

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/tenant"
)

// demoCopiesPerTitle is the minimum physical copies each demo physical title should have.
const demoCopiesPerTitle = 5

// SeedDemoCopies idempotently tops every PHYSICAL demo title up to demoCopiesPerTitle available
// copies (so circulation/holdings demos have stock). E-book titles are skipped — they lend
// digitally, not via physical copies. No-op for non-demo tenants / before the demo bibs exist.
func SeedDemoCopies(ctx context.Context, client *ent.Client, log *zap.Logger) error {
	t, err := client.Tenant.Query().Where(tenant.Slug(DemoTenantSlug)).First(ctx)
	if err != nil {
		return nil
	}
	br, err := client.Branch.Query().Where(branch.TenantID(t.ID), branch.IsActive(true)).First(ctx)
	if err != nil {
		br, err = client.Branch.Create().
			SetTenantID(t.ID).SetName("Main Library").SetCode("HQ").SetIsDefault(true).SetIsActive(true).Save(ctx)
		if err != nil {
			return nil
		}
	}
	bibs, err := client.BibRecord.Query().Where(bibrecord.TenantID(t.ID)).All(ctx)
	if err != nil {
		return nil
	}
	created := 0
	for _, b := range bibs {
		if b.Format == bibrecord.FormatEBOOK {
			continue // digital titles have no physical copies
		}
		have, _ := client.BookCopy.Query().
			Where(bookcopy.TenantID(t.ID), bookcopy.BibRecordID(b.ID)).Count(ctx)
		for n := have + 1; n <= demoCopiesPerTitle; n++ {
			barcode := fmt.Sprintf("DEMO-%s-%02d", strings.ToUpper(b.ID.String()[:8]), n)
			if _, cerr := client.BookCopy.Create().
				SetTenantID(t.ID).SetBibRecordID(b.ID).SetBranchID(br.ID).
				SetBarcode(barcode).SetAccessionNo(barcode).
				SetStatus(bookcopy.StatusAVAILABLE).SetCondition("good").
				SetAcquisitionDate(time.Now()).Save(ctx); cerr != nil {
				log.Warn("demo seed: create copy failed", zap.String("barcode", barcode), zap.Error(cerr))
				continue
			}
			created++
		}
	}
	if created > 0 {
		log.Info("demo seed: topped up demo copies", zap.Int("created", created))
	}
	return nil
}
