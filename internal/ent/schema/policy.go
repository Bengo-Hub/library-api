package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// MemberTier defines the borrowing entitlements + fees for a class of member
// (e.g. Standard, Student, Researcher). Limits feed the circulation rules engine.
type MemberTier struct {
	ent.Schema
}

func (MemberTier) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (MemberTier) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Int("max_concurrent_loans").Default(3),
		field.Int("loan_period_days").Default(14),
		field.Int("max_renewals").Default(2),
		field.Int("hold_limit").Default(5),
		field.Int("ebook_concurrent_limit").Default(3),
		rateField("daily_fine_rate"),
		moneyField("max_fine_before_block"),
		moneyField("annual_fee"),
		field.Bool("is_default").Default(false),
		// Patron category auto-transition fields.
		field.Int("enrollment_period_months").Optional().Nillable().Comment("Auto-set expires_at = joined_at + N months on member creation"),
		field.Int("max_age_years").Optional().Nillable().Comment("Auto-move to graduated_tier_id when member reaches this age"),
		field.Int("min_age_years").Optional().Nillable().Comment("Minimum age for this tier (advisory)"),
		field.UUID("graduated_tier_id", uuidType()).Optional().Nillable().Comment("Tier to auto-move to when member exceeds max_age_years"),
	}
}

func (MemberTier) Indexes() []ent.Index {
	return []ent.Index{index.Fields("tenant_id", "name").Unique()}
}

// LoanPolicy is a reusable circulation policy resolvable at copy → bib → tier → tenant
// precedence. Drives loan period, renewals, holdability and fine accrual.
type LoanPolicy struct {
	ent.Schema
}

func (LoanPolicy) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (LoanPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Int("loan_period_days").Default(14),
		field.Int("max_renewals").Default(2),
		field.Bool("holdable").Default(true),
		rateField("fine_per_day"),
		field.Int("grace_days").Default(0),
		field.Bool("is_default").Default(false),
	}
}

func (LoanPolicy) Indexes() []ent.Index {
	return []ent.Index{index.Fields("tenant_id", "name").Unique()}
}
