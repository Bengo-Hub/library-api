# RBAC and Seed (library-api)

**Last updated:** 2026-06-26

library-api owns its own roles + permissions (the service is the source of truth for its `library.{module}.{action}` codes — `reference_service_rbac_authme_sync`). Effective permissions = **SSO JWT ∪ this local RBAC**. The module lives at `internal/modules/rbac/` and is backed by Ent.

---

## Schemas

| Schema | Table | Description |
|--------|-------|-------------|
| `LibraryRole` | `library_roles` | Role definitions. System roles (`is_system`, seeded) have no `tenant_id` and are shared by every tenant. Custom roles carry the creating tenant's `tenant_id`. `permissions` is a JSON array of dotted codes. |
| `LibraryUser` | `library_users` | Per-tenant projection of an auth user (JIT-provisioned + healed). `roles` is a JSON array of `LibraryRole` names. |

> System roles are global reference data. Custom roles are tenant scoped: a tenant sees and assigns only system roles, legacy custom roles (no `tenant_id`) and its own custom roles. Role names are unique among rows with no tenant and unique per tenant otherwise.

---

## Global Roles

Seeded once by `rbac.SeedGlobalRoles` (called from `app.go` on startup; idempotent, heals permission drift on existing system roles):

| Role | Permissions | Description |
|------|-------------|-------------|
| `library_admin` | `*` (wildcard — all) | Full library administration |
| `library_staff` | `library.catalog.view`, `library.catalog.manage`, `library.circulation.checkout`, `library.circulation.return`, `library.circulation.renew`, `library.holds.manage`, `library.members.view`, `library.members.manage`, `library.fines.view`, `library.fines.assess`, `library.ebooks.view` | Circulation desk + cataloging |
| `library_member` | `library.catalog.view`, `library.ebooks.view`, `library.holds.place` | Patron self-service (read + own loans/holds) |

---

## Permission Catalog

Permission codes follow the `library.{module}.{action}` pattern (`PermissionCatalog()` is the static list the team matrix renders; keep it in sync with the role sets and route guards):

| Code | Label |
|------|-------|
| `library.catalog.view` | View catalog |
| `library.catalog.manage` | Manage catalog |
| `library.circulation.checkout` | Check out |
| `library.circulation.return` | Check in |
| `library.circulation.renew` | Renew |
| `library.holds.manage` | Manage holds |
| `library.holds.place` | Place holds |
| `library.members.view` | View members |
| `library.members.manage` | Manage members |
| `library.fines.view` | View fines |
| `library.fines.assess` | Assess/waive fines |
| `library.ebooks.view` | Access e-books |

**Modules:** `catalog`, `circulation`, `holds`, `members`, `fines`, `ebooks`.

---

## JIT Provisioning — heal existing users

`EnsureUserFromToken` runs on **every** authenticated request (mounted as middleware in `router.go`, not only on first login):

1. Parse `tenant_id` + `subject` from JWT claims.
2. Map the SSO global roles → library roles (`MapGlobalRoles`).
3. If the `LibraryUser` does not exist → create it with the mapped roles.
4. If it exists → **merge** the mapped roles into any explicitly-granted roles and update email.

This means a user created before a role mapping existed self-heals within the very request that previously would have lacked the role (no lockout window, no "first-login only" gap — the treasury #30 gotcha).

---

## Union RBAC — `RequireServicePermission`

`internal/http/middleware/permission.go` enforces the union model in order:

1. **Superuser / platform-owner bypass** — `claims.IsSuperuser() || claims.IsPlatformOwner` → allow.
2. **JWT-carried permission** — `claims.HasAnyPermission(perms…)` → allow.
3. **Local RBAC fallback** — `rbac.Service.HasAnyPermission(ctx, tenantID, subject, perms…)` (a wildcard `*` role grants all) → allow.
4. Else **403** `{"error":"You do not have permission…","code":"permission_denied"}`.

---

## `/auth/me` union

`GET /api/v1/{tenant}/library/auth/me` (`authme.go`) returns:

```json
{
  "service": "library-api",
  "user_id": "...", "tenant_id": "...", "tenant_slug": "...", "email": "...",
  "roles": ["library_staff"],
  "permissions": ["<JWT perms> ∪ <local RBAC perms>"],
  "is_platform_owner": false,
  "is_superuser": false
}
```

The permission array is the **union** of `claims.Permissions` and the user's locally-granted permission codes (`rbac.ListPermissions`). The UI bootstraps its sidebar/route gating from this.

---

## RBAC / Team API

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/{tenant}/library/rbac/roles` | List the roles the tenant can see: system roles, legacy custom roles and its own custom roles |
| `GET` | `/{tenant}/library/rbac/roles/{id}` | One visible role (another tenant's custom role is 404) |
| `POST` | `/{tenant}/library/rbac/roles` | Create a custom role in the tenant (team.manage and admin level) |
| `PUT` | `/{tenant}/library/rbac/roles/{id}` | Edit a custom role's permissions (team.manage and admin level) |
| `DELETE` | `/{tenant}/library/rbac/roles/{id}` | Delete a custom role (team.manage and admin level) |
| `GET` | `/{tenant}/library/rbac/permissions` | List the permission catalog |
| `GET` | `/{tenant}/library/team` | List the tenant's provisioned library users |
| `PUT` | `/{tenant}/library/team/{user_id}/roles` | Assign roles to a team member, replacing the set (team.manage and admin level) |

Admin level means a real platform owner or a user holding `library_admin` in the tenant. `library_staff` holds `library.team.manage` only so it can manage its own PIN; it cannot touch roles.
System roles are read only through the API (403); only the seed changes them. A caller can never put a permission on a role that it does not hold itself, so `*` needs a caller holding `*`.
A legacy custom role (no `tenant_id`, created before roles were tenant scoped) stays readable by every tenant, but only a platform owner may edit, delete or newly assign it.

---

## Seed

`go run ./cmd/seed` is idempotent:

1. **Global roles** are always ensured (also done by the API on startup).
2. When **`SEED_TENANT_ID`** is set, demo data is seeded for that tenant:
   - Default **branch** `MAIN` ("Main Library", `is_default`).
   - Default **member tier** `Standard` (3 concurrent loans, 14-day period, 2 renewals, hold limit 5, KES 10/day fine, KES 1000 block, KES 500 annual fee).
   - Two sample **bib records** + **copies** (`The Go Programming Language`, `Things Fall Apart`) for E2E.

Without `SEED_TENANT_ID`, only the global roles are ensured (the rest is skipped).

---

## Module Structure

```
internal/modules/rbac/
  service.go   -- SeedGlobalRoles, MapGlobalRoles, EnsureUserFromToken,
                  HasAnyPermission, ListPermissions, ListRoles, ListUsers,
                  AssignRoles, PermissionCatalog
```

## References

- Union-RBAC / `/auth/me` pattern: treasury-api, pos-api, erp-api (`reference_service_rbac_authme_sync`).
- library-ui: sidebar + route gating read these permission codes via `GET /auth/me`.
