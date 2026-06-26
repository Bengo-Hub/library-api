package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Fine is a charge against a member (overdue, lost, damage, membership). Payment is
// settled via a treasury payment intent; treasury_intent_id links the two and the
// treasury.payment.succeeded consumer flips status to PAID (idempotent on that id).
type Fine struct {
	ent.Schema
}

func (Fine) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Fine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("member_id", uuidType()),
		field.UUID("loan_id", uuidType()).Optional().Nillable(),
		field.Enum("reason").
			Values("OVERDUE", "LOST", "DAMAGE", "MEMBERSHIP", "OTHER").
			Default("OVERDUE"),
		field.String("description").Optional(),
		moneyField("amount"),
		moneyField("amount_paid"),
		field.Enum("status").
			Values("UNPAID", "PARTIAL", "PAID", "WAIVED").
			Default("UNPAID"),
		field.String("treasury_intent_id").Optional(),
		field.String("waived_by").Optional(),
		field.Time("assessed_at").Optional().Nillable(),
		field.Time("paid_at").Optional().Nillable(),
	}
}

func (Fine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "member_id", "status"),
		index.Fields("tenant_id", "treasury_intent_id"),
	}
}
