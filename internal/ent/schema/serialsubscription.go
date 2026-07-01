package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// SerialSubscription tracks a periodical subscription for a bib record.
type SerialSubscription struct {
	ent.Schema
}

func (SerialSubscription) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (SerialSubscription) Fields() []ent.Field {
	uuidType := func() uuid.UUID { return uuid.UUID{} }
	return []ent.Field{
		field.UUID("bib_record_id", uuidType()),
		field.UUID("vendor_id", uuidType()).Optional().Nillable(),
		field.UUID("fund_id", uuidType()).Optional().Nillable(),
		field.Time("start_date"),
		field.Time("end_date").Optional().Nillable(),
		field.Enum("frequency").Values("DAILY", "WEEKLY", "MONTHLY", "QUARTERLY", "ANNUAL"),
		moneyField("price"),
		field.String("currency_code").Default("KES"),
		field.Enum("status").Values("ACTIVE", "EXPIRED", "CANCELLED").Default("ACTIVE"),
		field.String("notes").Optional(),
	}
}

func (SerialSubscription) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "bib_record_id"),
		index.Fields("tenant_id", "status"),
	}
}
