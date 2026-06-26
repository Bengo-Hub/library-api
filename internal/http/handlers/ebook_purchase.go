package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/library-service/internal/ent/ebook"
	"github.com/bengobox/library-service/internal/ent/ebookpurchase"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

type purchaseResponse struct {
	PurchaseID  string `json:"purchase_id"`
	IntentID    string `json:"intent_id"`
	InitiateURL string `json:"initiate_url"`
	Amount      string `json:"amount"`
}

// Purchase godoc
// @Summary Buy an e-book outright (Phase 2) — creates a treasury payment intent
// @Tags Ebooks
// @Router /{tenant}/library/ebooks/{id}/purchase [post]
func (h *EbookHandler) Purchase(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	claims, _ := ClaimsFrom(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	var body struct {
		MemberID string `json:"member_id"`
	}
	_ = Decode(r, &body)
	memberID, ok := h.resolveMemberID(r, tenantID, body.MemberID)
	if !ok {
		respondError(w, http.StatusBadRequest, "member_id is required (no library membership linked to your account)", "no_member")
		return
	}
	eb, err := h.db.Ebook.Query().Where(ebook.IDEQ(id), ebook.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "ebook not found", "not_found")
		return
	}
	if !eb.IsPurchasable {
		respondError(w, http.StatusConflict, "this e-book is not for sale", "not_purchasable")
		return
	}
	if h.treasury == nil {
		respondError(w, http.StatusServiceUnavailable, "payments unavailable", "treasury_unwired")
		return
	}

	purchase, err := h.db.EbookPurchase.Create().
		SetTenantID(tenantID).SetEbookID(id).SetMemberID(memberID).
		SetAmount(eb.Price).SetDownloadToken(randomToken()).
		Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "purchase_failed")
		return
	}
	resp, err := h.treasury.CreateIntent(r.Context(), claims.GetTenantSlug(), purchase.ID.String(), treasury.CreateIntentRequest{
		SourceService: "library",
		ReferenceID:   purchase.ID.String(),
		ReferenceType: "ebook_sale",
		Amount:        eb.Price.InexactFloat64(),
		Currency:      "KES",
		PaymentMethod: "pending",
		Description:   "E-book purchase",
	})
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error(), "intent_failed")
		return
	}
	intentID := resp.ResolvedID()
	_, _ = h.db.EbookPurchase.UpdateOneID(purchase.ID).SetTreasuryIntentID(intentID).Save(r.Context())
	respondJSON(w, http.StatusOK, purchaseResponse{
		PurchaseID: purchase.ID.String(), IntentID: intentID, InitiateURL: resp.InitiateURL, Amount: resp.Amount,
	})
}

// Download godoc
// @Summary Download a purchased e-book (token-gated; requires a PAID purchase)
// @Tags Ebooks
// @Router /{tenant}/library/ebooks/{id}/download [get]
func (h *EbookHandler) Download(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		respondError(w, http.StatusUnauthorized, "missing token", "unauthorized")
		return
	}
	p, err := h.db.EbookPurchase.Query().
		Where(ebookpurchase.TenantID(tenantID), ebookpurchase.EbookID(id), ebookpurchase.DownloadToken(token), ebookpurchase.StatusEQ(ebookpurchase.StatusPAID)).
		Only(r.Context())
	if err != nil {
		respondError(w, http.StatusForbidden, "no paid purchase for that token", "not_paid")
		return
	}
	eb, err := h.db.Ebook.Query().Where(ebook.IDEQ(id)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "ebook not found", "not_found")
		return
	}
	_, _ = h.db.EbookPurchase.UpdateOneID(p.ID).AddDownloadCount(1).Save(r.Context())
	w.Header().Set("X-Library-Watermark", p.MemberID.String()+"-purchase")
	h.streamEbookFile(w, r, eb, true) // attachment download from EBOOK_ROOT
}
