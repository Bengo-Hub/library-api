package membership

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/events"
)

// PatronCategoryScheduler runs daily:
//  1. Members with expires_at < now and status=ACTIVE → status=EXPIRED
//  2. Members whose birth_date + max_age_years <= today and tier has graduated_tier_id → moved to graduated tier
type PatronCategoryScheduler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewPatronCategoryScheduler builds the scheduler.
func NewPatronCategoryScheduler(db *ent.Client, log *zap.Logger) *PatronCategoryScheduler {
	return &PatronCategoryScheduler{db: db, log: log}
}

// Start runs the sweep immediately then on the given interval (defaults to 24h).
func (s *PatronCategoryScheduler) Start(ctx context.Context, interval time.Duration) {
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

func (s *PatronCategoryScheduler) sweep(ctx context.Context) {
	now := time.Now()
	s.expireMembers(ctx, now)
	s.graduateMembers(ctx, now)
}

// expireMembers sets ACTIVE members whose membership has passed expires_at to EXPIRED.
func (s *PatronCategoryScheduler) expireMembers(ctx context.Context, now time.Time) {
	expired, err := s.db.Member.Query().
		Where(member.StatusEQ(member.StatusACTIVE), member.ExpiresAtLT(now), member.ExpiresAtNotNil()).
		Limit(500).All(ctx)
	if err != nil {
		s.log.Warn("patron expiry query failed", zap.Error(err))
		return
	}
	for _, m := range expired {
		if _, err := s.db.Member.UpdateOneID(m.ID).SetStatus(member.StatusEXPIRED).Save(ctx); err != nil {
			s.log.Warn("patron expiry update failed", zap.String("member", m.ID.String()), zap.Error(err))
			continue
		}
		_ = events.Publish(ctx, s.db.OutboxEvent, m.TenantID, m.ID.String(), events.EventMemberExpired, map[string]any{
			"member_id": m.ID, "email": m.ContactEmail, "name": m.DisplayName, "expires_at": m.ExpiresAt,
		})
	}
	if len(expired) > 0 {
		s.log.Info("patron expiry sweep", zap.Int("expired", len(expired)))
	}
}

// graduateMembers moves members whose age has exceeded their tier's max_age_years to the graduated tier.
func (s *PatronCategoryScheduler) graduateMembers(ctx context.Context, now time.Time) {
	// Load tiers that have both max_age_years and graduated_tier_id set.
	tiers, err := s.db.MemberTier.Query().
		Where(membertier.MaxAgeYearsNotNil(), membertier.GraduatedTierIDNotNil()).
		All(ctx)
	if err != nil {
		s.log.Warn("graduation tier query failed", zap.Error(err))
		return
	}
	for _, tier := range tiers {
		if tier.MaxAgeYears == nil || tier.GraduatedTierID == nil {
			continue
		}
		cutoff := now.AddDate(-(*tier.MaxAgeYears), 0, 0)
		// Find ACTIVE members in this tier born on or before the cutoff (i.e. older than max_age).
		members, merr := s.db.Member.Query().
			Where(member.TierID(tier.ID), member.StatusEQ(member.StatusACTIVE),
				member.BirthDateLTE(cutoff), member.BirthDateNotNil()).
			Limit(200).All(ctx)
		if merr != nil {
			s.log.Warn("graduation member query failed", zap.String("tier", tier.ID.String()), zap.Error(merr))
			continue
		}
		for _, m := range members {
			newTierID := *tier.GraduatedTierID
			// Compute new expires_at from graduated tier's enrollment period if set.
			var newTier *ent.MemberTier
			if gt, gterr := s.db.MemberTier.Get(ctx, newTierID); gterr == nil {
				newTier = gt
			}
			upd := s.db.Member.UpdateOneID(m.ID).SetTierID(newTierID)
			if newTier != nil && newTier.EnrollmentPeriodMonths != nil && m.JoinedAt != nil {
				newExpires := now.AddDate(0, *newTier.EnrollmentPeriodMonths, 0)
				upd = upd.SetExpiresAt(newExpires)
			}
			if _, err := upd.Save(ctx); err != nil {
				s.log.Warn("graduation update failed", zap.String("member", m.ID.String()), zap.Error(err))
				continue
			}
			_ = events.Publish(ctx, s.db.OutboxEvent, m.TenantID, m.ID.String(), events.EventMemberGraduated, map[string]any{
				"member_id": m.ID, "old_tier_id": tier.ID, "new_tier_id": newTierID,
				"email": m.ContactEmail, "name": m.DisplayName,
			})
		}
	}
}

// SetExpiresAtFromTier computes and sets expires_at on a new member based on their tier's
// enrollment_period_months (called at member creation time).
func SetExpiresAtFromTier(ctx context.Context, db *ent.Client, memberID uuid.UUID, tier *ent.MemberTier, joinedAt time.Time) {
	if tier.EnrollmentPeriodMonths == nil {
		return
	}
	expires := joinedAt.AddDate(0, *tier.EnrollmentPeriodMonths, 0)
	_, _ = db.Member.UpdateOneID(memberID).SetExpiresAt(expires).Save(ctx)
}
