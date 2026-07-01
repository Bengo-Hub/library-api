package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AcquisitionInvoice tracks a vendor invoice linked to a PurchaseOrder,
// settled via treasury-api (reference_type=acquisition_invoice).
type AcquisitionInvoice struct {
	ent.Schema
}

func (AcquisitionInvoice) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (AcquisitionInvoice) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("vendor_id", uuidType()),
		field.UUID("po_id", uuidType()).Optional().Nillable(),
		field.String("invoice_no").Optional().Comment("Vendor's invoice number"),
		field.String("reference_id").Optional().Comment("LIB-ACQ-{HEX} — our Paystack-identifiable reference"),
		field.UUID("treasury_invoice_id", uuidType()).Optional().Nillable().Comment("ID returned by treasury-api"),
		field.Time("invoice_date").Optional().Nillable(),
		moneyField("amount"),
		field.Enum("status").Values("PENDING", "PAID", "CANCELLED").Default("PENDING"),
		field.String("notes").Optional(),
	}
}

func (AcquisitionInvoice) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "vendor_id"),
		index.Fields("tenant_id", "po_id"),
		index.Fields("tenant_id", "status"),
	}
}
