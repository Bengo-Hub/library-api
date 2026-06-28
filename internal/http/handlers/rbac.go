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

type roleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// CreateRole godoc
// @Router /{tenant}/library/rbac/roles [post]
func (h *RBACHandler) CreateRole(w http.ResponseWriter, r *http.Request) {
	var req roleRequest
	if err := Decode(r, &req); err != nil || req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required", "invalid_request")
		return
	}
	row, err := h.rbac.CreateRole(r.Context(), req.Name, req.Description, req.Permissions)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// UpdateRole godoc
// @Router /{tenant}/library/rbac/roles/{id} [put]
func (h *RBACHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	var req roleRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	row, err := h.rbac.UpdateRolePermissions(r.Context(), id, req.Permissions, req.Description)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}

// DeleteRole godoc
// @Router /{tenant}/library/rbac/roles/{id} [delete]
func (h *RBACHandler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	if err := h.rbac.DeleteRole(r.Context(), id); err != nil {
		respondError(w, http.StatusConflict, err.Error(), "delete_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": true})
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
	// Clean shape: the UI keys role/PIN assignment on user_id (not the local row id).
	out := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		out = append(out, map[string]any{
			"user_id":   u.UserID,
			"email":     u.Email,
			"full_name": u.DisplayName,
			"name":      u.DisplayName,
			"roles":      u.Roles,
			"branch_ids": u.BranchIds,
			"is_active":  u.IsActive,
			"has_pin":    u.PinHash != nil && *u.PinHash != "",
		})
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

// AssignBranches godoc
// @Summary Set the branches a staff member may log in to (branch-scoped PIN login)
// @Router /{tenant}/library/team/{user_id}/branches [put]
func (h *RBACHandler) AssignBranches(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	userID := chi.URLParam(r, "user_id")
	var body struct {
		BranchIDs []string `json:"branch_ids"`
	}
	if err := Decode(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	if err := h.rbac.AssignBranches(r.Context(), tenantID, userID, body.BranchIDs); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "assign_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
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
