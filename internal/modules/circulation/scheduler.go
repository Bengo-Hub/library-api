package circulation

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/events"
)

// StartOverdueScheduler periodically flips past-due ACTIVE loans to OVERDUE and emits
// library.loan.overdue (for the dashboard + overdue notices). Idempotent: only loans not
// already OVERDUE are touched, so it is safe to run on every replica. Fine accrual itself
// happens at return time (assessOverdueFine).
func (s *Service) StartOverdueScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// Run once shortly after startup, then on the interval.
		s.sweepOverdue(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweepOverdue(ctx)
			}
		}
	}()
}

func (s *Service) sweepOverdue(ctx context.Context) {
	now := time.Now()
	due, err := s.db.Loan.Query().
		Where(loan.StatusEQ(loan.StatusACTIVE), loan.DueAtLT(now), loan.InHouse(false)).
		Limit(500).All(ctx)
	if err != nil {
		s.log.Warn("overdue sweep query failed", zap.Error(err))
		return
	}
	for _, l := range due {
		if _, err := s.db.Loan.UpdateOneID(l.ID).SetStatus(loan.StatusOVERDUE).Save(ctx); err != nil {
			s.log.Warn("overdue mark failed", zap.String("loan", l.ID.String()), zap.Error(err))
			continue
		}
		oEmail, oName := s.MemberContact(ctx, l.MemberID)
		_ = events.Publish(ctx, s.db.OutboxEvent, l.TenantID, l.ID.String(), events.EventLoanOverdue, map[string]any{
			"loan_id": l.ID, "member_id": l.MemberID, "due_at": l.DueAt,
			"email": oEmail, "name": oName,
		})
	}
	if len(due) > 0 {
		s.log.Info("overdue sweep complete", zap.Int("marked", len(due)))
	}
}
