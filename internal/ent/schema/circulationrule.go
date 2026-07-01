package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CirculationRule implements a 3-dimensional (Branch × Patron Tier × Item Format) matrix
// of circulation parameters. The rule_resolver picks the most specific matching rule via
// an 8-level specificity cascade (see internal/modules/circulation/rule_resolver.go).
// NULL in branch_id / tier_id / format means "applies to all" at that dimension.
type CirculationRule struct {
	ent.Schema
}

func (CirculationRule) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (CirculationRule) Fields() []ent.Field {
	return []ent.Field{
		// Dimensions — null means "all" for that dimension (specificity cascade).
		field.UUID("branch_id", uuidType()).Optional().Nillable().Comment("NULL = all branches"),
		field.UUID("tier_id", uuidType()).Optional().Nillable().Comment("NULL = all patron tiers"),
		field.Enum("item_format").
			Values("PHYSICAL", "EBOOK", "AUDIOBOOK", "PERIODICAL").
			Optional().Nillable().Comment("NULL = all item formats"),

		// Loan parameters.
		field.Int("loan_period_days").Default(14),
		field.Int("loan_period_hours").Default(0).Comment("Used when is_hourly=true"),
		field.Bool("is_hourly").Default(false),
		field.Int("max_renewals").Default(2),
		field.Bool("holdable").Default(true),

		// Fine parameters.
		rateField("fine_per_day"),
		field.Int("grace_days").Default(0),
		moneyField("max_fine_cap"),
		field.Bool("cap_fine_at_replacement_price").Default(false),

		// Item financial parameters (wired in Task 9).
		moneyField("rental_charge"),
		moneyField("replacement_cost"),
		moneyField("processing_fee"),

		// Due-date calculation mode.
		field.Enum("due_date_mode").
			Values("DAYS", "CALENDAR", "DATEDUE", "DAYWEEK").
			Default("DAYS").
			Comment("DAYS=raw count; CALENDAR=skip closed days; DATEDUE=push past closure; DAYWEEK=push to matching open weekday"),

		field.String("label").Optional().Comment("Human-readable admin label for this rule"),
	}
}

func (CirculationRule) Indexes() []ent.Index {
	return []ent.Index{
		// Composite lookup index for rule resolver queries.
		index.Fields("tenant_id", "branch_id", "tier_id", "item_format"),
	}
}
