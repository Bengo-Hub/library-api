package handlers

import (
	"encoding/json"
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
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/finepayment"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/fines"
	"github.com/bengobox/library-service/internal/payref"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

// fineStatusToWire maps the stored enum to the library-ui FineStatus (UNPAID → outstanding).
func fineStatusToWire(s fine.Status) string {
	if s == fine.StatusUNPAID {
		return "outstanding"
	}
	return strings.ToLower(string(s))
}

// fineStatusFromWire maps a UI status filter back to the stored enum.
func fineStatusFromWire(s string) fine.Status {
	if strings.EqualFold(s, "outstanding") {
		return fine.StatusUNPAID
	}
	return fine.Status(strings.ToUpper(s))
}

type fineResponse struct {
	ID           string  `json:"id"`
	MemberID     string  `json:"member_id"`
	MemberName   string  `json:"member_name,omitempty"`
	MembershipNo string  `json:"membership_no,omitempty"`
	LoanID       string  `json:"loan_id,omitempty"`
	Type         string  `json:"type"`
	Reason       string  `json:"reason,omitempty"`
	Amount       float64 `json:"amount"`
	AmountPaid   float64 `json:"amount_paid"`
	Balance      float64 `json:"balance"`
	Status       string  `json:"status"`
	AssessedAt   string  `json:"assessed_at,omitempty"`
}

func (h *FineHandler) buildFineResponses(r *http.Request, tenantID uuid.UUID, rows []*ent.Fine) []fineResponse {
	ctx := r.Context()
	ids := make([]uuid.UUID, 0, len(rows))
	for _, f := range rows {
		ids = append(ids, f.MemberID)
	}
	type minfo struct{ name, no string }
	members := map[uuid.UUID]minfo{}
	if len(ids) > 0 {
		ms, _ := h.db.Member.Query().Where(member.TenantID(tenantID), member.IDIn(ids...)).All(ctx)
		for _, m := range ms {
			members[m.ID] = minfo{name: m.DisplayName, no: m.MembershipNo}
		}
	}
	out := make([]fineResponse, 0, len(rows))
	for _, f := range rows {
		bal := f.Amount.Sub(f.AmountPaid)
		loanID := ""
		if f.LoanID != nil {
			loanID = f.LoanID.String()
		}
		out = append(out, fineResponse{
			ID: f.ID.String(), LoanID: loanID, MemberID: f.MemberID.String(), MemberName: members[f.MemberID].name,
			MembershipNo: members[f.MemberID].no, Type: strings.ToLower(string(f.Reason)), Reason: f.Description,
			Amount: f.Amount.InexactFloat64(), AmountPaid: f.AmountPaid.InexactFloat64(), Balance: bal.InexactFloat64(),
			Status: fineStatusToWire(f.Status), AssessedAt: f.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out
}

// FineHandler serves fine list/waive/pay endpoints. Payment is settled via a treasury
// payment intent; the treasury.payment.succeeded consumer flips status to PAID.
type FineHandler struct {
	db       *ent.Client
	treasury *treasury.Client
	log      *zap.Logger
}

// NewFineHandler builds the fine handler.
func NewFineHandler(db *ent.Client, treasuryClient *treasury.Client, log *zap.Logger) *FineHandler {
	return &FineHandler{db: db, treasury: treasuryClient, log: log}
}

// List godoc
// @Summary List fines (filter by status/member)
// @Tags Fines
// @Router /{tenant}/library/fines [get]
func (h *FineHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	q := h.db.Fine.Query().Where(fine.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(fine.StatusEQ(fineStatusFromWire(s)))
	}
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		if id, err := uuid.Parse(mid); err == nil {
			q = q.Where(fine.MemberID(id))
		}
	}
	params := sharedpagination.Parse(r)
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(fine.FieldCreatedAt)).Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(h.buildFineResponses(r, tenantID, rows), total, params))
}

// AssessMembershipFee creates a membership fee as a fine (type=membership) with a manual amount.
// @Router /{tenant}/library/fines/membership [post]
func (h *FineHandler) AssessMembershipFee(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req struct {
		MemberID string  `json:"member_id"`
		Amount   float64 `json:"amount"`
		Reason   string  `json:"reason"`
	}
	if err := Decode(r, &req); err != nil || req.MemberID == "" || req.Amount <= 0 {
		respondError(w, http.StatusBadRequest, "member_id and a positive amount are required", "invalid_request")
		return
	}
	memberID, err := uuid.Parse(req.MemberID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad member_id", "invalid_request")
		return
	}
	if exists, _ := h.db.Member.Query().Where(member.IDEQ(memberID), member.TenantID(tenantID)).Exist(r.Context()); !exists {
		respondError(w, http.StatusNotFound, "member not found", "not_found")
		return
	}
	desc := strings.TrimSpace(req.Reason)
	if desc == "" {
		desc = "Membership fee"
	}
	row, err := h.db.Fine.Create().
		SetTenantID(tenantID).SetMemberID(memberID).SetReason(fine.ReasonMEMBERSHIP).
		SetAmount(decimal.NewFromFloat(req.Amount)).SetDescription(desc).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	// Emit fine.assessed on the shared outbox (uniform {aggregate}.{event} subject).
	_ = events.Publish(r.Context(), h.db.OutboxEvent, tenantID, row.ID.String(), events.EventFineAssessed, map[string]any{
		"fine_id": row.ID, "member_id": memberID, "amount": req.Amount, "reason": "membership",
	})
	respondJSON(w, http.StatusCreated, h.buildFineResponses(r, tenantID, []*ent.Fine{row})[0])
}

// Waive godoc
// @Summary Waive a fine (sensitive — audited)
// @Tags Fines
// @Router /{tenant}/library/fines/{id}/waive [post]
func (h *FineHandler) Waive(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	// Lock the row so a waive cannot race a payment being recorded on the same fine.
	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "waive_failed")
		return
	}
	f, err := tx.Fine.Query().Where(fine.IDEQ(id), fine.TenantID(tenantID)).ForUpdate().Only(r.Context())
	if ent.IsNotFound(err) {
		_ = tx.Rollback()
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	if f.Status == fine.StatusPAID || f.Status == fine.StatusWAIVED {
		_ = tx.Rollback()
		respondError(w, http.StatusConflict, "fine already settled", "already_settled")
		return
	}
	row, err := tx.Fine.UpdateOneID(f.ID).SetStatus(fine.StatusWAIVED).SetWaivedBy(UserIDFrom(r)).Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "waive_failed")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "waive_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}

type recordPaymentRequest struct {
	// Amount may be left out to settle the whole balance; a smaller amount records a part payment.
	Amount          json.Number `json:"amount"`
	Method          string      `json:"method"`
	Reference       string      `json:"reference"`
	Note            string      `json:"note"`
	ClientReference string      `json:"client_reference"`
}

// RecordPayment godoc
// @Summary Record a payment taken at the desk (cash, M-Pesa, card, bank) against a fine or fee
// @Description Settles the fine without treasury. A part payment leaves it PARTIAL.
// @Tags Fines
// @Router /{tenant}/library/fines/{id}/record-payment [post]
func (h *FineHandler) RecordPayment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	var req recordPaymentRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	method, ok := fines.ParseMethod(req.Method)
	if !ok {
		respondError(w, http.StatusBadRequest, "unknown payment method", "invalid_request")
		return
	}
	amount := decimal.Zero
	if s := strings.TrimSpace(req.Amount.String()); s != "" {
		if amount, err = decimal.NewFromString(s); err != nil {
			respondError(w, http.StatusBadRequest, "amount must be a number", "invalid_request")
			return
		}
	}
	in := fines.PaymentInput{
		Amount: amount, Method: method, Reference: strings.TrimSpace(req.Reference),
		Note: strings.TrimSpace(req.Note), ReceivedBy: UserIDFrom(r),
	}
	if req.ClientReference != "" {
		in.IdempotencyKey = "desk:" + req.ClientReference
	}

	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "payment_failed")
		return
	}
	applied, err := fines.ApplyPayment(r.Context(), tx, tenantID, id, in)
	if err != nil {
		_ = tx.Rollback()
		switch {
		case ent.IsNotFound(err):
			respondError(w, http.StatusNotFound, "not found", "not_found")
		case errors.Is(err, fines.ErrSettled):
			respondError(w, http.StatusConflict, err.Error(), "already_settled")
		case errors.Is(err, fines.ErrInvalidAmount):
			respondError(w, http.StatusBadRequest, err.Error(), "invalid_amount")
		case errors.Is(err, fines.ErrOverpayment):
			respondError(w, http.StatusConflict, err.Error(), "overpayment")
		default:
			respondError(w, http.StatusInternalServerError, err.Error(), "payment_failed")
		}
		return
	}
	if applied.Fine.ID != id {
		_ = tx.Rollback()
		respondError(w, http.StatusConflict, "client_reference was already used for another fine", "reference_reused")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "payment_failed")
		return
	}
	respondJSON(w, http.StatusOK, h.buildFineResponses(r, tenantID, []*ent.Fine{applied.Fine})[0])
}

type finePaymentResponse struct {
	ID         string  `json:"id"`
	Amount     float64 `json:"amount"`
	Method     string  `json:"method"`
	Reference  string  `json:"reference,omitempty"`
	Note       string  `json:"note,omitempty"`
	ReceivedBy string  `json:"received_by,omitempty"`
	PaidAt     string  `json:"paid_at"`
}

// Payments godoc
// @Summary List the payments recorded against a fine (receipt history)
// @Tags Fines
// @Router /{tenant}/library/fines/{id}/payments [get]
func (h *FineHandler) Payments(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	rows, err := h.db.FinePayment.Query().
		Where(finepayment.TenantID(tenantID), finepayment.FineID(id)).
		Order(ent.Asc(finepayment.FieldPaidAt)).Limit(100).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	out := make([]finePaymentResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, finePaymentResponse{
			ID: p.ID.String(), Amount: p.Amount.InexactFloat64(), Method: strings.ToLower(string(p.Method)),
			Reference: p.Reference, Note: p.Note, ReceivedBy: p.ReceivedBy, PaidAt: p.PaidAt.Format(time.RFC3339),
		})
	}
	respondJSON(w, http.StatusOK, out)
}

// payResponse carries the treasury initiate URL for the shared pay page.
type payResponse struct {
	IntentID    string `json:"intent_id"`
	InitiateURL string `json:"initiate_url"`
	Amount      string `json:"amount"`
}

// Pay godoc
// @Summary Create a treasury payment intent for a fine
// @Tags Fines
// @Router /{tenant}/library/fines/{id}/pay [post]
func (h *FineHandler) Pay(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	f, err := h.db.Fine.Query().Where(fine.IDEQ(id), fine.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	if f.Status == fine.StatusPAID || f.Status == fine.StatusWAIVED {
		respondError(w, http.StatusConflict, "fine already settled", "already_settled")
		return
	}
	if h.treasury == nil {
		respondError(w, http.StatusServiceUnavailable, "payments unavailable", "treasury_unwired")
		return
	}
	outstanding := f.Amount.Sub(f.AmountPaid)
	resp, err := h.treasury.CreateIntent(r.Context(), f.TenantID.String(), f.ID.String(), treasury.CreateIntentRequest{
		SourceService: "library",
		ReferenceID:   payref.Build("LIB", TenantSlug(r), f.TenantID, f.ID),
		ReferenceType: "library_fine",
		Amount:        outstanding.InexactFloat64(),
		Currency:      "KES",
		PaymentMethod: "pending",
		Description:   "Library fine payment",
		Metadata:      map[string]any{"service": "library", "entity_id": f.ID.String()},
	})
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error(), "intent_failed")
		return
	}
	intentID := resp.ResolvedID()
	_, _ = h.db.Fine.UpdateOneID(f.ID).SetTreasuryIntentID(intentID).Save(r.Context())
	_ = events.Publish(r.Context(), h.db.OutboxEvent, tenantID, f.ID.String(), events.EventFineAssessed, map[string]any{
		"fine_id": f.ID, "intent_id": intentID, "amount": outstanding.String(),
	})
	respondJSON(w, http.StatusOK, payResponse{IntentID: intentID, InitiateURL: resp.InitiateURL, Amount: resp.Amount})
}
