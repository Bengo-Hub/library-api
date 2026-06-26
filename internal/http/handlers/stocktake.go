package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/stockcount"
)

// ListStocktakes godoc
// @Summary List stocktake / cycle-count sessions
// @Tags Catalog
// @Router /{tenant}/library/catalog/stocktake [get]
func (h *CatalogHandler) ListStocktakes(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.StockCount.Query().Where(stockcount.TenantID(tenantID)).Order(ent.Desc(stockcount.FieldCreatedAt)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// StartStocktake godoc
// @Summary Start a branch stocktake (snapshots the expected copy count)
// @Tags Catalog
// @Router /{tenant}/library/catalog/stocktake [post]
func (h *CatalogHandler) StartStocktake(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req struct {
		BranchID  string `json:"branch_id"`
		Reference string `json:"reference"`
	}
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	branchID, err := uuid.Parse(req.BranchID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "branch_id is required", "invalid_request")
		return
	}
	expected, _ := h.countableCopies(r, tenantID, branchID)
	row, err := h.db.StockCount.Create().
		SetTenantID(tenantID).SetBranchID(branchID).SetReference(req.Reference).
		SetExpectedCount(len(expected)).SetCountedBy(UserIDFrom(r)).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// ScanStocktake godoc
// @Summary Record a scanned copy as present during a stocktake
// @Tags Catalog
// @Router /{tenant}/library/catalog/stocktake/{id}/scan [post]
func (h *CatalogHandler) ScanStocktake(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	var req struct {
		Barcode string `json:"barcode"`
	}
	if err := Decode(r, &req); err != nil || req.Barcode == "" {
		respondError(w, http.StatusBadRequest, "barcode is required", "invalid_request")
		return
	}
	sc, err := h.db.StockCount.Query().Where(stockcount.IDEQ(id), stockcount.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "stocktake not found", "not_found")
		return
	}
	if sc.Status != stockcount.StatusCOUNTING {
		respondError(w, http.StatusConflict, "stocktake is not open for counting", "not_counting")
		return
	}
	c, err := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.Barcode(req.Barcode)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "no copy with that barcode", "copy_not_found")
		return
	}
	if c.BranchID != sc.BranchID {
		respondError(w, http.StatusConflict, "copy belongs to a different branch", "wrong_branch")
		return
	}
	// Dedupe + append.
	seen := map[string]bool{}
	for _, s := range sc.ScannedCopyIds {
		seen[s] = true
	}
	if !seen[c.ID.String()] {
		sc.ScannedCopyIds = append(sc.ScannedCopyIds, c.ID.String())
	}
	updated, err := h.db.StockCount.UpdateOneID(sc.ID).
		SetScannedCopyIds(sc.ScannedCopyIds).SetScannedCount(len(sc.ScannedCopyIds)).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "scan_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"stocktake": updated, "copy": c})
}

// FinalizeStocktake godoc
// @Summary Finalize a stocktake — unscanned copies at the branch are flagged LOST
// @Tags Catalog
// @Router /{tenant}/library/catalog/stocktake/{id}/finalize [post]
func (h *CatalogHandler) FinalizeStocktake(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	sc, err := h.db.StockCount.Query().Where(stockcount.IDEQ(id), stockcount.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "stocktake not found", "not_found")
		return
	}
	if sc.Status != stockcount.StatusCOUNTING {
		respondError(w, http.StatusConflict, "stocktake already finalized", "not_counting")
		return
	}
	scanned := map[string]bool{}
	for _, s := range sc.ScannedCopyIds {
		scanned[s] = true
	}
	copies, _ := h.countableCopies(r, tenantID, sc.BranchID)
	missing := 0
	for _, c := range copies {
		if !scanned[c.ID.String()] {
			_, _ = h.db.BookCopy.UpdateOneID(c.ID).SetStatus(bookcopy.StatusLOST).Save(r.Context())
			missing++
		}
	}
	updated, err := h.db.StockCount.UpdateOneID(sc.ID).
		SetStatus(stockcount.StatusCOMPLETED).SetMissingCount(missing).SetCompletedAt(time.Now()).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "finalize_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"stocktake": updated, "missing": missing})
}

// countableCopies returns the copies at a branch that a stocktake should account for
// (excludes already-LOST/WITHDRAWN and copies legitimately out on loan / in transit).
func (h *CatalogHandler) countableCopies(r *http.Request, tenantID, branchID uuid.UUID) ([]*ent.BookCopy, error) {
	return h.db.BookCopy.Query().
		Where(bookcopy.TenantID(tenantID), bookcopy.BranchID(branchID),
			bookcopy.StatusNotIn(bookcopy.StatusLOST, bookcopy.StatusWITHDRAWN, bookcopy.StatusON_LOAN, bookcopy.StatusIN_TRANSIT)).
		All(r.Context())
}
