package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LibraryRole is a role definition. Permissions are dotted codes enforced by
// RequireServicePermission, e.g. "library.circulation.checkout".
// System roles (is_system, seeded) have no tenant_id and are shared by every tenant.
// Custom roles created through the API carry the creating tenant's tenant_id.
// A custom role with no tenant_id is a legacy row created before roles were tenant scoped.
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
		field.UUID("tenant_id", uuidType()).Optional().Nillable().
			Comment("Owning tenant of a custom role; NULL for system roles and legacy custom roles"),
	}
}

func (LibraryRole) Indexes() []ent.Index {
	return []ent.Index{
		// System and legacy roles: names unique among rows with no tenant.
		index.Fields("name").Unique().
			Annotations(entsql.IndexWhere("tenant_id IS NULL")).
			StorageKey("libraryrole_name_global"),
		// Tenant custom roles: names unique within the tenant, so two tenants may share a name.
		index.Fields("tenant_id", "name").Unique().
			Annotations(entsql.IndexWhere("tenant_id IS NOT NULL")).
			StorageKey("libraryrole_tenant_id_name"),
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
		field.JSON("branch_ids", []string{}).Optional().
			Comment("Library Branch IDs this user may log in to (empty = unrestricted for admins)"),
		field.Bool("is_active").Default(true),
		field.String("pin_hash").Optional().Nillable().Sensitive().
			Comment("bcrypt hash of the desk/kiosk PIN (supplements SSO); never returned"),
		field.String("pin_fast_hash").Optional().Comment("hex(SHA256(tenant:user:pin)) for O(1) PIN lookup"),
		field.Int("pin_failed_attempts").Default(0).Comment("consecutive wrong-PIN attempts (lockout)"),
		field.Time("pin_locked_until").Optional().Nillable().Comment("PIN locked until this time after too many failures"),
	}
}

func (LibraryUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "user_id").Unique(),
		index.Fields("tenant_id", "email"),
	}
}
