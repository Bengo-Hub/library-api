// Package middleware holds library-api HTTP middleware. RequireServicePermission enforces
// the union RBAC model: superuser/platform-owner bypass → JWT claim → local RBAC fallback
// → 403. Mirrors the treasury/pos/erp pattern (reference_service_rbac_authme_sync).
package middleware

import (
	"context"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
)

// PermissionChecker is the subset of rbac.Service the middleware needs.
type PermissionChecker interface {
	HasAnyPermission(ctx context.Context, tenantID uuid.UUID, userID string, perms ...string) bool
}

// RequireServicePermission returns middleware requiring any of perms. GET requests still
// pass through this gate (read authorization is enforced where mounted); callers mount it
// only on routes that need it.
func RequireServicePermission(rbacSvc PermissionChecker, perms ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok || claims == nil {
				writeForbidden(w)
				return
			}
			// 1. Platform-owner / superuser bypass.
			if claims.IsSuperuser() || claims.IsPlatformOwner {
				next.ServeHTTP(w, r)
				return
			}
			// 2. JWT-carried permission.
			if claims.HasAnyPermission(perms...) {
				next.ServeHTTP(w, r)
				return
			}
			// 3. Local RBAC fallback.
			if rbacSvc != nil && claims.TenantID != "" {
				if tenantID, err := uuid.Parse(claims.TenantID); err == nil {
					if rbacSvc.HasAnyPermission(r.Context(), tenantID, claims.Subject, perms...) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
			writeForbidden(w)
		})
	}
}

// RequirePlatformOwner gates platform-level configuration (encryption key, integration secrets)
// to the platform owner / superuser only. Mounted on the platform routes.
func RequirePlatformOwner() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok || claims == nil || !(claims.IsPlatformOwner || claims.IsSuperuser()) {
				writeForbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"You do not have permission to perform this action.","code":"permission_denied"}`))
}
