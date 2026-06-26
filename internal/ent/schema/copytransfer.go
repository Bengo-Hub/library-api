package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CopyTransfer records the inter-branch movement of a physical copy. While in transit the
// copy's status is IN_TRANSIT; on receipt the copy is reassigned to the destination branch
// and returned to AVAILABLE.
type CopyTransfer struct {
	ent.Schema
}

func (CopyTransfer) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (CopyTransfer) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("copy_id", uuidType()),
		field.UUID("from_branch_id", uuidType()),
		field.UUID("to_branch_id", uuidType()),
		field.Enum("status").
			Values("IN_TRANSIT", "RECEIVED", "CANCELLED").
			Default("IN_TRANSIT"),
		field.String("initiated_by").Optional(),
		field.String("received_by").Optional(),
		field.String("notes").Optional(),
		field.Time("received_at").Optional().Nillable(),
	}
}

func (CopyTransfer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "copy_id"),
	}
}
