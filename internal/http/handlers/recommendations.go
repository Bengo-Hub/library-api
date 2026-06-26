package handlers

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/loan"
)

// Recommend godoc
// @Summary "Members who borrowed this also borrowed…" for a bib record (co-loan counts)
// @Tags Catalog
// @Router /{tenant}/library/catalog/bibs/{id}/recommendations [get]
func (h *CatalogHandler) Recommend(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	bibID, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	ctx := r.Context()

	// Copies of this title.
	copyIDs, _ := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordID(bibID)).IDs(ctx)
	if len(copyIDs) == 0 {
		respondJSON(w, http.StatusOK, listEnvelope{Data: []any{}, Total: 0})
		return
	}
	// Members who borrowed any copy of this title.
	loans, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.CopyIDIn(copyIDs...)).All(ctx)
	memberSet := map[string]bool{}
	for _, l := range loans {
		memberSet[l.MemberID.String()] = true
	}
	if len(memberSet) == 0 {
		respondJSON(w, http.StatusOK, listEnvelope{Data: []any{}, Total: 0})
		return
	}
	memberIDs := make([]string, 0, len(memberSet))
	for m := range memberSet {
		memberIDs = append(memberIDs, m)
	}

	// Other titles those members borrowed, ranked by co-loan frequency.
	others, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID)).All(ctx)
	counts := map[string]int{} // bib_id -> co-loan count
	copyToBib := map[string]string{}
	for _, l := range others {
		if !memberSet[l.MemberID.String()] {
			continue
		}
		bib, ok := copyToBib[l.CopyID.String()]
		if !ok {
			c, err := h.db.BookCopy.Get(ctx, l.CopyID)
			if err != nil {
				continue
			}
			bib = c.BibRecordID.String()
			copyToBib[l.CopyID.String()] = bib
		}
		if bib == bibID.String() {
			continue // skip the seed title itself
		}
		counts[bib]++
	}

	type rec struct {
		BibID string `json:"bib_record_id"`
		Score int    `json:"score"`
		Title string `json:"title"`
	}
	recs := make([]rec, 0, len(counts))
	for bib, n := range counts {
		recs = append(recs, rec{BibID: bib, Score: n})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Score > recs[j].Score })
	if len(recs) > 8 {
		recs = recs[:8]
	}
	for i := range recs {
		if id, err := ParseUUIDParam(recs[i].BibID); err == nil {
			if b, err := h.db.BibRecord.Get(ctx, id); err == nil {
				recs[i].Title = b.Title
			}
		}
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: recs, Total: len(recs)})
}
