# Sprint 03 — Auth, RBAC & Subscription Gate

**Status:** ✅ Shipped
**Goal:** Wire SSO/JWT validation, the union-RBAC model (global roles + JIT heal + `/auth/me`), per-route permission enforcement, the mutations-only subscription gate, and the document-sequence allocator that later sprints depend on.

---

## Scope

The security + identity backbone. Every `/api/v1/{tenant}/library` route flows through: RequireAuth → JIT heal → mutations-only subscription gate → (where mounted) RequireServicePermission.

---

## Task Checklist

### JWT / SSO
- [x] `shared-auth-client` validator from JWKS (`AUTH_JWKS_URL`), cache 1h / refresh 5m, issuer/audience config.
- [x] `RequireAuth` mounted on the `/api/v1/{tenant}/library` route group.
- [x] S2S API-key auth (`AUTH_ENABLE_API_KEY_AUTH`, shared `INTERNAL_SERVICE_KEY` via `X-API-Key`) — `NewAuthMiddlewareWithAPIKey`.

### RBAC service (`internal/modules/rbac/service.go`)
- [x] Global roles: `library_admin` (`*`), `library_staff`, `library_member`.
- [x] `SeedGlobalRoles` — idempotent upsert + drift-heal of system roles' permission sets (called from `app.go` on startup).
- [x] `MapGlobalRoles` — SSO globals → library roles (superuser/admin/owner→admin; staff/cashier/manager→staff; else member).
- [x] `EnsureUserFromToken` — JIT upsert of `LibraryUser`, **heals existing users on every request** (merges mapped roles into explicitly-granted ones).
- [x] `HasAnyPermission` + `ListPermissions` — resolve role permission sets (wildcard `*` grants all).
- [x] `ListRoles`, `ListUsers`, `AssignRoles`, `PermissionCatalog` (the `library.{module}.{action}` static list).

### Middleware
- [x] JIT-heal middleware in `router.go` calling `EnsureUserFromToken` per request.
- [x] `RequireServicePermission` (`middleware/permission.go`) — union order: superuser/platform-owner bypass → JWT-carried permission → local RBAC fallback → 403 `permission_denied`.
- [x] `RequireActiveSubscriptionForMutations` (`middleware/subscription.go`) — GET/HEAD/OPTIONS pass; mutations require active sub; superuser/platform-owner/`IsGatingExempt`/active bypass; 403 `{code:subscription_inactive,upgrade:true}`.

### `/auth/me` (`handlers/authme.go`)
- [x] `GET /auth/me` returns service identity + **JWT permissions ∪ local RBAC permissions** + roles + is_platform_owner/is_superuser.

### RBAC / team endpoints (`handlers/rbac.go`)
- [x] `GET /rbac/roles`, `GET /rbac/permissions`, `GET /team`, `PUT /team/{user_id}/roles`.

### Sequence allocator (`internal/modules/sequence/sequence.go`)
- [x] `Next(tx, tenant, kind, prefix, padWidth)` — `SELECT … FOR UPDATE` row-locked monotonic allocation; kinds `membership_no`, `accession_no`, `loan_no`; creates the counter row on first use.

---

## Acceptance Criteria

- [x] Unauthenticated requests are rejected by `RequireAuth`.
- [x] A user provisioned before a role mapping existed self-heals within the same request (no lockout).
- [x] `/auth/me` returns the union permission set the UI gates on.
- [x] Mutations are blocked for inactive subscriptions; GETs always pass; demo/PAYG/superuser bypass.
- [x] Concurrent sequence allocations never collide (row lock).

---

## Dependencies

- Sprint 01 (auth config + middleware host), Sprint 02 (`LibraryRole`/`LibraryUser`/`DocumentSequence` schemas).
