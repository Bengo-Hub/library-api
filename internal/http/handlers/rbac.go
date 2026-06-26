package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/modules/rbac"
)

// RBACHandler serves the team / roles / permissions endpoints for the RBAC matrix UI.
type RBACHandler struct {
	rbac *rbac.Service
	log  *zap.Logger
}

// NewRBACHandler builds the RBAC handler.
func NewRBACHandler(rbacSvc *rbac.Service, log *zap.Logger) *RBACHandler {
	return &RBACHandler{rbac: rbacSvc, log: log}
}

// ListRoles godoc
// @Router /{tenant}/library/rbac/roles [get]
func (h *RBACHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	rows, err := h.rbac.ListRoles(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// ListPermissions godoc
// @Router /{tenant}/library/rbac/permissions [get]
func (h *RBACHandler) ListPermissions(w http.ResponseWriter, r *http.Request) {
	cat := rbac.PermissionCatalog()
	respondJSON(w, http.StatusOK, listEnvelope{Data: cat, Total: len(cat)})
}

// ListTeam godoc
// @Router /{tenant}/library/team [get]
func (h *RBACHandler) ListTeam(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	rows, err := h.rbac.ListUsers(r.Context(), tenantID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// AssignRoles godoc
// @Router /{tenant}/library/team/{user_id}/roles [put]
func (h *RBACHandler) AssignRoles(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	userID := chi.URLParam(r, "user_id")
	var body struct {
		Roles []string `json:"roles"`
	}
	if err := Decode(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	if err := h.rbac.AssignRoles(r.Context(), tenantID, userID, body.Roles); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "assign_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
}
