package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/circulation"
	"github.com/bengobox/library-service/internal/payref"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

// PatronPortalHandler serves self-service /me routes for library members.
type PatronPortalHandler struct {
	db             *ent.Client
	circulationSvc *circulation.Service
	treasury       *treasury.Client
	log            *zap.Logger
}

func NewPatronPortalHandler(db *ent.Client, svc *circulation.Service, tc *treasury.Client, log *zap.Logger) *PatronPortalHandler {
	return &PatronPortalHandler{db: db, circulationSvc: svc, treasury: tc, log: log}
}

// resolveMember resolves the Member record for the currently authenticated user.
// Returns 401 if no JWT, 403 if user has no linked member record for this tenant.
func (h *PatronPortalHandler) resolveMember(w http.ResponseWriter, r *http.Request) (*ent.Member, bool) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return nil, false
	}
	userIDStr := UserIDFrom(r)
	if userIDStr == "" {
		respondError(w, http.StatusUnauthorized, "missing user identity", "unauthorized")
		return nil, false
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "invalid user id", "unauthorized")
		return nil, false
	}
	m, err := h.db.Member.Query().
		Where(member.TenantID(tenantID), member.UserID(userID)).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusForbidden, "no member record for this user", "not_a_member")
		return nil, false
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "lookup_failed")
		return nil, false
	}
	return m, true
}

// MyLoans godoc
// @Summary Current member's active and recent loans
// @Tags PatronPortal
// @Router /{tenant}/library/me/loans [get]
func (h *PatronPortalHandler) MyLoans(w http.ResponseWriter, r *http.Request) {
	m, ok := h.resolveMember(w, r)
	if !ok {
		return
	}
	tenantID, _ := TenantUUID(r)
	rows, _ := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.MemberID(m.ID)).
		Order(ent.Desc(loan.FieldCheckoutAt)).
		Limit(50).All(r.Context())
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// MyHolds godoc
// @Summary Current member's holds queue
// @Tags PatronPortal
// @Router /{tenant}/library/me/holds [get]
func (h *PatronPortalHandler) MyHolds(w http.ResponseWriter, r *http.Request) {
	m, ok := h.resolveMember(w, r)
	if !ok {
		return
	}
	tenantID, _ := TenantUUID(r)
	rows, _ := h.db.Hold.Query().
		Where(hold.TenantID(tenantID), hold.MemberID(m.ID), hold.StatusIn(hold.StatusWAITING, hold.StatusREADY)).
		Order(ent.Asc(hold.FieldPlacedAt)).
		Limit(50).All(r.Context())
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// MyFines godoc
// @Summary Current member's outstanding fines
// @Tags PatronPortal
// @Router /{tenant}/library/me/fines [get]
func (h *PatronPortalHandler) MyFines(w http.ResponseWriter, r *http.Request) {
	m, ok := h.resolveMember(w, r)
	if !ok {
		return
	}
	tenantID, _ := TenantUUID(r)
	rows, _ := h.db.Fine.Query().
		Where(fine.TenantID(tenantID), fine.MemberID(m.ID), fine.StatusIn(fine.StatusUNPAID, fine.StatusPARTIAL)).
		Order(ent.Desc(fine.FieldAssessedAt)).
		Limit(50).All(r.Context())
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// MyPlaceHold godoc
// @Summary Place a hold on behalf of the current member
// @Tags PatronPortal
// @Router /{tenant}/library/me/holds [post]
func (h *PatronPortalHandler) MyPlaceHold(w http.ResponseWriter, r *http.Request) {
	m, ok := h.resolveMember(w, r)
	if !ok {
		return
	}
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()

	var req struct {
		BibRecordID string `json:"bib_record_id"`
		CopyID      string `json:"copy_id"`
		BranchID    string `json:"branch_id"`
	}
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	bibID, err := uuid.Parse(req.BibRecordID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bib_record_id required", "invalid_request")
		return
	}

	// Honour tier hold limit.
	activeHolds, _ := h.db.Hold.Query().
		Where(hold.TenantID(tenantID), hold.MemberID(m.ID), hold.StatusIn(hold.StatusWAITING, hold.StatusREADY)).
		Count(ctx)
	tier, _ := h.db.MemberTier.Get(ctx, m.TierID)
	if tier != nil && activeHolds >= tier.HoldLimit {
		respondError(w, http.StatusConflict, "hold limit reached for your membership tier", "hold_limit_reached")
		return
	}

	branchID := uuid.Nil
	if req.BranchID != "" {
		branchID, _ = uuid.Parse(req.BranchID)
	} else if m.HomeBranchID != nil {
		branchID = *m.HomeBranchID
	} else if c, err := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordID(bibID)).First(ctx); err == nil {
		branchID = c.BranchID
	}

	pos, _ := h.db.Hold.Query().Where(hold.TenantID(tenantID), hold.BibRecordID(bibID), hold.StatusEQ(hold.StatusWAITING)).Count(ctx)
	create := h.db.Hold.Create().
		SetTenantID(tenantID).SetBibRecordID(bibID).SetMemberID(m.ID).SetBranchID(branchID).
		SetQueuePosition(pos + 1).SetPlacedAt(time.Now())

	if req.CopyID != "" {
		cid, err := uuid.Parse(req.CopyID)
		if err == nil {
			create = create.SetCopyID(cid)
		}
	}
	row, err := create.Save(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// MyRenewLoan godoc
// @Summary Self-renew a loan
// @Tags PatronPortal
// @Router /{tenant}/library/me/loans/{id}/renew [post]
func (h *PatronPortalHandler) MyRenewLoan(w http.ResponseWriter, r *http.Request) {
	m, ok := h.resolveMember(w, r)
	if !ok {
		return
	}
	tenantID, _ := TenantUUID(r)
	loanID, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad loan id", "invalid_request")
		return
	}
	// Verify the loan belongs to this member before allowing renewal.
	l, err := h.db.Loan.Query().
		Where(loan.IDEQ(loanID), loan.TenantID(tenantID), loan.MemberID(m.ID)).
		Only(r.Context())
	if ent.IsNotFound(err) || l == nil {
		respondError(w, http.StatusNotFound, "loan not found", "not_found")
		return
	}
	updated, err := h.circulationSvc.Renew(r.Context(), tenantID, loanID)
	if err != nil {
		switch {
		case errors.Is(err, circulation.ErrRenewLimit):
			respondError(w, http.StatusConflict, err.Error(), "renewal_limit")
		case errors.Is(err, circulation.ErrRenewHeld):
			respondError(w, http.StatusConflict, err.Error(), "item_held")
		case errors.Is(err, circulation.ErrRenewRecalled):
			respondError(w, http.StatusConflict, err.Error(), "item_recalled")
		default:
			respondError(w, http.StatusInternalServerError, err.Error(), "renew_failed")
		}
		return
	}
	respondJSON(w, http.StatusOK, updated)
}

// MyPayFine godoc
// @Summary Create a treasury payment intent to pay a fine
// @Tags PatronPortal
// @Router /{tenant}/library/me/fines/{id}/pay [post]
func (h *PatronPortalHandler) MyPayFine(w http.ResponseWriter, r *http.Request) {
	m, ok := h.resolveMember(w, r)
	if !ok {
		return
	}
	tenantID, _ := TenantUUID(r)
	fineID, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad fine id", "invalid_request")
		return
	}
	f, err := h.db.Fine.Query().
		Where(fine.IDEQ(fineID), fine.TenantID(tenantID), fine.MemberID(m.ID)).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "fine not found", "not_found")
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
	resp, err := h.treasury.CreateIntent(r.Context(), tenantID.String(), f.ID.String(), treasury.CreateIntentRequest{
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
