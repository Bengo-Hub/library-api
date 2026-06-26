package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// MembershipFee is a periodic membership charge (e.g. annual). Settled via a treasury
// payment intent, reconciled by the treasury.payment.succeeded consumer.
type MembershipFee struct {
	ent.Schema
}

func (MembershipFee) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (MembershipFee) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("member_id", uuidType()),
		field.Time("period_start"),
		field.Time("period_end"),
		moneyField("amount"),
		field.Enum("status").
			Values("PENDING", "PAID", "WAIVED", "CANCELLED").
			Default("PENDING"),
		field.String("treasury_intent_id").Optional(),
		field.Time("paid_at").Optional().Nillable(),
	}
}

func (MembershipFee) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "member_id", "status"),
		index.Fields("tenant_id", "treasury_intent_id"),
	}
}
