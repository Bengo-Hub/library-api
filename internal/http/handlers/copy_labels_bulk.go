package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/modules/barcode"
)

// maxBulkLabels caps a single print request to keep the PDF render bounded (500 labels is
// well beyond one cataloging batch — Avery L7160 at 21/sheet is ~24 sheets, still fast).
const maxBulkLabels = 500

// printCopyLabelsRequest selects copies to print holding labels for. CopyIDs/BibID narrow to a
// specific set/title when given; BranchID/Status are additional filters that also work ALONE
// (e.g. "print labels for every available copy at this branch", the library-ui Copies page's
// "print for current filters" action, which supplies neither CopyIDs nor BibID) — the
// maxBulkLabels cap below is what actually bounds an unscoped request, not a required selector.
type printCopyLabelsRequest struct {
	CopyIDs  []string `json:"copy_ids,omitempty"`
	BibID    string   `json:"bib_id,omitempty"`    // all copies of one title
	BranchID string   `json:"branch_id,omitempty"` // filters alone or combined with copy_ids/bib_id
	Status   string   `json:"status,omitempty"`    // e.g. "available" — filters alone or combined
	Sheet    string   `json:"sheet,omitempty"`     // "l7160" (default) | "5160" — only used when format=avery_a4
	// Format: "avery_a4" (default, a full A4/Letter sheet for a cut-sheet office printer) |
	// "thermal_tspl" (raw TSPL text for a thermal roll printer, e.g. Xprinter XP-330B). Printing
	// avery_a4 output on a thermal roll printer (guessing a Windows paper preset) is exactly what
	// produced this endpoint's originally-reported rotated-label bug — thermal_tspl exists so
	// that mis-use is no longer necessary.
	Format string `json:"format,omitempty"`
	// Template selects the physical label-roll template when Format == thermal_tspl: a named
	// preset (see barcode.LabelTemplateByName — "1row_29x62" (default) | "2row_35x29" |
	// "3row_23x29" | "4row_17x29") or "custom" (paired with the custom_* fields below).
	Template     string  `json:"template,omitempty"`
	CustomLabelW float64 `json:"custom_label_w_in,omitempty"`
	CustomLabelH float64 `json:"custom_label_h_in,omitempty"`
	CustomLanes  int     `json:"custom_lanes,omitempty"`
	CustomGapX   float64 `json:"custom_gap_x_in,omitempty"`
	CustomGapY   float64 `json:"custom_gap_y_in,omitempty"`
	Rotate       bool    `json:"rotate,omitempty"`
}

// PrintCopyLabels renders a printable Avery-sheet PDF of holding labels for a set of copies
// resolved from the request's selector + filters, so a librarian cataloging many new copies
// can print all their labels in one PDF instead of one at a time via CopyLabel.
// @Summary Render a bulk sheet (Avery-layout PDF) of holding labels for many copies
// @Tags Catalog
// @Produce application/pdf
// @Router /{tenant}/library/catalog/copies/labels/print [post]
func (h *CatalogHandler) PrintCopyLabels(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req printCopyLabelsRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}

	q := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID))

	if len(req.CopyIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(req.CopyIDs))
		for _, s := range req.CopyIDs {
			id, err := uuid.Parse(s)
			if err != nil {
				respondError(w, http.StatusBadRequest, "bad copy id: "+s, "invalid_request")
				return
			}
			ids = append(ids, id)
		}
		q = q.Where(bookcopy.IDIn(ids...))
	} else if bibIDStr := strings.TrimSpace(req.BibID); bibIDStr != "" {
		bibID, err := uuid.Parse(bibIDStr)
		if err != nil {
			respondError(w, http.StatusBadRequest, "bad bib_id", "invalid_request")
			return
		}
		q = q.Where(bookcopy.BibRecordID(bibID))
	}

	if bid := strings.TrimSpace(req.BranchID); bid != "" {
		id, err := uuid.Parse(bid)
		if err != nil {
			respondError(w, http.StatusBadRequest, "bad branch_id", "invalid_request")
			return
		}
		q = q.Where(bookcopy.BranchID(id))
	}
	if s := strings.ToUpper(strings.TrimSpace(req.Status)); s != "" {
		q = q.Where(bookcopy.StatusEQ(bookcopy.Status(s)))
	}

	total, err := q.Clone().Count(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "count_failed")
		return
	}
	if total == 0 {
		respondError(w, http.StatusBadRequest, "no copies matched the given selection", "no_copies")
		return
	}
	if total > maxBulkLabels {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("selection matched %d copies, exceeding the %d-label print limit — narrow your filters", total, maxBulkLabels), "too_many_labels")
		return
	}

	rows, err := q.Order(ent.Asc(bookcopy.FieldBarcode)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}

	// Resolve each copy's bib title (same join pattern CopyLabel already uses), batched.
	bibIDs := make([]uuid.UUID, 0, len(rows))
	for _, c := range rows {
		bibIDs = append(bibIDs, c.BibRecordID)
	}
	titles := map[uuid.UUID]string{}
	if len(bibIDs) > 0 {
		bibs, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID), bibrecord.IDIn(bibIDs...)).All(r.Context())
		for _, b := range bibs {
			titles[b.ID] = b.Title
		}
	}

	labels := make([]barcode.CopyLabel, 0, len(rows))
	for _, c := range rows {
		labels = append(labels, barcode.CopyLabel{
			Barcode:    c.Barcode,
			Title:      titles[c.BibRecordID],
			CallNumber: c.CallNumber,
		})
	}

	if strings.EqualFold(strings.TrimSpace(req.Format), "thermal_tspl") {
		tmpl := barcode.LabelTemplateByName(req.Template)
		if strings.EqualFold(strings.TrimSpace(req.Template), "custom") {
			tmpl = barcode.CustomLabelTemplate(req.CustomLabelW, req.CustomLabelH, req.CustomLanes, req.CustomGapX, req.CustomGapY, req.Rotate)
		} else {
			tmpl.Rotate = req.Rotate
		}
		out := barcode.RenderThermalTSPL(labels, tmpl)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "inline; filename=\"copy-labels.tspl\"")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return
	}

	tenantName := ""
	if t, terr := h.db.Tenant.Get(r.Context(), tenantID); terr == nil {
		tenantName = t.Name
	}

	pdf, err := barcode.RenderSheet(labels, barcode.AverySpecByName(req.Sheet), tenantName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "label_failed")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"copy-labels.pdf\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}
