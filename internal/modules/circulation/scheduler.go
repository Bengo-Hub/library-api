package circulation

import (
	"context"
	sharedcache "github.com/Bengo-Hub/cache"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/events"
)

// StartOverdueScheduler periodically flips past-due ACTIVE loans to OVERDUE and emits
// library.loan.overdue (for the dashboard + overdue notices). Every replica runs the ticker;
// sweepOverdue claims each hour so only one replica sweeps (two concurrent sweeps both saw the
// same ACTIVE loans and emitted the overdue event twice). Fine accrual itself happens at
// return time (assessOverdueFine).
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
	// Runs on every replica's ticker; only the first replica in each period does the work.
	if !sharedcache.ClaimPeriod(ctx, "library:overdue", time.Hour) {
		return
	}
	now := time.Now()
	marked := 0
	// Work through the backlog in batches so a large catalogue is never left half swept; the
	// batch cap keeps one tick bounded and the next hourly tick continues from where it stopped.
	for batch := 0; batch < maxSweepBatches; batch++ {
		n, err := s.sweepOverdueBatch(ctx, now)
		if err != nil {
			s.log.Warn("overdue sweep batch failed", zap.Error(err))
			break
		}
		marked += n
		if n < sweepBatchSize {
			break
		}
	}
	if marked > 0 {
		s.log.Info("overdue sweep complete", zap.Int("marked", marked))
	}
}

const (
	sweepBatchSize  = 500
	maxSweepBatches = 40
)

// sweepOverdueBatch flips one batch of past-due ACTIVE loans to OVERDUE and queues their
// overdue notices in the same transaction, so a loan is never flipped without its event.
func (s *Service) sweepOverdueBatch(ctx context.Context, now time.Time) (int, error) {
	due, err := s.db.Loan.Query().
		Where(loan.StatusEQ(loan.StatusACTIVE), loan.DueAtLT(now), loan.InHouse(false)).
		Order(ent.Asc(loan.FieldDueAt)).
		Limit(sweepBatchSize).All(ctx)
	if err != nil || len(due) == 0 {
		return 0, err
	}
	memberIDs := make([]uuid.UUID, 0, len(due))
	loanIDs := make([]uuid.UUID, 0, len(due))
	for _, l := range due {
		memberIDs = append(memberIDs, l.MemberID)
		loanIDs = append(loanIDs, l.ID)
	}
	contacts := map[uuid.UUID]*ent.Member{}
	if ms, merr := s.db.Member.Query().Where(member.IDIn(memberIDs...)).
		Select(member.FieldID, member.FieldContactEmail, member.FieldDisplayName).All(ctx); merr == nil {
		for _, m := range ms {
			contacts[m.ID] = m
		}
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Loan.Update().Where(loan.IDIn(loanIDs...), loan.StatusEQ(loan.StatusACTIVE)).
		SetStatus(loan.StatusOVERDUE).Save(ctx); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	for _, l := range due {
		payload := map[string]any{"loan_id": l.ID, "member_id": l.MemberID, "due_at": l.DueAt}
		if m := contacts[l.MemberID]; m != nil {
			payload["email"], payload["name"] = m.ContactEmail, m.DisplayName
		}
		if err := events.Publish(ctx, tx.OutboxEvent, l.TenantID, l.ID.String(), events.EventLoanOverdue, payload); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(due), nil
}
