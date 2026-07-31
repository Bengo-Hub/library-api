package handlers

import (
	"encoding/json"
	"net/http"

	sharedpagination "github.com/Bengo-Hub/pagination"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
)

// ClaimsFrom returns the validated JWT claims from the request context.
func ClaimsFrom(r *http.Request) (*authclient.Claims, bool) {
	return authclient.ClaimsFromContext(r.Context())
}

// TenantUUID resolves the tenant UUID for the current request from JWT claims.
func TenantUUID(r *http.Request) (uuid.UUID, bool) {
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
