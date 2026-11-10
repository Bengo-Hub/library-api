package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sharedpagination "github.com/Bengo-Hub/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/modules/barcode"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/fines"
	"github.com/bengobox/library-service/internal/modules/refdata"
	"github.com/bengobox/library-service/internal/modules/sequence"
	membercrm "github.com/bengobox/library-service/internal/modules/members"
)

// MemberHandler serves member + tier + loan-policy endpoints.
type MemberHandler struct {
	db  *ent.Client
	crm *membercrm.Syncer
	log *zap.Logger
}

// NewMemberHandler builds the member handler. crm may be nil (members are then saved unlinked).
func NewMemberHandler(db *ent.Client, crm *membercrm.Syncer, log *zap.Logger) *MemberHandler {
	return &MemberHandler{db: db, crm: crm, log: log}
}

// respondCRMError answers 400 with the CRM's message when it refused the contact and reports
// whether it did.
func respondCRMError(w http.ResponseWriter, err error) bool {
	var rej *membercrm.RejectedError
	if errors.As(err, &rej) {
		respondError(w, http.StatusBadRequest, rej.Message, "crm_rejected")
		return true
	}
	return false
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
	BirthDate    string `json:"birth_date"`
	IsWalkIn     bool   `json:"is_walk_in"`
}

func (req memberRequest) name() string {
	if dn := strings.TrimSpace(req.DisplayName); dn != "" {
		return dn
	}
	return strings.TrimSpace(strings.TrimSpace(req.FirstName) + " " + strings.TrimSpace(req.LastName))
}
func (req memberRequest) email() string   { return firstNonEmpty(req.Email, req.ContactEmail) }
func (req memberRequest) phone() string   { return firstNonEmpty(req.Phone, req.ContactPhone) }
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
	OutstandingFines float64 `json:"outstanding_fines"` // money owed: fines and unpaid fees, net of part payments
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

func (h *MemberHandler) toMemberResponse(r *http.Request, m *ent.Member, tierName string, loans int, owed float64) memberResponse {
	first, last := splitName(m.DisplayName)
	resp := memberResponse{
		ID: m.ID.String(), MembershipNo: m.MembershipNo, FirstName: first, LastName: last,
		FullName: m.DisplayName, Email: m.ContactEmail, Phone: m.ContactPhone,
		TierID: m.TierID.String(), TierName: tierName, Status: strings.ToLower(string(m.Status)),
		Address: m.Address, Notes: m.Notes, ActiveLoans: loans, OutstandingFines: owed,
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
	// Two grouped queries for the whole page instead of two counts per member.
	ids := make([]uuid.UUID, 0, len(rows))
	for _, m := range rows {
		ids = append(ids, m.ID)
	}
	loansBy := map[uuid.UUID]int{}
	if len(ids) > 0 {
		var counts []struct {
			MemberID uuid.UUID `json:"member_id"`
			Count    int       `json:"count"`
		}
		if err := h.db.Loan.Query().
			Where(loan.TenantID(tenantID), loan.MemberIDIn(ids...), loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE)).
			GroupBy(loan.FieldMemberID).Aggregate(ent.Count()).Scan(ctx, &counts); err == nil {
			for _, c := range counts {
				loansBy[c.MemberID] = c.Count
			}
		}
	}
	owed, _ := fines.OutstandingByMember(ctx, h.db, tenantID, ids)
	out := make([]memberResponse, 0, len(rows))
	for _, m := range rows {
		out = append(out, h.toMemberResponse(r, m, tierNames[m.TierID], loansBy[m.ID], owed[m.ID].InexactFloat64()))
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
	params := sharedpagination.Parse(r)
	q := h.db.Member.Query().Where(member.TenantID(tenantID))
	if s := r.URL.Query().Get("q"); s != "" {
		q = q.Where(member.Or(member.DisplayNameContainsFold(s), member.MembershipNoContainsFold(s), member.ContactPhoneContainsFold(s), member.ContactEmailContainsFold(s)))
	}
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		q = q.Where(member.StatusEQ(member.Status(strings.ToUpper(st))))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(member.FieldCreatedAt)).Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(h.buildMemberResponses(r, tenantID, rows), total, params))
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

	// Link to the CRM first, sending phone and email, so the name, phone and email saved here
	// are the CRM's. A refusal is the form's fault (400); an outage saves the member unlinked.
	var ident membercrm.Identity
	if cid, perr := uuid.Parse(req.crm()); perr == nil {
		ident.Name = req.name()
		ident.Phone, ident.Email = membercrm.Normalise(req.phone(), req.email())
		ident.CrmContactID = &cid
	} else {
		ident, err = h.crm.Link(r.Context(), tenantID, req.name(), req.phone(), req.email())
		if respondCRMError(w, err) {
			return
		}
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
	now := time.Now()
	c := tx.Member.Create().
		SetTenantID(tenantID).SetMembershipNo(memberNo).SetTierID(tierID).
		SetDisplayName(ident.Name).SetContactPhone(ident.Phone).
		SetContactEmail(ident.Email).SetAddress(req.Address).SetNotes(req.Notes).SetIsWalkIn(req.IsWalkIn).
		SetJoinedAt(now)
	if st := strings.TrimSpace(req.Status); st != "" {
		c.SetStatus(member.Status(strings.ToUpper(st)))
	}
	if uid, perr := uuid.Parse(req.ssoUser()); perr == nil {
		c.SetUserID(uid)
	}
	if ident.CrmContactID != nil {
		c.SetCrmContactID(*ident.CrmContactID)
	}
	if bid, perr := uuid.Parse(req.branch()); perr == nil {
		c.SetHomeBranchID(bid)
	}
	if bd, ok := parseDate(req.BirthDate); ok {
		c.SetBirthDate(bd)
	}
	// Honor explicit expires_at; otherwise auto-compute from tier enrollment period.
	if t, ok := parseDate(req.ExpiresAt); ok {
		c.SetExpiresAt(t)
	} else if tier, terr := tx.MemberTier.Get(r.Context(), tierID); terr == nil && tier.EnrollmentPeriodMonths != nil {
		c.SetExpiresAt(now.AddDate(0, *tier.EnrollmentPeriodMonths, 0))
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
	current, err := h.db.Member.Query().Where(member.IDEQ(id), member.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	var req memberRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.Member.UpdateOneID(id)
	// Name, phone and email belong to the CRM: an edit goes to the CRM first (or links an
	// unlinked member) and the member keeps what the CRM answers.
	crmID := current.CrmContactID
	explicitCRM := false
	if cid, perr := uuid.Parse(req.crm()); perr == nil {
		crmID = &cid
		explicitCRM = true
	}
	name := firstNonEmpty(req.name(), current.DisplayName)
	phone, email := membercrm.Normalise(firstNonEmpty(req.phone(), current.ContactPhone), firstNonEmpty(req.email(), current.ContactEmail))
	curPhone, curEmail := membercrm.Normalise(current.ContactPhone, current.ContactEmail)
	// Only a real change of name, phone or email (or an explicit contact id) goes to the CRM,
	// so a status toggle that resends the name does not call it.
	identityChanged := strings.TrimSpace(name) != strings.TrimSpace(current.DisplayName) || phone != curPhone || email != curEmail
	if identityChanged || explicitCRM {
		// A staff edit lifts the "contact deleted in the CRM" flag so the member can link again.
		if md, changed := membercrm.ClearUnlinked(current.Metadata); changed {
			u.SetMetadata(md)
		}
	}
	if identityChanged {
		ident, serr := h.crm.Save(r.Context(), tenantID, id, crmID, name, phone, email)
		if respondCRMError(w, serr) {
			return
		}
		if ident.Name != "" {
			u.SetDisplayName(ident.Name)
		}
		u.SetContactPhone(ident.Phone).SetContactEmail(ident.Email)
		if ident.CrmContactID != nil {
			crmID = ident.CrmContactID
		}
	}
	if crmID != nil {
		u.SetCrmContactID(*crmID)
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
	if n, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.MemberID(id), loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE)).Count(ctx); n > 0 {
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

// MemberCard renders a printable membership card (PDF) with a scannable CODE128 barcode of the
// membership number — used at the self-checkout kiosk to resolve the member.
// @Router /{tenant}/library/members/{id}/card.pdf [get]
func (h *MemberHandler) MemberCard(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	m, err := h.db.Member.Query().Where(member.IDEQ(id), member.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "load_failed")
		return
	}
	card := barcode.MemberCard{Name: m.DisplayName, MembershipNo: m.MembershipNo}
	if t, terr := h.db.Tenant.Get(r.Context(), tenantID); terr == nil {
		card.Org = t.Name
	}
	if tier, terr := h.db.MemberTier.Get(r.Context(), m.TierID); terr == nil {
		card.Tier = tier.Name
	}
	if m.ExpiresAt != nil {
		card.ExpiresAt = m.ExpiresAt.Format("Jan 2006")
	}
	pdf, err := barcode.RenderMemberCard(card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "render_failed")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"member-card-"+m.MembershipNo+".pdf\"")
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	_, _ = w.Write(pdf)
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
	lq := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.MemberID(id))
	params := sharedpagination.Parse(r)
	total, _ := lq.Clone().Count(r.Context())
	rows, err := lq.Order(ent.Desc(loan.FieldCheckoutAt)).Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(rows, total, params))
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
	fq := h.db.Fine.Query().Where(fine.TenantID(tenantID), fine.MemberID(id))
	params := sharedpagination.Parse(r)
	total, _ := fq.Clone().Count(r.Context())
	rows, err := fq.Order(ent.Desc(fine.FieldCreatedAt)).Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(rows, total, params))
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
