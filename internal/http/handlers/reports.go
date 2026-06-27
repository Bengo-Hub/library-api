package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
)

// ReportsHandler serves dashboard/summary endpoints.
type ReportsHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewReportsHandler builds the reports handler.
func NewReportsHandler(db *ent.Client, log *zap.Logger) *ReportsHandler {
	return &ReportsHandler{db: db, log: log}
}

// Summary godoc
// @Summary Dashboard summary counts
// @Tags Reports
// @Router /{tenant}/library/reports/summary [get]
func (h *ReportsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	activeLoans, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.StatusEQ(loan.StatusACTIVE)).Count(ctx)
	overdue, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.StatusEQ(loan.StatusACTIVE), loan.DueAtLT(time.Now())).Count(ctx)
	holdsReady, _ := h.db.Hold.Query().Where(hold.TenantID(tenantID), hold.StatusEQ(hold.StatusREADY)).Count(ctx)
	holdsWaiting, _ := h.db.Hold.Query().Where(hold.TenantID(tenantID), hold.StatusEQ(hold.StatusWAITING)).Count(ctx)
	members, _ := h.db.Member.Query().Where(member.TenantID(tenantID)).Count(ctx)
	bibs, _ := h.db.BibRecord.Query().Count(ctx)
	copies, _ := h.db.BookCopy.Query().Count(ctx)

	respondJSON(w, http.StatusOK, map[string]any{
		"active_loans":  activeLoans,
		"overdue_loans": overdue,
		"holds_ready":   holdsReady,
		"holds_waiting": holdsWaiting,
		"members":       members,
		"titles":        bibs,
		"copies":        copies,
	})
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
	ctx := r.Context()
	since := time.Now().AddDate(0, 0, -daysParam(r, 30))
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	loans, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.CheckoutAtGTE(since)).All(ctx)
	bibCount := map[string]int{}
	copyToBib := map[string]string{}
	for _, l := range loans {
		bib, ok := copyToBib[l.CopyID.String()]
		if !ok {
			c, err := h.db.BookCopy.Get(ctx, l.CopyID)
			if err != nil {
				continue
			}
			bib = c.BibRecordID.String()
			copyToBib[l.CopyID.String()] = bib
		}
		bibCount[bib]++
	}
	type row struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Loans int    `json:"loans"`
	}
	rows := make([]row, 0, len(bibCount))
	for bib, n := range bibCount {
		rows = append(rows, row{ID: bib, Loans: n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Loans > rows[j].Loans })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for i := range rows {
		if id, err := ParseUUIDParam(rows[i].ID); err == nil {
			if b, err := h.db.BibRecord.Get(ctx, id); err == nil {
				rows[i].Title = b.Title
			}
		}
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// Circulation godoc
// @Summary Daily checkout/return trend over a recent window
// @Tags Reports
// @Router /{tenant}/library/reports/circulation [get]
func (h *ReportsHandler) Circulation(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	days := daysParam(r, 30)
	since := time.Now().AddDate(0, 0, -days)
	loans, _ := h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.CheckoutAtGTE(since)).All(ctx)

	checkouts := map[string]int{}
	returns := map[string]int{}
	for _, l := range loans {
		checkouts[l.CheckoutAt.Format("2006-01-02")]++
		if l.ReturnedAt != nil {
			returns[l.ReturnedAt.Format("2006-01-02")]++
		}
	}
	type point struct {
		Date      string `json:"date"`
		Checkouts int    `json:"checkouts"`
		Returns   int    `json:"returns"`
	}
	out := make([]point, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, point{Date: d, Checkouts: checkouts[d], Returns: returns[d]})
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

// Overdue godoc
// @Summary Currently overdue loans
// @Tags Reports
// @Router /{tenant}/library/reports/overdue [get]
func (h *ReportsHandler) Overdue(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	rows, _ := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE), loan.DueAtLT(time.Now())).
		Order(ent.Asc(loan.FieldDueAt)).Limit(200).All(ctx)
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}
