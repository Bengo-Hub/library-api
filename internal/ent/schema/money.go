package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/shopspring/decimal"
)

// moneyField builds a fixed-precision decimal money column (numeric(18,4)) backed by
// shopspring/decimal — NEVER float. All library money columns (fines, fees, prices,
// acquisition cost) MUST go through this helper so the Postgres type, Go type and
// default are uniform. Returns a fully-built field (NOT NULL, default 0).
func moneyField(name string) ent.Field {
	return field.Other(name, decimal.Decimal{}).
		SchemaType(map[string]string{"postgres": "numeric(18,4)"}).
		Default(decimal.Zero)
}

// moneyFieldOptional is the nullable variant of moneyField.
func moneyFieldOptional(name string) ent.Field {
	return field.Other(name, decimal.Decimal{}).
		SchemaType(map[string]string{"postgres": "numeric(18,4)"}).
		Optional().
		Nillable()
}

// rateField builds a percentage/rate decimal column (numeric(10,4)) — e.g. daily
// fine rate. Same decimal guarantees as moneyField.
func rateField(name string) ent.Field {
	return field.Other(name, decimal.Decimal{}).
		SchemaType(map[string]string{"postgres": "numeric(10,4)"}).
		Default(decimal.Zero)
}
