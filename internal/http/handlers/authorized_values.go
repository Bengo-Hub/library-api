package handlers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/authorizedvalue"
	"github.com/bengobox/library-service/internal/modules/refdata"
)

// AuthorizedValueHandler serves the controlled vocabulary admin endpoints.
type AuthorizedValueHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewAuthorizedValueHandler builds the handler.
func NewAuthorizedValueHandler(db *ent.Client, log *zap.Logger) *AuthorizedValueHandler {
	return &AuthorizedValueHandler{db: db, log: log}
}

type avRequest struct {
	Category    string `json:"category"`
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
	DisplayOrder int   `json:"display_order"`
	IsActive    bool   `json:"is_active"`
}

// ListCategories returns a distinct list of category names visible to this tenant
// (global + own tenant rows).
// GET /admin/authorized-values/categories
func (h *AuthorizedValueHandler) ListCategories(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.AuthorizedValue.Query().
		Where(authorizedvalue.TenantIDIn(refdata.GlobalTenantID, tenantID)).
		All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	seen := map[string]struct{}{}
	cats := []string{}
	for _, av := range rows {
		if _, ok := seen[av.Category]; !ok {
			seen[av.Category] = struct{}{}
			cats = append(cats, av.Category)
		}
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: cats, Total: len(cats)})
}

// List returns all authorized values for a given category.
// GET /admin/authorized-values?category=LOC
func (h *AuthorizedValueHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	cat := r.URL.Query().Get("category")

	q := h.db.AuthorizedValue.Query().
		Where(authorizedvalue.TenantIDIn(refdata.GlobalTenantID, tenantID)).
		Order(ent.Asc(authorizedvalue.FieldDisplayOrder), ent.Asc(authorizedvalue.FieldValue))

	if cat != "" {
		q = q.Where(authorizedvalue.CategoryEQ(cat))
	}

	rows, err := q.All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// Create inserts a new authorized value (tenant-scoped, never global).
// POST /admin/authorized-values
func (h *AuthorizedValueHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req avRequest
	if err := Decode(r, &req); err != nil || req.Category == "" || req.Value == "" {
		respondError(w, http.StatusBadRequest, "category and value are required", "invalid_request")
		return
	}
	row, err := h.db.AuthorizedValue.Create().
		SetTenantID(tenantID).
		SetCategory(req.Category).
		SetValue(req.Value).
		SetLabel(req.Label).
		SetDescription(req.Description).
		SetDisplayOrder(req.DisplayOrder).
		SetIsActive(req.IsActive).
		SetIsSystem(false).
		Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// Update modifies label, description, display_order, is_active of an authorized value.
// PUT /admin/authorized-values/{id}
func (h *AuthorizedValueHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	// Allow editing own-tenant rows or global system rows (display_order / label only).
	existing, err := h.db.AuthorizedValue.Query().
		Where(authorizedvalue.IDEQ(id), authorizedvalue.TenantIDIn(refdata.GlobalTenantID, tenantID)).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "query_failed")
		return
	}
	var req avRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.AuthorizedValue.UpdateOneID(existing.ID).
		SetLabel(req.Label).
		SetDescription(req.Description).
		SetDisplayOrder(req.DisplayOrder).
		SetIsActive(req.IsActive)
	// Non-system rows allow value rename.
	if !existing.IsSystem && req.Value != "" {
		u.SetValue(req.Value)
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}

// Delete removes a non-system authorized value.
// DELETE /admin/authorized-values/{id}
func (h *AuthorizedValueHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	existing, err := h.db.AuthorizedValue.Query().
		Where(authorizedvalue.IDEQ(id), authorizedvalue.TenantIDIn(refdata.GlobalTenantID, tenantID)).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "query_failed")
		return
	}
	if existing.IsSystem {
		respondError(w, http.StatusConflict, "system values cannot be deleted", "system_value")
		return
	}
	if err := h.db.AuthorizedValue.DeleteOneID(id).Exec(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "delete_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ValidateShelfLocation checks that a shelf_location value exists in the LOC category.
// Returns the UUID value or uuid.Nil if validation is disabled (no LOC rows).
func ValidateShelfLocation(ctx context.Context, db *ent.Client, tenantID uuid.UUID, location string) bool {
	if location == "" {
		return true // optional field
	}
	count, _ := db.AuthorizedValue.Query().
		Where(
			authorizedvalue.TenantIDIn(refdata.GlobalTenantID, tenantID),
			authorizedvalue.CategoryEQ("LOC"),
			authorizedvalue.ValueEQ(location),
			authorizedvalue.IsActive(true),
		).Count(ctx)
	return count > 0
}
