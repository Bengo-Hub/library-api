package circulation

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
	cr "github.com/bengobox/library-service/internal/ent/circulationrule"
)

// ResolvedRule holds the effective circulation parameters resolved for a (branch, tier, format) triple.
type ResolvedRule struct {
	*ent.CirculationRule
}

// DefaultRule returns a safe fallback when no rule is configured.
func DefaultRule() *ResolvedRule {
	r := &ent.CirculationRule{}
	r.LoanPeriodDays = 14
	r.MaxRenewals = 2
	r.Holdable = true
	r.GraceDays = 0
	r.IsHourly = false
	return &ResolvedRule{r}
}

// ResolveRule finds the most specific CirculationRule matching the given branch, tier,
// and item format using Koha's 8-level specificity cascade (most specific → broadest).
// Results are cached in Redis for 60 s; changes to rules must call InvalidateRuleCache.
func (s *Service) ResolveRule(ctx context.Context, tenantID, branchID, tierID uuid.UUID, format string) *ResolvedRule {
	if s.cache != nil {
		if rule := s.ruleFromCache(ctx, tenantID, branchID, tierID, format); rule != nil {
			return rule
		}
	}

	rule := s.resolveRuleDB(ctx, tenantID, branchID, tierID, format)
	if rule == nil {
		return DefaultRule()
	}

	if s.cache != nil {
		s.cacheRule(ctx, tenantID, branchID, tierID, format, rule)
	}
	return &ResolvedRule{rule}
}

// resolveRuleDB runs the 8-level specificity cascade against the database.
func (s *Service) resolveRuleDB(ctx context.Context, tenantID, branchID, tierID uuid.UUID, format string) *ent.CirculationRule {
	nilBranch := branchID == uuid.Nil
	nilTier := tierID == uuid.Nil
	nilFormat := format == ""

	fmtVal := cr.ItemFormat(format)

	type candidate struct {
		branchPred func() bool // true = specific, false = all
		tierPred   func() bool
		formatPred func() bool
		query      func() (*ent.CirculationRule, error)
	}

	// Helpers to build branch / tier / format predicates for a query.
	withBranch := func(specific bool) func(q *ent.CirculationRuleQuery) *ent.CirculationRuleQuery {
		return func(q *ent.CirculationRuleQuery) *ent.CirculationRuleQuery {
			if specific {
				return q.Where(cr.BranchIDEQ(branchID))
			}
			return q.Where(cr.BranchIDIsNil())
		}
	}
	withTier := func(specific bool) func(q *ent.CirculationRuleQuery) *ent.CirculationRuleQuery {
		return func(q *ent.CirculationRuleQuery) *ent.CirculationRuleQuery {
			if specific {
				return q.Where(cr.TierIDEQ(tierID))
			}
			return q.Where(cr.TierIDIsNil())
		}
	}
	withFormat := func(specific bool) func(q *ent.CirculationRuleQuery) *ent.CirculationRuleQuery {
		return func(q *ent.CirculationRuleQuery) *ent.CirculationRuleQuery {
			if specific {
				return q.Where(cr.ItemFormatEQ(fmtVal))
			}
			return q.Where(cr.ItemFormatIsNil())
		}
	}

	tryQuery := func(bSpecific, tSpecific, fSpecific bool) *ent.CirculationRule {
		if bSpecific && nilBranch {
			return nil
		}
		if tSpecific && nilTier {
			return nil
		}
		if fSpecific && nilFormat {
			return nil
		}
		q := s.db.CirculationRule.Query().Where(cr.TenantIDEQ(tenantID))
		q = withBranch(bSpecific)(q)
		q = withTier(tSpecific)(q)
		q = withFormat(fSpecific)(q)
		rule, err := q.First(ctx)
		if err != nil {
			return nil
		}
		return rule
	}

	// 8-level cascade: most specific (B+T+F) → least specific (nil+nil+nil).
	levels := [8][3]bool{
		{true, true, true},   // 1. branch + tier + format
		{true, true, false},  // 2. branch + tier + all formats
		{true, false, true},  // 3. branch + all tiers + format
		{true, false, false}, // 4. branch + all tiers + all formats
		{false, true, true},  // 5. default + tier + format
		{false, true, false}, // 6. default + tier + all formats
		{false, false, true}, // 7. default + all tiers + format
		{false, false, false}, // 8. default + all tiers + all formats (global fallback)
	}
	for _, l := range levels {
		if r := tryQuery(l[0], l[1], l[2]); r != nil {
			return r
		}
	}
	return nil
}

// InvalidateRuleCache clears the rule cache for a tenant when admin rules change.
func (s *Service) InvalidateRuleCache(ctx context.Context, tenantID uuid.UUID) {
	if s.cache == nil {
		return
	}
	pattern := fmt.Sprintf("cirrule:%s:*", tenantID.String())
	keys, err := s.cache.Keys(ctx, pattern).Result()
	if err != nil || len(keys) == 0 {
		return
	}
	_ = s.cache.Del(ctx, keys...).Err()
}

func ruleCacheKey(tenantID, branchID, tierID uuid.UUID, format string) string {
	return fmt.Sprintf("cirrule:%s:%s:%s:%s", tenantID, branchID, tierID, format)
}

func (s *Service) ruleFromCache(ctx context.Context, tenantID, branchID, tierID uuid.UUID, format string) *ResolvedRule {
	// Simple cache marker: store rule ID only; then fetch from DB for full struct.
	// This avoids JSON serialisation of decimal types.
	key := ruleCacheKey(tenantID, branchID, tierID, format)
	idStr, err := s.cache.Get(ctx, key).Result()
	if err != nil {
		return nil
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil
	}
	rule, err := s.db.CirculationRule.Get(ctx, id)
	if err != nil {
		return nil
	}
	return &ResolvedRule{rule}
}

func (s *Service) cacheRule(ctx context.Context, tenantID, branchID, tierID uuid.UUID, format string, rule *ent.CirculationRule) {
	key := ruleCacheKey(tenantID, branchID, tierID, format)
	_ = s.cache.Set(ctx, key, rule.ID.String(), 60*1e9).Err() // 60s TTL
}
