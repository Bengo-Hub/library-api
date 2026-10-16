package circulation

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	cr "github.com/bengobox/library-service/internal/ent/circulationrule"
)

// ResolvedRule holds the effective circulation parameters resolved for a (branch, tier, format) triple.
type ResolvedRule struct {
	*ent.CirculationRule
}

// DefaultRule returns a safe fallback when no rule is configured and no tier is known.
func DefaultRule() *ResolvedRule {
	r := &ent.CirculationRule{}
	r.LoanPeriodDays = 14
	r.MaxRenewals = 2
	r.Holdable = true
	return &ResolvedRule{r}
}

// RuleFromTier is the baseline used when no circulation rule matches: the member tier's own
// loan period, renewal cap and daily fine rate. Tiers stay the simple way to configure a
// library; the rule matrix only overrides them for specific branch, tier or format cases.
func RuleFromTier(t *ent.MemberTier) *ResolvedRule {
	r := DefaultRule()
	if t == nil {
		return r
	}
	if t.LoanPeriodDays > 0 {
		r.LoanPeriodDays = t.LoanPeriodDays
	}
	r.MaxRenewals = t.MaxRenewals
	r.FinePerDay = t.DailyFineRate
	return r
}

// BorrowingFee is the fee charged per book at checkout.
func (r *ResolvedRule) BorrowingFee() decimal.Decimal { return r.RentalCharge }

// LateRate is the charge for each day a book is returned late. A rule can tie it to the
// borrowing fee, otherwise it uses the rule's own rate and then the tier's daily rate.
func (r *ResolvedRule) LateRate(tierRate decimal.Decimal) decimal.Decimal {
	switch {
	case r.LateFeeUsesRentalCharge:
		return r.RentalCharge
	case r.FinePerDay.IsPositive():
		return r.FinePerDay
	default:
		return tierRate
	}
}

// LoanDuration is how long a new loan runs under this rule.
func (r *ResolvedRule) LoanDuration() time.Duration {
	if r.IsHourly && r.LoanPeriodHours > 0 {
		return time.Duration(r.LoanPeriodHours) * time.Hour
	}
	return time.Duration(r.LoanPeriodDays) * 24 * time.Hour
}

// ResolveRule returns the most specific CirculationRule for the given branch, tier and item
// format, falling back to the tier baseline when none matches. One indexed query fetches every
// candidate and the most specific wins, so a checkout never pays for a multi-step cascade.
func (s *Service) ResolveRule(ctx context.Context, tenantID, branchID uuid.UUID, tier *ent.MemberTier, format string) *ResolvedRule {
	tierID := uuid.Nil
	if tier != nil {
		tierID = tier.ID
	}
	if rule := s.bestRule(ctx, tenantID, branchID, tierID, format); rule != nil {
		return &ResolvedRule{rule}
	}
	return RuleFromTier(tier)
}

// bestRule scores each candidate by specificity. Branch outranks tier, and tier outranks
// format, which reproduces the 8-level cascade from most specific (branch+tier+format) down
// to the global default (all branches, all tiers, all formats).
func (s *Service) bestRule(ctx context.Context, tenantID, branchID, tierID uuid.UUID, format string) *ent.CirculationRule {
	q := s.db.CirculationRule.Query().Where(cr.TenantIDEQ(tenantID))
	if branchID == uuid.Nil {
		q = q.Where(cr.BranchIDIsNil())
	} else {
		q = q.Where(cr.Or(cr.BranchIDIsNil(), cr.BranchIDEQ(branchID)))
	}
	if tierID == uuid.Nil {
		q = q.Where(cr.TierIDIsNil())
	} else {
		q = q.Where(cr.Or(cr.TierIDIsNil(), cr.TierIDEQ(tierID)))
	}
	if format == "" {
		q = q.Where(cr.ItemFormatIsNil())
	} else {
		q = q.Where(cr.Or(cr.ItemFormatIsNil(), cr.ItemFormatEQ(cr.ItemFormat(format))))
	}
	rows, err := q.All(ctx)
	if err != nil {
		s.log.Warn("circulation rule lookup failed; using tier baseline", zap.Error(err))
		return nil
	}
	var best *ent.CirculationRule
	bestScore := -1
	for _, r := range rows {
		score := 0
		if r.BranchID != nil {
			score += 4
		}
		if r.TierID != nil {
			score += 2
		}
		if r.ItemFormat != nil {
			score++
		}
		// Duplicate dimensions are rejected on save; if legacy rows tie, the latest edit wins.
		if score > bestScore || (score == bestScore && r.UpdatedAt.After(best.UpdatedAt)) {
			best, bestScore = r, score
		}
	}
	return best
}
