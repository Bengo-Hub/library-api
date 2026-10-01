package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/migrate"
)

// migrationLockKey serializes migrations across pods: every replica runs this binary on start
// and concurrent schema runs can race on the same DDL. Stable and unique per service
// ("LIBM", library migrate).
const migrationLockKey int64 = 0x4C49_424D

// cmd/migrate applies the embedded Atlas versioned migrations. Uses POSTGRES_MIGRATE_URL
// (direct, bypassing pgbouncer) when set, else POSTGRES_URL.
func main() {
	_ = godotenv.Load()

	dsn := os.Getenv("POSTGRES_MIGRATE_URL")
	if dsn == "" {
		dsn = os.Getenv("POSTGRES_URL")
	}
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("db ping: %v", err)
	}
	// One connection: the advisory lock and every migration statement share one session.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if _, err := sqlDB.Exec("SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		log.Fatalf("acquire migration lock: %v", err)
	}

	drv := entsql.OpenDB(dialect.Postgres, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	ctx := context.Background()
	migrateErr := client.Schema.Create(ctx, schema.WithDir(migrate.Dir))
	if _, err := sqlDB.Exec("SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
		log.Printf("release migration lock: %v", err)
	}
	if migrateErr != nil {
		log.Fatalf("schema create: %v", migrateErr)
	}

	fmt.Println("migrations completed successfully")
}
