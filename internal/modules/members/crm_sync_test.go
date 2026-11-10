package members

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/Bengo-Hub/shared-service-client/crm"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
)

func testDB(t *testing.T) (*ent.Client, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("no Postgres available, skipping member CRM integration test")
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { client.Close(); sqlDB.Close() })
	return client, sqlDB
}

// TestMemberSnapshotRefresh checks that crm.contact.updated rewrites the name, phone and email
// of members linked to that contact, in the event's tenant only.
func TestMemberSnapshotRefresh(t *testing.T) {
	db, sqlDB := testDB(t)
	ctx := context.Background()
	tenantID := uuid.New()
	otherTenant := uuid.New()
	contactID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/contacts/batch" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.TenantID != tenantID.String() {
			t.Errorf("batch asked for tenant %s", body.TenantID)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{
			"id": contactID.String(), "first_name": "Grace", "last_name": "Updated",
			"phone": "+254700111222", "email": "grace@example.test",
		}}})
	}))
	t.Cleanup(srv.Close)
	s := NewSyncer(db, sqlDB, crm.New(srv.URL, "test-key", nil), zap.NewNop())

	newMember := func(tenant uuid.UUID) *ent.Member {
		tier, err := db.MemberTier.Create().SetTenantID(tenant).SetName("Adult").Save(ctx)
		if err != nil {
			t.Fatalf("tier: %v", err)
		}
		m, err := db.Member.Create().SetTenantID(tenant).SetMembershipNo("M-" + uuid.NewString()[:8]).
			SetTierID(tier.ID).SetCrmContactID(contactID).SetDisplayName("Grace Old").
			SetContactPhone("+254700000000").SetContactEmail("old@example.test").Save(ctx)
		if err != nil {
			t.Fatalf("member: %v", err)
		}
		return m
	}
	mine := newMember(tenantID)
	theirs := newMember(otherTenant)

	evt := sharedevents.NewEvent("crm.contact.updated", "contact", contactID, tenantID, map[string]interface{}{
		"contact_id": contactID.String(), "fields": []interface{}{"phone", "email", "last_name"},
	})
	data, err := evt.ToJSON()
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	if err := s.HandleContactUpdated(ctx, data); err != nil {
		t.Fatalf("handle: %v", err)
	}

	got, err := db.Member.Get(ctx, mine.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.DisplayName != "Grace Updated" || got.ContactPhone != "+254700111222" || got.ContactEmail != "grace@example.test" {
		t.Fatalf("snapshot not refreshed: %q %q %q", got.DisplayName, got.ContactPhone, got.ContactEmail)
	}
	other, err := db.Member.Get(ctx, theirs.ID)
	if err != nil {
		t.Fatalf("reload other: %v", err)
	}
	if other.DisplayName != "Grace Old" || other.ContactPhone != "+254700000000" {
		t.Fatalf("a member in another tenant was changed: %q %q", other.DisplayName, other.ContactPhone)
	}
}

// linkedMember creates a member in tenant linked to contactID.
func linkedMember(t *testing.T, db *ent.Client, tenant, contactID uuid.UUID) *ent.Member {
	t.Helper()
	ctx := context.Background()
	tier, err := db.MemberTier.Create().SetTenantID(tenant).SetName("Adult").Save(ctx)
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	m, err := db.Member.Create().SetTenantID(tenant).SetMembershipNo("M-" + uuid.NewString()[:8]).
		SetTierID(tier.ID).SetCrmContactID(contactID).SetDisplayName("Dee Leted").
		SetContactPhone("+254700333444").Save(ctx)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	return m
}

func deletedEvent(t *testing.T, tenant, contactID uuid.UUID) []byte {
	t.Helper()
	data, err := sharedevents.NewEvent("crm.contact.deleted", "contact", contactID, tenant, map[string]interface{}{
		"contact_id": contactID.String(),
	}).ToJSON()
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	return data
}

// TestContactDeletedClearsLink checks that crm.contact.deleted unlinks the tenant's members,
// flags them and keeps the member rows.
func TestContactDeletedClearsLink(t *testing.T) {
	db, sqlDB := testDB(t)
	ctx := context.Background()
	tenantID, otherTenant, contactID := uuid.New(), uuid.New(), uuid.New()
	s := NewSyncer(db, sqlDB, nil, zap.NewNop())
	mine := linkedMember(t, db, tenantID, contactID)
	theirs := linkedMember(t, db, otherTenant, contactID)

	if err := s.HandleContactDeleted(ctx, deletedEvent(t, tenantID, contactID)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got, err := db.Member.Get(ctx, mine.ID)
	if err != nil {
		t.Fatalf("member must be kept: %v", err)
	}
	if got.CrmContactID != nil {
		t.Fatalf("crm_contact_id not cleared")
	}
	if _, ok := got.Metadata[MetaCRMUnlinkedAt]; !ok {
		t.Fatalf("metadata %v lacks %s", got.Metadata, MetaCRMUnlinkedAt)
	}
	if got.DisplayName != "Dee Leted" || got.ContactPhone != "+254700333444" {
		t.Fatalf("snapshot should be kept: %q %q", got.DisplayName, got.ContactPhone)
	}
	other, err := db.Member.Get(ctx, theirs.ID)
	if err != nil || other.CrmContactID == nil || *other.CrmContactID != contactID {
		t.Fatalf("a member in another tenant was unlinked: %v", err)
	}
}

// TestReconcilerSkipsDeleted checks that a member unlinked by crm.contact.deleted is not
// linked again, while an ordinary unlinked member of the same age is.
func TestReconcilerSkipsDeleted(t *testing.T) {
	db, sqlDB := testDB(t)
	ctx := context.Background()
	tenantID, contactID := uuid.New(), uuid.New()
	tag := uuid.NewString()[:8]
	deletedEmail := "deleted-" + tag + "@example.test"
	controlEmail := "control-" + tag + "@example.test"

	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		email := body["email"]
		if email != deletedEmail && email != controlEmail {
			// Other rows in the shared test database are refused, so they stay untouched.
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not part of this test"})
			return
		}
		mu.Lock()
		seen[email] = true
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": uuid.NewString(), "email": email})
	}))
	t.Cleanup(srv.Close)
	s := NewSyncer(db, sqlDB, crm.New(srv.URL, "test-key", nil), zap.NewNop())

	tier, err := db.MemberTier.Create().SetTenantID(tenantID).SetName("Adult").Save(ctx)
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	// Both rows are made the oldest so they fall inside the reconcile batch.
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	deleted, err := db.Member.Create().SetTenantID(tenantID).SetMembershipNo("D-" + tag).SetTierID(tier.ID).
		SetCreatedAt(old).SetCrmContactID(contactID).SetContactEmail(deletedEmail).Save(ctx)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	if _, err := db.Member.Create().SetTenantID(tenantID).SetMembershipNo("C-" + tag).SetTierID(tier.ID).
		SetCreatedAt(old).SetContactEmail(controlEmail).Save(ctx); err != nil {
		t.Fatalf("control member: %v", err)
	}
	if err := s.HandleContactDeleted(ctx, deletedEvent(t, tenantID, contactID)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	if _, err := s.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen[controlEmail] {
		t.Skip("reconcile did not reach the control member (lock held elsewhere or batch full)")
	}
	if seen[deletedEmail] {
		t.Fatalf("reconciler re-linked a member whose CRM contact was deleted")
	}
	got, err := db.Member.Get(ctx, deleted.ID)
	if err != nil || got.CrmContactID != nil {
		t.Fatalf("deleted member was linked again: %v", err)
	}
}
