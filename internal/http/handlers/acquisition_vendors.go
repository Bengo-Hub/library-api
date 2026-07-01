package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/vendor"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

// AcquisitionHandler serves vendor, budget/fund, purchase-order, and invoice endpoints.
type AcquisitionHandler struct {
	db       *ent.Client
	treasury *treasury.Client
	log      *zap.Logger
}

// NewAcquisitionHandler creates the acquisitions handler.
func NewAcquisitionHandler(db *ent.Client, tc *treasury.Client, log *zap.Logger) *AcquisitionHandler {
	return &AcquisitionHandler{db: db, treasury: tc, log: log}
}

type vendorRequest struct {
	Name          string `json:"name"`
	Code          string `json:"code"`
	ContactName   string `json:"contact_name"`
	ContactEmail  string `json:"contact_email"`
	ContactPhone  string `json:"contact_phone"`
	Address       string `json:"address"`
	Website       string `json:"website"`
	AccountNumber string `json:"account_number"`
	PaymentTerms  string `json:"payment_terms"`
	Notes         string `json:"notes"`
	IsActive      *bool  `json:"is_active"`
}

func (h *AcquisitionHandler) ListVendors(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	q := h.db.Vendor.Query().Where(vendor.TenantID(tenantID))
	if active := r.URL.Query().Get("active"); active == "true" {
		q = q.Where(vendor.IsActive(true))
	}
	rows, err := q.Order(vendor.ByName()).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list vendors", "internal")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

func (h *AcquisitionHandler) GetVendor(w http.ResponseWriter, r *http.Request) {
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
	v, err := h.db.Vendor.Query().Where(vendor.IDEQ(id), vendor.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "vendor not found", "not_found")
		return
	}
	respondJSON(w, http.StatusOK, v)
}

func (h *AcquisitionHandler) CreateVendor(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	var req vendorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required", "validation_error")
		return
	}
	terms := vendor.PaymentTermsNET_30
	if t := vendorTerms(req.PaymentTerms); t != "" {
		terms = t
	}
	c := h.db.Vendor.Create().
		SetTenantID(tenantID).SetName(req.Name).SetPaymentTerms(terms).SetIsActive(true)
	if req.Code != "" {
		c = c.SetCode(req.Code)
	}
	if req.ContactName != "" {
		c = c.SetContactName(req.ContactName)
	}
	if req.ContactEmail != "" {
		c = c.SetContactEmail(req.ContactEmail)
	}
	if req.ContactPhone != "" {
		c = c.SetContactPhone(req.ContactPhone)
	}
	if req.Address != "" {
		c = c.SetAddress(req.Address)
	}
	if req.Website != "" {
		c = c.SetWebsite(req.Website)
	}
	if req.AccountNumber != "" {
		c = c.SetAccountNumber(req.AccountNumber)
	}
	if req.Notes != "" {
		c = c.SetNotes(req.Notes)
	}
	v, err := c.Save(r.Context())
	if err != nil {
		h.log.Warn("create vendor failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to create vendor", "internal")
		return
	}
	respondJSON(w, http.StatusCreated, v)
}

func (h *AcquisitionHandler) UpdateVendor(w http.ResponseWriter, r *http.Request) {
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
	exists, _ := h.db.Vendor.Query().Where(vendor.IDEQ(id), vendor.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "vendor not found", "not_found")
		return
	}
	var req vendorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	u := h.db.Vendor.UpdateOneID(id)
	if req.Name != "" {
		u = u.SetName(req.Name)
	}
	if req.Code != "" {
		u = u.SetCode(req.Code)
	}
	if req.ContactName != "" {
		u = u.SetContactName(req.ContactName)
	}
	if req.ContactEmail != "" {
		u = u.SetContactEmail(req.ContactEmail)
	}
	if req.ContactPhone != "" {
		u = u.SetContactPhone(req.ContactPhone)
	}
	if req.Address != "" {
		u = u.SetAddress(req.Address)
	}
	if req.Website != "" {
		u = u.SetWebsite(req.Website)
	}
	if req.AccountNumber != "" {
		u = u.SetAccountNumber(req.AccountNumber)
	}
	if req.Notes != "" {
		u = u.SetNotes(req.Notes)
	}
	if req.PaymentTerms != "" {
		if t := vendorTerms(req.PaymentTerms); t != "" {
			u = u.SetPaymentTerms(t)
		}
	}
	if req.IsActive != nil {
		u = u.SetIsActive(*req.IsActive)
	}
	v, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update vendor", "internal")
		return
	}
	respondJSON(w, http.StatusOK, v)
}

func vendorTerms(s string) vendor.PaymentTerms {
	switch s {
	case "NET_30":
		return vendor.PaymentTermsNET_30
	case "NET_60":
		return vendor.PaymentTermsNET_60
	case "COD":
		return vendor.PaymentTermsCOD
	case "PREPAID":
		return vendor.PaymentTermsPREPAID
	}
	return ""
}
