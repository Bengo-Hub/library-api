package handlers

import (
	"errors"
	"net/http"

	sharedpagination "github.com/Bengo-Hub/pagination"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
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

// roleActor builds the caller of a role management call from the request. The tenant is the
// request tenant (the URL tenant for a platform owner).
func roleActor(r *http.Request) (rbac.Actor, bool) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		return rbac.Actor{}, false
	}
	a := rbac.Actor{TenantID: tenantID}
	if claims, ok := ClaimsFrom(r); ok && claims != nil {
		a.UserID = claims.Subject
		a.PlatformOwner = claims.IsPlatformOwner
		a.ClaimPermissions = claims.Permissions
	}
	return a, true
}

// respondRoleError maps a role management error to its HTTP status. Database text is never sent
// to the client: an unexpected error is logged and answered with a generic 500.
func (h *RBACHandler) respondRoleError(w http.ResponseWriter, err error, fallback string) {
	var notAssignable rbac.ErrRoleNotAssignable
	var notHeld rbac.ErrPermissionNotHeld
	switch {
	case errors.As(err, &notAssignable):
		if rbac.IsReservedRole(notAssignable.Role) {
			respondError(w, http.StatusBadRequest, err.Error(), "role_name_reserved")
			return
		}
		respondError(w, http.StatusBadRequest, err.Error(), "role_not_assignable")
	case errors.As(err, &notHeld):
		respondError(w, http.StatusForbidden, err.Error(), "permission_not_held")
	case errors.Is(err, rbac.ErrRoleNotFound), ent.IsNotFound(err):
		respondError(w, http.StatusNotFound, "not found", "not_found")
	case errors.Is(err, rbac.ErrSystemRoleReadOnly):
		respondError(w, http.StatusForbidden, err.Error(), "system_role_read_only")
	case errors.Is(err, rbac.ErrLegacyRoleLocked):
		respondError(w, http.StatusForbidden, err.Error(), "legacy_role_locked")
	case errors.Is(err, rbac.ErrAdminRequired):
		respondError(w, http.StatusForbidden, err.Error(), "admin_required")
	case errors.Is(err, rbac.ErrRoleNameTaken), ent.IsConstraintError(err):
		respondError(w, http.StatusConflict, rbac.ErrRoleNameTaken.Error(), "role_name_taken")
	default:
		if h.log != nil {
			h.log.Error("rbac: role request failed", zap.String("code", fallback), zap.Error(err))
		}
		respondError(w, http.StatusInternalServerError, "internal error", fallback)
	}
}

// ListRoles godoc
// @Router /{tenant}/library/rbac/roles [get]
func (h *RBACHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	rows, err := h.rbac.ListRoles(r.Context(), tenantID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	params := sharedpagination.Parse(r)
	page, total := paginateSlice(rows, params)
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(page, total, params))
}

// ListPermissions godoc
// @Router /{tenant}/library/rbac/permissions [get]
func (h *RBACHandler) ListPermissions(w http.ResponseWriter, r *http.Request) {
	cat := rbac.PermissionCatalog()
	params := sharedpagination.Parse(r)
	page, total := paginateSlice(cat, params)
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(page, total, params))
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
	actor, ok := roleActor(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	row, err := h.rbac.CreateRole(r.Context(), actor, req.Name, req.Description, req.Permissions)
	if err != nil {
		h.respondRoleError(w, err, "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// GetRole godoc
// @Router /{tenant}/library/rbac/roles/{id} [get]
func (h *RBACHandler) GetRole(w http.ResponseWriter, r *http.Request) {
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	row, err := h.rbac.GetRole(r.Context(), tenantID, id)
	if err != nil {
		h.respondRoleError(w, err, "get_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
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
	actor, ok := roleActor(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	row, err := h.rbac.UpdateRolePermissions(r.Context(), actor, id, req.Permissions, req.Description)
	if err != nil {
		h.respondRoleError(w, err, "update_failed")
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
	actor, ok := roleActor(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	if err := h.rbac.DeleteRole(r.Context(), actor, id); err != nil {
		h.respondRoleError(w, err, "delete_failed")
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
	params := sharedpagination.Parse(r)
	page, total := paginateSlice(out, params)
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(page, total, params))
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
		h.respondRoleError(w, err, "assign_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
}

// AssignRoles godoc
// @Router /{tenant}/library/team/{user_id}/roles [put]
func (h *RBACHandler) AssignRoles(w http.ResponseWriter, r *http.Request) {
	actor, ok := roleActor(r)
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
	if err := h.rbac.AssignRoles(r.Context(), actor, userID, body.Roles); err != nil {
		h.respondRoleError(w, err, "assign_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
}
