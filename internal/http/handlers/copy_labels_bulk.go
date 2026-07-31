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

// printCopyLabelsRequest selects copies to print holding labels for. Exactly one of CopyIDs
// or BibID must be given as the primary selector; BranchID/Status are optional additional
// filters layered on top.
type printCopyLabelsRequest struct {
	CopyIDs  []string `json:"copy_ids,omitempty"`
	BibID    string   `json:"bib_id,omitempty"`    // all copies of one title
	BranchID string   `json:"branch_id,omitempty"` // combine with status/bib as an additional filter
	Status   string   `json:"status,omitempty"`    // e.g. "available" — filter by bookcopy status
	Sheet    string   `json:"sheet,omitempty"`     // "l7160" (default) | "5160"
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
	hasCopyIDs := len(req.CopyIDs) > 0
	hasBibID := strings.TrimSpace(req.BibID) != ""
	if hasCopyIDs == hasBibID {
		respondError(w, http.StatusBadRequest, "exactly one of copy_ids or bib_id is required", "invalid_request")
		return
	}

	q := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID))

	if hasCopyIDs {
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
	} else {
		bibID, err := uuid.Parse(req.BibID)
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
