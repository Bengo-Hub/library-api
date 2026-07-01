package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/membershipfee"
	"github.com/bengobox/library-service/internal/modules/membership"
	"github.com/bengobox/library-service/internal/payref"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

// MembershipHandler serves membership-fee list/issue/pay. Payment uses a treasury intent
// (reference_type membership_fee), reconciled to PAID by the payment consumer.
type MembershipHandler struct {
	db       *ent.Client
	svc      *membership.Service
	treasury *treasury.Client
	log      *zap.Logger
}

// NewMembershipHandler builds the membership handler.
func NewMembershipHandler(db *ent.Client, svc *membership.Service, treasuryClient *treasury.Client, log *zap.Logger) *MembershipHandler {
	return &MembershipHandler{db: db, svc: svc, treasury: treasuryClient, log: log}
}

// List godoc
// @Summary List membership fees
// @Tags Members
// @Router /{tenant}/library/membership-fees [get]
func (h *MembershipHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.svc.ListFees(r.Context(), tenantID, r.URL.Query().Get("status"))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// Issue godoc
// @Summary Issue an annual membership fee for a member and start payment
// @Tags Members
// @Router /{tenant}/library/members/{id}/membership-fee [post]
func (h *MembershipHandler) Issue(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	fee, err := h.svc.IssueFee(r.Context(), tenantID, id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "issue_failed")
		return
	}
	h.startPayment(w, r, fee)
}

// Pay godoc
// @Summary Create a treasury intent for an existing pending membership fee
// @Tags Members
// @Router /{tenant}/library/membership-fees/{id}/pay [post]
func (h *MembershipHandler) Pay(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	fee, err := h.db.MembershipFee.Query().Where(membershipfee.IDEQ(id), membershipfee.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	if fee.Status == membershipfee.StatusPAID {
		respondError(w, http.StatusConflict, "already paid", "already_settled")
		return
	}
	h.startPayment(w, r, fee)
}

func (h *MembershipHandler) startPayment(w http.ResponseWriter, r *http.Request, fee *ent.MembershipFee) {
	if h.treasury == nil {
		respondError(w, http.StatusServiceUnavailable, "payments unavailable", "treasury_unwired")
		return
	}
	// treasury's S2S endpoint keys the path on the tenant UUID (not slug).
	resp, err := h.treasury.CreateIntent(r.Context(), fee.TenantID.String(), fee.ID.String(), treasury.CreateIntentRequest{
		SourceService: "library",
		ReferenceID:   payref.Build("LIB", TenantSlug(r), fee.TenantID, fee.ID),
		ReferenceType: "membership_fee",
		Amount:        fee.Amount.InexactFloat64(),
		Currency:      "KES",
		PaymentMethod: "pending",
		Description:   "Library membership fee",
		Metadata:      map[string]any{"service": "library", "entity_id": fee.ID.String()},
	})
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error(), "intent_failed")
		return
	}
	intentID := resp.ResolvedID()
	_, _ = h.db.MembershipFee.UpdateOneID(fee.ID).SetTreasuryIntentID(intentID).Save(r.Context())
	respondJSON(w, http.StatusOK, payResponse{IntentID: intentID, InitiateURL: resp.InitiateURL, Amount: resp.Amount})
}
