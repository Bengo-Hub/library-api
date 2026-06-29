package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DocumentSequence is the per-tenant monotonic counter behind human-readable numbers
// such as membership_no and accession_no. Allocation happens in a transaction with a
// row lock so concurrent checkouts/registrations never collide.
type DocumentSequence struct {
	ent.Schema
}

func (DocumentSequence) Mixin() []ent.Mixin {
	return []ent.Mixin{BaseMixin{}, TenantMixin{}}
}

func (DocumentSequence) Fields() []ent.Field {
	return []ent.Field{
		field.String("kind").NotEmpty().Comment("membership_no | accession_no | loan_no"),
		field.String("prefix").Optional(),
		field.Int64("next_value").Default(1),
		field.Int("pad_width").Default(5),
		field.String("format").Optional().
			Comment("Template: {prefix} {seq} {yy} {yyyy} {mm}. Empty = {prefix}{seq}."),
		field.String("reset_period").Default("none").
			Comment("none | yearly | monthly — when the counter restarts at 1"),
		field.String("period_key").Optional().
			Comment("Current period marker (e.g. 2026 or 2026-06); a change triggers a reset"),
	}
}

func (DocumentSequence) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "kind").Unique(),
	}
}
