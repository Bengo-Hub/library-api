package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BookCopy is a physical item/holding of a BibRecord at a Branch. Each carries a unique
// barcode (scanned at circulation) and an accession number. status is the live
// circulation state surfaced by OPAC.
type BookCopy struct {
	ent.Schema
}

func (BookCopy) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (BookCopy) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("bib_record_id", uuidType()),
		field.UUID("branch_id", uuidType()),
		field.String("barcode").NotEmpty().Comment("Unique per tenant; scanned at circulation"),
		field.String("accession_no").Optional(),
		field.String("call_number").Optional(),
		field.String("shelf_location").Optional(),
		field.Enum("status").
			Values("AVAILABLE", "ON_LOAN", "RESERVED", "IN_HOUSE", "IN_TRANSIT", "LOST", "DAMAGED", "REPAIR", "WITHDRAWN").
			Default("AVAILABLE"),
		field.String("condition").Default("good"),
		field.Bool("is_reference_only").Default(false).Comment("Reference/in-house reading only — never leaves the building"),
		moneyFieldOptional("acquisition_cost"),
		field.Time("acquisition_date").Optional().Nillable(),
		field.UUID("loan_policy_id", uuidType()).Optional().Nillable().Comment("Per-copy policy override"),
	}
}

func (BookCopy) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "barcode").Unique(),
		index.Fields("tenant_id", "bib_record_id"),
		index.Fields("tenant_id", "branch_id", "status"),
	}
}
