package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuthorizedValue implements Koha's controlled vocabulary system. Each row is one
// entry within a named category (LOC, CCODE, NOT_LOAN, PAYMENT_TYPE, etc.).
// is_system rows are seeded on startup and may not be deleted.
type AuthorizedValue struct {
	ent.Schema
}

func (AuthorizedValue) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (AuthorizedValue) Fields() []ent.Field {
	return []ent.Field{
		field.String("category").NotEmpty(),
		field.String("value").NotEmpty(),
		field.String("label").Optional(),
		field.String("description").Optional(),
		field.Bool("is_system").Default(false),
		field.Int("display_order").Default(0),
		field.Bool("is_active").Default(true),
	}
}

func (AuthorizedValue) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "category", "value").Unique(),
		index.Fields("tenant_id", "category", "display_order"),
	}
}
