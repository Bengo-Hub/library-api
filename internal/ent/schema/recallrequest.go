package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// RecallRequest is created when a waiting hold requester asks to shorten an active loan.
// The loan's due_at is advanced to new_due_at and the borrower is notified to return early.
type RecallRequest struct {
	ent.Schema
}

func (RecallRequest) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (RecallRequest) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("loan_id", uuidType()),
		field.UUID("hold_id", uuidType()).Optional().Nillable(),
		field.UUID("requested_by_member_id", uuidType()),
		field.Time("new_due_at"),
		field.Time("notify_sent_at").Optional().Nillable(),
		field.Enum("status").
			Values("PENDING", "RETURNED", "CANCELLED").
			Default("PENDING"),
	}
}

func (RecallRequest) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "loan_id", "status"),
	}
}
