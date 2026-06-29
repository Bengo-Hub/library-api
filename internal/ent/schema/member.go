package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Member is a library-owned patron registry record. It references the auth user_id
// (when the patron has an SSO login) and an optional crm_contact_id (marketflow is the
// customer SoT). Walk-in/anonymous patrons are supported via is_walk_in with no refs.
// Only a cached display_name/contact is held here — no PII duplication beyond refs.
type Member struct {
	ent.Schema
}

func (Member) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Member) Fields() []ent.Field {
	return []ent.Field{
		field.String("membership_no").NotEmpty().Comment("Human-readable, via DocumentSequence"),
		field.UUID("user_id", uuidType()).Optional().Nillable().Comment("auth-api user ref (SSO patrons)"),
		field.UUID("crm_contact_id", uuidType()).Optional().Nillable().Comment("marketflow contact ref (SoT)"),
		field.UUID("tier_id", uuidType()),
		field.UUID("home_branch_id", uuidType()).Optional().Nillable(),
		field.String("display_name").Optional().Comment("Cache for desk UX; SoT is auth/CRM"),
		field.String("contact_phone").Optional(),
		field.String("contact_email").Optional(),
		field.String("address").Optional(),
		field.String("notes").Optional(),
		field.Enum("status").
			Values("ACTIVE", "SUSPENDED", "EXPIRED", "BLOCKED", "PENDING").
			Default("ACTIVE"),
		field.Bool("is_walk_in").Default(false),
		field.Time("joined_at").Optional().Nillable(),
		field.Time("expires_at").Optional().Nillable(),
	}
}

func (Member) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "membership_no").Unique(),
		index.Fields("tenant_id", "user_id"),
		index.Fields("tenant_id", "crm_contact_id"),
		index.Fields("tenant_id", "status"),
	}
}
