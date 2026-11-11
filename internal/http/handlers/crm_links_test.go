package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bengo-Hub/httpware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// marketflow asks, before erasing a CRM contact, whether the library still links it. A member
// carrying it answers yes whichever known tenant asks; no member answers no. {tenant} may
// be the UUID or the slug, and the route needs the service key.
func TestCRMLinksAnswersPerTenant(t *testing.T) {
	db, _ := memberTestDB(t)
	ctx := context.Background()
	tenantA, slugA, tierA := newTenant(t, db)
	tenantB, _, _ := newTenant(t, db)
	linked, unlinked := uuid.New(), uuid.New()
	if _, err := db.Member.Create().SetTenantID(tenantA).SetMembershipNo("M-" + uuid.NewString()[:8]).
		SetTierID(tierA).SetCrmContactID(linked).Save(ctx); err != nil {
		t.Fatal(err)
	}

	const key = "test-service-key"
	r := chi.NewRouter()
	r.With(httpware.RequireServiceKey(key)).
		Get("/api/v1/s2s/{tenant}/crm-links/{contactID}", NewCRMLinksHandler(db, zap.NewNop()).Referenced)

	ask := func(tenant, contact string, withKey bool) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/s2s/"+tenant+"/crm-links/"+contact, nil)
		if withKey {
			req.Header.Set("X-API-Key", key)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	if code, body := ask(tenantA.String(), linked.String(), true); code != http.StatusOK || body["referenced"] != true || len(body) != 1 {
		t.Fatalf("linked member by tenant id: %d %v", code, body)
	}
	if code, body := ask(slugA, linked.String(), true); code != http.StatusOK || body["referenced"] != true {
		t.Fatalf("linked member by slug: %d %v", code, body)
	}
	// Asked under another known tenant, the member under tenant A still counts: contact ids are
	// unique across tenants, and a tenant mismatch must only ever keep the contact.
	if code, body := ask(tenantB.String(), linked.String(), true); code != http.StatusOK || body["referenced"] != true {
		t.Fatalf("a link under another tenant must count: %d %v", code, body)
	}
	if code, body := ask(tenantA.String(), unlinked.String(), true); code != http.StatusOK || body["referenced"] != false {
		t.Fatalf("unlinked contact: %d %v", code, body)
	}
	if code, _ := ask(tenantA.String(), "not-a-uuid", true); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
	if code, _ := ask("no-such-tenant-"+uuid.NewString()[:8], linked.String(), true); code != http.StatusNotFound {
		t.Fatalf("unknown slug: %d", code)
	}
	// A tenant UUID the library does not know (sync drift) cannot vouch for anything.
	if code, _ := ask(uuid.NewString(), linked.String(), true); code != http.StatusNotFound {
		t.Fatalf("unknown tenant id: %d", code)
	}
	if code, _ := ask(tenantA.String(), linked.String(), false); code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", code)
	}
}
