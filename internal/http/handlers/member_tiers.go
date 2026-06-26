package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent/loanpolicy"
	"github.com/bengobox/library-service/internal/ent/membertier"
)

type tierRequest struct {
	Name                 string `json:"name"`
	MaxConcurrentLoans   int    `json:"max_concurrent_loans"`
	LoanPeriodDays       int    `json:"loan_period_days"`
	MaxRenewals          int    `json:"max_renewals"`
	HoldLimit            int    `json:"hold_limit"`
	EbookConcurrentLimit int    `json:"ebook_concurrent_limit"`
	DailyFineRate        string `json:"daily_fine_rate"`
	MaxFineBeforeBlock   string `json:"max_fine_before_block"`
	AnnualFee            string `json:"annual_fee"`
	IsDefault            bool   `json:"is_default"`
}

// ListTiers godoc
// @Router /{tenant}/library/member-tiers [get]
func (h *MemberHandler) ListTiers(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.MemberTier.Query().Where(membertier.TenantID(tenantID)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
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
	c := h.db.MemberTier.Create().SetTenantID(tenantID).SetName(req.Name).SetIsDefault(req.IsDefault)
	if req.MaxConcurrentLoans > 0 {
		c.SetMaxConcurrentLoans(req.MaxConcurrentLoans)
	}
	if req.LoanPeriodDays > 0 {
		c.SetLoanPeriodDays(req.LoanPeriodDays)
	}
	if req.MaxRenewals > 0 {
		c.SetMaxRenewals(req.MaxRenewals)
	}
	if req.HoldLimit > 0 {
		c.SetHoldLimit(req.HoldLimit)
	}
	if req.EbookConcurrentLimit > 0 {
		c.SetEbookConcurrentLimit(req.EbookConcurrentLimit)
	}
	if d, err := decimal.NewFromString(req.DailyFineRate); err == nil {
		c.SetDailyFineRate(d)
	}
	if d, err := decimal.NewFromString(req.MaxFineBeforeBlock); err == nil {
		c.SetMaxFineBeforeBlock(d)
	}
	if d, err := decimal.NewFromString(req.AnnualFee); err == nil {
		c.SetAnnualFee(d)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// UpdateTier godoc
// @Router /{tenant}/library/member-tiers/{id} [put]
func (h *MemberHandler) UpdateTier(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.MemberTier.Query().Where(membertier.IDEQ(id), membertier.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req tierRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	u := h.db.MemberTier.UpdateOneID(id)
	if req.Name != "" {
		u.SetName(req.Name)
	}
	if req.MaxConcurrentLoans > 0 {
		u.SetMaxConcurrentLoans(req.MaxConcurrentLoans)
	}
	if req.LoanPeriodDays > 0 {
		u.SetLoanPeriodDays(req.LoanPeriodDays)
	}
	if req.MaxRenewals > 0 {
		u.SetMaxRenewals(req.MaxRenewals)
	}
	u.SetIsDefault(req.IsDefault)
	if d, err := decimal.NewFromString(req.DailyFineRate); err == nil {
		u.SetDailyFineRate(d)
	}
	if d, err := decimal.NewFromString(req.MaxFineBeforeBlock); err == nil {
		u.SetMaxFineBeforeBlock(d)
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, row)
}

type policyRequest struct {
	Name           string `json:"name"`
	LoanPeriodDays int    `json:"loan_period_days"`
	MaxRenewals    int    `json:"max_renewals"`
	Holdable       bool   `json:"holdable"`
	FinePerDay     string `json:"fine_per_day"`
	GraceDays      int    `json:"grace_days"`
	IsDefault      bool   `json:"is_default"`
}

// ListPolicies godoc
// @Router /{tenant}/library/loan-policies [get]
func (h *MemberHandler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.LoanPolicy.Query().Where(loanpolicy.TenantID(tenantID)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
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
		SetHoldable(req.Holdable).SetGraceDays(req.GraceDays).SetIsDefault(req.IsDefault)
	if req.LoanPeriodDays > 0 {
		c.SetLoanPeriodDays(req.LoanPeriodDays)
	}
	if req.MaxRenewals > 0 {
		c.SetMaxRenewals(req.MaxRenewals)
	}
	if d, err := decimal.NewFromString(req.FinePerDay); err == nil {
		c.SetFinePerDay(d)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}
