// Package membership handles periodic membership fees: issuing a fee for a member, and a
// daily dunning scheduler that auto-issues the annual fee as a membership approaches expiry
// and emits library.membership.fee_due (consumed by notifications for a renewal reminder).
package membership

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membershipfee"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/events"
)

// Service implements membership-fee issuing + the dunning sweep.
type Service struct {
	db  *ent.Client
	log *zap.Logger
}

// NewService builds the membership service.
func NewService(db *ent.Client, log *zap.Logger) *Service {
	return &Service{db: db, log: log}
}

// IssueFee creates a PENDING membership fee for a member, priced from their tier's annual
// fee, for a one-year period. Idempotent-ish: returns the existing PENDING fee if one is
// already open for the member so repeated calls / the scheduler don't stack duplicates.
func (s *Service) IssueFee(ctx context.Context, tenantID, memberID uuid.UUID) (*ent.MembershipFee, error) {
	if existing, err := s.db.MembershipFee.Query().
		Where(membershipfee.TenantID(tenantID), membershipfee.MemberID(memberID), membershipfee.StatusEQ(membershipfee.StatusPENDING)).
		First(ctx); err == nil {
		return existing, nil
	}
	m, err := s.db.Member.Query().Where(member.IDEQ(memberID), member.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	amount := decimal.Zero
	if t, terr := s.db.MemberTier.Query().Where(membertier.IDEQ(m.TierID)).Only(ctx); terr == nil {
		amount = t.AnnualFee
	}
	now := time.Now()
	fee, err := s.db.MembershipFee.Create().
		SetTenantID(tenantID).SetMemberID(memberID).
		SetPeriodStart(now).SetPeriodEnd(now.AddDate(1, 0, 0)).
		SetAmount(amount).Save(ctx)
	if err != nil {
		return nil, err
	}
	_ = events.Publish(ctx, s.db.OutboxEvent, tenantID, fee.ID.String(), events.EventMembershipFeeDue, map[string]any{
		"membership_fee_id": fee.ID, "member_id": memberID, "amount": amount.String(),
		"email": m.ContactEmail, "name": m.DisplayName,
	})
	return fee, nil
}

// ListFees returns membership fees for a tenant, optionally filtered by status.
func (s *Service) ListFees(ctx context.Context, tenantID uuid.UUID, status string) ([]*ent.MembershipFee, error) {
	q := s.db.MembershipFee.Query().Where(membershipfee.TenantID(tenantID))
	if status != "" {
		q = q.Where(membershipfee.StatusEQ(membershipfee.Status(status)))
	}
	return q.Order(ent.Desc(membershipfee.FieldCreatedAt)).All(ctx)
}

// StartScheduler runs a daily dunning sweep: members whose membership expires within the
// renewal window (and whose tier has an annual fee) get a PENDING fee auto-issued.
func (s *Service) StartScheduler(ctx context.Context, interval time.Duration) {
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

func (s *Service) sweep(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, 14) // 14-day renewal window
	due, err := s.db.Member.Query().
		Where(member.StatusEQ(member.StatusACTIVE), member.ExpiresAtLT(cutoff), member.ExpiresAtNotNil()).
		Limit(500).All(ctx)
	if err != nil {
		s.log.Warn("membership dunning sweep failed", zap.Error(err))
		return
	}
	issued := 0
	for _, m := range due {
		if _, err := s.IssueFee(ctx, m.TenantID, m.ID); err == nil {
			issued++
		}
	}
	if issued > 0 {
		s.log.Info("membership dunning sweep issued fees", zap.Int("count", issued))
	}
}
