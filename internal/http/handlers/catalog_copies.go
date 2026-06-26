package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// copyRequest is the create payload for a physical copy/holding.
type copyRequest struct {
	BibRecordID     string `json:"bib_record_id"`
	BranchID        string `json:"branch_id"`
	Barcode         string `json:"barcode"`
	AccessionNo     string `json:"accession_no"`
	CallNumber      string `json:"call_number"`
	ShelfLocation   string `json:"shelf_location"`
	Condition       string `json:"condition"`
	IsReferenceOnly bool   `json:"is_reference_only"`
	AcquisitionCost string `json:"acquisition_cost"`
}

// ListCopies returns all copies for a bib record.
// @Router /{tenant}/library/catalog/bibs/{id}/copies [get]
func (h *CatalogHandler) ListCopies(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	bibID, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	rows, err := h.db.BookCopy.Query().
		Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordID(bibID)).
		All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// CreateCopy adds a physical copy, auto-allocating an accession number when none is given.
// @Router /{tenant}/library/catalog/copies [post]
func (h *CatalogHandler) CreateCopy(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req copyRequest
	if err := Decode(r, &req); err != nil || req.Barcode == "" || req.BibRecordID == "" {
		respondError(w, http.StatusBadRequest, "bib_record_id and barcode are required", "invalid_request")
		return
	}
	bibID, err := uuid.Parse(req.BibRecordID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad bib_record_id", "invalid_request")
		return
	}
	// Default to the tenant's HQ branch (get-or-create) when none is supplied.
	var branchID uuid.UUID
	if req.BranchID != "" {
		if branchID, err = uuid.Parse(req.BranchID); err != nil {
			respondError(w, http.StatusBadRequest, "bad branch_id", "invalid_request")
			return
		}
	} else if def := EnsureDefaultBranch(r.Context(), h.db, tenantID); def != nil {
		branchID = def.ID
	} else {
		respondError(w, http.StatusInternalServerError, "could not resolve a branch", "no_branch")
		return
	}

	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "tx_failed")
		return
	}
	accession := req.AccessionNo
	if accession == "" {
		accession, err = sequence.Next(r.Context(), tx, tenantID, sequence.KindAccession, "ACC", 6)
		if err != nil {
			_ = tx.Rollback()
			respondError(w, http.StatusInternalServerError, err.Error(), "sequence_failed")
			return
		}
	}
	c := tx.BookCopy.Create().
		SetTenantID(tenantID).
		SetBibRecordID(bibID).
		SetBranchID(branchID).
		SetBarcode(req.Barcode).
		SetAccessionNo(accession).
		SetIsReferenceOnly(req.IsReferenceOnly)
	if req.CallNumber != "" {
		c.SetCallNumber(req.CallNumber)
	}
	if req.ShelfLocation != "" {
		c.SetShelfLocation(req.ShelfLocation)
	}
	if req.Condition != "" {
		c.SetCondition(req.Condition)
	}
	if req.AcquisitionCost != "" {
		if d, derr := decimal.NewFromString(req.AcquisitionCost); derr == nil {
			c.SetAcquisitionCost(d)
		}
	}
	row, err := c.Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "commit_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// UpdateCopy updates a copy's mutable fields (status, location, condition).
// @Router /{tenant}/library/catalog/copies/{id} [put]
func (h *CatalogHandler) UpdateCopy(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.BookCopy.Query().Where(bookcopy.IDEQ(id), bookcopy.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req struct {
		Status        string `json:"status"`
		CallNumber    string `json:"call_number"`
		ShelfLocation string `json:"shelf_location"`
		Condition     string `json:"condition"`
	}
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.BookCopy.UpdateOneID(id)
	if req.Status != "" {
		u.SetStatus(bookcopy.Status(req.Status))
	}
	if req.CallNumber != "" {
		u.SetCallNumber(req.CallNumber)
	}
	if req.ShelfLocation != "" {
		u.SetShelfLocation(req.ShelfLocation)
	}
	if req.Condition != "" {
		u.SetCondition(req.Condition)
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}

// GetCopyByBarcode resolves a copy by its scanned barcode (circulation desk lookup).
// @Router /{tenant}/library/catalog/copies/by-barcode/{barcode} [get]
func (h *CatalogHandler) GetCopyByBarcode(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	barcode := chi.URLParam(r, "barcode")
	row, err := h.db.BookCopy.Query().
		Where(bookcopy.TenantID(tenantID), bookcopy.Barcode(barcode)).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "no copy with that barcode", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "lookup_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}
