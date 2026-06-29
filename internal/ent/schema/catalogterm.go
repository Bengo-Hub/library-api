package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CatalogTerm is a reusable cataloging dictionary value (author, publisher, place of publication,
// subject) used to power searchable "filter existing or add new" pickers in the cataloging form.
// BibRecord keeps its own denormalized name strings; these rows are just the suggestion corpus,
// grown automatically as titles are catalogued.
type CatalogTerm struct {
	ent.Schema
}

func (CatalogTerm) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (CatalogTerm) Fields() []ent.Field {
	return []ent.Field{
		field.String("kind").NotEmpty().Comment("author | publisher | place | subject"),
		field.String("value").NotEmpty(),
	}
}

func (CatalogTerm) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "kind", "value").Unique(),
		index.Fields("tenant_id", "kind"),
	}
}
