package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	cr "github.com/bengobox/library-service/internal/ent/circulationrule"
	"github.com/bengobox/library-service/internal/modules/circulation"
)

// CirculationRuleHandler serves the 3D circulation rules matrix admin endpoints.
type CirculationRuleHandler struct {
	db   *ent.Client
	svc  *circulation.Service
	log  *zap.Logger
}

// NewCirculationRuleHandler builds the handler.
func NewCirculationRuleHandler(db *ent.Client, svc *circulation.Service, log *zap.Logger) *CirculationRuleHandler {
	return &CirculationRuleHandler{db: db, svc: svc, log: log}
}

type circulationRuleRequest struct {
	BranchID                   *string `json:"branch_id"`
	TierID                     *string `json:"tier_id"`
	ItemFormat                 *string `json:"item_format"`
	LoanPeriodDays             int     `json:"loan_period_days"`
	LoanPeriodHours            int     `json:"loan_period_hours"`
	IsHourly                   bool    `json:"is_hourly"`
	MaxRenewals                int     `json:"max_renewals"`
	Holdable                   bool    `json:"holdable"`
	FinePerDay                 string  `json:"fine_per_day"`
	GraceDays                  int     `json:"grace_days"`
	MaxFineCap                 string  `json:"max_fine_cap"`
	CapFineAtReplacementPrice  bool    `json:"cap_fine_at_replacement_price"`
	RentalCharge               string  `json:"rental_charge"`
	ReplacementCost            string  `json:"replacement_cost"`
	ProcessingFee              string  `json:"processing_fee"`
	DueDateMode                string  `json:"due_date_mode"`
	Label                      string  `json:"label"`
}

func (req *circulationRuleRequest) finePerDay() decimal.Decimal {
	d, _ := decimal.NewFromString(req.FinePerDay)
	return d
}
func (req *circulationRuleRequest) maxFineCap() decimal.Decimal {
	d, _ := decimal.NewFromString(req.MaxFineCap)
	return d
}
func (req *circulationRuleRequest) rentalCharge() decimal.Decimal {
	d, _ := decimal.NewFromString(req.RentalCharge)
	return d
}
func (req *circulationRuleRequest) replacementCost() decimal.Decimal {
	d, _ := decimal.NewFromString(req.ReplacementCost)
	return d
}
func (req *circulationRuleRequest) processingFee() decimal.Decimal {
	d, _ := decimal.NewFromString(req.ProcessingFee)
	return d
}
func (req *circulationRuleRequest) dueDateMode() cr.DueDateMode {
	if req.DueDateMode == "" {
		return cr.DueDateModeDAYS
	}
	return cr.DueDateMode(req.DueDateMode)
}

// List returns all circulation rules for the tenant, optionally filtered by branch_id.
// GET /admin/circulation-rules?branch_id=<uuid>
func (h *CirculationRuleHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	q := h.db.CirculationRule.Query().Where(cr.TenantIDEQ(tenantID))
	if bid := r.URL.Query().Get("branch_id"); bid != "" {
		if id, err := uuid.Parse(bid); err == nil {
			q = q.Where(cr.BranchIDEQ(id))
		}
	}
	rows, err := q.Order(ent.Asc(cr.FieldBranchID), ent.Asc(cr.FieldTierID)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// Create inserts a new circulation rule.
// POST /admin/circulation-rules
func (h *CirculationRuleHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req circulationRuleRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	if req.LoanPeriodDays <= 0 && !req.IsHourly {
		req.LoanPeriodDays = 14
	}

	c := h.db.CirculationRule.Create().
		SetTenantID(tenantID).
		SetLoanPeriodDays(req.LoanPeriodDays).
		SetLoanPeriodHours(req.LoanPeriodHours).
		SetIsHourly(req.IsHourly).
		SetMaxRenewals(req.MaxRenewals).
		SetHoldable(req.Holdable).
		SetFinePerDay(req.finePerDay()).
		SetGraceDays(req.GraceDays).
		SetMaxFineCap(req.maxFineCap()).
		SetCapFineAtReplacementPrice(req.CapFineAtReplacementPrice).
		SetRentalCharge(req.rentalCharge()).
		SetReplacementCost(req.replacementCost()).
		SetProcessingFee(req.processingFee()).
		SetDueDateMode(req.dueDateMode())

	if req.BranchID != nil && *req.BranchID != "" {
		if id, err := uuid.Parse(*req.BranchID); err == nil {
			c.SetBranchID(id)
		}
	}
	if req.TierID != nil && *req.TierID != "" {
		if id, err := uuid.Parse(*req.TierID); err == nil {
			c.SetTierID(id)
		}
	}
	if req.ItemFormat != nil && *req.ItemFormat != "" {
		c.SetItemFormat(cr.ItemFormat(*req.ItemFormat))
	}
	if req.Label != "" {
		c.SetLabel(req.Label)
	}

	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	h.svc.InvalidateRuleCache(r.Context(), tenantID)
	respondJSON(w, http.StatusCreated, row)
}

// Update modifies an existing circulation rule by ID.
// PUT /admin/circulation-rules/{id}
func (h *CirculationRuleHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.CirculationRule.Query().Where(cr.IDEQ(id), cr.TenantIDEQ(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req circulationRuleRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}

	u := h.db.CirculationRule.UpdateOneID(id).
		SetLoanPeriodDays(req.LoanPeriodDays).
		SetLoanPeriodHours(req.LoanPeriodHours).
		SetIsHourly(req.IsHourly).
		SetMaxRenewals(req.MaxRenewals).
		SetHoldable(req.Holdable).
		SetFinePerDay(req.finePerDay()).
		SetGraceDays(req.GraceDays).
		SetMaxFineCap(req.maxFineCap()).
		SetCapFineAtReplacementPrice(req.CapFineAtReplacementPrice).
		SetRentalCharge(req.rentalCharge()).
		SetReplacementCost(req.replacementCost()).
		SetProcessingFee(req.processingFee()).
		SetDueDateMode(req.dueDateMode())

	if req.BranchID != nil {
		if *req.BranchID == "" {
			u.ClearBranchID()
		} else if id2, err2 := uuid.Parse(*req.BranchID); err2 == nil {
			u.SetBranchID(id2)
		}
	}
	if req.TierID != nil {
		if *req.TierID == "" {
			u.ClearTierID()
		} else if id2, err2 := uuid.Parse(*req.TierID); err2 == nil {
			u.SetTierID(id2)
		}
	}
	if req.ItemFormat != nil {
		if *req.ItemFormat == "" {
			u.ClearItemFormat()
		} else {
			u.SetItemFormat(cr.ItemFormat(*req.ItemFormat))
		}
	}
	u.SetLabel(req.Label)

	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	h.svc.InvalidateRuleCache(r.Context(), tenantID)
	respondJSON(w, http.StatusOK, row)
}

// Delete removes a circulation rule by ID.
// DELETE /admin/circulation-rules/{id}
func (h *CirculationRuleHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.CirculationRule.Query().Where(cr.IDEQ(id), cr.TenantIDEQ(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if err := h.db.CirculationRule.DeleteOneID(id).Exec(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "delete_failed")
		return
	}
	h.svc.InvalidateRuleCache(r.Context(), tenantID)
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
