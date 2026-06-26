package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/copytransfer"
)

// ListTransfers godoc
// @Summary List inter-branch copy transfers
// @Tags Catalog
// @Router /{tenant}/library/catalog/transfers [get]
func (h *CatalogHandler) ListTransfers(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	q := h.db.CopyTransfer.Query().Where(copytransfer.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(copytransfer.StatusEQ(copytransfer.Status(s)))
	}
	rows, err := q.Order(ent.Desc(copytransfer.FieldCreatedAt)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// CreateTransfer godoc
// @Summary Start an inter-branch transfer of a copy (copy → IN_TRANSIT)
// @Tags Catalog
// @Router /{tenant}/library/catalog/transfers [post]
func (h *CatalogHandler) CreateTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req struct {
		CopyID     string `json:"copy_id"`
		ToBranchID string `json:"to_branch_id"`
		Notes      string `json:"notes"`
	}
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	copyID, err := uuid.Parse(req.CopyID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "copy_id is required", "invalid_request")
		return
	}
	toBranch, err := uuid.Parse(req.ToBranchID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "to_branch_id is required", "invalid_request")
		return
	}
	c, err := h.db.BookCopy.Query().Where(bookcopy.IDEQ(copyID), bookcopy.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "copy not found", "not_found")
		return
	}
	if c.Status != bookcopy.StatusAVAILABLE {
		respondError(w, http.StatusConflict, "only AVAILABLE copies can be transferred", "copy_unavailable")
		return
	}
	if c.BranchID == toBranch {
		respondError(w, http.StatusBadRequest, "copy is already at that branch", "same_branch")
		return
	}

	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "tx_failed")
		return
	}
	t, err := tx.CopyTransfer.Create().
		SetTenantID(tenantID).SetCopyID(copyID).SetFromBranchID(c.BranchID).SetToBranchID(toBranch).
		SetInitiatedBy(UserIDFrom(r)).SetNotes(req.Notes).Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	if _, err := tx.BookCopy.UpdateOneID(copyID).SetStatus(bookcopy.StatusIN_TRANSIT).Save(r.Context()); err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "commit_failed")
		return
	}
	respondJSON(w, http.StatusCreated, t)
}

// ReceiveTransfer godoc
// @Summary Receive a transfer at the destination (copy → destination branch, AVAILABLE)
// @Tags Catalog
// @Router /{tenant}/library/catalog/transfers/{id}/receive [post]
func (h *CatalogHandler) ReceiveTransfer(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	t, err := h.db.CopyTransfer.Query().Where(copytransfer.IDEQ(id), copytransfer.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if t.Status != copytransfer.StatusIN_TRANSIT {
		respondError(w, http.StatusConflict, "transfer is not in transit", "not_in_transit")
		return
	}
	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "tx_failed")
		return
	}
	updated, err := tx.CopyTransfer.UpdateOneID(id).SetStatus(copytransfer.StatusRECEIVED).SetReceivedAt(time.Now()).SetReceivedBy(UserIDFrom(r)).Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	if _, err := tx.BookCopy.UpdateOneID(t.CopyID).SetBranchID(t.ToBranchID).SetStatus(bookcopy.StatusAVAILABLE).Save(r.Context()); err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "commit_failed")
		return
	}
	respondJSON(w, http.StatusOK, updated)
}
