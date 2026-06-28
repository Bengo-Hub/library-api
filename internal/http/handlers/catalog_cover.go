package handlers

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent/bibrecord"
)

// maxCoverBytes caps an uploaded cover image (8 MB).
const maxCoverBytes = 8 << 20

// UploadCover godoc
// @Summary Upload a bib cover image (front or back) to the media store
// @Description multipart/form-data field "file"; query ?side=front|back (default front)
// @Tags Catalog
// @Accept multipart/form-data
// @Produce json
// @Param id path string true "Bib record id"
// @Param side query string false "front|back"
// @Router /{tenant}/library/catalog/bibs/{id}/cover [post]
func (h *CatalogHandler) UploadCover(w http.ResponseWriter, r *http.Request) {
	if h.mediaRoot == "" {
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

	if err := r.ParseMultipartForm(maxCoverBytes); err != nil {
		respondError(w, http.StatusBadRequest, "invalid upload", "invalid_request")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		respondError(w, http.StatusBadRequest, "file is required", "invalid_request")
		return
	}
	defer file.Close()

	ext := coverExt(header.Filename, header.Header.Get("Content-Type"))
	if ext == "" {
		respondError(w, http.StatusBadRequest, "unsupported image type (use jpg/png/webp/gif)", "invalid_request")
		return
	}

	dir := filepath.Join(h.mediaRoot, "covers", tenantID.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.log.Error("mkdir cover dir failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "could not store image", "store_failed")
		return
	}
	fname := fmt.Sprintf("%s_%s%s", bib.ID.String(), side, ext)
	dst, err := os.Create(filepath.Join(dir, fname))
	if err != nil {
		h.log.Error("create cover file failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "could not store image", "store_failed")
		return
	}
	if _, err := io.Copy(dst, io.LimitReader(file, maxCoverBytes)); err != nil {
		dst.Close()
		respondError(w, http.StatusInternalServerError, "could not store image", "store_failed")
		return
	}
	dst.Close()

	// Absolute URL so the UI <img> (on the UI host) can load it from the API/media host directly.
	url := fmt.Sprintf("%s/media/covers/%s/%s", publicBaseURL(r), tenantID.String(), fname)

	upd := h.db.BibRecord.UpdateOneID(bib.ID)
	if side == "back" {
		upd.SetCoverBackImageURL(url)
	} else {
		upd.SetCoverImageURL(url)
	}
	if _, err := upd.Save(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"cover_url": url, "side": side})
}

// coverExt returns a safe image extension from the filename or content-type, "" if unsupported.
func coverExt(filename, contentType string) string {
	allowed := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}
	if e := strings.ToLower(filepath.Ext(filename)); allowed[e] {
		if e == ".jpeg" {
			return ".jpg"
		}
		return e
	}
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 && allowed[exts[0]] {
		return exts[0]
	}
	return ""
}

// publicBaseURL builds the externally reachable scheme://host for media URLs, honoring the
// ingress's X-Forwarded-Proto (TLS terminates at the ingress, so r.TLS is usually nil).
func publicBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "https" // production default behind the TLS ingress
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}
