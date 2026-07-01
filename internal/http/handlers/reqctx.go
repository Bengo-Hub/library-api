package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

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

// PageParams parses ?limit & ?offset with sane defaults/caps.
func PageParams(r *http.Request) (limit, offset int) {
	limit, offset = 50, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// ParseUUIDParam parses a URL path param as a UUID.
func ParseUUIDParam(v string) (uuid.UUID, error) {
	return uuid.Parse(v)
}

// listEnvelope is the uniform list response shape (UI maps `data` + `total`).
type listEnvelope struct {
	Data  any `json:"data"`
	Total int `json:"total"`
}
