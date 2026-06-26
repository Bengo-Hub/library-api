package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EbookLoan is an active controlled-digital-lending grant. mode distinguishes an
// in-browser reading session from a (Phase-2) downloaded copy. access_token is a
// short-lived signed token gating the reader; last_read_position persists progress.
type EbookLoan struct {
	ent.Schema
}

func (EbookLoan) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (EbookLoan) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("ebook_id", uuidType()),
		field.UUID("member_id", uuidType()),
		field.Enum("mode").Values("ONLINE_READ", "DOWNLOAD").Default("ONLINE_READ"),
		field.Time("issued_at"),
		field.Time("expires_at"),
		field.Time("returned_at").Optional().Nillable(),
		field.String("access_token").Optional(),
		field.JSON("last_read_position", map[string]any{}).Optional(),
	}
}

func (EbookLoan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "ebook_id", "returned_at"),
		index.Fields("tenant_id", "member_id"),
	}
}

// EbookPurchase records a Phase-2 one-time purchase, settled via a treasury intent.
// download_token gates the secured download; download_count enforces any cap.
type EbookPurchase struct {
	ent.Schema
}

func (EbookPurchase) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (EbookPurchase) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("ebook_id", uuidType()),
		field.UUID("member_id", uuidType()),
		field.String("treasury_intent_id").Optional(),
		moneyField("amount"),
		field.Enum("status").Values("PENDING", "PAID", "REFUNDED").Default("PENDING"),
		field.String("download_token").Optional(),
		field.Int("download_count").Default(0),
		field.Time("purchased_at").Optional().Nillable(),
	}
}

func (EbookPurchase) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "member_id"),
		index.Fields("tenant_id", "treasury_intent_id"),
	}
}
