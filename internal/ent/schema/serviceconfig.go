package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ServiceConfig holds service-level configuration key-value pairs. When tenant_id is
// nil it is a platform-level default; a non-nil tenant_id is a tenant override.
type ServiceConfig struct {
	ent.Schema
}

func (ServiceConfig) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).Optional().Nillable().
			Comment("Nil = platform-level default; set = tenant-specific override"),
		field.String("config_key").NotEmpty(),
		field.Text("config_value").NotEmpty().Comment("Configuration value as JSON string"),
		field.String("config_type").Default("string").Comment("string, int, bool, json, float"),
		field.String("description").Optional(),
		field.Bool("is_secret").Default(false).Comment("If true, value is masked in API responses"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ServiceConfig) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "config_key").Unique(),
		index.Fields("config_key"),
	}
}
