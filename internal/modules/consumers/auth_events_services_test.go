package consumers

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	entlibraryuser "github.com/bengobox/library-service/internal/ent/libraryuser"
)

// auth-api's services list decides whether the library keeps a member: a list without library
// removes this tenant's library user on updated and pin_set; another tenant's user and an event
// without the list are left alone.
func TestServicesListRemovesLibraryUser(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil || db.Ping() != nil {
		t.Skip("no Postgres available")
	}
	t.Cleanup(func() { _ = db.Close() })
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	ctx := context.Background()
	c := &AuthEventsConsumer{log: zap.NewNop(), orm: client}
	tenantA, tenantB := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = client.LibraryUser.Delete().Where(entlibraryuser.TenantIDIn(tenantA, tenantB)).Exec(context.Background())
	})
	seed := func(tenant uuid.UUID) string {
		id := uuid.NewString()
		client.LibraryUser.Create().SetTenantID(tenant).SetUserID(id).SetIsActive(true).ExecX(ctx)
		return id
	}
	exists := func(tenant uuid.UUID, user string) bool {
		return client.LibraryUser.Query().Where(entlibraryuser.TenantID(tenant), entlibraryuser.UserID(user)).ExistX(ctx)
	}
	event := func(kind string, tenant uuid.UUID, payload map[string]any) *sharedevents.Event {
		return sharedevents.NewEvent(kind, "auth.user", uuid.New(), tenant, payload)
	}

	cashier := seed(tenantA)
	if err := c.handleUserUpdated(ctx, event("updated", tenantA, map[string]any{"user_id": cashier, "services": []any{"pos"}})); err != nil {
		t.Fatal(err)
	}
	if exists(tenantA, cashier) {
		t.Fatal("a member whose services leave out library must be removed")
	}

	terminal := seed(tenantA)
	if err := c.handlePinSet(ctx, event("pin_set", tenantA, map[string]any{"user_id": terminal, "service": "library", "pin_hash": "x", "services": []any{}})); err != nil {
		t.Fatal(err)
	}
	if exists(tenantA, terminal) {
		t.Fatal("an empty services list removes the member on pin_set too")
	}

	elsewhere := seed(tenantB)
	if err := c.handleUserUpdated(ctx, event("updated", tenantA, map[string]any{"user_id": elsewhere, "services": []any{"pos"}})); err != nil {
		t.Fatal(err)
	}
	if !exists(tenantB, elsewhere) {
		t.Fatal("a revoke in one tenant must not touch another tenant's library user")
	}
	if handled, err := c.revoked(ctx, event("updated", tenantB, map[string]any{"user_id": elsewhere})); handled || err != nil {
		t.Fatalf("an event without services must not revoke: %v %v", handled, err)
	}
}
