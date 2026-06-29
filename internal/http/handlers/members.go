package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/modules/refdata"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// MemberHandler serves member + tier + loan-policy endpoints.
type MemberHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewMemberHandler builds the member handler.
func NewMemberHandler(db *ent.Client, log *zap.Logger) *MemberHandler {
	return &MemberHandler{db: db, log: log}
}

type memberRequest struct {
	DisplayName  string `json:"display_name"`
	ContactPhone string `json:"contact_phone"`
	ContactEmail string `json:"contact_email"`
	TierID       string `json:"tier_id"`
	UserID       string `json:"user_id"`
	CRMContactID string `json:"crm_contact_id"`
	HomeBranchID string `json:"home_branch_id"`
	IsWalkIn     bool   `json:"is_walk_in"`
}

// ListMembers godoc
// @Router /{tenant}/library/members [get]
func (h *MemberHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	limit, offset := PageParams(r)
	q := h.db.Member.Query().Where(member.TenantID(tenantID))
	if s := r.URL.Query().Get("q"); s != "" {
		q = q.Where(member.Or(member.DisplayNameContainsFold(s), member.MembershipNoContainsFold(s), member.ContactPhoneContainsFold(s)))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(member.FieldCreatedAt)).Limit(limit).Offset(offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: total})
}

// CreateMember registers a member, allocating membership_no and emitting member.registered.
// @Router /{tenant}/library/members [post]
func (h *MemberHandler) CreateMember(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req memberRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	// Resolve tier: explicit, else tenant default.
	tierID, err := h.resolveTier(r, tenantID, req.TierID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "no member tier configured — create one first", "no_tier")
		return
	}

	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "tx_failed")
		return
	}
	memberNo, err := sequence.Next(r.Context(), tx, tenantID, sequence.KindMembership, "MBR", 5)
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "sequence_failed")
		return
	}
	c := tx.Member.Create().
		SetTenantID(tenantID).SetMembershipNo(memberNo).SetTierID(tierID).
		SetDisplayName(req.DisplayName).SetContactPhone(req.ContactPhone).
		SetContactEmail(req.ContactEmail).SetIsWalkIn(req.IsWalkIn)
	if req.UserID != "" {
		if uid, perr := uuid.Parse(req.UserID); perr == nil {
			c.SetUserID(uid)
		}
	}
	if req.CRMContactID != "" {
		if cid, perr := uuid.Parse(req.CRMContactID); perr == nil {
			c.SetCrmContactID(cid)
		}
	}
	if req.HomeBranchID != "" {
		if bid, perr := uuid.Parse(req.HomeBranchID); perr == nil {
			c.SetHomeBranchID(bid)
		}
	}
	row, err := c.Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	_ = events.Publish(r.Context(), tx.OutboxEvent, tenantID, row.ID.String(), events.EventMemberRegistered, map[string]any{
		"member_id": row.ID, "membership_no": row.MembershipNo, "display_name": row.DisplayName,
		"email": row.ContactEmail, "name": row.DisplayName,
	})
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "commit_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// GetMember godoc
// @Router /{tenant}/library/members/{id} [get]
func (h *MemberHandler) GetMember(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	row, err := h.db.Member.Query().Where(member.IDEQ(id), member.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}

// UpdateMember godoc
// @Router /{tenant}/library/members/{id} [put]
func (h *MemberHandler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.Member.Query().Where(member.IDEQ(id), member.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req struct {
		memberRequest
		Status string `json:"status"`
	}
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.Member.UpdateOneID(id)
	if req.DisplayName != "" {
		u.SetDisplayName(req.DisplayName)
	}
	if req.ContactPhone != "" {
		u.SetContactPhone(req.ContactPhone)
	}
	if req.ContactEmail != "" {
		u.SetContactEmail(req.ContactEmail)
	}
	if req.Status != "" {
		u.SetStatus(member.Status(req.Status))
	}
	if req.TierID != "" {
		if tid, perr := uuid.Parse(req.TierID); perr == nil {
			u.SetTierID(tid)
		}
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}

// MemberLoans lists a member's loans.
// @Router /{tenant}/library/members/{id}/loans [get]
func (h *MemberHandler) MemberLoans(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	rows, err := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.MemberID(id)).
		Order(ent.Desc(loan.FieldCheckoutAt)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// MemberFines lists a member's fines.
// @Router /{tenant}/library/members/{id}/fines [get]
func (h *MemberHandler) MemberFines(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	rows, err := h.db.Fine.Query().
		Where(fine.TenantID(tenantID), fine.MemberID(id)).
		Order(ent.Desc(fine.FieldCreatedAt)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// resolveTier returns the explicit tier or a default: tenant default → global default → any
// tenant tier → any global tier (so a member always lands on a sensible tier).
func (h *MemberHandler) resolveTier(r *http.Request, tenantID uuid.UUID, explicit string) (uuid.UUID, error) {
	if explicit != "" {
		return uuid.Parse(explicit)
	}
	ctx := r.Context()
	tenantOrGlobal := membertier.Or(membertier.TenantID(tenantID), membertier.TenantID(refdata.GlobalTenantID))
	// Prefer the tenant's own default, then the global default, then any tier (tenant or global).
	if t, err := h.db.MemberTier.Query().Where(membertier.TenantID(tenantID), membertier.IsDefault(true)).First(ctx); err == nil {
		return t.ID, nil
	}
	if t, err := h.db.MemberTier.Query().Where(membertier.TenantID(refdata.GlobalTenantID), membertier.IsDefault(true)).First(ctx); err == nil {
		return t.ID, nil
	}
	t, err := h.db.MemberTier.Query().Where(tenantOrGlobal).First(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	return t.ID, nil
}
