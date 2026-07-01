package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// MemberNotificationPref stores per-member notification opt-in/out per event type + channel.
// Absent rows inherit the tier default; a row overrides it.
type MemberNotificationPref struct {
	ent.Schema
}

func (MemberNotificationPref) Mixin() []ent.Mixin { return []ent.Mixin{BaseMixin{}, TenantMixin{}} }

func (MemberNotificationPref) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("member_id", uuidType()),
		field.String("event_type").NotEmpty().Comment("e.g. loan.overdue, hold.ready, fine.assessed"),
		field.Enum("channel").Values("EMAIL", "SMS", "WHATSAPP", "PUSH", "NONE"),
		field.Bool("is_enabled").Default(true),
	}
}

func (MemberNotificationPref) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "member_id", "event_type", "channel").Unique(),
	}
}
