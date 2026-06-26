package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Hold is a reservation request on a BibRecord. Holds queue by position; on return of a
// copy the next WAITING hold is promoted to READY (with a pickup expiry). copy_id is set
// only once a specific copy is assigned at fulfillment.
type Hold struct {
	ent.Schema
}

func (Hold) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Hold) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("bib_record_id", uuidType()),
		field.UUID("member_id", uuidType()),
		field.UUID("branch_id", uuidType()),
		field.UUID("copy_id", uuidType()).Optional().Nillable().Comment("Assigned at fulfillment"),
		field.Int("queue_position").Default(0),
		field.Enum("status").
			Values("WAITING", "READY", "FULFILLED", "CANCELLED", "EXPIRED").
			Default("WAITING"),
		field.Time("placed_at"),
		field.Time("ready_at").Optional().Nillable(),
		field.Time("expires_at").Optional().Nillable().Comment("Pickup deadline once READY"),
	}
}

func (Hold) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "bib_record_id", "status"),
		index.Fields("tenant_id", "member_id", "status"),
	}
}
