package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Loan is a circulation transaction (one copy out to one member). in_house marks a
// reference/reading-room session that never leaves the building (auto-closed at branch
// close by the scheduler).
type Loan struct {
	ent.Schema
}

func (Loan) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Loan) Fields() []ent.Field {
	return []ent.Field{
		field.String("loan_no").Optional(),
		field.String("client_reference").Optional().
			Comment("Offline idempotency key (client-generated); checkout is get-or-create on it"),
		field.UUID("copy_id", uuidType()),
		field.UUID("member_id", uuidType()),
		field.UUID("branch_id", uuidType()),
		field.Time("checkout_at"),
		field.Time("due_at"),
		field.Time("returned_at").Optional().Nillable(),
		field.Int("renewals_count").Default(0),
		field.Enum("status").
			Values("ACTIVE", "RETURNED", "OVERDUE", "LOST", "CLAIMED_RETURNED").
			Default("ACTIVE"),
		field.Bool("in_house").Default(false),
		field.String("checked_out_by").Optional().Comment("Staff user id"),
		field.String("returned_by").Optional(),
	}
}

func (Loan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "member_id", "status"),
		index.Fields("tenant_id", "copy_id", "status"),
		index.Fields("tenant_id", "status", "due_at"),
	}
}
