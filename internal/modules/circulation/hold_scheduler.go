package circulation

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/events"
)

// StartHoldExpiryScheduler runs hourly: expired READY holds are cancelled, the assigned
// copy is freed (AVAILABLE), and the next WAITING hold in queue is promoted.
func (s *Service) StartHoldExpiryScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		s.sweepExpiredHolds(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweepExpiredHolds(ctx)
			}
		}
	}()
}

func (s *Service) sweepExpiredHolds(ctx context.Context) {
	now := time.Now()
	expired, err := s.db.Hold.Query().
		Where(hold.StatusEQ(hold.StatusREADY), hold.ExpiresAtLT(now)).
		Limit(200).All(ctx)
	if err != nil {
		s.log.Warn("hold expiry sweep query failed", zap.Error(err))
		return
	}
	for _, h := range expired {
		s.expireHold(ctx, h, now)
	}
	if len(expired) > 0 {
		s.log.Info("hold expiry sweep complete", zap.Int("expired", len(expired)))
	}
}

func (s *Service) expireHold(ctx context.Context, h *ent.Hold, now time.Time) {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		s.log.Warn("hold expiry: tx failed", zap.Error(err))
		return
	}
	if _, err := tx.Hold.UpdateOneID(h.ID).SetStatus(hold.StatusEXPIRED).Save(ctx); err != nil {
		_ = tx.Rollback()
		s.log.Warn("hold expiry: mark failed", zap.String("hold", h.ID.String()), zap.Error(err))
		return
	}

	// Free the assigned copy and promote the next waiter, if any.
	if h.CopyID != nil {
		next, _ := tx.Hold.Query().
			Where(hold.TenantID(h.TenantID), hold.BibRecordID(h.BibRecordID), hold.StatusEQ(hold.StatusWAITING)).
			Order(ent.Asc(hold.FieldQueuePosition), ent.Asc(hold.FieldPlacedAt)).
			First(ctx)
		if next != nil {
			expires := now.Add(48 * time.Hour)
			_, _ = tx.Hold.UpdateOneID(next.ID).SetStatus(hold.StatusREADY).SetCopyID(*h.CopyID).SetReadyAt(now).SetExpiresAt(expires).Save(ctx)
			_, _ = tx.BookCopy.UpdateOneID(*h.CopyID).SetStatus(bookcopy.StatusRESERVED).Save(ctx)
			nEmail, nName := s.MemberContact(ctx, next.MemberID)
			_ = events.Publish(ctx, tx.OutboxEvent, next.TenantID, next.ID.String(), events.EventHoldReady, map[string]any{
				"hold_id": next.ID, "member_id": next.MemberID, "bib_record_id": h.BibRecordID,
				"email": nEmail, "name": nName, "expires_at": expires,
			})
		} else {
			_, _ = tx.BookCopy.UpdateOneID(*h.CopyID).SetStatus(bookcopy.StatusAVAILABLE).Save(ctx)
		}
	}

	// Notify the member whose hold expired.
	bibTitle := ""
	if bib, berr := s.db.BibRecord.Query().Where(bibrecord.IDEQ(h.BibRecordID)).Only(ctx); berr == nil {
		bibTitle = bib.Title
	}
	mEmail, mName := s.MemberContact(ctx, h.MemberID)
	_ = events.Publish(ctx, tx.OutboxEvent, h.TenantID, h.ID.String(), events.EventHoldExpired, map[string]any{
		"hold_id": h.ID, "member_id": h.MemberID, "bib_title": bibTitle,
		"email": mEmail, "name": mName,
	})
	if err := tx.Commit(); err != nil {
		s.log.Warn("hold expiry: commit failed", zap.String("hold", h.ID.String()), zap.Error(err))
	}
}
