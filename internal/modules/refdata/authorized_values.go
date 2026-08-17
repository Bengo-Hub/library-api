package refdata

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/authorizedvalue"
)

type avSeed struct {
	Category    string
	Value       string
	Label       string
	Description string
	Order       int
}

// systemAVs is the set of standard authorized-value rows seeded for every tenant.
// These match Koha's standard categories and are marked is_system=true.
var systemAVs = []avSeed{
	// LOC — Shelving Locations
	{"LOC", "REF", "Reference", "Non-circulating reference material", 1},
	{"LOC", "JUV", "Junior", "Junior section", 2},
	{"LOC", "PER", "Periodicals", "Magazines, journals, newspapers", 3},
	{"LOC", "AV", "AV / Media", "Audio-visual and digital media", 4},
	{"LOC", "RSRV", "Course Reserve", "Short-loan course reserve shelf", 5},
	{"LOC", "RARE", "Special Collections", "Rare and archival material", 6},

	// CCODE — Collection Codes
	{"CCODE", "GEN", "General", "", 1},
	{"CCODE", "REF", "Reference", "", 2},
	{"CCODE", "CHI", "Children's", "", 3},
	{"CCODE", "JUV", "Young Adult", "", 4},
	{"CCODE", "PER", "Periodical", "", 5},
	{"CCODE", "AV", "Audiovisual", "", 6},

	// NOT_LOAN — Item Not-for-Loan Status
	{"NOT_LOAN", "0", "Available", "Normal circulating status", 1},
	{"NOT_LOAN", "1", "Not for loan", "Withdrawn from circulation temporarily", 2},
	{"NOT_LOAN", "2", "Staff collection only", "Restricted to staff use", 3},
	{"NOT_LOAN", "4", "Lost", "Item reported or confirmed lost", 4},
	{"NOT_LOAN", "5", "In processing", "Being processed/cataloged", 5},
	{"NOT_LOAN", "6", "In repair", "Sent for repair or binding", 6},

	// LOST — Item Lost Status
	{"LOST", "0", "Not lost", "", 1},
	{"LOST", "1", "Lost", "Declared lost", 2},
	{"LOST", "2", "Long overdue (lost)", "Not returned for extended period", 3},
	{"LOST", "3", "Lost and paid", "Lost fine collected", 4},

	// DAMAGED — Item Damage Status
	{"DAMAGED", "0", "Not damaged", "", 1},
	{"DAMAGED", "1", "Damaged", "Item has visible damage", 2},
	{"DAMAGED", "2", "Damaged — withdrawn", "Damaged beyond repair", 3},

	// PAYMENT_TYPE — Fine/Fee Payment Methods
	{"PAYMENT_TYPE", "CASH", "Cash", "", 1},
	{"PAYMENT_TYPE", "MPESA", "M-Pesa", "Mobile money", 2},
	{"PAYMENT_TYPE", "CARD", "Card", "Debit/credit card", 3},
	{"PAYMENT_TYPE", "WAIVER", "Waiver", "Administrative waiver", 4},
}

// legacyAVRelabels updates the label/description of system authorized values that were
// renamed in a later curation pass, keyed by category+value (the seed check below is keyed
// on value, not label, so a plain rename never reaches already-seeded tenants otherwise).
var legacyAVRelabels = []avSeed{
	{"LOC", "JUV", "Junior", "Junior section", 2},
}

// legacyAVRemovals deletes system authorized values dropped from the curated set (e.g.
// merged into another value or judged redundant), so already-seeded tenants don't keep
// stale shelving locations around.
var legacyAVRemovals = []struct{ Category, Value string }{
	{"LOC", "GEN"},
	{"LOC", "CHI"},
}

// SeedAuthorizedValues idempotently inserts system authorized values for the
// given tenant. Safe to call on every startup.
func SeedAuthorizedValues(ctx context.Context, db *ent.Client, tenantID uuid.UUID, log *zap.Logger) error {
	for _, av := range legacyAVRelabels {
		n, err := db.AuthorizedValue.Update().
			Where(authorizedvalue.TenantIDEQ(tenantID), authorizedvalue.CategoryEQ(av.Category), authorizedvalue.ValueEQ(av.Value)).
			SetLabel(av.Label).
			SetDescription(av.Description).
			Save(ctx)
		if err != nil {
			log.Warn("relabel legacy authorized value failed", zap.String("category", av.Category), zap.String("value", av.Value), zap.Error(err))
		} else if n > 0 {
			log.Info("relabeled legacy authorized value", zap.String("category", av.Category), zap.String("value", av.Value), zap.String("label", av.Label))
		}
	}

	for _, r := range legacyAVRemovals {
		n, err := db.AuthorizedValue.Delete().
			Where(authorizedvalue.TenantIDEQ(tenantID), authorizedvalue.CategoryEQ(r.Category), authorizedvalue.ValueEQ(r.Value)).
			Exec(ctx)
		if err != nil {
			log.Warn("remove legacy authorized value failed", zap.String("category", r.Category), zap.String("value", r.Value), zap.Error(err))
		} else if n > 0 {
			log.Info("removed legacy authorized value", zap.String("category", r.Category), zap.String("value", r.Value))
		}
	}

	for _, av := range systemAVs {
		exists, err := db.AuthorizedValue.Query().
			Where(
				authorizedvalue.TenantIDEQ(tenantID),
				authorizedvalue.CategoryEQ(av.Category),
				authorizedvalue.ValueEQ(av.Value),
			).Exist(ctx)
		if err != nil {
			log.Warn("authorized value seed check failed", zap.String("category", av.Category), zap.String("value", av.Value), zap.Error(err))
			continue
		}
		if exists {
			continue
		}
		_, err = db.AuthorizedValue.Create().
			SetTenantID(tenantID).
			SetCategory(av.Category).
			SetValue(av.Value).
			SetLabel(av.Label).
			SetDescription(av.Description).
			SetDisplayOrder(av.Order).
			SetIsSystem(true).
			SetIsActive(true).
			Save(ctx)
		if err != nil {
			log.Warn("authorized value seed failed", zap.String("category", av.Category), zap.String("value", av.Value), zap.Error(err))
		}
	}
	return nil
}
