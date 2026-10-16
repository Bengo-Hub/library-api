package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	sharedpagination "github.com/Bengo-Hub/pagination"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/modules/fines"
)

// ReportsHandler serves dashboard/summary endpoints.
type ReportsHandler struct {
	db    *ent.Client
	cache *sharedcache.Aside // may be nil (Redis unconfigured) — Summary then always computes live
	log   *zap.Logger
}

// NewReportsHandler builds the reports handler. cache may be nil (Redis unconfigured).
func NewReportsHandler(db *ent.Client, cache *sharedcache.Aside, log *zap.Logger) *ReportsHandler {
	return &ReportsHandler{db: db, cache: cache, log: log}
}

// Summary godoc
// @Summary Dashboard summary counts
// @Tags Reports
// @Router /{tenant}/library/reports/summary [get]
func (h *ReportsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()

	fetch := func(ctx context.Context) (map[string]any, error) {
		return h.buildSummary(ctx, tenantID), nil
	}
	var (
		out map[string]any
		err error
	)
	if h.cache != nil {
		// Dashboard summary tolerates a short staleness window in exchange for not issuing ~13
		// sequential COUNT queries on every dashboard load (see reference_library_pin_sso_gap for
		// why library-api previously had no cache-aside adoption at all).
		out, err = sharedcache.GetOrSet(ctx, h.cache, "library:reports:summary:"+tenantID.String(), sharedcache.TTLOperational, fetch)
	} else {
		out, err = fetch(ctx)
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "summary_failed")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// buildSummary computes the dashboard summary counts live (cache miss / cache disabled path).
// A loan counts as out from checkout until return, so ACTIVE and OVERDUE both belong to it;
// the hourly sweep moves late loans to OVERDUE and they must not drop off the dashboard.
func (h *ReportsHandler) buildSummary(ctx context.Context, tenantID uuid.UUID) map[string]any {
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	out := loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE)
	activeLoans, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), out).Count(ctx)
	overdue, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), out, loan.DueAtLT(now)).Count(ctx)
	holdsReady, _ := h.db.Hold.Query().Where(hold.TenantID(tenantID), hold.StatusEQ(hold.StatusREADY)).Count(ctx)
	holdsWaiting, _ := h.db.Hold.Query().Where(hold.TenantID(tenantID), hold.StatusEQ(hold.StatusWAITING)).Count(ctx)
	totalMembers, _ := h.db.Member.Query().Where(member.TenantID(tenantID)).Count(ctx)
	activeMembers, _ := h.db.Member.Query().Where(member.TenantID(tenantID), member.StatusEQ(member.StatusACTIVE)).Count(ctx)
	bibs, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID)).Count(ctx)
	copies, _ := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID)).Count(ctx)
	available, _ := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.StatusEQ(bookcopy.StatusAVAILABLE)).Count(ctx)
	checkoutsToday, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.CheckoutAtGTE(startOfDay)).Count(ctx)
	returnsToday, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.ReturnedAtGTE(startOfDay)).Count(ctx)

	// Money: collection value, what members still owe (fines and unpaid fees, net of part
	// payments) and what was received today, all summed in SQL.
	outstanding, _ := fines.Outstanding(ctx, h.db, tenantID, uuid.Nil)

	return map[string]any{
		"active_loans":      activeLoans,
		"overdue_loans":     overdue,
		"holds_ready":       holdsReady,
		"holds_pending":     holdsWaiting,
		"total_members":     totalMembers,
		"active_members":    activeMembers,
		"total_titles":      bibs,
		"total_copies":      copies,
		"available_copies":  available,
		"collection_value":  h.collectionValue(ctx, tenantID).InexactFloat64(),
		"outstanding_fines": outstanding.InexactFloat64(),
		"collected_today":   h.collectedSince(ctx, tenantID, startOfDay).InexactFloat64(),
		"checkouts_today":   checkoutsToday,
		"returns_today":     returnsToday,
		"circulation_trend": h.circulationTrend(ctx, tenantID, 14),
		"popular_titles":    h.popularTitles(ctx, tenantID, now.AddDate(0, 0, -30), 5),
	}
}

func daysParam(r *http.Request, def int) int {
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			return n
		}
	}
	return def
}

// Popular godoc
// @Summary Most-borrowed titles over a recent window
// @Tags Reports
// @Router /{tenant}/library/reports/popular [get]
func (h *ReportsHandler) Popular(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	since := time.Now().AddDate(0, 0, -daysParam(r, 30))
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	rows := h.popularTitles(r.Context(), tenantID, since, limit)
	// "limit" here is this report's own top-N cutoff (default 10, capped at 50 above), not
	// generic offset pagination — reuse the shared envelope shape without reinterpreting it
	// via sharedpagination.Parse (which has different defaults/caps).
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(rows, len(rows), sharedpagination.Params{Limit: limit, Offset: 0, Page: 1}))
}

// Circulation godoc
// @Summary Daily checkout/return trend over a recent window
// @Tags Reports
// @Router /{tenant}/library/reports/circulation [get]
func (h *ReportsHandler) Circulation(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	out := h.circulationTrend(r.Context(), tenantID, daysParam(r, 30))
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(out, len(out), sharedpagination.Params{Limit: len(out), Offset: 0, Page: 1}))
}

// Overdue godoc
// @Summary Currently overdue loans
// @Tags Reports
// @Router /{tenant}/library/reports/overdue [get]
func (h *ReportsHandler) Overdue(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	q := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE), loan.DueAtLT(time.Now()))
	params := sharedpagination.Parse(r)
	total, _ := q.Clone().Count(ctx)
	rows, _ := q.Order(ent.Asc(loan.FieldDueAt)).Limit(params.Limit).Offset(params.Offset).All(ctx)
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(rows, total, params))
}
