package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/branch"
)

// BranchHandler serves library branch (location) endpoints.
type BranchHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewBranchHandler builds the branch handler.
func NewBranchHandler(db *ent.Client, log *zap.Logger) *BranchHandler {
	return &BranchHandler{db: db, log: log}
}

type branchRequest struct {
	Name      string `json:"name"`
	Code      string `json:"code"`
	Address   string `json:"address"`
	IsDefault bool   `json:"is_default"`
}

// List godoc
// @Router /{tenant}/library/branches [get]
func (h *BranchHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.Branch.Query().Where(branch.TenantID(tenantID)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	// is_hq lets the UI's select-outlet gate show all branches for privileged users.
	isHQ := false
	if claims, ok := ClaimsFrom(r); ok && claims != nil {
		isHQ = claims.IsSuperuser() || claims.IsPlatformOwner || claims.IsAdmin() || claims.CanAccessAllOutlets()
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": rows, "total": len(rows), "is_hq": isHQ})
}

// Create godoc
// @Router /{tenant}/library/branches [post]
func (h *BranchHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req branchRequest
	if err := Decode(r, &req); err != nil || req.Name == "" || req.Code == "" {
		respondError(w, http.StatusBadRequest, "name and code are required", "invalid_request")
		return
	}
	row, err := h.db.Branch.Create().
		SetTenantID(tenantID).SetName(req.Name).SetCode(req.Code).
		SetAddress(req.Address).SetIsDefault(req.IsDefault).
		Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// Update godoc
// @Router /{tenant}/library/branches/{id} [put]
func (h *BranchHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.Branch.Query().Where(branch.IDEQ(id), branch.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req branchRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.Branch.UpdateOneID(id)
	if req.Name != "" {
		u.SetName(req.Name)
	}
	if req.Code != "" {
		u.SetCode(req.Code)
	}
	u.SetAddress(req.Address).SetIsDefault(req.IsDefault)
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}
