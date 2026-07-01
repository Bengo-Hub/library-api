package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AcquisitionFund is a named allocation within an AcquisitionBudget.
type AcquisitionFund struct {
	ent.Schema
}

func (AcquisitionFund) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (AcquisitionFund) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("budget_id", uuidType()),
		field.String("name").NotEmpty(),
		field.String("code").Optional(),
		moneyField("allocated_amount"),
		moneyField("spent"),
		field.String("description").Optional(),
	}
}

func (AcquisitionFund) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "budget_id"),
		index.Fields("tenant_id", "code"),
	}
}
