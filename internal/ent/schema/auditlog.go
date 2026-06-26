package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuditLog records sensitive mutations (waivers, withdrawals, manual overrides) for
// the library service. Scoped by aggregate_type so one table serves every domain.
type AuditLog struct {
	ent.Schema
}

func (AuditLog) Mixin() []ent.Mixin {
	return []ent.Mixin{BaseMixin{}, TenantMixin{}}
}

func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.String("user_id").Optional().Comment("Actor (auth user) or service account"),
		field.String("aggregate_type").NotEmpty().Comment("e.g. loan, member, fine, ebook"),
		field.String("aggregate_id").Optional(),
		field.String("action").NotEmpty().Comment("e.g. checkout, return, waive, withdraw"),
		field.JSON("changes", map[string]any{}).Optional(),
		field.String("ip_address").Optional(),
	}
}

func (AuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "aggregate_type", "aggregate_id"),
		index.Fields("tenant_id", "created_at"),
	}
}
