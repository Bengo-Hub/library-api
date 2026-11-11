package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	httpware "github.com/Bengo-Hub/httpware"
	sharedpagination "github.com/Bengo-Hub/pagination"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/tenant"
)

// ClaimsFrom returns the validated JWT claims from the request context.
func ClaimsFrom(r *http.Request) (*authclient.Claims, bool) {
	return authclient.ClaimsFromContext(r.Context())
}

// scopedTenant is the tenant TenantScope resolved for a platform owner from the URL.
type scopedTenant struct {
	id   uuid.UUID
	slug string
}

type scopedTenantKey struct{}

// TenantScope checks the {tenant} URL segment against the caller. It runs httpware.TenantV2,
// so a tenant user naming another tenant gets 403. A platform owner may act for any tenant:
// the URL tenant (a UUID or a slug looked up in the local Tenant table) becomes the request's
// tenant, and an unknown slug gets 404. db may be nil when no platform owner is expected.
//
// The tenant check is report-only for now: a mismatch is logged through log (nil logs nothing) and
// the request goes on, until the logs show no legitimate caller would be refused.
func TenantScope(db *ent.Client, log *zap.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = zap.NewNop()
	}
	v2 := httpware.TenantV2(httpware.TenantConfig{
		// Report-only until the logs show no legitimate caller is refused, then remove
		// ReportOnly so a tenant that differs from the token's gets a 403.
		ReportOnly: true,
		OnMismatch: func(r *http.Request, source, supplied, claimed string) {
			fields := []zap.Field{
				zap.String("source", source),
				zap.String("supplied", supplied),
				zap.String("claimed", claimed),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			}
			if c, ok := authclient.ClaimsFromContext(r.Context()); ok && c != nil {
				fields = append(fields, zap.String("subject", c.Subject))
			}
			log.Warn("tenant mismatch (report-only)", fields...)
		},
		ClaimsExtractor: func(ctx context.Context) (string, string, bool, bool) {
			claims, found := authclient.ClaimsFromContext(ctx)
			if !found || claims == nil {
				return "", "", false, false
			}
			return claims.TenantID, claims.GetTenantSlug(), claims.IsPlatformOwner, true
		},
		URLParamFunc: chi.URLParam,
		URLParamName: "tenant",
	})
	return func(next http.Handler) http.Handler {
		resolve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !httpware.IsPlatformOwner(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			st, found, err := platformOwnerTenant(r, db)
			if err != nil {
				respondError(w, http.StatusInternalServerError, "tenant lookup failed", "tenant_lookup_failed")
				return
			}
			if !found {
				respondError(w, http.StatusNotFound, "tenant not found", "tenant_not_found")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scopedTenantKey{}, st)))
		})
		return v2(resolve)
	}
}

// platformOwnerTenant turns the tenant TenantV2 kept for a platform owner into a UUID. TenantV2
// keeps a URL UUID as the id, and for a URL slug clears the id and keeps only the slug.
func platformOwnerTenant(r *http.Request, db *ent.Client) (scopedTenant, bool, error) {
	ctx := r.Context()
	slug := httpware.GetTenantSlug(ctx)
	if id, err := uuid.Parse(httpware.GetTenantID(ctx)); err == nil && id != uuid.Nil {
		return scopedTenant{id: id, slug: slug}, true, nil
	}
	if slug == "" {
		return scopedTenant{}, false, nil
	}
	claims, _ := ClaimsFrom(r)
	if db != nil {
		t, err := db.Tenant.Query().Where(tenant.Slug(slug)).Only(ctx)
		if err == nil {
			return scopedTenant{id: t.ID, slug: t.Slug}, true, nil
		}
		if !ent.IsNotFound(err) {
			return scopedTenant{}, false, err
		}
	}
	// The owner's own tenant may be missing from the local table; the claims still name it.
	if claims != nil && strings.EqualFold(claims.GetTenantSlug(), slug) {
		if id, err := uuid.Parse(claims.TenantID); err == nil {
			return scopedTenant{id: id, slug: slug}, true, nil
		}
	}
	return scopedTenant{}, false, nil
}

// TenantUUID returns the request's tenant: the URL tenant for a platform owner (set by
// TenantScope), else the tenant in the JWT claims.
func TenantUUID(r *http.Request) (uuid.UUID, bool) {
	if st, ok := r.Context().Value(scopedTenantKey{}).(scopedTenant); ok && st.id != uuid.Nil {
		return st.id, true
	}
	claims, ok := ClaimsFrom(r)
	if !ok || claims == nil || claims.TenantID == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// TenantSlug resolves the tenant slug from JWT claims (best-effort; "" when absent).
// Used to build service-identifiable payment references (see internal/payref).
func TenantSlug(r *http.Request) string {
	if st, ok := r.Context().Value(scopedTenantKey{}).(scopedTenant); ok && st.slug != "" {
		return st.slug
	}
	if claims, ok := ClaimsFrom(r); ok && claims != nil {
		return claims.GetTenantSlug()
	}
	return ""
}

// UserIDFrom returns the acting user's id (JWT subject).
func UserIDFrom(r *http.Request) string {
	if claims, ok := ClaimsFrom(r); ok && claims != nil {
		return claims.Subject
	}
	return ""
}

// Decode reads and unmarshals a JSON request body into dst.
func Decode(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

// ParseUUIDParam parses a URL path param as a UUID.
func ParseUUIDParam(v string) (uuid.UUID, error) {
	return uuid.Parse(v)
}

// NOTE: pagination is now handled uniformly via github.com/Bengo-Hub/pagination
// (see sharedpagination.Parse / sharedpagination.NewResponse). The old PageParams
// helper and listEnvelope type have been removed in favor of calling the shared
// package directly at each list-handler call site.

// paginateSlice applies offset/limit pagination to a slice that was already fully
// materialized in memory (e.g. because the handler dedupes/derives/re-orders rows
// after fetching them, so DB-level LIMIT/OFFSET can't be applied to the raw query
// without changing what the page actually represents). The returned sub-slice, together
// with the original total length, is meant to be passed straight into
// sharedpagination.NewResponse(page, total, p).
func paginateSlice[T any](items []T, p sharedpagination.Params) (page []T, total int) {
	total = len(items)
	start := p.Offset
	if start > total {
		start = total
	}
	end := start + p.Limit
	if end > total {
		end = total
	}
	return items[start:end], total
}

// FeesEnabled reports whether the tenant's plan includes library_fines. Borrowing fees are
// part of that feature, so a tenant without it never has a fee raised at checkout. Requests
// without claims (service calls) are not gated, like the feature middleware.
func FeesEnabled(r *http.Request) bool {
	claims, ok := ClaimsFrom(r)
	if !ok || claims == nil {
		return true
	}
	return claims.FeatureEnabled("library_fines")
}
