package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	sharedpagination "github.com/Bengo-Hub/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/modules/circulation"
	"github.com/bengobox/library-service/internal/modules/fines"
)

// loanResponse is the library-ui Loan contract (copy barcode + bib title + member resolved,
// status lower-cased, overdue derived from due date).
type loanResponse struct {
	ID           string  `json:"id"`
	CopyID       string  `json:"copy_id"`
	CopyBarcode  string  `json:"copy_barcode,omitempty"`
	BibRecordID  string  `json:"bib_record_id,omitempty"`
	BibTitle     string  `json:"bib_title,omitempty"`
	MemberID     string  `json:"member_id"`
	MemberName   string  `json:"member_name,omitempty"`
	MembershipNo string  `json:"membership_no,omitempty"`
	Status       string  `json:"status"`
	InHouse      bool    `json:"in_house"`
	CheckedOutAt string  `json:"checked_out_at"`
	DueDate      string  `json:"due_date"`
	ReturnedAt   *string `json:"returned_at,omitempty"`
	Renewals     int     `json:"renewals"`
	BranchID     string  `json:"branch_id,omitempty"`
}

func (h *CirculationHandler) buildLoanResponses(r *http.Request, tenantID uuid.UUID, rows []*ent.Loan) []loanResponse {
	ctx := r.Context()
	now := time.Now()
	copyIDs := make([]uuid.UUID, 0, len(rows))
	memberIDs := make([]uuid.UUID, 0, len(rows))
	for _, l := range rows {
		copyIDs = append(copyIDs, l.CopyID)
		memberIDs = append(memberIDs, l.MemberID)
	}
	// copy → barcode + bib_record_id, then bib → title.
	type cinfo struct {
		barcode string
		bibID   uuid.UUID
	}
	copies := map[uuid.UUID]cinfo{}
	bibIDset := map[uuid.UUID]struct{}{}
	if len(copyIDs) > 0 {
		cs, _ := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.IDIn(copyIDs...)).All(ctx)
		for _, c := range cs {
			copies[c.ID] = cinfo{barcode: c.Barcode, bibID: c.BibRecordID}
			bibIDset[c.BibRecordID] = struct{}{}
		}
	}
	titles := map[uuid.UUID]string{}
	if len(bibIDset) > 0 {
		ids := make([]uuid.UUID, 0, len(bibIDset))
		for id := range bibIDset {
			ids = append(ids, id)
		}
		bs, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID), bibrecord.IDIn(ids...)).All(ctx)
		for _, b := range bs {
			titles[b.ID] = b.Title
		}
	}
	type minfo struct{ name, no string }
	members := map[uuid.UUID]minfo{}
	if len(memberIDs) > 0 {
		ms, _ := h.db.Member.Query().Where(member.TenantID(tenantID), member.IDIn(memberIDs...)).All(ctx)
		for _, m := range ms {
			members[m.ID] = minfo{name: m.DisplayName, no: m.MembershipNo}
		}
	}
	out := make([]loanResponse, 0, len(rows))
	for _, l := range rows {
		c := copies[l.CopyID]
		status := strings.ToLower(string(l.Status))
		if l.Status == loan.StatusACTIVE && l.DueAt.Before(now) {
			status = "overdue"
		}
		resp := loanResponse{
			ID: l.ID.String(), CopyID: l.CopyID.String(), CopyBarcode: c.barcode, BibRecordID: c.bibID.String(),
			BibTitle: titles[c.bibID], MemberID: l.MemberID.String(), MemberName: members[l.MemberID].name,
			MembershipNo: members[l.MemberID].no, Status: status, InHouse: l.InHouse,
			CheckedOutAt: l.CheckoutAt.Format(time.RFC3339), DueDate: l.DueAt.Format(time.RFC3339),
			Renewals: l.RenewalsCount, BranchID: l.BranchID.String(),
		}
		if l.ReturnedAt != nil {
			s := l.ReturnedAt.Format(time.RFC3339)
			resp.ReturnedAt = &s
		}
		out = append(out, resp)
	}
	return out
}

func (h *CirculationHandler) loanResponseOne(r *http.Request, tenantID uuid.UUID, l *ent.Loan) loanResponse {
	out := h.buildLoanResponses(r, tenantID, []*ent.Loan{l})
	return out[0]
}

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

// checkoutPayment is the librarian confirming the borrowing fee was collected at the desk.
type checkoutPayment struct {
	Collected bool   `json:"collected"`
	Method    string `json:"method"`
	Reference string `json:"reference"`
}

type checkoutRequest struct {
	MemberID        string           `json:"member_id"`
	CopyID          string           `json:"copy_id"`
	CopyBarcode     string           `json:"copy_barcode"`
	InHouse         bool             `json:"in_house"`
	ClientReference string           `json:"client_reference"`
	Payment         *checkoutPayment `json:"payment"`
}

// feeResponse describes the borrowing fee a checkout raised.
type feeResponse struct {
	FineID     string  `json:"fine_id"`
	Amount     float64 `json:"amount"`
	AmountPaid float64 `json:"amount_paid"`
	Status     string  `json:"status"`
	Paid       bool    `json:"paid"`
}

func newFeeResponse(f *ent.Fine) *feeResponse {
	if f == nil {
		return nil
	}
	return &feeResponse{
		FineID: f.ID.String(), Amount: f.Amount.InexactFloat64(), AmountPaid: f.AmountPaid.InexactFloat64(),
		Status: fineStatusToWire(f.Status), Paid: f.Status == fine.StatusPAID,
	}
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
	opts := circulation.CheckoutOptions{InHouse: req.InHouse, StaffID: UserIDFrom(r), ClientRef: req.ClientReference, SkipFee: !FeesEnabled(r)}
	if req.Payment != nil && req.Payment.Collected && !opts.SkipFee {
		method, ok := fines.ParseMethod(req.Payment.Method)
		if !ok {
			respondError(w, http.StatusBadRequest, "unknown payment method", "invalid_request")
			return
		}
		opts.Payment = &circulation.FeePayment{Method: method, Reference: strings.TrimSpace(req.Payment.Reference)}
	}
	out, err := h.svc.Checkout(r.Context(), tenantID, memberID, copyID, opts)
	if err != nil {
		h.writeCircErr(w, err)
		return
	}
	// Shape: { loan, fee } — the library-ui CheckoutResult reads res.loan and res.fee.
	body := map[string]any{"loan": h.loanResponseOne(r, tenantID, out.Loan)}
	if fee := newFeeResponse(out.Fee); fee != nil {
		body["fee"] = fee
	}
	respondJSON(w, http.StatusCreated, body)
}

type checkoutQuoteResponse struct {
	BorrowingFee  float64 `json:"borrowing_fee"`
	LateFeePerDay float64 `json:"late_fee_per_day"`
	GraceDays     int     `json:"grace_days"`
	MaxFineCap    float64 `json:"max_fine_cap"`
	DueDate       string  `json:"due_date"`
}

// CheckoutQuote godoc
// @Summary Preview the borrowing fee, late-fee terms and due date for a checkout
// @Tags Circulation
// @Router /{tenant}/library/circulation/checkout-quote [get]
func (h *CirculationHandler) CheckoutQuote(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	q := r.URL.Query()
	memberID, err := uuid.Parse(q.Get("member_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "member_id is required", "invalid_request")
		return
	}
	copyID, err := h.resolveCopyID(r, tenantID, q.Get("copy_id"), q.Get("copy_barcode"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "copy_id or copy_barcode is required", "invalid_request")
		return
	}
	quote, err := h.svc.QuoteCheckout(r.Context(), tenantID, memberID, copyID, q.Get("in_house") == "true")
	if err != nil {
		if ent.IsNotFound(err) {
			respondError(w, http.StatusNotFound, "member or copy not found", "not_found")
			return
		}
		h.writeCircErr(w, err)
		return
	}
	if !FeesEnabled(r) {
		quote.BorrowingFee = decimal.Zero // no fines feature on the plan: no fee is raised
	}
	respondJSON(w, http.StatusOK, checkoutQuoteResponse{
		BorrowingFee: quote.BorrowingFee.InexactFloat64(), LateFeePerDay: quote.LateFeePerDay.InexactFloat64(),
		GraceDays: quote.GraceDays, MaxFineCap: quote.MaxFineCap.InexactFloat64(),
		DueDate: quote.DueAt.Format(time.RFC3339),
	})
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
	// Shape to the library-ui ReturnResult { loan, fine_amount, hold_triggered, message }.
	out := map[string]any{"hold_triggered": res.PromotedHld != nil}
	if res.Loan != nil {
		out["loan"] = h.loanResponseOne(r, tenantID, res.Loan)
	}
	if res.Fine != nil {
		out["fine_amount"] = res.Fine.Amount.Sub(res.Fine.AmountPaid).InexactFloat64()
		out["message"] = "Returned with an outstanding fine."
	} else {
		out["message"] = "Returned."
	}
	if res.PromotedHld != nil {
		out["message"] = "Returned — a waiting hold is now ready for pickup."
	}
	respondJSON(w, http.StatusOK, out)
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
	respondJSON(w, http.StatusOK, map[string]any{"loan": h.loanResponseOne(r, tenantID, l)})
}

// ListLoans godoc
// @Summary List loans (filter by status/member/overdue)
// @Tags Circulation
// @Router /{tenant}/library/circulation/loans [get]
func (h *CirculationHandler) ListLoans(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	params := sharedpagination.Parse(r)
	q := h.db.Loan.Query().Where(loan.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		// "overdue" is a derived view: loans still out and past due. The hourly sweep flips
		// ACTIVE loans to OVERDUE, so both stored statuses belong in it.
		if strings.EqualFold(s, "overdue") {
			q = q.Where(loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE), loan.DueAtLT(time.Now()))
		} else {
			q = q.Where(loan.StatusEQ(loan.Status(strings.ToUpper(s))))
		}
	}
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		if id, err := uuid.Parse(mid); err == nil {
			q = q.Where(loan.MemberID(id))
		}
	}
	if r.URL.Query().Get("overdue") == "true" {
		q = q.Where(loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE), loan.DueAtLT(time.Now()))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(loan.FieldCheckoutAt)).Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(h.buildLoanResponses(r, tenantID, rows), total, params))
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

// Recall godoc
// @Summary Recall a loan — shorten due date and notify borrower to return early
// @Tags Circulation
// @Router /{tenant}/library/circulation/loans/{loan_id}/recall [post]
func (h *CirculationHandler) Recall(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		RequestedByMemberID string `json:"requested_by_member_id"`
		HoldID              string `json:"hold_id,omitempty"`
	}
	if err := Decode(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	requesterID, err := uuid.Parse(body.RequestedByMemberID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "requested_by_member_id is required", "invalid_request")
		return
	}
	var holdID *uuid.UUID
	if body.HoldID != "" {
		if id, err := uuid.Parse(body.HoldID); err == nil {
			holdID = &id
		}
	}
	if err := h.svc.Recall(r.Context(), tenantID, loanID, requesterID, holdID); err != nil {
		h.writeCircErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"message": "Recall registered. Due date shortened and borrower notified."})
}

// MarkLost godoc
// @Summary Mark a loan as LOST, set copy status LOST, assess replacement fine
// @Tags Circulation
// @Router /{tenant}/library/circulation/loans/{loan_id}/mark-lost [post]
func (h *CirculationHandler) MarkLost(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.MarkLost(r.Context(), tenantID, loanID, UserIDFrom(r)); err != nil {
		h.writeCircErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"message": "Loan marked as lost. Replacement fine assessed."})
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
	case errors.Is(err, circulation.ErrLoanNotLostable):
		respondError(w, http.StatusConflict, err.Error(), "loan_not_lostable")
	case errors.Is(err, circulation.ErrRenewOverdue):
		respondError(w, http.StatusConflict, err.Error(), "renew_overdue")
	case errors.Is(err, circulation.ErrRenewRecalled):
		respondError(w, http.StatusConflict, err.Error(), "renew_recalled")
	case errors.Is(err, circulation.ErrNoWaitingHolder):
		respondError(w, http.StatusConflict, err.Error(), "no_waiting_holder")
	case errors.Is(err, circulation.ErrAlreadyRecalled):
		respondError(w, http.StatusConflict, err.Error(), "already_recalled")
	case errors.Is(err, circulation.ErrRenewLimit):
		respondError(w, http.StatusConflict, err.Error(), "renew_limit")
	case errors.Is(err, circulation.ErrRenewHeld):
		respondError(w, http.StatusConflict, err.Error(), "renew_held")
	default:
		respondError(w, http.StatusInternalServerError, err.Error(), "circulation_failed")
	}
}
