package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
)

// testCatalogHandler opens a real Postgres connection (same convention as
// internal/modules/circulation/service_test.go) and skips when none is available, so this
// never blocks an offline build.
func testCatalogHandler(t *testing.T) (*CatalogHandler, *ent.Client) {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/library?sslmode=disable"
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("no Postgres available — skipping catalog duplicate-detection integration test")
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { client.Close(); sqlDB.Close() })
	return NewCatalogHandler(client, nil, "", nil, zap.NewNop()), client
}

// TestFindDuplicateBibs exercises the exact bug reported: the catalog silently allowed the same
// ISBN and the same exact title to be catalogued twice. Runs against a real Postgres so the
// ent predicates (fold-case title match, tenant scoping, self-exclusion) are verified for real,
// not just compiled.
func TestFindDuplicateBibs(t *testing.T) {
	h, client := testCatalogHandler(t)
	ctx := context.Background()
	tenantID := uuid.New()
	otherTenantID := uuid.New()

	seed, err := client.BibRecord.Create().
		SetTenantID(tenantID).
		SetTitle("Things Fall Apart").
		SetIsbn13("9780000000001").
		Save(ctx)
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	t.Run("exact ISBN match is found", func(t *testing.T) {
		isbnMatches, _ := h.findDuplicateBibs(ctx, tenantID, "9780000000001", "", "", nil)
		if len(isbnMatches) != 1 || isbnMatches[0].ID != seed.ID {
			t.Fatalf("expected 1 isbn match on seed, got %d", len(isbnMatches))
		}
	})

	t.Run("case/whitespace-insensitive title match is found", func(t *testing.T) {
		_, titleMatches := h.findDuplicateBibs(ctx, tenantID, "", "", "  things FALL apart  ", nil)
		if len(titleMatches) != 1 || titleMatches[0].ID != seed.ID {
			t.Fatalf("expected 1 title match on seed, got %d", len(titleMatches))
		}
	})

	t.Run("editing the same record excludes itself", func(t *testing.T) {
		isbnMatches, titleMatches := h.findDuplicateBibs(ctx, tenantID, "9780000000001", "", "Things Fall Apart", &seed.ID)
		if len(isbnMatches) != 0 || len(titleMatches) != 0 {
			t.Fatalf("expected no self-match when excluding seed.ID, got isbn=%d title=%d", len(isbnMatches), len(titleMatches))
		}
	})

	t.Run("another tenant's identical title/isbn never collides", func(t *testing.T) {
		isbnMatches, titleMatches := h.findDuplicateBibs(ctx, otherTenantID, "9780000000001", "", "Things Fall Apart", nil)
		if len(isbnMatches) != 0 || len(titleMatches) != 0 {
			t.Fatalf("expected tenant isolation, got isbn=%d title=%d", len(isbnMatches), len(titleMatches))
		}
	})

	t.Run("unrelated title/isbn never collides", func(t *testing.T) {
		isbnMatches, titleMatches := h.findDuplicateBibs(ctx, tenantID, "9789999999999", "", "A Completely Different Book", nil)
		if len(isbnMatches) != 0 || len(titleMatches) != 0 {
			t.Fatalf("expected no matches for unrelated isbn/title, got isbn=%d title=%d", len(isbnMatches), len(titleMatches))
		}
	})
}

// TestRejectDuplicateBib exercises the HTTP-facing behavior CreateBib/UpdateBib/ImportMarc rely
// on: an ISBN collision always blocks (409 duplicate_isbn, no override), a title-only collision
// blocks unless Force is set (409 duplicate_title), and a genuinely new title passes through.
func TestRejectDuplicateBib(t *testing.T) {
	h, client := testCatalogHandler(t)
	ctx := context.Background()
	tenantID := uuid.New()

	seed, err := client.BibRecord.Create().
		SetTenantID(tenantID).
		SetTitle("Blossoms of the Savannah").
		SetIsbn13("9780000000002").
		Save(ctx)
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	decodeBody := func(rec *httptest.ResponseRecorder) map[string]any {
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response body: %v (body=%s)", err, rec.Body.String())
		}
		return body
	}

	t.Run("same ISBN is always rejected, force does not bypass it", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := bibRequest{Title: "A Totally Different Title", ISBN13: "9780000000002", Force: true}
		if blocked := h.rejectDuplicateBib(ctx, rec, tenantID, req, nil); !blocked {
			t.Fatalf("expected an ISBN collision to be rejected even with force=true")
		}
		if rec.Code != 409 {
			t.Fatalf("expected 409, got %d", rec.Code)
		}
		if code := decodeBody(rec)["code"]; code != "duplicate_isbn" {
			t.Fatalf("expected code=duplicate_isbn, got %v", code)
		}
	})

	t.Run("same title without force is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := bibRequest{Title: "Blossoms of the Savannah"}
		if blocked := h.rejectDuplicateBib(ctx, rec, tenantID, req, nil); !blocked {
			t.Fatalf("expected a title collision to be rejected")
		}
		if code := decodeBody(rec)["code"]; code != "duplicate_title" {
			t.Fatalf("expected code=duplicate_title, got %v", code)
		}
	})

	t.Run("same title WITH force is allowed through", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := bibRequest{Title: "Blossoms of the Savannah", Force: true}
		if blocked := h.rejectDuplicateBib(ctx, rec, tenantID, req, nil); blocked {
			t.Fatalf("expected force=true to bypass a title-only collision, got blocked with body=%s", rec.Body.String())
		}
	})

	t.Run("a genuinely new title/isbn is never blocked", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := bibRequest{Title: "Petals of Blood", ISBN13: "9780000000099"}
		if blocked := h.rejectDuplicateBib(ctx, rec, tenantID, req, nil); blocked {
			t.Fatalf("expected a new title/isbn to pass through, got blocked with body=%s", rec.Body.String())
		}
	})

	t.Run("editing the seed record itself is not a collision", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := bibRequest{Title: "Blossoms of the Savannah", ISBN13: "9780000000002"}
		if blocked := h.rejectDuplicateBib(ctx, rec, tenantID, req, &seed.ID); blocked {
			t.Fatalf("expected editing the same record to pass through, got blocked with body=%s", rec.Body.String())
		}
	})
}
