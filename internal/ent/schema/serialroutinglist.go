package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// SerialRoutingList defines the ordered circulation routing for a subscription.
type SerialRoutingList struct {
	ent.Schema
}

func (SerialRoutingList) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (SerialRoutingList) Fields() []ent.Field {
	uuidType := func() uuid.UUID { return uuid.UUID{} }
	return []ent.Field{
		field.UUID("subscription_id", uuidType()),
		field.UUID("member_id", uuidType()),
		field.Int("position").Default(0),
	}
}

func (SerialRoutingList) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "subscription_id", "member_id").Unique(),
		index.Fields("tenant_id", "subscription_id", "position"),
	}
}
