package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AcquisitionBudget is a fiscal-year budget envelope for library acquisitions.
type AcquisitionBudget struct {
	ent.Schema
}

func (AcquisitionBudget) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (AcquisitionBudget) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Int("fiscal_year"),
		moneyField("total_amount"),
		moneyField("allocated"),
		moneyField("spent"),
		field.Enum("status").Values("OPEN", "CLOSED").Default("OPEN"),
		field.String("notes").Optional(),
	}
}

func (AcquisitionBudget) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "fiscal_year"),
		index.Fields("tenant_id", "name"),
	}
}
