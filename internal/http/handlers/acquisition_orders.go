package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/purchaseorder"
	"github.com/bengobox/library-service/internal/ent/purchaseorderline"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

type poRequest struct {
	VendorID     string  `json:"vendor_id"`
	FundID       string  `json:"fund_id"`
	OrderDate    string  `json:"order_date"`
	ExpectedDate string  `json:"expected_date"`
	Notes        string  `json:"notes"`
	CurrencyCode string  `json:"currency_code"`
	Tax          float64 `json:"tax"`
}

type poLineRequest struct {
	BibRecordID string  `json:"bib_record_id"`
	Title       string  `json:"title"`
	ISBN        string  `json:"isbn"`
	Author      string  `json:"author"`
	UnitPrice   float64 `json:"unit_price"`
	Quantity    int     `json:"quantity"`
	Notes       string  `json:"notes"`
}

type receiveLineRequest struct {
	ReceivedQty int    `json:"received_qty"`
	BranchID    string `json:"branch_id"`
	ShelfLoc    string `json:"shelf_location"`
}

func (h *AcquisitionHandler) ListOrders(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	q := h.db.PurchaseOrder.Query().Where(purchaseorder.TenantIDEQ(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(purchaseorder.StatusEQ(purchaseorder.Status(s)))
	}
	if vid := r.URL.Query().Get("vendor_id"); vid != "" {
		if id, err := uuid.Parse(vid); err == nil {
			q = q.Where(purchaseorder.VendorIDEQ(id))
		}
	}
	limit, offset := PageParams(r)
	total, _ := q.Count(r.Context())
	rows, err := q.Limit(limit).Offset(offset).Order(purchaseorder.ByCreatedAt()).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list orders", "internal")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": rows, "total": total})
}

func (h *AcquisitionHandler) GetOrder(w http.ResponseWriter, r *http.Request) {
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
	po, err := h.db.PurchaseOrder.Query().Where(purchaseorder.IDEQ(id), purchaseorder.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "order not found", "not_found")
		return
	}
	lines, _ := h.db.PurchaseOrderLine.Query().Where(purchaseorderline.TenantIDEQ(tenantID), purchaseorderline.PoIDEQ(id)).All(r.Context())
	respondJSON(w, http.StatusOK, map[string]any{"order": po, "lines": lines})
}

func (h *AcquisitionHandler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	var req poRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	vendorID, err := uuid.Parse(req.VendorID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "vendor_id required", "validation_error")
		return
	}
	currency := req.CurrencyCode
	if currency == "" {
		currency = "KES"
	}
	c := h.db.PurchaseOrder.Create().
		SetTenantID(tenantID).SetVendorID(vendorID).SetStatus(purchaseorder.StatusDRAFT).
		SetCurrencyCode(currency).SetTax(decimal.NewFromFloat(req.Tax))
	if req.Notes != "" {
		c = c.SetNotes(req.Notes)
	}
	if req.FundID != "" {
		if fid, err2 := uuid.Parse(req.FundID); err2 == nil {
			c = c.SetFundID(fid)
		}
	}
	if req.OrderDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.OrderDate); err2 == nil {
			c = c.SetOrderDate(t)
		}
	}
	if req.ExpectedDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.ExpectedDate); err2 == nil {
			c = c.SetExpectedDate(t)
		}
	}
	po, err := c.Save(r.Context())
	if err != nil {
		h.log.Warn("create PO failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to create order", "internal")
		return
	}
	respondJSON(w, http.StatusCreated, po)
}

func (h *AcquisitionHandler) UpdateOrder(w http.ResponseWriter, r *http.Request) {
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
	po, err := h.db.PurchaseOrder.Query().Where(purchaseorder.IDEQ(id), purchaseorder.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "order not found", "not_found")
		return
	}
	if po.Status != purchaseorder.StatusDRAFT {
		respondError(w, http.StatusConflict, "only DRAFT orders can be edited", "invalid_status")
		return
	}
	var req poRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	u := h.db.PurchaseOrder.UpdateOneID(id)
	if req.Notes != "" {
		u = u.SetNotes(req.Notes)
	}
	if req.FundID != "" {
		if fid, err2 := uuid.Parse(req.FundID); err2 == nil {
			u = u.SetFundID(fid)
		}
	}
	if req.ExpectedDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.ExpectedDate); err2 == nil {
			u = u.SetExpectedDate(t)
		}
	}
	updated, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update order", "internal")
		return
	}
	respondJSON(w, http.StatusOK, updated)
}

func (h *AcquisitionHandler) SubmitOrder(w http.ResponseWriter, r *http.Request) {
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
	po, err := h.db.PurchaseOrder.Query().Where(purchaseorder.IDEQ(id), purchaseorder.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "order not found", "not_found")
		return
	}
	if po.Status != purchaseorder.StatusDRAFT {
		respondError(w, http.StatusConflict, "order is not in DRAFT status", "invalid_status")
		return
	}
	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "tx failed", "internal")
		return
	}
	poNo, _ := sequence.Next(r.Context(), tx, tenantID, "purchase_order", "PO", 5)
	u := tx.PurchaseOrder.UpdateOneID(id).SetStatus(purchaseorder.StatusSUBMITTED).SetOrderDate(time.Now())
	if poNo != "" {
		u = u.SetPoNumber(poNo)
	}
	updated, err := u.Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, "failed to submit order", "internal")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, "commit failed", "internal")
		return
	}
	respondJSON(w, http.StatusOK, updated)
}

func (h *AcquisitionHandler) AddLine(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid order id", "invalid_id")
		return
	}
	exists, _ := h.db.PurchaseOrder.Query().Where(purchaseorder.IDEQ(poID), purchaseorder.TenantIDEQ(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "order not found", "not_found")
		return
	}
	var req poLineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	qty := req.Quantity
	if qty <= 0 {
		qty = 1
	}
	c := h.db.PurchaseOrderLine.Create().
		SetTenantID(tenantID).SetPoID(poID).
		SetUnitPrice(decimal.NewFromFloat(req.UnitPrice)).SetQuantity(qty)
	if req.BibRecordID != "" {
		if bid, err2 := uuid.Parse(req.BibRecordID); err2 == nil {
			c = c.SetBibRecordID(bid)
		}
	}
	if req.Title != "" {
		c = c.SetTitle(req.Title)
	}
	if req.ISBN != "" {
		c = c.SetIsbn(req.ISBN)
	}
	if req.Author != "" {
		c = c.SetAuthor(req.Author)
	}
	if req.Notes != "" {
		c = c.SetNotes(req.Notes)
	}
	line, err := c.Save(r.Context())
	if err != nil {
		h.log.Warn("add PO line failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to add line", "internal")
		return
	}
	h.recomputePOTotal(r.Context(), tenantID, poID)
	respondJSON(w, http.StatusCreated, line)
}

func (h *AcquisitionHandler) ReceiveLine(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid order id", "invalid_id")
		return
	}
	lineID, err := uuid.Parse(chi.URLParam(r, "line_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid line id", "invalid_id")
		return
	}
	line, err := h.db.PurchaseOrderLine.Query().
		Where(purchaseorderline.IDEQ(lineID), purchaseorderline.TenantIDEQ(tenantID), purchaseorderline.PoIDEQ(poID)).
		Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "line not found", "not_found")
		return
	}
	var req receiveLineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ReceivedQty <= 0 {
		respondError(w, http.StatusBadRequest, "received_qty must be > 0", "validation_error")
		return
	}

	ctx := r.Context()
	newQty := line.ReceivedQty + req.ReceivedQty
	newStatus := purchaseorderline.StatusPARTIAL
	if newQty >= line.Quantity {
		newStatus = purchaseorderline.StatusRECEIVED
		newQty = line.Quantity
	}
	updatedLine, err := h.db.PurchaseOrderLine.UpdateOneID(lineID).
		SetReceivedQty(newQty).SetStatus(newStatus).Save(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to receive line", "internal")
		return
	}

	// Auto-create BookCopy records for received quantity.
	if line.BibRecordID != nil {
		_, bibErr := h.db.BibRecord.Query().Where(bibrecord.IDEQ(*line.BibRecordID)).Only(ctx)
		if bibErr == nil {
			branchID := uuid.Nil
			if req.BranchID != "" {
				if bid, err2 := uuid.Parse(req.BranchID); err2 == nil {
					branchID = bid
				}
			}
			for i := 0; i < req.ReceivedQty; i++ {
				copyTx, txErr := h.db.Tx(ctx)
				if txErr != nil {
					continue
				}
				accNo, _ := sequence.Next(ctx, copyTx, tenantID, sequence.KindAccession, "ACC", 6)
				cc := copyTx.BookCopy.Create().
					SetTenantID(tenantID).SetBibRecordID(*line.BibRecordID).
					SetStatus(bookcopy.StatusAVAILABLE)
				if accNo != "" {
					cc = cc.SetAccessionNo(accNo)
				}
				if branchID != uuid.Nil {
					cc = cc.SetBranchID(branchID)
				}
				if req.ShelfLoc != "" {
					cc = cc.SetShelfLocation(req.ShelfLoc)
				}
				if _, saveErr := cc.Save(ctx); saveErr != nil {
					_ = copyTx.Rollback()
					continue
				}
				_ = copyTx.Commit()
			}
		}
	}

	// Advance PO status if all lines are received.
	h.advancePOStatus(ctx, tenantID, poID)
	respondJSON(w, http.StatusOK, updatedLine)
}

func (h *AcquisitionHandler) recomputePOTotal(ctx context.Context, tenantID, poID uuid.UUID) {
	lines, err := h.db.PurchaseOrderLine.Query().
		Where(purchaseorderline.TenantIDEQ(tenantID), purchaseorderline.PoIDEQ(poID)).All(ctx)
	if err != nil {
		return
	}
	subtotal := decimal.Zero
	for _, l := range lines {
		subtotal = subtotal.Add(l.UnitPrice.Mul(decimal.NewFromInt(int64(l.Quantity))))
	}
	po, err := h.db.PurchaseOrder.Get(ctx, poID)
	if err != nil {
		return
	}
	total := subtotal.Add(po.Tax)
	_, _ = h.db.PurchaseOrder.UpdateOneID(poID).SetSubtotal(subtotal).SetTotal(total).Save(ctx)
}

func (h *AcquisitionHandler) advancePOStatus(ctx context.Context, tenantID, poID uuid.UUID) {
	lines, err := h.db.PurchaseOrderLine.Query().
		Where(purchaseorderline.TenantIDEQ(tenantID), purchaseorderline.PoIDEQ(poID)).All(ctx)
	if err != nil || len(lines) == 0 {
		return
	}
	allReceived := true
	anyReceived := false
	for _, l := range lines {
		if l.Status == purchaseorderline.StatusRECEIVED {
			anyReceived = true
		} else if l.Status != purchaseorderline.StatusCANCELLED {
			allReceived = false
		}
	}
	var newStatus purchaseorder.Status
	if allReceived {
		newStatus = purchaseorder.StatusRECEIVED
	} else if anyReceived {
		newStatus = purchaseorder.StatusPARTIAL
	} else {
		return
	}
	_, _ = h.db.PurchaseOrder.UpdateOneID(poID).SetStatus(newStatus).Save(ctx)
}
