package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Ebook is the digital edition of a BibRecord. The file lives on a per-tenant PVC
// (file_url is relative). lending_model drives concurrency: CONTROLLED_DIGITAL caps
// simultaneous active loans at max_concurrent_loans. is_purchasable + price enable the
// Phase-2 one-time purchase/download flow.
type Ebook struct {
	ent.Schema
}

func (Ebook) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Ebook) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("bib_record_id", uuidType()),
		field.String("file_url").NotEmpty().Comment("Relative PVC path"),
		field.Enum("format").Values("PDF", "EPUB", "AUDIO").Default("PDF"),
		field.Enum("drm_policy").Values("NONE", "WATERMARK", "TOKEN_GATED").Default("WATERMARK"),
		field.Enum("lending_model").
			Values("CONTROLLED_DIGITAL", "ONE_COPY_ONE_USER", "PURCHASE", "OPEN").
			Default("CONTROLLED_DIGITAL"),
		field.Int("max_concurrent_loans").Default(1),
		field.Int("loan_duration_days").Default(14),
		field.Bool("is_purchasable").Default(false),
		moneyField("price"),
		field.Int64("file_size").Optional(),
		field.String("checksum").Optional(),
	}
}

func (Ebook) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "bib_record_id"),
	}
}
