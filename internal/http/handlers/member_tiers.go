package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/loanpolicy"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/modules/refdata"
)

// tierRequest accepts the library-ui MemberTierInput contract (max_loans/max_holds/membership_fee
// as numbers) as well as the legacy names, so the form and internal callers both work.
type tierRequest struct {
	Name                 string  `json:"name"`
	MaxLoans             int     `json:"max_loans"`
	MaxConcurrentLoans   int     `json:"max_concurrent_loans"`
	LoanPeriodDays       int     `json:"loan_period_days"`
	MaxRenewals          int     `json:"max_renewals"`
	MaxHolds             int     `json:"max_holds"`
	HoldLimit            int     `json:"hold_limit"`
	EbookConcurrentLimit int     `json:"ebook_concurrent_limit"`
	DailyFineRate        float64 `json:"daily_fine_rate"`
	MaxFineBeforeBlock   float64 `json:"max_fine_before_block"`
	MembershipFee        float64 `json:"membership_fee"`
	AnnualFee            float64 `json:"annual_fee"`
	Description          string  `json:"description"`
	IsDefault            bool    `json:"is_default"`
}

func (r tierRequest) loans() int {
	if r.MaxLoans > 0 {
		return r.MaxLoans
	}
	return r.MaxConcurrentLoans
}
func (r tierRequest) holds() int {
	if r.MaxHolds > 0 {
		return r.MaxHolds
	}
	return r.HoldLimit
}
func (r tierRequest) fee() float64 {
	if r.MembershipFee > 0 {
		return r.MembershipFee
	}
	return r.AnnualFee
}

// tierResponse is the library-ui MemberTier contract.
type tierResponse struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	MaxLoans             int     `json:"max_loans"`
	LoanPeriodDays       int     `json:"loan_period_days"`
	MaxRenewals          int     `json:"max_renewals"`
	MaxHolds             int     `json:"max_holds"`
	EbookConcurrentLimit int     `json:"ebook_concurrent_limit"`
	DailyFineRate        float64 `json:"daily_fine_rate"`
	MembershipFee        float64 `json:"membership_fee"`
	IsDefault            bool    `json:"is_default"`
	IsGlobal             bool    `json:"is_global"`
}

func toTierResponse(t *ent.MemberTier) tierResponse {
	return tierResponse{
		ID: t.ID.String(), Name: t.Name, MaxLoans: t.MaxConcurrentLoans, LoanPeriodDays: t.LoanPeriodDays,
		MaxRenewals: t.MaxRenewals, MaxHolds: t.HoldLimit, EbookConcurrentLimit: t.EbookConcurrentLimit,
		DailyFineRate: t.DailyFineRate.InexactFloat64(), MembershipFee: t.AnnualFee.InexactFloat64(),
		IsDefault: t.IsDefault, IsGlobal: t.TenantID == refdata.GlobalTenantID,
	}
}

// ListTiers returns tenant tiers + global default tiers not shadowed by a same-named tenant tier.
// @Router /{tenant}/library/member-tiers [get]
func (h *MemberHandler) ListTiers(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.MemberTier.Query().
		Where(membertier.Or(membertier.TenantID(tenantID), membertier.TenantID(refdata.GlobalTenantID))).
		Order(ent.Asc(membertier.FieldName)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	tenantNames := map[string]bool{}
	for _, t := range rows {
		if t.TenantID == tenantID {
			tenantNames[strings.ToLower(t.Name)] = true
		}
	}
	out := make([]tierResponse, 0, len(rows))
	for _, t := range rows {
		if t.TenantID == refdata.GlobalTenantID && tenantNames[strings.ToLower(t.Name)] {
			continue // a tenant tier overrides the global of the same name
		}
		out = append(out, toTierResponse(t))
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

func applyTier(c *ent.MemberTierCreate, req tierRequest) {
	c.SetMaxConcurrentLoans(req.loans()).SetLoanPeriodDays(req.LoanPeriodDays).
		SetMaxRenewals(req.MaxRenewals).SetHoldLimit(req.holds()).
		SetDailyFineRate(decimal.NewFromFloat(req.DailyFineRate)).
		SetMaxFineBeforeBlock(decimal.NewFromFloat(req.MaxFineBeforeBlock)).
		SetAnnualFee(decimal.NewFromFloat(req.fee())).SetIsDefault(req.IsDefault)
	if req.EbookConcurrentLimit > 0 {
		c.SetEbookConcurrentLimit(req.EbookConcurrentLimit)
	}
}

// CreateTier godoc
// @Router /{tenant}/library/member-tiers [post]
func (h *MemberHandler) CreateTier(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req tierRequest
	if err := Decode(r, &req); err != nil || req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required", "invalid_request")
		return
	}
	c := h.db.MemberTier.Create().SetTenantID(tenantID).SetName(req.Name)
	applyTier(c, req)
	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, toTierResponse(row))
}

// UpdateTier edits a tenant tier; editing a shared global tier transparently creates an editable
// tenant-owned copy (copy-on-write) so admins can customize the seeded defaults.
// @Router /{tenant}/library/member-tiers/{id} [put]
func (h *MemberHandler) UpdateTier(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	existing, err := h.db.MemberTier.Query().
		Where(membertier.IDEQ(id), membertier.Or(membertier.TenantID(tenantID), membertier.TenantID(refdata.GlobalTenantID))).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "load_failed")
		return
	}
	var req tierRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	name := req.Name
	if name == "" {
		name = existing.Name
	}
	// Copy-on-write: editing a global tier creates a tenant-owned copy.
	if existing.TenantID == refdata.GlobalTenantID {
		c := h.db.MemberTier.Create().SetTenantID(tenantID).SetName(name)
		applyTier(c, req)
		row, cerr := c.Save(r.Context())
		if cerr != nil {
			respondError(w, http.StatusInternalServerError, cerr.Error(), "create_failed")
			return
		}
		respondJSON(w, http.StatusOK, toTierResponse(row))
		return
	}
	u := h.db.MemberTier.UpdateOneID(id).SetName(name).
		SetMaxConcurrentLoans(req.loans()).SetLoanPeriodDays(req.LoanPeriodDays).
		SetMaxRenewals(req.MaxRenewals).SetHoldLimit(req.holds()).
		SetDailyFineRate(decimal.NewFromFloat(req.DailyFineRate)).
		SetAnnualFee(decimal.NewFromFloat(req.fee())).SetIsDefault(req.IsDefault)
	if req.EbookConcurrentLimit > 0 {
		u.SetEbookConcurrentLimit(req.EbookConcurrentLimit)
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, toTierResponse(row))
}

// policyRequest accepts the library-ui LoanPolicyInput (daily_fine_rate/grace_period_days) + legacy names.
type policyRequest struct {
	Name            string  `json:"name"`
	LoanPeriodDays  int     `json:"loan_period_days"`
	MaxRenewals     int     `json:"max_renewals"`
	Holdable        bool    `json:"holdable"`
	DailyFineRate   float64 `json:"daily_fine_rate"`
	FinePerDay      float64 `json:"fine_per_day"`
	GracePeriodDays int     `json:"grace_period_days"`
	GraceDays       int     `json:"grace_days"`
	IsDefault       bool    `json:"is_default"`
}

func (r policyRequest) fine() float64 {
	if r.DailyFineRate > 0 {
		return r.DailyFineRate
	}
	return r.FinePerDay
}
func (r policyRequest) grace() int {
	if r.GracePeriodDays > 0 {
		return r.GracePeriodDays
	}
	return r.GraceDays
}

type policyResponse struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	LoanPeriodDays  int     `json:"loan_period_days"`
	MaxRenewals     int     `json:"max_renewals"`
	Holdable        bool    `json:"holdable"`
	DailyFineRate   float64 `json:"daily_fine_rate"`
	GracePeriodDays int     `json:"grace_period_days"`
	IsDefault       bool    `json:"is_default"`
	IsGlobal        bool    `json:"is_global"`
}

func toPolicyResponse(p *ent.LoanPolicy) policyResponse {
	return policyResponse{
		ID: p.ID.String(), Name: p.Name, LoanPeriodDays: p.LoanPeriodDays, MaxRenewals: p.MaxRenewals,
		Holdable: p.Holdable, DailyFineRate: p.FinePerDay.InexactFloat64(), GracePeriodDays: p.GraceDays,
		IsDefault: p.IsDefault, IsGlobal: p.TenantID == refdata.GlobalTenantID,
	}
}

// ListPolicies godoc
// @Router /{tenant}/library/loan-policies [get]
func (h *MemberHandler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.LoanPolicy.Query().
		Where(loanpolicy.Or(loanpolicy.TenantID(tenantID), loanpolicy.TenantID(refdata.GlobalTenantID))).
		Order(ent.Asc(loanpolicy.FieldName)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	tenantNames := map[string]bool{}
	for _, p := range rows {
		if p.TenantID == tenantID {
			tenantNames[strings.ToLower(p.Name)] = true
		}
	}
	out := make([]policyResponse, 0, len(rows))
	for _, p := range rows {
		if p.TenantID == refdata.GlobalTenantID && tenantNames[strings.ToLower(p.Name)] {
			continue
		}
		out = append(out, toPolicyResponse(p))
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

// CreatePolicy godoc
// @Router /{tenant}/library/loan-policies [post]
func (h *MemberHandler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req policyRequest
	if err := Decode(r, &req); err != nil || req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required", "invalid_request")
		return
	}
	c := h.db.LoanPolicy.Create().SetTenantID(tenantID).SetName(req.Name).
		SetHoldable(req.Holdable).SetGraceDays(req.grace()).SetIsDefault(req.IsDefault).
		SetFinePerDay(decimal.NewFromFloat(req.fine()))
	if req.LoanPeriodDays > 0 {
		c.SetLoanPeriodDays(req.LoanPeriodDays)
	}
	if req.MaxRenewals > 0 {
		c.SetMaxRenewals(req.MaxRenewals)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, toPolicyResponse(row))
}

// UpdatePolicy edits a tenant policy; editing a shared global policy creates a tenant-owned copy.
// @Router /{tenant}/library/loan-policies/{id} [put]
func (h *MemberHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	existing, err := h.db.LoanPolicy.Query().
		Where(loanpolicy.IDEQ(id), loanpolicy.Or(loanpolicy.TenantID(tenantID), loanpolicy.TenantID(refdata.GlobalTenantID))).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "load_failed")
		return
	}
	var req policyRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	name := req.Name
	if name == "" {
		name = existing.Name
	}
	if existing.TenantID == refdata.GlobalTenantID {
		c := h.db.LoanPolicy.Create().SetTenantID(tenantID).SetName(name).
			SetHoldable(req.Holdable).SetGraceDays(req.grace()).SetIsDefault(req.IsDefault).
			SetLoanPeriodDays(req.LoanPeriodDays).SetMaxRenewals(req.MaxRenewals).
			SetFinePerDay(decimal.NewFromFloat(req.fine()))
		row, cerr := c.Save(r.Context())
		if cerr != nil {
			respondError(w, http.StatusInternalServerError, cerr.Error(), "create_failed")
			return
		}
		respondJSON(w, http.StatusOK, toPolicyResponse(row))
		return
	}
	row, err := h.db.LoanPolicy.UpdateOneID(id).SetName(name).
		SetHoldable(req.Holdable).SetGraceDays(req.grace()).SetIsDefault(req.IsDefault).
		SetLoanPeriodDays(req.LoanPeriodDays).SetMaxRenewals(req.MaxRenewals).
		SetFinePerDay(decimal.NewFromFloat(req.fine())).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, toPolicyResponse(row))
}
