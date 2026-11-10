package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/Bengo-Hub/shared-service-client/crm"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/member"
	membercrm "github.com/bengobox/library-service/internal/modules/members"
)

// memberTestDB opens the test Postgres or skips the test.
func memberTestDB(t *testing.T) (*ent.Client, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("no Postgres available, skipping member integration test")
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { client.Close(); sqlDB.Close() })
	return client, sqlDB
}

// fakeCRM records upsert calls and answers each with a new contact echoing the input.
type fakeCRM struct {
	mu      sync.Mutex
	upserts []map[string]string
}

func (f *fakeCRM) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/contacts/upsert" {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.upserts = append(f.upserts, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": uuid.NewString(), "first_name": body["first_name"], "last_name": body["last_name"],
			"phone": body["phone"], "email": body["email"],
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeCRM) calls() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.upserts...)
}

// memberRouter mounts the member routes behind the same tenant check as the real router, with
// fixed claims standing in for the auth middleware.
func memberRouter(h *MemberHandler, db *ent.Client, claims *authclient.Claims) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1/{tenant}/library", func(lib chi.Router) {
		lib.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(authclient.ContextWithClaims(req.Context(), claims)))
			})
		})
		lib.Use(TenantScope(db))
		lib.Post("/members", h.CreateMember)
		lib.Post("/members/import", h.ImportMembers)
	})
	return r
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// newTenant creates a local tenant row and a default tier for it.
func newTenant(t *testing.T, db *ent.Client) (uuid.UUID, string, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	slug := "t-" + id.String()[:8]
	if _, err := db.Tenant.Create().SetID(id).SetSlug(slug).SetName(slug).Save(ctx); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tier, err := db.MemberTier.Create().SetTenantID(id).SetName("Adult").SetIsDefault(true).Save(ctx)
	if err != nil {
		t.Fatalf("create tier: %v", err)
	}
	return id, slug, tier.ID
}

func TestPlatformOwnerCreatesInURLTenant(t *testing.T) {
	db, _ := memberTestDB(t)
	ctx := context.Background()
	ownTenant, ownSlug, _ := newTenant(t, db)
	target, targetSlug, targetTier := newTenant(t, db)

	h := NewMemberHandler(db, nil, zap.NewNop())
	claims := &authclient.Claims{TenantID: ownTenant.String(), TenantSlug: ownSlug, IsPlatformOwner: true}
	no := "M-" + uuid.NewString()[:8]
	rec := postJSON(t, memberRouter(h, db, claims), "/api/v1/"+targetSlug+"/library/members", map[string]any{
		"first_name": "Ann", "last_name": "Owner", "membership_no": no, "tier_id": targetTier.String(),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if n, _ := db.Member.Query().Where(member.TenantID(target), member.MembershipNo(no)).Count(ctx); n != 1 {
		t.Fatalf("member not created in the URL tenant (found %d)", n)
	}
	if n, _ := db.Member.Query().Where(member.TenantID(ownTenant), member.MembershipNo(no)).Count(ctx); n != 0 {
		t.Fatalf("member was created in the platform owner's own tenant")
	}
}

func TestTenantUserCannotNameOtherTenant(t *testing.T) {
	h := NewMemberHandler(nil, nil, zap.NewNop())
	claims := &authclient.Claims{TenantID: uuid.NewString(), TenantSlug: "alpha"}
	rec := postJSON(t, memberRouter(h, nil, claims), "/api/v1/beta/library/members", map[string]any{
		"first_name": "Mallory", "membership_no": "M-1",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestEmailOnlyMemberLinks(t *testing.T) {
	db, sqlDB := memberTestDB(t)
	ctx := context.Background()
	tenantID, slug, _ := newTenant(t, db)
	fake := &fakeCRM{}
	srv := fake.server(t)
	syncer := membercrm.NewSyncer(db, sqlDB, crm.New(srv.URL, "test-key", nil), zap.NewNop())
	h := NewMemberHandler(db, syncer, zap.NewNop())
	claims := &authclient.Claims{TenantID: tenantID.String(), TenantSlug: slug}

	no := "M-" + uuid.NewString()[:8]
	rec := postJSON(t, memberRouter(h, db, claims), "/api/v1/"+slug+"/library/members", map[string]any{
		"display_name": "Eve Email", "email": " Eve@Example.test ", "membership_no": no,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	calls := fake.calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 CRM upsert, got %d", len(calls))
	}
	if calls[0]["email"] != "eve@example.test" || calls[0]["phone"] != "" || calls[0]["tenant_id"] != tenantID.String() {
		t.Fatalf("upsert body %+v", calls[0])
	}
	m, err := db.Member.Query().Where(member.TenantID(tenantID), member.MembershipNo(no)).Only(ctx)
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	if m.CrmContactID == nil {
		t.Fatalf("email-only member was not linked to the CRM")
	}
	if m.ContactEmail != "eve@example.test" {
		t.Fatalf("snapshot email %q", m.ContactEmail)
	}
}

// failingCRM answers every call with status and a marketflow style error body.
func failingCRM(t *testing.T, status int, msg string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": "test_error"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// createWithCRM posts one member to a tenant whose CRM answers with status.
func createWithCRM(t *testing.T, status int, msg string) (*httptest.ResponseRecorder, *ent.Client, uuid.UUID, string) {
	t.Helper()
	db, sqlDB := memberTestDB(t)
	tenantID, slug, _ := newTenant(t, db)
	srv := failingCRM(t, status, msg)
	syncer := membercrm.NewSyncer(db, sqlDB, crm.New(srv.URL, "test-key", nil), zap.NewNop())
	h := NewMemberHandler(db, syncer, zap.NewNop())
	claims := &authclient.Claims{TenantID: tenantID.String(), TenantSlug: slug}
	no := "M-" + uuid.NewString()[:8]
	rec := postJSON(t, memberRouter(h, db, claims), "/api/v1/"+slug+"/library/members", map[string]any{
		"display_name": "Carl Crm", "phone": "0712345678", "email": "carl@example.test", "membership_no": no,
	})
	return rec, db, tenantID, no
}

func TestCreateCRMRefusalReturns400(t *testing.T) {
	rec, db, tenantID, no := createWithCRM(t, http.StatusBadRequest, "phone number is not valid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "phone number is not valid") || !strings.Contains(rec.Body.String(), "crm_rejected") {
		t.Fatalf("body should carry the CRM message and crm_rejected: %s", rec.Body.String())
	}
	if n, _ := db.Member.Query().Where(member.TenantID(tenantID), member.MembershipNo(no)).Count(context.Background()); n != 0 {
		t.Fatalf("a refused member must not be saved")
	}
}

// assertSavedUnlinked checks a 201 and a saved member without a CRM link.
func assertSavedUnlinked(t *testing.T, rec *httptest.ResponseRecorder, db *ent.Client, tenantID uuid.UUID, no string) {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body.String())
	}
	m, err := db.Member.Query().Where(member.TenantID(tenantID), member.MembershipNo(no)).Only(context.Background())
	if err != nil {
		t.Fatalf("member not saved: %v", err)
	}
	if m.CrmContactID != nil {
		t.Fatalf("member should be unlinked, got %s", m.CrmContactID)
	}
	if m.ContactPhone != "+254712345678" {
		t.Fatalf("phone snapshot %q not normalised", m.ContactPhone)
	}
}

func TestCreateCRMDownSavesUnlinked(t *testing.T) {
	rec, db, tenantID, no := createWithCRM(t, http.StatusServiceUnavailable, "maintenance")
	assertSavedUnlinked(t, rec, db, tenantID, no)
}

func TestCreateCRMAuthFailureSavesUnlinked(t *testing.T) {
	rec, db, tenantID, no := createWithCRM(t, http.StatusUnauthorized, "invalid api key")
	assertSavedUnlinked(t, rec, db, tenantID, no)
}

func TestImportLinksMembers(t *testing.T) {
	db, sqlDB := memberTestDB(t)
	ctx := context.Background()
	tenantID, slug, _ := newTenant(t, db)
	fake := &fakeCRM{}
	srv := fake.server(t)
	syncer := membercrm.NewSyncer(db, sqlDB, crm.New(srv.URL, "test-key", nil), zap.NewNop())
	h := NewMemberHandler(db, syncer, zap.NewNop())
	claims := &authclient.Claims{TenantID: tenantID.String(), TenantSlug: slug}

	suffix := uuid.NewString()[:6]
	csvBody := "display_name,email,phone,membership_no\n" +
		"Pat Phone,,0712 345 678,P1-" + suffix + "\n" +
		"Ella Both,ella@example.test,+254722000000,P2-" + suffix + "\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "members.csv")
	_, _ = fw.Write([]byte(csvBody))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/"+slug+"/library/members/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	memberRouter(h, db, claims).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var started struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &started)

	deadline := time.Now().Add(15 * time.Second)
	for {
		job, ok := loadImportJob(ctx, tenantID, started.JobID)
		if ok && job.Status == "done" {
			if job.Imported != 2 {
				t.Fatalf("imported %d, errors %+v", job.Imported, job.Errors)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("import did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}

	calls := fake.calls()
	if len(calls) != 2 {
		t.Fatalf("want 2 CRM upserts, got %d", len(calls))
	}
	if calls[0]["phone"] != "+254712345678" || calls[0]["email"] != "" {
		t.Fatalf("first upsert %+v", calls[0])
	}
	if calls[1]["phone"] != "+254722000000" || calls[1]["email"] != "ella@example.test" {
		t.Fatalf("second upsert must send phone and email: %+v", calls[1])
	}
	rows, err := db.Member.Query().Where(member.TenantID(tenantID), member.MembershipNoHasSuffix(suffix)).All(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("load members: %v (%d rows)", err, len(rows))
	}
	for _, m := range rows {
		if m.CrmContactID == nil {
			t.Fatalf("imported member %s not linked", m.MembershipNo)
		}
		if strings.HasPrefix(m.MembershipNo, "P1-") && m.ContactPhone != "+254712345678" {
			t.Fatalf("phone snapshot %q not normalised", m.ContactPhone)
		}
	}
}
