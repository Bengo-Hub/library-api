package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Vendor is a book supplier / publisher used in purchase orders.
type Vendor struct {
	ent.Schema
}

func (Vendor) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (Vendor) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("code").Optional(),
		field.String("contact_name").Optional(),
		field.String("contact_email").Optional(),
		field.String("contact_phone").Optional(),
		field.String("address").Optional(),
		field.String("website").Optional(),
		field.String("account_number").Optional(),
		field.Enum("payment_terms").Values("NET_30", "NET_60", "COD", "PREPAID").Default("NET_30"),
		field.String("notes").Optional(),
		field.Bool("is_active").Default(true),
	}
}

func (Vendor) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "name"),
	}
}
