package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Tenant is a thin local projection of the auth-api tenant (SoT). It caches slug +
// display name for slug→UUID resolution and JIT provisioning; branding is NOT stored
// here (auth-api owns branding, resolved via Redis cache).
type Tenant struct {
	ent.Schema
}

func (Tenant) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Immutable().
			Comment("Mirrors the auth-api tenant UUID (not generated locally)"),
		field.String("slug").NotEmpty(),
		field.String("name").Optional(),
		field.String("region").Optional(),
		field.Bool("is_active").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Tenant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("slug").Unique(),
	}
}
