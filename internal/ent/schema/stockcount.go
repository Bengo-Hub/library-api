package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StockCount is a branch stocktake / cycle-count session. Staff scan present copies; on
// finalize, copies at the branch that were not scanned are flagged LOST and the variance is
// recorded. Scanned copy IDs are tracked inline (no separate line table) for simplicity.
type StockCount struct {
	ent.Schema
}

func (StockCount) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (StockCount) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("branch_id", uuidType()),
		field.String("reference").Optional(),
		field.Enum("status").
			Values("COUNTING", "COMPLETED", "CANCELLED").
			Default("COUNTING"),
		field.JSON("scanned_copy_ids", []string{}).Optional().
			Comment("Barcodes/copy IDs recorded as present during the count"),
		field.Int("expected_count").Default(0),
		field.Int("scanned_count").Default(0),
		field.Int("missing_count").Default(0),
		// Valuation trail (sum of BookCopy.acquisition_cost): expected at start, running total as
		// copies are scanned, and what's left unaccounted for once missing copies are flagged LOST.
		moneyField("expected_value"),
		moneyField("scanned_value"),
		moneyField("missing_value"),
		field.String("counted_by").Optional(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (StockCount) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "branch_id", "status"),
	}
}
