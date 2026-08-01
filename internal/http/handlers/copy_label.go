package handlers

import (
	"net/http"
	"strconv"
	"strings"

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
	lbl := barcode.CopyLabel{Barcode: c.Barcode, Title: title, CallNumber: c.CallNumber}

	// format=thermal_tspl prints directly to a thermal roll printer (e.g. Xprinter XP-330B) via
	// TSPL — the format that was missing entirely before, forcing operators to force-fit the
	// avery_a4 PDF onto a thermal roll and guess a Windows paper preset (the rotated-label bug).
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "thermal_tspl") {
		tmpl := resolveTemplate(r)
		out := barcode.RenderThermalTSPL([]barcode.CopyLabel{lbl}, tmpl)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "inline; filename=\"label-"+c.Barcode+".tspl\"")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return
	}

	tmpl := resolveTemplate(r)
	pdf, err := barcode.RenderPDF(lbl, tmpl)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "label_failed")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"label-"+c.Barcode+".pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// resolveTemplate resolves the LabelTemplate for a single-label print from query params:
// ?template=<preset|custom>&rotate=true&custom_label_w_in=&custom_label_h_in=&custom_lanes=&
// custom_gap_x_in=&custom_gap_y_in=. Empty/unknown template falls back to the pre-existing
// 29x62mm single-lane default inside LabelTemplateByName.
func resolveTemplate(r *http.Request) barcode.LabelTemplate {
	q := r.URL.Query()
	rotate := strings.EqualFold(strings.TrimSpace(q.Get("rotate")), "true")
	if strings.EqualFold(strings.TrimSpace(q.Get("template")), "custom") {
		return barcode.CustomLabelTemplate(
			parseFloatOr(q.Get("custom_label_w_in"), 29.0/25.4),
			parseFloatOr(q.Get("custom_label_h_in"), 62.0/25.4),
			int(parseFloatOr(q.Get("custom_lanes"), 1)),
			parseFloatOr(q.Get("custom_gap_x_in"), 0),
			parseFloatOr(q.Get("custom_gap_y_in"), 0),
			rotate,
		)
	}
	tmpl := barcode.LabelTemplateByName(q.Get("template"))
	tmpl.Rotate = rotate
	return tmpl
}

func parseFloatOr(s string, fallback float64) float64 {
	if s == "" {
		return fallback
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return fallback
}
