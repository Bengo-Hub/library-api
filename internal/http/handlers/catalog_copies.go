package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// copyRequest is the create/update payload for a physical copy/holding. Field names mirror the
// library-ui Copy type (note: `price` maps to the acquisition_cost column).
type copyRequest struct {
	BibRecordID     string   `json:"bib_record_id"`
	BranchID        string   `json:"branch_id"`
	Barcode         string   `json:"barcode"`
	AccessionNo     string   `json:"accession_no"`
	CallNumber      string   `json:"call_number"`
	ShelfLocation   string   `json:"shelf_location"`
	Status          string   `json:"status"`
	Condition       string   `json:"condition"`
	IsReferenceOnly bool     `json:"is_reference_only"`
	Price           *float64 `json:"price"`
	AcquisitionDate string   `json:"acquisition_date"`
	Notes           string   `json:"notes"`
}

// copyResponse is the wire shape the library-ui consumes (branch_name resolved, status lower-cased,
// acquisition_cost surfaced as `price`, active-loan due date attached).
type copyResponse struct {
	ID              string   `json:"id"`
	BibRecordID     string   `json:"bib_record_id"`
	Barcode         string   `json:"barcode"`
	AccessionNo     string   `json:"accession_no,omitempty"`
	CallNumber      string   `json:"call_number,omitempty"`
	ShelfLocation   string   `json:"shelf_location,omitempty"`
	Status          string   `json:"status"`
	Condition       string   `json:"condition,omitempty"`
	IsReferenceOnly bool     `json:"is_reference_only"`
	BranchID        string   `json:"branch_id,omitempty"`
	BranchName      string   `json:"branch_name,omitempty"`
	Price           *float64 `json:"price,omitempty"`
	AcquisitionDate *string  `json:"acquisition_date,omitempty"`
	Notes           string   `json:"notes,omitempty"`
	CurrentLoanID   *string  `json:"current_loan_id,omitempty"`
	DueDate         *string  `json:"due_date,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

// statusToWire lower-cases the stored UPPERCASE enum to the library-ui CopyStatus contract.
func statusToWire(s bookcopy.Status) string { return strings.ToLower(string(s)) }

// buildCopyResponses batch-resolves branch names + active-loan due dates for a set of copies.
func (h *CatalogHandler) buildCopyResponses(r *http.Request, tenantID uuid.UUID, rows []*ent.BookCopy) []copyResponse {
	ctx := r.Context()
	branchIDs := make([]uuid.UUID, 0, len(rows))
	copyIDs := make([]uuid.UUID, 0, len(rows))
	for _, c := range rows {
		branchIDs = append(branchIDs, c.BranchID)
		copyIDs = append(copyIDs, c.ID)
	}
	// Branch names.
	names := map[uuid.UUID]string{}
	if len(branchIDs) > 0 {
		brs, _ := h.db.Branch.Query().
			Where(branch.TenantID(tenantID), branch.IDIn(branchIDs...)).All(ctx)
		for _, b := range brs {
			names[b.ID] = b.Name
		}
	}
	// Active-loan due dates keyed by copy.
	type loanInfo struct {
		id  string
		due time.Time
	}
	dues := map[uuid.UUID]loanInfo{}
	if len(copyIDs) > 0 {
		lns, _ := h.db.Loan.Query().
			Where(loan.TenantID(tenantID), loan.CopyIDIn(copyIDs...), loan.StatusEQ(loan.StatusACTIVE)).All(ctx)
		for _, l := range lns {
			dues[l.CopyID] = loanInfo{id: l.ID.String(), due: l.DueAt}
		}
	}
	out := make([]copyResponse, 0, len(rows))
	for _, c := range rows {
		out = append(out, h.toCopyResponse(c, names[c.BranchID], dues[c.ID].id, dues[c.ID].due))
	}
	return out
}

func (h *CatalogHandler) toCopyResponse(c *ent.BookCopy, branchName, loanID string, due time.Time) copyResponse {
	resp := copyResponse{
		ID: c.ID.String(), BibRecordID: c.BibRecordID.String(), Barcode: c.Barcode,
		AccessionNo: c.AccessionNo, CallNumber: c.CallNumber, ShelfLocation: c.ShelfLocation,
		Status: statusToWire(c.Status), Condition: c.Condition, IsReferenceOnly: c.IsReferenceOnly,
		BranchID: c.BranchID.String(), BranchName: branchName, Notes: c.Notes,
		CreatedAt: c.CreatedAt.Format(time.RFC3339), UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
	if c.AcquisitionCost != nil {
		v := c.AcquisitionCost.InexactFloat64()
		resp.Price = &v
	}
	if c.AcquisitionDate != nil {
		d := c.AcquisitionDate.Format("2006-01-02")
		resp.AcquisitionDate = &d
	}
	if loanID != "" {
		resp.CurrentLoanID = &loanID
		d := due.Format(time.RFC3339)
		resp.DueDate = &d
	}
	return resp
}

// singleCopyResponse resolves the branch name + active loan for one copy (create/update/lookup).
func (h *CatalogHandler) singleCopyResponse(r *http.Request, tenantID uuid.UUID, c *ent.BookCopy) copyResponse {
	resps := h.buildCopyResponses(r, tenantID, []*ent.BookCopy{c})
	if len(resps) == 1 {
		return resps[0]
	}
	return h.toCopyResponse(c, "", "", time.Time{})
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
		Order(ent.Asc(bookcopy.FieldBarcode)).
		All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	out := h.buildCopyResponses(r, tenantID, rows)
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
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
		SetIsReferenceOnly(req.IsReferenceOnly).
		SetNotes(req.Notes)
	if req.CallNumber != "" {
		c.SetCallNumber(req.CallNumber)
	}
	if req.ShelfLocation != "" {
		c.SetShelfLocation(req.ShelfLocation)
	}
	if req.Condition != "" {
		c.SetCondition(req.Condition)
	}
	if req.Status != "" {
		c.SetStatus(bookcopy.Status(strings.ToUpper(req.Status)))
	}
	if req.Price != nil {
		c.SetAcquisitionCost(decimal.NewFromFloat(*req.Price))
	}
	if d, ok := parseDate(req.AcquisitionDate); ok {
		c.SetAcquisitionDate(d)
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
	respondJSON(w, http.StatusCreated, h.singleCopyResponse(r, tenantID, row))
}

// UpdateCopy updates a copy's mutable fields.
// @Router /{tenant}/library/catalog/copies/{id} [put]
func (h *CatalogHandler) UpdateCopy(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	existing, err := h.db.BookCopy.Query().Where(bookcopy.IDEQ(id), bookcopy.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "load_failed")
		return
	}
	var req copyRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.BookCopy.UpdateOneID(id)
	if req.Barcode != "" {
		u.SetBarcode(req.Barcode)
	}
	if req.BranchID != "" {
		if bid, perr := uuid.Parse(req.BranchID); perr == nil {
			u.SetBranchID(bid)
		}
	}
	if req.Status != "" {
		u.SetStatus(bookcopy.Status(strings.ToUpper(req.Status)))
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
	u.SetIsReferenceOnly(req.IsReferenceOnly)
	u.SetNotes(req.Notes)
	if req.Price != nil {
		u.SetAcquisitionCost(decimal.NewFromFloat(*req.Price))
	}
	if d, ok := parseDate(req.AcquisitionDate); ok {
		u.SetAcquisitionDate(d)
	}
	if _, err := u.Save(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	row, _ := h.db.BookCopy.Get(r.Context(), id)
	if row == nil {
		row = existing
	}
	respondJSON(w, http.StatusOK, h.singleCopyResponse(r, tenantID, row))
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
	respondJSON(w, http.StatusOK, h.singleCopyResponse(r, tenantID, row))
}

// parseDate accepts an ISO date (2006-01-02) or RFC3339 timestamp; ok=false when empty/invalid.
func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
