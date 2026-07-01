package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PurchaseOrderLine is a single title/item line on a PurchaseOrder.
type PurchaseOrderLine struct {
	ent.Schema
}

func (PurchaseOrderLine) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (PurchaseOrderLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("po_id", uuidType()),
		field.UUID("bib_record_id", uuidType()).Optional().Nillable().Comment("Set when matched to an existing bib"),
		field.String("title").Optional(),
		field.String("isbn").Optional(),
		field.String("author").Optional(),
		moneyField("unit_price"),
		field.Int("quantity").Default(1),
		field.Int("received_qty").Default(0),
		field.Enum("status").Values("PENDING", "PARTIAL", "RECEIVED", "CANCELLED").Default("PENDING"),
		field.String("notes").Optional(),
	}
}

func (PurchaseOrderLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "po_id"),
	}
}
