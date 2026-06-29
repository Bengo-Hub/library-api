package handlers

import (
	"net/http"
	"strings"

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

// memberRequest accepts the library-ui MemberInput contract (first_name/last_name/email/phone/…)
// as well as the legacy display_name/contact_* names, so both the form and internal callers work.
type memberRequest struct {
	MembershipNo string `json:"membership_no"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	ContactEmail string `json:"contact_email"`
	Phone        string `json:"phone"`
	ContactPhone string `json:"contact_phone"`
	TierID       string `json:"tier_id"`
	Status       string `json:"status"`
	SSOUserID    string `json:"sso_user_id"`
	UserID       string `json:"user_id"`
	CRMCustomer  string `json:"crm_customer_id"`
	CRMContactID string `json:"crm_contact_id"`
	BranchID     string `json:"branch_id"`
	HomeBranchID string `json:"home_branch_id"`
	Address      string `json:"address"`
	Notes        string `json:"notes"`
	ExpiresAt    string `json:"expires_at"`
	IsWalkIn     bool   `json:"is_walk_in"`
}

func (req memberRequest) name() string {
	if dn := strings.TrimSpace(req.DisplayName); dn != "" {
		return dn
	}
	return strings.TrimSpace(strings.TrimSpace(req.FirstName) + " " + strings.TrimSpace(req.LastName))
}
func (req memberRequest) email() string { return firstNonEmpty(req.Email, req.ContactEmail) }
func (req memberRequest) phone() string { return firstNonEmpty(req.Phone, req.ContactPhone) }
func (req memberRequest) ssoUser() string { return firstNonEmpty(req.SSOUserID, req.UserID) }
func (req memberRequest) crm() string     { return firstNonEmpty(req.CRMCustomer, req.CRMContactID) }
func (req memberRequest) branch() string  { return firstNonEmpty(req.BranchID, req.HomeBranchID) }

// memberResponse is the library-ui Member contract: display_name split into full/first/last,
// contact_* surfaced as email/phone, status lower-cased, tier name + summary counts resolved.
type memberResponse struct {
	ID               string  `json:"id"`
	MembershipNo     string  `json:"membership_no"`
	FirstName        string  `json:"first_name"`
	LastName         string  `json:"last_name"`
	FullName         string  `json:"full_name"`
	Email            string  `json:"email,omitempty"`
	Phone            string  `json:"phone,omitempty"`
	TierID           string  `json:"tier_id,omitempty"`
	TierName         string  `json:"tier_name,omitempty"`
	Status           string  `json:"status"`
	SSOUserID        string  `json:"sso_user_id,omitempty"`
	CRMCustomerID    string  `json:"crm_customer_id,omitempty"`
	BranchID         string  `json:"branch_id,omitempty"`
	Address          string  `json:"address,omitempty"`
	Notes            string  `json:"notes,omitempty"`
	JoinedAt         *string `json:"joined_at,omitempty"`
	ExpiresAt        *string `json:"expires_at,omitempty"`
	ActiveLoans      int     `json:"active_loans"`
	OutstandingFines int     `json:"outstanding_fines"`
	CreatedAt        string  `json:"created_at,omitempty"`
}

func splitName(full string) (first, last string) {
	full = strings.TrimSpace(full)
	if full == "" {
		return "", ""
	}
	parts := strings.SplitN(full, " ", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (h *MemberHandler) toMemberResponse(r *http.Request, m *ent.Member, tierName string, loans, fines int) memberResponse {
	first, last := splitName(m.DisplayName)
	resp := memberResponse{
		ID: m.ID.String(), MembershipNo: m.MembershipNo, FirstName: first, LastName: last,
		FullName: m.DisplayName, Email: m.ContactEmail, Phone: m.ContactPhone,
		TierID: m.TierID.String(), TierName: tierName, Status: strings.ToLower(string(m.Status)),
		Address: m.Address, Notes: m.Notes, ActiveLoans: loans, OutstandingFines: fines,
		CreatedAt: m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if m.UserID != nil {
		resp.SSOUserID = m.UserID.String()
	}
	if m.CrmContactID != nil {
		resp.CRMCustomerID = m.CrmContactID.String()
	}
	if m.HomeBranchID != nil {
		resp.BranchID = m.HomeBranchID.String()
	}
	if m.ExpiresAt != nil {
		s := m.ExpiresAt.Format("2006-01-02")
		resp.ExpiresAt = &s
	}
	if m.JoinedAt != nil {
		s := m.JoinedAt.Format("2006-01-02")
		resp.JoinedAt = &s
	}
	return resp
}

// buildMemberResponses batch-resolves tier names + active-loan / outstanding-fine counts.
func (h *MemberHandler) buildMemberResponses(r *http.Request, tenantID uuid.UUID, rows []*ent.Member) []memberResponse {
	ctx := r.Context()
	tierNames := map[uuid.UUID]string{}
	tiers, _ := h.db.MemberTier.Query().
		Where(membertier.Or(membertier.TenantID(tenantID), membertier.TenantID(refdata.GlobalTenantID))).All(ctx)
	for _, t := range tiers {
		tierNames[t.ID] = t.Name
	}
	out := make([]memberResponse, 0, len(rows))
	for _, m := range rows {
		loans, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.MemberID(m.ID), loan.StatusEQ(loan.StatusACTIVE)).Count(ctx)
		fines, _ := h.db.Fine.Query().Where(fine.TenantID(tenantID), fine.MemberID(m.ID), fine.StatusEQ(fine.StatusUNPAID)).Count(ctx)
		out = append(out, h.toMemberResponse(r, m, tierNames[m.TierID], loans, fines))
	}
	return out
}

func (h *MemberHandler) singleMemberResponse(r *http.Request, tenantID uuid.UUID, m *ent.Member) memberResponse {
	out := h.buildMemberResponses(r, tenantID, []*ent.Member{m})
	return out[0]
}

// ListMembers godoc
// @Router /{tenant}/library/members [get]
func (h *MemberHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	limit, offset := PageParams(r)
	q := h.db.Member.Query().Where(member.TenantID(tenantID))
	if s := r.URL.Query().Get("q"); s != "" {
		q = q.Where(member.Or(member.DisplayNameContainsFold(s), member.MembershipNoContainsFold(s), member.ContactPhoneContainsFold(s), member.ContactEmailContainsFold(s)))
	}
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		q = q.Where(member.StatusEQ(member.Status(strings.ToUpper(st))))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(member.FieldCreatedAt)).Limit(limit).Offset(offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: h.buildMemberResponses(r, tenantID, rows), Total: total})
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
	// Honor an explicit membership_no, else allocate via the document-sequence.
	memberNo := strings.TrimSpace(req.MembershipNo)
	if memberNo == "" {
		memberNo, err = sequence.Next(r.Context(), tx, tenantID, sequence.KindMembership, "MBR", 5)
		if err != nil {
			_ = tx.Rollback()
			respondError(w, http.StatusInternalServerError, err.Error(), "sequence_failed")
			return
		}
	}
	c := tx.Member.Create().
		SetTenantID(tenantID).SetMembershipNo(memberNo).SetTierID(tierID).
		SetDisplayName(req.name()).SetContactPhone(req.phone()).
		SetContactEmail(req.email()).SetAddress(req.Address).SetNotes(req.Notes).SetIsWalkIn(req.IsWalkIn)
	if st := strings.TrimSpace(req.Status); st != "" {
		c.SetStatus(member.Status(strings.ToUpper(st)))
	}
	if uid, perr := uuid.Parse(req.ssoUser()); perr == nil {
		c.SetUserID(uid)
	}
	if cid, perr := uuid.Parse(req.crm()); perr == nil {
		c.SetCrmContactID(cid)
	}
	if bid, perr := uuid.Parse(req.branch()); perr == nil {
		c.SetHomeBranchID(bid)
	}
	if t, ok := parseDate(req.ExpiresAt); ok {
		c.SetExpiresAt(t)
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
	respondJSON(w, http.StatusCreated, h.singleMemberResponse(r, tenantID, row))
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
	respondJSON(w, http.StatusOK, h.singleMemberResponse(r, tenantID, row))
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
	var req memberRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.Member.UpdateOneID(id)
	if n := req.name(); n != "" {
		u.SetDisplayName(n)
	}
	if p := req.phone(); p != "" {
		u.SetContactPhone(p)
	}
	if e := req.email(); e != "" {
		u.SetContactEmail(e)
	}
	u.SetAddress(req.Address).SetNotes(req.Notes)
	if st := strings.TrimSpace(req.Status); st != "" {
		u.SetStatus(member.Status(strings.ToUpper(st)))
	}
	if tid, perr := uuid.Parse(req.TierID); perr == nil {
		u.SetTierID(tid)
	}
	if uid, perr := uuid.Parse(req.ssoUser()); perr == nil {
		u.SetUserID(uid)
	}
	if cid, perr := uuid.Parse(req.crm()); perr == nil {
		u.SetCrmContactID(cid)
	}
	if bid, perr := uuid.Parse(req.branch()); perr == nil {
		u.SetHomeBranchID(bid)
	}
	if t, ok := parseDate(req.ExpiresAt); ok {
		u.SetExpiresAt(t)
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, h.singleMemberResponse(r, tenantID, row))
}

// DeleteMember removes a member (blocked when they have active loans or unpaid fines).
// @Router /{tenant}/library/members/{id} [delete]
func (h *MemberHandler) DeleteMember(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	ctx := r.Context()
	exists, _ := h.db.Member.Query().Where(member.IDEQ(id), member.TenantID(tenantID)).Exist(ctx)
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if n, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.MemberID(id), loan.StatusEQ(loan.StatusACTIVE)).Count(ctx); n > 0 {
		respondError(w, http.StatusConflict, "member has active loans — return them first", "has_active_loans")
		return
	}
	if n, _ := h.db.Fine.Query().Where(fine.TenantID(tenantID), fine.MemberID(id), fine.StatusIn(fine.StatusUNPAID, fine.StatusPARTIAL)).Count(ctx); n > 0 {
		respondError(w, http.StatusConflict, "member has unpaid fines — settle or waive them first", "has_unpaid_fines")
		return
	}
	if err := h.db.Member.DeleteOneID(id).Exec(ctx); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "delete_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": true})
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
