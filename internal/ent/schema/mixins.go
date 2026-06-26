package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

// BaseMixin provides the UUID primary key + created/updated timestamps shared by
// every library entity. Keep schema files lean — embed this via Mixin().
type BaseMixin struct {
	mixin.Schema
}

func (BaseMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// TenantMixin adds the tenant_id scoping column + a leading index. Business data
// carries tenant_id; global reference data (roles/permissions) does NOT embed this.
type TenantMixin struct {
	mixin.Schema
}

func (TenantMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("tenant_id", uuid.UUID{}),
	}
}

func (TenantMixin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
	}
}

// uuidType returns the zero UUID used as the Go type for field.UUID columns, so domain
// schemas can declare FK columns without each importing google/uuid.
func uuidType() uuid.UUID { return uuid.UUID{} }
