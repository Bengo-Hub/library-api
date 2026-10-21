package handlers

import (
	"net/http"
	"strings"

	"github.com/Bengo-Hub/httpware/objectstore"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent/bibrecord"
)

// UploadCover godoc
// @Summary Upload a bib cover image (front or back) to the media store
// @Description multipart/form-data field "file" (JPEG, PNG, WebP or GIF, up to 8 MB); query ?side=front|back (default front)
// @Tags Catalog
// @Accept multipart/form-data
// @Produce json
// @Param id path string true "Bib record id"
// @Param side query string false "front|back"
// @Router /{tenant}/library/catalog/bibs/{id}/cover [post]
func (h *CatalogHandler) UploadCover(w http.ResponseWriter, r *http.Request) {
	if !h.media.Enabled() {
		respondError(w, http.StatusServiceUnavailable, "media storage not configured", "media_disabled")
		return
	}
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	bib, err := h.db.BibRecord.Query().Where(bibrecord.IDEQ(id), bibrecord.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "title not found", "not_found")
		return
	}

	side := strings.ToLower(r.URL.Query().Get("side"))
	if side != "back" {
		side = "front"
	}

	// Size limit and type checked from the bytes; JPEG and PNG re-encoded (EXIF removed).
	up, err := objectstore.ReadUpload(w, r, "file", objectstore.TypeCovers)
	if err != nil {
		if objectstore.IsUploadError(err) {
			respondError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		respondError(w, http.StatusInternalServerError, "could not read the upload", "store_failed")
		return
	}
	owner := objectstore.Owner{TenantID: tenantID.String()}
	name := uuid.NewString()
	key, err := h.media.Key(owner, objectstore.TypeCovers, bib.ID.String(), name+up.Ext)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not store image", "store_failed")
		return
	}
	if err := h.media.Put(r.Context(), key, up.Data, up.ContentType); err != nil {
		h.log.Error("store cover failed", zap.Error(err))
		respondError(w, http.StatusBadGateway, "could not store image", "store_failed")
		return
	}

	// No thumbnail copy: grid and list views get resized covers from Cloudflare Image
	// Transformations (shared-ui-lib MediaImage), so only the original is stored.
	url := h.media.URL(key)
	old := bib.CoverImageURL
	upd := h.db.BibRecord.UpdateOneID(bib.ID)
	if side == "back" {
		old = bib.CoverBackImageURL
		upd.SetCoverBackImageURL(url)
	} else {
		upd.SetCoverImageURL(url)
	}
	if _, err := upd.Save(r.Context()); err != nil {
		_ = h.media.Delete(r.Context(), key)
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	// The replaced cover is deleted (only this service's own files; an Open Library link is just
	// replaced).
	if h.media.Owned(old) {
		if err := h.media.Delete(r.Context(), old); err != nil {
			h.log.Warn("replaced cover not deleted from storage", zap.String("ref", old), zap.Error(err))
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"cover_url": url, "side": side})
}
