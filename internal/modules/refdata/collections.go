// Package refdata seeds shared reference data. Global library collections (Fiction, Reference,
// Children's…) are seeded once under the nil-UUID "global" tenant so every tenant sees them as
// defaults; a tenant can still create its own custom collections (its real tenant_id). This
// follows the "shared core reference data" rule while keeping collections joinable per tenant.
package refdata

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/collection"
)

// GlobalTenantID is the sentinel tenant_id for platform-wide shared collections (nil UUID).
// No real tenant uses it, so listing global+own is `tenant_id IN (caller, nil)`.
var GlobalTenantID = uuid.Nil

type defaultCollection struct {
	Name          string
	Code          string
	ReferenceOnly bool
}

// defaultCollections is the curated global default set shared across all tenants.
var defaultCollections = []defaultCollection{
	{"General Collection", "GEN", false},
	{"Fiction", "FIC", false},
	{"Non-Fiction", "NONFIC", false},
	{"Reference", "REF", true},
	{"Children's", "JUV", false},
	{"Young Adult", "YA", false},
	{"Biography & Memoir", "BIO", false},
	{"Textbooks & Academic", "TXT", false},
	{"Periodicals & Journals", "PER", false},
	{"Rare & Special Collections", "RARE", true},
	{"Audiovisual & Media", "AV", false},
	{"African & Local Studies", "AFR", false},
	{"Research", "RES", false},
}

// SeedGlobalCollections idempotently inserts the global default collections (by name under the
// nil tenant). Safe to run on every boot.
func SeedGlobalCollections(ctx context.Context, client *ent.Client, log *zap.Logger) error {
	for _, c := range defaultCollections {
		exists, err := client.Collection.Query().
			Where(collection.TenantID(GlobalTenantID), collection.Name(c.Name)).Exist(ctx)
		if err != nil {
			log.Warn("seed global collection check failed", zap.String("name", c.Name), zap.Error(err))
			continue
		}
		if exists {
			continue
		}
		if _, err := client.Collection.Create().
			SetTenantID(GlobalTenantID).
			SetName(c.Name).
			SetCode(c.Code).
			SetIsReferenceOnly(c.ReferenceOnly).
			Save(ctx); err != nil {
			log.Warn("seed global collection failed", zap.String("name", c.Name), zap.Error(err))
		}
	}
	return nil
}
