package database

import (
	"context"
	"database/sql"
	"fmt"
)

// preMigrateStatements are targeted, idempotent DDL steps that must run before ent's
// Schema.Create. Schema.Create diffs the live database and never drops indexes, so an index the
// schema no longer declares would otherwise stay in production forever.
var preMigrateStatements = []string{
	// S2-9: role names used to be unique across all tenants. Names are now unique among rows
	// with no tenant (libraryrole_name_global) and per tenant (libraryrole_tenant_id_name).
	// While the old index stays, a second tenant cannot reuse a custom role name.
	`DROP INDEX IF EXISTS "libraryrole_name"`,
}

// PreMigrate runs the targeted DDL steps above on db. Safe to run on every start.
func PreMigrate(ctx context.Context, db *sql.DB) error {
	for _, stmt := range preMigrateStatements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("pre-migrate %q: %w", stmt, err)
		}
	}
	return nil
}
