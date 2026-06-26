package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BibRecord is the bibliographic master (the "work"/title). Physical copies and
// e-books both hang off a BibRecord. Carries ISBN/ISSN/classification plus a MARC-lite
// + Dublin Core JSON payload for richer cataloguing and OPAC search.
type BibRecord struct {
	ent.Schema
}

func (BibRecord) Mixin() []ent.Mixin {
	return []ent.Mixin{BaseMixin{}, TenantMixin{}}
}

func (BibRecord) Fields() []ent.Field {
	return []ent.Field{
		field.String("title").NotEmpty(),
		field.String("subtitle").Optional(),
		field.String("isbn10").Optional(),
		field.String("isbn13").Optional(),
		field.String("issn").Optional(),
		field.String("lccn").Optional(),
		field.String("edition").Optional(),
		field.String("language").Default("en"),
		field.String("ddc_classification").Optional().Comment("Dewey Decimal"),
		field.String("lc_call_number").Optional().Comment("Library of Congress call number"),
		field.Int("publication_year").Optional(),
		field.Int("page_count").Optional(),
		field.String("publisher_name").Optional().Comment("Denormalized for display; publisher_id is the ref"),
		field.UUID("publisher_id", uuidType()).Optional().Nillable(),
		field.UUID("primary_subject_id", uuidType()).Optional().Nillable(),
		field.UUID("collection_id", uuidType()).Optional().Nillable(),
		field.Enum("format").
			Values("PHYSICAL", "EBOOK", "AUDIOBOOK", "PERIODICAL").
			Default("PHYSICAL"),
		field.Enum("record_status").
			Values("DRAFT", "ACTIVE", "ARCHIVED", "WITHDRAWN").
			Default("ACTIVE"),
		field.Text("summary").Optional(),
		field.String("cover_image_url").Optional().Comment("Relative media path, resolved at read"),
		field.JSON("authors", []string{}).Optional().Comment("Author display names (ordered)"),
		field.JSON("dublin_core", map[string]any{}).Optional(),
		field.JSON("marc", map[string]any{}).Optional().Comment("MARC-lite leader/008/tags"),
		field.UUID("default_loan_policy_id", uuidType()).Optional().Nillable(),
	}
}

func (BibRecord) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "isbn13"),
		index.Fields("tenant_id", "isbn10"),
		index.Fields("tenant_id", "format"),
		index.Fields("tenant_id", "title"),
	}
}
