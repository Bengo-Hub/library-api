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
	{"LOC", "GEN", "General Stacks", "Main circulating collection", 1},
	{"LOC", "REF", "Reference", "Non-circulating reference material", 2},
	{"LOC", "CHI", "Children's Section", "Children's collection", 3},
	{"LOC", "JUV", "Young Adult", "Young adult section", 4},
	{"LOC", "PER", "Periodicals", "Magazines, journals, newspapers", 5},
	{"LOC", "AV", "AV / Media", "Audio-visual and digital media", 6},
	{"LOC", "RSRV", "Course Reserve", "Short-loan course reserve shelf", 7},
	{"LOC", "RARE", "Special Collections", "Rare and archival material", 8},

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

// SeedAuthorizedValues idempotently inserts system authorized values for the
// given tenant. Safe to call on every startup.
func SeedAuthorizedValues(ctx context.Context, db *ent.Client, tenantID uuid.UUID, log *zap.Logger) error {
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
