package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/modules/circulation"
)

// CirculationHandler serves the circulation desk endpoints.
type CirculationHandler struct {
	db  *ent.Client
	svc *circulation.Service
	log *zap.Logger
}

// NewCirculationHandler builds the circulation handler.
func NewCirculationHandler(db *ent.Client, svc *circulation.Service, log *zap.Logger) *CirculationHandler {
	return &CirculationHandler{db: db, svc: svc, log: log}
}

type checkoutRequest struct {
	MemberID        string `json:"member_id"`
	CopyID          string `json:"copy_id"`
	CopyBarcode     string `json:"copy_barcode"`
	InHouse         bool   `json:"in_house"`
	ClientReference string `json:"client_reference"`
}

// Checkout godoc
// @Summary Check out a copy to a member (scan-driven)
// @Tags Circulation
// @Router /{tenant}/library/circulation/checkout [post]
func (h *CirculationHandler) Checkout(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req checkoutRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	memberID, err := uuid.Parse(req.MemberID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "member_id is required", "invalid_request")
		return
	}
	copyID, err := h.resolveCopyID(r, tenantID, req.CopyID, req.CopyBarcode)
	if err != nil {
		respondError(w, http.StatusBadRequest, "copy_id or copy_barcode is required", "invalid_request")
		return
	}
	l, err := h.svc.Checkout(r.Context(), tenantID, memberID, copyID, req.InHouse, UserIDFrom(r), req.ClientReference)
	if err != nil {
		h.writeCircErr(w, err)
		return
	}
	respondJSON(w, http.StatusCreated, l)
}

type returnRequest struct {
	CopyID      string `json:"copy_id"`
	CopyBarcode string `json:"copy_barcode"`
}

// Return godoc
// @Summary Check a copy back in
// @Tags Circulation
// @Router /{tenant}/library/circulation/return [post]
func (h *CirculationHandler) Return(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req returnRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	copyID, err := h.resolveCopyID(r, tenantID, req.CopyID, req.CopyBarcode)
	if err != nil {
		respondError(w, http.StatusBadRequest, "copy_id or copy_barcode is required", "invalid_request")
		return
	}
	res, err := h.svc.Return(r.Context(), tenantID, copyID, UserIDFrom(r))
	if err != nil {
		h.writeCircErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, res)
}

// Renew godoc
// @Summary Renew an active loan
// @Tags Circulation
// @Router /{tenant}/library/circulation/renew/{loan_id} [post]
func (h *CirculationHandler) Renew(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	loanID, err := uuid.Parse(chi.URLParam(r, "loan_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad loan id", "invalid_request")
		return
	}
	l, err := h.svc.Renew(r.Context(), tenantID, loanID)
	if err != nil {
		h.writeCircErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, l)
}

// ListLoans godoc
// @Summary List loans (filter by status/member/overdue)
// @Tags Circulation
// @Router /{tenant}/library/circulation/loans [get]
func (h *CirculationHandler) ListLoans(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	limit, offset := PageParams(r)
	q := h.db.Loan.Query().Where(loan.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(loan.StatusEQ(loan.Status(s)))
	}
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		if id, err := uuid.Parse(mid); err == nil {
			q = q.Where(loan.MemberID(id))
		}
	}
	if r.URL.Query().Get("overdue") == "true" {
		q = q.Where(loan.StatusEQ(loan.StatusACTIVE))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(loan.FieldCheckoutAt)).Limit(limit).Offset(offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: total})
}

func (h *CirculationHandler) resolveCopyID(r *http.Request, tenantID uuid.UUID, copyID, barcode string) (uuid.UUID, error) {
	if copyID != "" {
		return uuid.Parse(copyID)
	}
	if barcode == "" {
		return uuid.Nil, errors.New("no copy reference")
	}
	c, err := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.Barcode(barcode)).Only(r.Context())
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

// writeCircErr maps circulation domain errors to 4xx with stable codes.
func (h *CirculationHandler) writeCircErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, circulation.ErrMemberNotActive):
		respondError(w, http.StatusConflict, err.Error(), "member_not_active")
	case errors.Is(err, circulation.ErrMemberBlocked):
		respondError(w, http.StatusConflict, err.Error(), "member_blocked")
	case errors.Is(err, circulation.ErrLoanLimit):
		respondError(w, http.StatusConflict, err.Error(), "loan_limit")
	case errors.Is(err, circulation.ErrCopyUnavailable):
		respondError(w, http.StatusConflict, err.Error(), "copy_unavailable")
	case errors.Is(err, circulation.ErrNoActiveLoan):
		respondError(w, http.StatusNotFound, err.Error(), "no_active_loan")
	case errors.Is(err, circulation.ErrRenewLimit):
		respondError(w, http.StatusConflict, err.Error(), "renew_limit")
	case errors.Is(err, circulation.ErrRenewHeld):
		respondError(w, http.StatusConflict, err.Error(), "renew_held")
	default:
		respondError(w, http.StatusInternalServerError, err.Error(), "circulation_failed")
	}
}
