package refdata

import (
	"context"

	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/loanpolicy"
	"github.com/bengobox/library-service/internal/ent/membertier"
)

// SeedGlobalTiersPolicies seeds shared default member tiers + loan policies under the nil-tenant
// (GlobalTenantID), read-only defaults every tenant sees (same model as global collections).
// Idempotent by (nil-tenant, name).
func SeedGlobalTiersPolicies(ctx context.Context, client *ent.Client, log *zap.Logger) error {
	type tier struct {
		name                                                       string
		loans, period, renewals, holds, ebooks                     int
		fineRate, maxFine, annual                                  string
		isDefault                                                  bool
	}
	tiers := []tier{
		{"Standard", 3, 14, 2, 5, 3, "10", "1000", "500", true},
		{"Student", 5, 21, 3, 8, 5, "5", "500", "200", false},
		{"Senior Citizen", 5, 28, 3, 8, 5, "5", "500", "0", false},
		{"Staff", 10, 30, 5, 15, 10, "0", "2000", "0", false},
	}
	for _, t := range tiers {
		exists, err := client.MemberTier.Query().
			Where(membertier.TenantID(GlobalTenantID), membertier.Name(t.name)).Exist(ctx)
		if err != nil || exists {
			continue
		}
		if _, err := client.MemberTier.Create().
			SetTenantID(GlobalTenantID).SetName(t.name).
			SetMaxConcurrentLoans(t.loans).SetLoanPeriodDays(t.period).SetMaxRenewals(t.renewals).
			SetHoldLimit(t.holds).SetEbookConcurrentLimit(t.ebooks).
			SetDailyFineRate(decimal.RequireFromString(t.fineRate)).
			SetMaxFineBeforeBlock(decimal.RequireFromString(t.maxFine)).
			SetAnnualFee(decimal.RequireFromString(t.annual)).
			SetIsDefault(t.isDefault).Save(ctx); err != nil {
			log.Warn("seed global tier failed", zap.String("name", t.name), zap.Error(err))
		}
	}

	type policy struct {
		name                       string
		period, renewals, grace    int
		holdable                   bool
		finePerDay                 string
		isDefault                  bool
	}
	policies := []policy{
		{"Standard Loan", 14, 2, 2, true, "10", true},
		{"Short Loan", 7, 1, 0, true, "20", false},
		{"Reference Only", 0, 0, 0, false, "0", false},
	}
	for _, p := range policies {
		exists, err := client.LoanPolicy.Query().
			Where(loanpolicy.TenantID(GlobalTenantID), loanpolicy.Name(p.name)).Exist(ctx)
		if err != nil || exists {
			continue
		}
		if _, err := client.LoanPolicy.Create().
			SetTenantID(GlobalTenantID).SetName(p.name).
			SetLoanPeriodDays(p.period).SetMaxRenewals(p.renewals).SetGraceDays(p.grace).
			SetHoldable(p.holdable).SetFinePerDay(decimal.RequireFromString(p.finePerDay)).
			SetIsDefault(p.isDefault).Save(ctx); err != nil {
			log.Warn("seed global policy failed", zap.String("name", p.name), zap.Error(err))
		}
	}
	return nil
}
