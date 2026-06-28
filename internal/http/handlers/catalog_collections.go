package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/collection"
	"github.com/bengobox/library-service/internal/modules/refdata"
)

// collectionResponse is the UI-facing collection shape (global defaults + tenant custom).
type collectionResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Code            string `json:"code"`
	IsReferenceOnly bool   `json:"is_reference_only"`
	IsGlobal        bool   `json:"is_global"` // true = shared default (read-only for tenants)
}

func toCollectionResponse(c *ent.Collection) collectionResponse {
	return collectionResponse{
		ID:              c.ID.String(),
		Name:            c.Name,
		Code:            c.Code,
		IsReferenceOnly: c.IsReferenceOnly,
		IsGlobal:        c.TenantID == refdata.GlobalTenantID,
	}
}

type collectionRequest struct {
	Name            string `json:"name"`
	Code            string `json:"code"`
	IsReferenceOnly bool   `json:"is_reference_only"`
}

// ListCollections godoc
// @Summary List collections (global shared defaults + this tenant's custom ones)
// @Tags Catalog
// @Router /{tenant}/library/catalog/collections [get]
func (h *CatalogHandler) ListCollections(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	rows, err := h.db.Collection.Query().
		Where(collection.Or(collection.TenantID(tenantID), collection.TenantID(refdata.GlobalTenantID))).
		Order(ent.Asc(collection.FieldName)).
		All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	// Global defaults first, then tenant custom — each alphabetical.
	out := make([]collectionResponse, 0, len(rows))
	for _, c := range rows {
		if c.TenantID == refdata.GlobalTenantID {
			out = append(out, toCollectionResponse(c))
		}
	}
	for _, c := range rows {
		if c.TenantID != refdata.GlobalTenantID {
			out = append(out, toCollectionResponse(c))
		}
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

// CreateCollection godoc
// @Summary Create a tenant-custom collection
// @Tags Catalog
// @Router /{tenant}/library/catalog/collections [post]
func (h *CatalogHandler) CreateCollection(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req collectionRequest
	if err := Decode(r, &req); err != nil || req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required", "invalid_request")
		return
	}
	// Avoid duplicates against the tenant's own + global set (case-insensitive).
	dup, _ := h.db.Collection.Query().Where(
		collection.Or(collection.TenantID(tenantID), collection.TenantID(refdata.GlobalTenantID)),
		collection.NameEqualFold(req.Name),
	).Exist(r.Context())
	if dup {
		respondError(w, http.StatusConflict, "a collection with that name already exists", "duplicate")
		return
	}
	row, err := h.db.Collection.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetCode(req.Code).
		SetIsReferenceOnly(req.IsReferenceOnly).
		Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, toCollectionResponse(row))
}

// UpdateCollection godoc
// @Summary Update a tenant-custom collection (global defaults are read-only)
// @Tags Catalog
// @Router /{tenant}/library/catalog/collections/{id} [put]
func (h *CatalogHandler) UpdateCollection(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	existing, err := h.db.Collection.Query().Where(collection.IDEQ(id)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	if existing.TenantID == refdata.GlobalTenantID {
		respondError(w, http.StatusForbidden, "shared default collections cannot be edited", "read_only")
		return
	}
	if existing.TenantID != tenantID {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req collectionRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := existing.Update().SetIsReferenceOnly(req.IsReferenceOnly).SetCode(req.Code)
	if req.Name != "" {
		u.SetName(req.Name)
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, toCollectionResponse(row))
}

// DeleteCollection godoc
// @Summary Delete a tenant-custom collection (blocked when titles still reference it)
// @Tags Catalog
// @Router /{tenant}/library/catalog/collections/{id} [delete]
func (h *CatalogHandler) DeleteCollection(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	existing, err := h.db.Collection.Query().Where(collection.IDEQ(id)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	if existing.TenantID == refdata.GlobalTenantID {
		respondError(w, http.StatusForbidden, "shared default collections cannot be deleted", "read_only")
		return
	}
	if existing.TenantID != tenantID {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	inUse, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID), bibrecord.CollectionID(id)).Exist(r.Context())
	if inUse {
		respondError(w, http.StatusConflict, "reassign titles in this collection first", "in_use")
		return
	}
	if _, err := h.db.Collection.Delete().Where(collection.IDEQ(id)).Exec(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "delete_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
