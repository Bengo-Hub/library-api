// Command prune-users removes the library users created before auth.user events were gated by
// shared UserRelevance (2026-10-08). Back then every member of every tenant got a library user,
// so shop cashiers turned up as library staff. To stay clear of real libraries it only touches
// tenants that have never used the library (no branch and no member), and there only users with
// no PIN and no branch link. Anyone removed by mistake comes back on first sign-in.
//
// It reports counts per tenant and changes nothing unless run with --apply.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/ent/member"
	enttenant "github.com/bengobox/library-service/internal/ent/tenant"
)

func main() {
	apply := flag.Bool("apply", false, "delete the rows (default reports only)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn := os.Getenv("POSTGRES_MIGRATE_URL")
	if dsn == "" {
		dsn = os.Getenv("POSTGRES_URL")
	}
	if dsn == "" {
		log.Fatal("POSTGRES_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer client.Close()

	users, err := client.LibraryUser.Query().
		Where(libraryuser.PinHashIsNil()).
		Select(libraryuser.FieldID, libraryuser.FieldTenantID, libraryuser.FieldBranchIds).
		All(ctx)
	if err != nil {
		log.Fatalf("load users: %v", err)
	}

	// unused caches whether a tenant has never used the library.
	unused := map[uuid.UUID]bool{}
	isUnused := func(tid uuid.UUID) bool {
		if v, ok := unused[tid]; ok {
			return v
		}
		hasBranch, err := client.Branch.Query().Where(branch.TenantID(tid)).Exist(ctx)
		if err != nil {
			log.Fatalf("check branches: %v", err)
		}
		hasMember, err := client.Member.Query().Where(member.TenantID(tid)).Exist(ctx)
		if err != nil {
			log.Fatalf("check members: %v", err)
		}
		unused[tid] = !hasBranch && !hasMember
		return unused[tid]
	}

	byTenant := map[uuid.UUID]int{}
	var ids []uuid.UUID
	for _, u := range users {
		if len(u.BranchIds) > 0 || !isUnused(u.TenantID) {
			continue
		}
		byTenant[u.TenantID]++
		ids = append(ids, u.ID)
	}

	for tid, n := range byTenant {
		slug := "?"
		if t, err := client.Tenant.Query().Where(enttenant.ID(tid)).Only(ctx); err == nil {
			slug = t.Slug
		}
		log.Printf("tenant %-28s %s  removable: %d", slug, tid, n)
	}
	total, _ := client.LibraryUser.Query().Count(ctx)
	log.Printf("library users: %d total, %d removable, %d kept", total, len(ids), total-len(ids))

	if !*apply || len(ids) == 0 {
		log.Printf("dry run: nothing changed (pass --apply to delete)")
		return
	}
	n, err := client.LibraryUser.Delete().Where(libraryuser.IDIn(ids...)).Exec(ctx)
	if err != nil {
		log.Fatalf("delete: %v", err)
	}
	log.Printf("deleted %d library users", n)
}
