package handlers

import (
	"net/http"
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
