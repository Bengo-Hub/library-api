package handlers

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// AuthHandler serves the service-level identity endpoint. Effective permissions =
// SSO JWT ∪ local RBAC (reference_service_rbac_authme_sync).
type AuthHandler struct {
	rbac PermissionLister
	log  *zap.Logger
}

// PermissionLister returns the user's locally-granted permission codes.
type PermissionLister interface {
	HasAnyPermission(ctx context.Context, tenantID uuid.UUID, userID string, perms ...string) bool
	ListPermissions(ctx context.Context, tenantID uuid.UUID, userID string) []string
}

// NewAuthHandler builds the auth/me handler.
func NewAuthHandler(rbac PermissionLister, log *zap.Logger) *AuthHandler {
	return &AuthHandler{rbac: rbac, log: log}
}

// Me godoc
// @Summary Service identity + effective permissions (JWT ∪ local RBAC)
// @Tags Auth
// @Router /{tenant}/library/auth/me [get]
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFrom(r)
	if !ok || claims == nil {
		respondError(w, http.StatusUnauthorized, "unauthenticated", "unauthorized")
		return
	}
	perms := append([]string{}, claims.Permissions...)
	if h.rbac != nil {
		if tenantID, err := uuid.Parse(claims.TenantID); err == nil {
			perms = mergePerms(perms, h.rbac.ListPermissions(r.Context(), tenantID, claims.Subject))
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"service":           "library-api",
		"user_id":           claims.Subject,
		"tenant_id":         claims.TenantID,
		"tenant_slug":       claims.GetTenantSlug(),
		"email":             claims.Email,
		"roles":             claims.Roles,
		"permissions":       perms,
		"is_platform_owner": claims.IsPlatformOwner,
		"is_superuser":      claims.IsSuperuser(),
	})
}

func mergePerms(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range append(append([]string{}, a...), b...) {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
