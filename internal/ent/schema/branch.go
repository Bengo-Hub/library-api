package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Branch is a physical library branch/location (analogous to inventory's warehouse).
// Copies live at a branch; opening hours drive due-date calculation (skip closed days).
type Branch struct {
	ent.Schema
}

func (Branch) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Branch) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("code").NotEmpty(),
		field.String("address").Optional(),
		field.Float("latitude").Optional(),
		field.Float("longitude").Optional(),
		field.UUID("outlet_id", uuidType()).Optional().Nillable().
			Comment("Optional link to an auth-api outlet for outlet-scoped staff"),
		field.JSON("opening_hours", map[string]any{}).Optional().
			Comment("Per-weekday open/close used for due-date rollover"),
		field.Bool("is_default").Default(false),
		field.Bool("is_active").Default(true),
	}
}

func (Branch) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
	}
}
