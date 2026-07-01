package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent/acquisitionbudget"
	"github.com/bengobox/library-service/internal/ent/acquisitionfund"
)

type budgetRequest struct {
	Name        string  `json:"name"`
	FiscalYear  int     `json:"fiscal_year"`
	TotalAmount float64 `json:"total_amount"`
	Notes       string  `json:"notes"`
}

type fundRequest struct {
	Name            string  `json:"name"`
	Code            string  `json:"code"`
	AllocatedAmount float64 `json:"allocated_amount"`
	Description     string  `json:"description"`
}

func (h *AcquisitionHandler) ListBudgets(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	q := h.db.AcquisitionBudget.Query().Where(acquisitionbudget.TenantIDEQ(tenantID))
	if fy := r.URL.Query().Get("fiscal_year"); fy != "" {
		if n, err := strconv.Atoi(fy); err == nil {
			q = q.Where(acquisitionbudget.FiscalYearEQ(n))
		}
	}
	rows, err := q.Order(acquisitionbudget.ByFiscalYear()).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list budgets", "internal")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

func (h *AcquisitionHandler) CreateBudget(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	var req budgetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required", "validation_error")
		return
	}
	b, err := h.db.AcquisitionBudget.Create().
		SetTenantID(tenantID).SetName(req.Name).SetFiscalYear(req.FiscalYear).
		SetTotalAmount(decimal.NewFromFloat(req.TotalAmount)).
		SetNotes(req.Notes).
		Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create budget", "internal")
		return
	}
	respondJSON(w, http.StatusCreated, b)
}

func (h *AcquisitionHandler) UpdateBudget(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id", "invalid_id")
		return
	}
	exists, _ := h.db.AcquisitionBudget.Query().Where(acquisitionbudget.IDEQ(id), acquisitionbudget.TenantIDEQ(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "budget not found", "not_found")
		return
	}
	var req budgetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	u := h.db.AcquisitionBudget.UpdateOneID(id)
	if req.Name != "" {
		u = u.SetName(req.Name)
	}
	if req.FiscalYear != 0 {
		u = u.SetFiscalYear(req.FiscalYear)
	}
	if req.TotalAmount > 0 {
		u = u.SetTotalAmount(decimal.NewFromFloat(req.TotalAmount))
	}
	if req.Notes != "" {
		u = u.SetNotes(req.Notes)
	}
	b, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update budget", "internal")
		return
	}
	respondJSON(w, http.StatusOK, b)
}

func (h *AcquisitionHandler) ListFunds(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	budgetID, err := uuid.Parse(chi.URLParam(r, "budget_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid budget id", "invalid_id")
		return
	}
	rows, err := h.db.AcquisitionFund.Query().
		Where(acquisitionfund.TenantIDEQ(tenantID), acquisitionfund.BudgetIDEQ(budgetID)).
		Order(acquisitionfund.ByName()).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list funds", "internal")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

func (h *AcquisitionHandler) CreateFund(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	budgetID, err := uuid.Parse(chi.URLParam(r, "budget_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid budget id", "invalid_id")
		return
	}
	var req fundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required", "validation_error")
		return
	}
	c := h.db.AcquisitionFund.Create().
		SetTenantID(tenantID).SetBudgetID(budgetID).SetName(req.Name).
		SetAllocatedAmount(decimal.NewFromFloat(req.AllocatedAmount))
	if req.Code != "" {
		c = c.SetCode(req.Code)
	}
	if req.Description != "" {
		c = c.SetDescription(req.Description)
	}
	f, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create fund", "internal")
		return
	}
	respondJSON(w, http.StatusCreated, f)
}
