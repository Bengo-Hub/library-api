package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// SerialIssue tracks a single issue/volume of a serial subscription.
type SerialIssue struct {
	ent.Schema
}

func (SerialIssue) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (SerialIssue) Fields() []ent.Field {
	uuidType := func() uuid.UUID { return uuid.UUID{} }
	return []ent.Field{
		field.UUID("subscription_id", uuidType()),
		field.String("volume").Optional(),
		field.String("issue_no").Optional(),
		field.Time("expected_date"),
		field.Time("received_date").Optional().Nillable(),
		field.Enum("status").Values("EXPECTED", "RECEIVED", "LATE", "MISSING", "CLAIMED").Default("EXPECTED"),
		field.UUID("copy_id", uuidType()).Optional().Nillable(),
		field.String("notes").Optional(),
	}
}

func (SerialIssue) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "subscription_id"),
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "expected_date"),
	}
}
