package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

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
		q = q.Where(fine.StatusEQ(fine.Status(s)))
	}
	rows, err := q.Order(ent.Desc(fine.FieldCreatedAt)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
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
	f, err := h.db.Fine.Query().Where(fine.IDEQ(id), fine.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	row, err := h.db.Fine.UpdateOneID(f.ID).SetStatus(fine.StatusWAIVED).SetWaivedBy(UserIDFrom(r)).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "waive_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
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
		ReferenceID:   f.ID.String(),
		ReferenceType: "library_fine",
		Amount:        outstanding.InexactFloat64(),
		Currency:      "KES",
		PaymentMethod: "pending",
		Description:   "Library fine payment",
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
