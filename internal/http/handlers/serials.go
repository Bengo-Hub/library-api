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

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/serialissue"
	"github.com/bengobox/library-service/internal/ent/serialroutinglist"
	"github.com/bengobox/library-service/internal/ent/serialsubscription"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// SerialHandler handles serial subscription, issue, and routing endpoints.
type SerialHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewSerialHandler builds the serial handler.
func NewSerialHandler(db *ent.Client, log *zap.Logger) *SerialHandler {
	return &SerialHandler{db: db, log: log}
}

// ── Subscriptions ─────────────────────────────────────────────────────────────

type subscriptionRequest struct {
	BibRecordID  string  `json:"bib_record_id"`
	VendorID     string  `json:"vendor_id"`
	FundID       string  `json:"fund_id"`
	StartDate    string  `json:"start_date"`
	EndDate      string  `json:"end_date"`
	Frequency    string  `json:"frequency"`
	Price        float64 `json:"price"`
	CurrencyCode string  `json:"currency_code"`
	Notes        string  `json:"notes"`
}

func (h *SerialHandler) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	q := h.db.SerialSubscription.Query().Where(serialsubscription.TenantIDEQ(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(serialsubscription.StatusEQ(serialsubscription.Status(s)))
	}
	rows, err := q.Order(serialsubscription.ByStartDate()).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list subscriptions", "internal")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

func (h *SerialHandler) GetSubscription(w http.ResponseWriter, r *http.Request) {
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
	sub, err := h.db.SerialSubscription.Query().
		Where(serialsubscription.IDEQ(id), serialsubscription.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "subscription not found", "not_found")
		return
	}
	respondJSON(w, http.StatusOK, sub)
}

func (h *SerialHandler) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	bibID, err := uuid.Parse(req.BibRecordID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bib_record_id required", "validation_error")
		return
	}
	freq := serialsubscription.Frequency(req.Frequency)
	if req.Frequency == "" {
		freq = serialsubscription.FrequencyMONTHLY
	}
	startDate := time.Now()
	if req.StartDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.StartDate); err2 == nil {
			startDate = t
		}
	}
	c := h.db.SerialSubscription.Create().
		SetTenantID(tenantID).SetBibRecordID(bibID).SetFrequency(freq).
		SetStartDate(startDate).SetPrice(decimal.NewFromFloat(req.Price)).
		SetCurrencyCode(coalesceStr(req.CurrencyCode, "KES"))
	if req.VendorID != "" {
		if vid, err2 := uuid.Parse(req.VendorID); err2 == nil {
			c = c.SetVendorID(vid)
		}
	}
	if req.FundID != "" {
		if fid, err2 := uuid.Parse(req.FundID); err2 == nil {
			c = c.SetFundID(fid)
		}
	}
	if req.EndDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.EndDate); err2 == nil {
			c = c.SetEndDate(t)
		}
	}
	if req.Notes != "" {
		c = c.SetNotes(req.Notes)
	}
	sub, err := c.Save(r.Context())
	if err != nil {
		h.log.Warn("create subscription failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to create subscription", "internal")
		return
	}
	respondJSON(w, http.StatusCreated, sub)
}

func (h *SerialHandler) UpdateSubscription(w http.ResponseWriter, r *http.Request) {
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
	exists, _ := h.db.SerialSubscription.Query().Where(serialsubscription.IDEQ(id), serialsubscription.TenantIDEQ(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "subscription not found", "not_found")
		return
	}
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	u := h.db.SerialSubscription.UpdateOneID(id)
	if req.Frequency != "" {
		u = u.SetFrequency(serialsubscription.Frequency(req.Frequency))
	}
	if req.Price > 0 {
		u = u.SetPrice(decimal.NewFromFloat(req.Price))
	}
	if req.CurrencyCode != "" {
		u = u.SetCurrencyCode(req.CurrencyCode)
	}
	if req.EndDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.EndDate); err2 == nil {
			u = u.SetEndDate(t)
		}
	}
	if req.Notes != "" {
		u = u.SetNotes(req.Notes)
	}
	sub, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update subscription", "internal")
		return
	}
	respondJSON(w, http.StatusOK, sub)
}

// PredictIssues generates the next N expected issue dates based on frequency.
func (h *SerialHandler) PredictIssues(w http.ResponseWriter, r *http.Request) {
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
	sub, err := h.db.SerialSubscription.Query().
		Where(serialsubscription.IDEQ(id), serialsubscription.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "subscription not found", "not_found")
		return
	}

	// Determine the last expected date: latest existing issue, else subscription start.
	lastDate := sub.StartDate
	if existing, err2 := h.db.SerialIssue.Query().
		Where(serialissue.TenantIDEQ(tenantID), serialissue.SubscriptionIDEQ(id)).
		Order(serialissue.ByExpectedDate()).All(r.Context()); err2 == nil && len(existing) > 0 {
		lastDate = existing[len(existing)-1].ExpectedDate
	}

	n := 12
	type predicted struct {
		ExpectedDate string `json:"expected_date"`
		Volume       string `json:"volume,omitempty"`
		IssueNo      string `json:"issue_no,omitempty"`
	}
	issues := make([]predicted, 0, n)
	d := lastDate
	for i := 0; i < n; i++ {
		d = nextIssueDate(d, sub.Frequency)
		issues = append(issues, predicted{ExpectedDate: d.Format("2006-01-02")})
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": issues})
}

// ── Issues ────────────────────────────────────────────────────────────────────

type issueRequest struct {
	SubscriptionID string `json:"subscription_id"`
	Volume         string `json:"volume"`
	IssueNo        string `json:"issue_no"`
	ExpectedDate   string `json:"expected_date"`
	Notes          string `json:"notes"`
}

func (h *SerialHandler) ListIssues(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	q := h.db.SerialIssue.Query().Where(serialissue.TenantIDEQ(tenantID))
	if sid := r.URL.Query().Get("subscription_id"); sid != "" {
		if id, err := uuid.Parse(sid); err == nil {
			q = q.Where(serialissue.SubscriptionIDEQ(id))
		}
	}
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(serialissue.StatusEQ(serialissue.Status(s)))
	}
	rows, err := q.Order(serialissue.ByExpectedDate()).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list issues", "internal")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

func (h *SerialHandler) CreateIssue(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	var req issueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	subID, err := uuid.Parse(req.SubscriptionID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "subscription_id required", "validation_error")
		return
	}
	expected := time.Now()
	if req.ExpectedDate != "" {
		if t, err2 := time.Parse("2006-01-02", req.ExpectedDate); err2 == nil {
			expected = t
		}
	}
	c := h.db.SerialIssue.Create().
		SetTenantID(tenantID).SetSubscriptionID(subID).SetExpectedDate(expected)
	if req.Volume != "" {
		c = c.SetVolume(req.Volume)
	}
	if req.IssueNo != "" {
		c = c.SetIssueNo(req.IssueNo)
	}
	if req.Notes != "" {
		c = c.SetNotes(req.Notes)
	}
	iss, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create issue", "internal")
		return
	}
	respondJSON(w, http.StatusCreated, iss)
}

// ReceiveIssue marks an issue as received and creates a BookCopy in a transaction.
func (h *SerialHandler) ReceiveIssue(w http.ResponseWriter, r *http.Request) {
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
	iss, err := h.db.SerialIssue.Query().
		Where(serialissue.IDEQ(id), serialissue.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "issue not found", "not_found")
		return
	}
	if iss.Status == serialissue.StatusRECEIVED {
		respondJSON(w, http.StatusOK, iss)
		return
	}

	// Resolve bib_record_id from the subscription.
	sub, err := h.db.SerialSubscription.Query().
		Where(serialsubscription.IDEQ(iss.SubscriptionID), serialsubscription.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "subscription not found", "internal")
		return
	}

	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "tx init failed", "internal")
		return
	}

	accessionNo, seqErr := sequence.Next(r.Context(), tx, tenantID, "accession_no", "ACC", 6)
	if seqErr != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, "sequence allocation failed", "internal")
		return
	}

	// Verify bib exists before creating copy.
	bibExists, _ := tx.BibRecord.Query().Where(bibrecord.IDEQ(sub.BibRecordID), bibrecord.TenantID(tenantID)).Exist(r.Context())
	if !bibExists {
		_ = tx.Rollback()
		respondError(w, http.StatusBadRequest, "bib record not found for subscription", "not_found")
		return
	}

	copy, copyErr := tx.BookCopy.Create().
		SetTenantID(tenantID).SetBibRecordID(sub.BibRecordID).
		SetAccessionNo(accessionNo).SetStatus(bookcopy.StatusAVAILABLE).
		Save(r.Context())
	if copyErr != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, "failed to create copy", "internal")
		return
	}

	now := time.Now()
	updated, updErr := tx.SerialIssue.UpdateOneID(id).
		SetStatus(serialissue.StatusRECEIVED).SetReceivedDate(now).SetCopyID(copy.ID).Save(r.Context())
	if updErr != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, "failed to update issue", "internal")
		return
	}
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, "tx commit failed", "internal")
		return
	}
	respondJSON(w, http.StatusOK, updated)
}

// ClaimIssue marks an issue as CLAIMED (overdue, sending a claim to vendor).
func (h *SerialHandler) ClaimIssue(w http.ResponseWriter, r *http.Request) {
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
	iss, err := h.db.SerialIssue.Query().
		Where(serialissue.IDEQ(id), serialissue.TenantIDEQ(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "issue not found", "not_found")
		return
	}
	if iss.Status == serialissue.StatusCLAIMED {
		respondJSON(w, http.StatusOK, iss)
		return
	}
	updated, err := h.db.SerialIssue.UpdateOneID(id).SetStatus(serialissue.StatusCLAIMED).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to claim issue", "internal")
		return
	}
	// Publish serial.issue_late event so notifications-service can alert the librarian.
	_ = events.Publish(r.Context(), h.db.OutboxEvent, tenantID, id.String(), events.EventSerialIssueLate, map[string]any{
		"issue_id":        id.String(),
		"subscription_id": iss.SubscriptionID.String(),
		"expected_date":   iss.ExpectedDate.Format("2006-01-02"),
	})
	respondJSON(w, http.StatusOK, updated)
}

// ── Routing Lists ─────────────────────────────────────────────────────────────

func (h *SerialHandler) ListRouting(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	subID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id", "invalid_id")
		return
	}
	rows, err := h.db.SerialRoutingList.Query().
		Where(serialroutinglist.TenantIDEQ(tenantID), serialroutinglist.SubscriptionIDEQ(subID)).
		Order(serialroutinglist.ByPosition()).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list routing", "internal")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

func (h *SerialHandler) AddRouting(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	subID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id", "invalid_id")
		return
	}
	var req struct {
		MemberID string `json:"member_id"`
		Position int    `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MemberID == "" {
		respondError(w, http.StatusBadRequest, "member_id required", "validation_error")
		return
	}
	memID, err := uuid.Parse(req.MemberID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid member_id", "invalid_id")
		return
	}
	entry, err := h.db.SerialRoutingList.Create().
		SetTenantID(tenantID).SetSubscriptionID(subID).SetMemberID(memID).SetPosition(req.Position).
		Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to add routing", "internal")
		return
	}
	respondJSON(w, http.StatusCreated, entry)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nextIssueDate(from time.Time, freq serialsubscription.Frequency) time.Time {
	switch freq {
	case serialsubscription.FrequencyDAILY:
		return from.AddDate(0, 0, 1)
	case serialsubscription.FrequencyWEEKLY:
		return from.AddDate(0, 0, 7)
	case serialsubscription.FrequencyMONTHLY:
		return from.AddDate(0, 1, 0)
	case serialsubscription.FrequencyQUARTERLY:
		return from.AddDate(0, 3, 0)
	case serialsubscription.FrequencyANNUAL:
		return from.AddDate(1, 0, 0)
	default:
		return from.AddDate(0, 1, 0)
	}
}

func coalesceStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// SerialIssueScheduler flags EXPECTED issues past their expected_date as LATE
// and publishes serial.issue_late events so notifications-service can alert librarians.
type SerialIssueScheduler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewSerialIssueScheduler builds the scheduler.
func NewSerialIssueScheduler(db *ent.Client, log *zap.Logger) *SerialIssueScheduler {
	return &SerialIssueScheduler{db: db, log: log}
}

// Start runs the sweep immediately then on the given interval (defaults to 24h).
func (s *SerialIssueScheduler) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		s.sweep(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweep(ctx)
			}
		}
	}()
}

func (s *SerialIssueScheduler) sweep(ctx context.Context) {
	now := time.Now()
	issues, err := s.db.SerialIssue.Query().
		Where(
			serialissue.StatusEQ(serialissue.StatusEXPECTED),
			serialissue.ExpectedDateLT(now),
		).All(ctx)
	if err != nil {
		s.log.Warn("serial issue scheduler: query failed", zap.Error(err))
		return
	}
	for _, iss := range issues {
		_, err := s.db.SerialIssue.UpdateOneID(iss.ID).SetStatus(serialissue.StatusLATE).Save(ctx)
		if err != nil {
			s.log.Warn("serial issue scheduler: update failed", zap.Error(err), zap.String("id", iss.ID.String()))
			continue
		}
		_ = events.Publish(ctx, s.db.OutboxEvent, iss.TenantID, iss.ID.String(), events.EventSerialIssueLate, map[string]any{
			"issue_id":        iss.ID.String(),
			"subscription_id": iss.SubscriptionID.String(),
			"expected_date":   iss.ExpectedDate.Format("2006-01-02"),
		})
		s.log.Info("serial issue marked LATE", zap.String("id", iss.ID.String()))
	}
}
