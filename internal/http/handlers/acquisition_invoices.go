package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent/acquisitioninvoice"
	"github.com/bengobox/library-service/internal/ent/vendor"
	"github.com/bengobox/library-service/internal/payref"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

// invoiceRequest and handler methods below are part of AcquisitionHandler.

// AcquisitionInvoiceHandler embeds AcquisitionHandler so it can share the db/log.
// Wired at the router level by passing the same *AcquisitionHandler.

type invoiceRequest struct {
	VendorID    string  `json:"vendor_id"`
	POID        string  `json:"po_id"`
	InvoiceNo   string  `json:"invoice_no"`
	InvoiceDate string  `json:"invoice_date"`
	Amount      float64 `json:"amount"`
	Notes       string  `json:"notes"`
}

func (h *AcquisitionHandler) ListInvoices(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	q := h.db.AcquisitionInvoice.Query().Where(acquisitioninvoice.TenantIDEQ(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(acquisitioninvoice.StatusEQ(acquisitioninvoice.Status(s)))
	}
	if vid := r.URL.Query().Get("vendor_id"); vid != "" {
		if id, err := uuid.Parse(vid); err == nil {
			q = q.Where(acquisitioninvoice.VendorIDEQ(id))
		}
	}
	limit, offset := PageParams(r)
	total, _ := q.Count(r.Context())
	rows, err := q.Limit(limit).Offset(offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list invoices", "internal")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": rows, "total": total})
}

func (h *AcquisitionHandler) GetInvoice(w http.ResponseWriter, r *http.Request) {
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
	inv, err := h.db.AcquisitionInvoice.Query().
		Where(acquisitioninvoice.IDEQ(id), acquisitioninvoice.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "invoice not found", "not_found")
		return
	}
	respondJSON(w, http.StatusOK, inv)
}

// CreateInvoice creates a local AcquisitionInvoice record and submits a vendor bill to
// treasury-api (POST /api/v1/s2s/{tenant}/invoices). The returned treasury invoice ID
// is stored so the PaymentConsumer can reconcile payment.succeeded events back here.
func (h *AcquisitionHandler) CreateInvoice(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	tenantSlug := TenantSlug(r)
	var req invoiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount <= 0 {
		respondError(w, http.StatusBadRequest, "amount is required", "validation_error")
		return
	}
	vendorID, err := uuid.Parse(req.VendorID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "vendor_id required", "validation_error")
		return
	}

	// Resolve vendor name for treasury description.
	vendorName := ""
	if v, verr := h.db.Vendor.Query().Where(vendor.IDEQ(vendorID), vendor.TenantID(tenantID)).Only(r.Context()); verr == nil {
		vendorName = v.Name
	}

	// Create local invoice record first to get its ID for the payment reference.
	c := h.db.AcquisitionInvoice.Create().
		SetTenantID(tenantID).SetVendorID(vendorID).
		SetAmount(decimal.NewFromFloat(req.Amount)).SetStatus(acquisitioninvoice.StatusPENDING)
	if req.InvoiceNo != "" {
		c = c.SetInvoiceNo(req.InvoiceNo)
	}
	if req.Notes != "" {
		c = c.SetNotes(req.Notes)
	}
	if req.InvoiceDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.InvoiceDate); err2 == nil {
			c = c.SetInvoiceDate(t)
		}
	}
	if req.POID != "" {
		if pid, err2 := uuid.Parse(req.POID); err2 == nil {
			c = c.SetPoID(pid)
		}
	}
	inv, err := c.Save(r.Context())
	if err != nil {
		h.log.Warn("create acquisition invoice failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to create invoice", "internal")
		return
	}

	// Build service-identifiable payment reference.
	refID := payref.Build("LIB", tenantSlug, tenantID, inv.ID)
	_, _ = h.db.AcquisitionInvoice.UpdateOneID(inv.ID).SetReferenceID(refID).Save(r.Context())

	// Submit vendor bill to treasury (best-effort — invoice exists even if treasury fails).
	if h.treasury != nil && tenantSlug != "" {
		tresp, terr := h.treasury.CreateInvoice(r.Context(), tenantSlug, treasury.CreateInvoiceRequest{
			SourceService: "library",
			ReferenceID:   refID,
			ReferenceType: "acquisition_invoice",
			InvoiceType:   "vendor_bill",
			Amount:        req.Amount,
			Currency:      "KES",
			VendorName:    vendorName,
			Description:   "Acquisition invoice " + req.InvoiceNo,
			Metadata:      map[string]any{"entity_id": inv.ID.String(), "po_id": req.POID},
		})
		if terr != nil {
			h.log.Warn("treasury invoice creation failed", zap.Error(terr), zap.String("invoice", inv.ID.String()))
		} else if tresp != nil {
			if tid, terr2 := uuid.Parse(tresp.ResolvedID()); terr2 == nil {
				inv, _ = h.db.AcquisitionInvoice.UpdateOneID(inv.ID).SetTreasuryInvoiceID(tid).Save(r.Context())
			}
		}
	}

	respondJSON(w, http.StatusCreated, inv)
}
