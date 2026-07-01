package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PurchaseOrder is an order placed with a Vendor to acquire library materials.
type PurchaseOrder struct {
	ent.Schema
}

func (PurchaseOrder) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (PurchaseOrder) Fields() []ent.Field {
	return []ent.Field{
		field.String("po_number").Optional().Comment("Allocated from DocumentSequence kind=purchase_order"),
		field.UUID("vendor_id", uuidType()),
		field.UUID("fund_id", uuidType()).Optional().Nillable(),
		field.Enum("status").Values("DRAFT", "SUBMITTED", "PARTIAL", "RECEIVED", "CANCELLED").Default("DRAFT"),
		field.Time("order_date").Optional().Nillable(),
		field.Time("expected_date").Optional().Nillable(),
		field.String("notes").Optional(),
		moneyField("subtotal"),
		moneyField("tax"),
		moneyField("total"),
		field.String("currency_code").Default("KES"),
	}
}

func (PurchaseOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "vendor_id"),
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "po_number"),
	}
}
