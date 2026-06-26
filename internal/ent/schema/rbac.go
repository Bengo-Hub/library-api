package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LibraryRole is a GLOBAL role definition (per MEMORY: roles/permissions are shared
// reference data, never tenant-scoped). Permissions are dotted codes enforced by
// RequireServicePermission, e.g. "library.circulation.checkout".
type LibraryRole struct {
	ent.Schema
}

func (LibraryRole) Mixin() []ent.Mixin {
	return []ent.Mixin{BaseMixin{}}
}

func (LibraryRole) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("description").Optional(),
		field.JSON("permissions", []string{}).Optional().
			Comment("Dotted permission codes granted by this role"),
		field.Bool("is_system").Default(false),
	}
}

func (LibraryRole) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}

// LibraryUser is the per-tenant projection of an auth-api user, JIT-provisioned on
// first authenticated request. Roles are stored as names resolved against LibraryRole;
// the effective permission set is JWT ∪ this local RBAC (see reference_service_rbac_authme_sync).
type LibraryUser struct {
	ent.Schema
}

func (LibraryUser) Mixin() []ent.Mixin {
	return []ent.Mixin{BaseMixin{}, TenantMixin{}}
}

func (LibraryUser) Fields() []ent.Field {
	return []ent.Field{
		field.String("user_id").NotEmpty().Comment("auth-api user UUID/sub"),
		field.String("email").Optional(),
		field.String("display_name").Optional(),
		field.JSON("roles", []string{}).Optional().Comment("Assigned LibraryRole names"),
		field.Bool("is_active").Default(true),
		field.String("pin_hash").Optional().Nillable().Sensitive().
			Comment("bcrypt hash of the desk/kiosk PIN (supplements SSO); never returned"),
		field.String("pin_fast_hash").Optional().Comment("hex(SHA256(tenant:user:pin)) for O(1) PIN lookup"),
	}
}

func (LibraryUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "user_id").Unique(),
		index.Fields("tenant_id", "email"),
	}
}
