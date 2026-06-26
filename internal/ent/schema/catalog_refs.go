package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Author is a controlled author/contributor record. BibRecord also stores denormalized
// author display names for fast OPAC rendering; this table backs authority/browse.
type Author struct {
	ent.Schema
}

func (Author) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Author) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("sort_name").Optional().Comment("e.g. Surname, Forename"),
		field.Text("biography").Optional(),
	}
}

func (Author) Indexes() []ent.Index {
	return []ent.Index{index.Fields("tenant_id", "name")}
}

// Publisher is a controlled publisher record.
type Publisher struct {
	ent.Schema
}

func (Publisher) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Publisher) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("place").Optional(),
	}
}

func (Publisher) Indexes() []ent.Index {
	return []ent.Index{index.Fields("tenant_id", "name")}
}

// Subject is a hierarchical subject heading (self-referential via parent_id) under a
// classification scheme (LCSH/DDC/local).
type Subject struct {
	ent.Schema
}

func (Subject) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Subject) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("code").Optional(),
		field.Enum("scheme").Values("LCSH", "DDC", "LOCAL").Default("LOCAL"),
		field.UUID("parent_id", uuidType()).Optional().Nillable(),
	}
}

func (Subject) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name"),
		index.Fields("tenant_id", "parent_id"),
	}
}

// Collection is a hierarchical shelving/grouping (e.g. Reference, Children's, Research).
// is_reference_only marks collections whose copies cannot leave the building.
type Collection struct {
	ent.Schema
}

func (Collection) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Collection) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("code").Optional(),
		field.UUID("parent_id", uuidType()).Optional().Nillable(),
		field.Bool("is_reference_only").Default(false),
	}
}

func (Collection) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "name"),
		index.Fields("tenant_id", "parent_id"),
	}
}
