package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/modules/barcode"
)

// CopyLabel godoc
// @Summary Render a printable spine/barcode label (PDF) for a copy
// @Tags Catalog
// @Produce application/pdf
// @Router /{tenant}/library/catalog/copies/{id}/label.pdf [get]
func (h *CatalogHandler) CopyLabel(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	c, err := h.db.BookCopy.Query().Where(bookcopy.IDEQ(id), bookcopy.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "copy not found", "not_found")
		return
	}
	title := ""
	if b, berr := h.db.BibRecord.Query().Where(bibrecord.IDEQ(c.BibRecordID)).Only(r.Context()); berr == nil {
		title = b.Title
	}
	pdf, err := barcode.RenderPDF(barcode.CopyLabel{
		Barcode:    c.Barcode,
		Title:      title,
		CallNumber: c.CallNumber,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "label_failed")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"label-"+c.Barcode+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}
