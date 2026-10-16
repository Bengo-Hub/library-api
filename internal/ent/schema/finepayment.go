package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// FinePayment is one payment received against a fine or fee (borrowing fee, overdue fine,
// membership charge). It is the cash-desk ledger: partial payments, the method used and who
// took the money all live here, and fine.amount_paid is always the sum of these rows.
// Payments recorded by the librarian never touch treasury; treasury settlements arrive through
// the payment consumer and are stored with method TREASURY.
type FinePayment struct {
	ent.Schema
}

func (FinePayment) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (FinePayment) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("fine_id", uuidType()),
		field.UUID("member_id", uuidType()),
		moneyField("amount"),
		field.Enum("method").
			Values("CASH", "MPESA", "CARD", "BANK", "TREASURY", "OTHER").
			Default("CASH"),
		field.String("reference").Optional().Comment("Receipt, M-Pesa code or treasury intent id"),
		field.String("note").Optional(),
		field.String("received_by").Optional().Comment("Staff user id"),
		field.Time("paid_at").Default(time.Now),
		field.String("idempotency_key").Optional().Nillable().
			Comment("Replay guard: a second payment with the same key returns the first"),
	}
}

func (FinePayment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "fine_id"),
		index.Fields("tenant_id", "paid_at"),
		index.Fields("tenant_id", "idempotency_key").Unique(),
	}
}
