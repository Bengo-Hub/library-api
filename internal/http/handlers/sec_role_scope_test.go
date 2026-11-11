package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/libraryrole"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	libmw "github.com/bengobox/library-service/internal/http/middleware"
	"github.com/bengobox/library-service/internal/modules/rbac"
	"github.com/bengobox/library-service/internal/platform/database"
)

// roleScopeDB opens the test Postgres (or skips) and seeds the system roles.
func roleScopeDB(t *testing.T) (*ent.Client, *rbac.Service) {
	t.Helper()
	db, _ := memberTestDB(t)
	svc := rbac.NewService(db, zap.NewNop(), "")
	if err := svc.SeedGlobalRoles(context.Background()); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	return db, svc
}

// roleUser creates a library user in the tenant with the given roles and returns its user id.
func roleUser(t *testing.T, db *ent.Client, tid uuid.UUID, roles ...string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.LibraryUser.Create().SetTenantID(tid).SetUserID(id).SetRoles(roles).Save(context.Background()); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func tenantClaims(tid uuid.UUID, userID string) *authclient.Claims {
	return &authclient.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: userID},
		TenantID:         tid.String(),
	}
}

func ownerClaims(tid uuid.UUID) *authclient.Claims {
	c := tenantClaims(tid, uuid.NewString())
	c.IsPlatformOwner = true
	return c
}

// callRole runs one handler with the caller's claims, URL params and JSON body.
func callRole(t *testing.T, fn http.HandlerFunc, method string, claims *authclient.Claims, params map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/", &buf)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = authclient.ContextWithClaims(ctx, claims)
	rec := httptest.NewRecorder()
	fn(rec, req.WithContext(ctx))
	return rec
}

func systemRole(t *testing.T, db *ent.Client, name string) *ent.LibraryRole {
	t.Helper()
	r, err := db.LibraryRole.Query().Where(libraryrole.Name(name), libraryrole.TenantIDIsNil()).Only(context.Background())
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return r
}

func userRoles(t *testing.T, db *ent.Client, tid uuid.UUID, userID string) []string {
	t.Helper()
	u, err := db.LibraryUser.Query().Where(libraryuser.TenantID(tid), libraryuser.UserID(userID)).Only(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return u.Roles
}

func uniqueRoleName(prefix string) string { return prefix + "_" + uuid.NewString()[:8] }

// TestSystemRolesReadOnly: the seeded roles cannot be created, edited or deleted through the
// API, not even by a library admin or a platform owner. Before the fix any tenant's staff could
// set library_member to "*".
func TestSystemRolesReadOnly(t *testing.T) {
	db, svc := roleScopeDB(t)
	h := NewRBACHandler(svc, zap.NewNop())
	tid, _, _ := newTenant(t, db)
	admin := tenantClaims(tid, roleUser(t, db, tid, rbac.RoleAdmin))

	for _, name := range []string{rbac.RoleAdmin, rbac.RoleStaff, rbac.RoleMember} {
		role := systemRole(t, db, name)
		before := append([]string(nil), role.Permissions...)
		id := map[string]string{"id": role.ID.String()}
		for _, caller := range []*authclient.Claims{admin, ownerClaims(tid)} {
			if rec := callRole(t, h.UpdateRole, http.MethodPut, caller, id, map[string]any{"permissions": []string{"*"}}); rec.Code != http.StatusForbidden {
				t.Fatalf("update %s: status = %d, body = %s; want 403", name, rec.Code, rec.Body.String())
			}
			if rec := callRole(t, h.DeleteRole, http.MethodDelete, caller, id, nil); rec.Code != http.StatusForbidden {
				t.Fatalf("delete %s: status = %d; want 403", name, rec.Code)
			}
			if rec := callRole(t, h.CreateRole, http.MethodPost, caller, nil, map[string]any{"name": name, "permissions": []string{"*"}}); rec.Code != http.StatusForbidden {
				t.Fatalf("create %s: status = %d; want 403", name, rec.Code)
			}
		}
		after := systemRole(t, db, name)
		if len(after.Permissions) != len(before) {
			t.Fatalf("%s permissions changed: %v -> %v", name, before, after.Permissions)
		}
	}
	if got := systemRole(t, db, rbac.RoleMember).Permissions; len(got) == 0 || got[0] == "*" {
		t.Fatalf("library_member permissions = %v", got)
	}
}

// TestCustomRoleInvisibleToOtherTenant: a custom role belongs to the tenant that created it.
// Another tenant cannot read, edit, delete or assign it, and a same-named role in another tenant
// never lends its permissions across.
func TestCustomRoleInvisibleToOtherTenant(t *testing.T) {
	db, svc := roleScopeDB(t)
	ctx := context.Background()
	h := NewRBACHandler(svc, zap.NewNop())
	tenantA, _, _ := newTenant(t, db)
	tenantB, _, _ := newTenant(t, db)
	adminA := tenantClaims(tenantA, roleUser(t, db, tenantA, rbac.RoleAdmin))
	adminB := tenantClaims(tenantB, roleUser(t, db, tenantB, rbac.RoleAdmin))
	name := uniqueRoleName("desk")
	// The same name ends up in two tenants. Remove both afterwards so the shared test database can
	// still take the old global name index in TestRoleNameUniquePerTenantAfterLegacyIndex.
	t.Cleanup(func() { _, _ = db.LibraryRole.Delete().Where(libraryrole.Name(name)).Exec(context.Background()) })

	rec := callRole(t, h.CreateRole, http.MethodPost, adminA, nil, map[string]any{"name": name, "permissions": []string{"library.reports.view"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create in A: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created ent.LibraryRole
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	roleA, err := db.LibraryRole.Get(ctx, created.ID)
	if err != nil || roleA.TenantID == nil || *roleA.TenantID != tenantA {
		t.Fatalf("role A tenant_id = %v, err = %v; want %s", roleA, err, tenantA)
	}

	// Tenant B does not see it.
	rows, err := svc.ListRoles(ctx, tenantB)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == roleA.ID {
			t.Fatal("tenant B listed tenant A's custom role")
		}
	}
	id := map[string]string{"id": roleA.ID.String()}
	if rec := callRole(t, h.GetRole, http.MethodGet, adminB, id, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get from B: status = %d; want 404", rec.Code)
	}
	if rec := callRole(t, h.UpdateRole, http.MethodPut, adminB, id, map[string]any{"permissions": []string{"library.catalog.view"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("update from B: status = %d; want 404", rec.Code)
	}
	if rec := callRole(t, h.DeleteRole, http.MethodDelete, adminB, id, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete from B: status = %d; want 404", rec.Code)
	}
	userB := roleUser(t, db, tenantB, rbac.RoleMember)
	if rec := callRole(t, h.AssignRoles, http.MethodPut, adminB, map[string]string{"user_id": userB}, map[string]any{"roles": []string{name}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("assign A's role in B: status = %d; want 400", rec.Code)
	}

	// Tenant B may use the same name for its own role, and the two never mix.
	rec = callRole(t, h.CreateRole, http.MethodPost, adminB, nil, map[string]any{"name": name, "permissions": []string{"library.catalog.view"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("same name in B: status = %d, body = %s; want 201", rec.Code, rec.Body.String())
	}
	if rec := callRole(t, h.AssignRoles, http.MethodPut, adminB, map[string]string{"user_id": userB}, map[string]any{"roles": []string{name}}); rec.Code != http.StatusOK {
		t.Fatalf("assign B's own role: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if svc.HasAnyPermission(ctx, tenantB, userB, "library.reports.view") {
		t.Fatal("tenant B user picked up a permission from tenant A's same-named role")
	}
	if !svc.HasAnyPermission(ctx, tenantB, userB, "library.catalog.view") {
		t.Fatal("tenant B user lost its own role's permission")
	}

	// A custom role with no tenant (created before this fix) stays readable, but only a
	// platform owner may edit or assign it.
	t.Run("legacy role", func(t *testing.T) {
		legacyName := uniqueRoleName("legacy")
		legacy, err := db.LibraryRole.Create().SetName(legacyName).SetPermissions([]string{"library.catalog.view"}).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		visible, err := svc.ListRoles(ctx, tenantB)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range visible {
			found = found || r.ID == legacy.ID
		}
		if !found {
			t.Fatal("legacy role should stay readable")
		}
		lid := map[string]string{"id": legacy.ID.String()}
		if rec := callRole(t, h.UpdateRole, http.MethodPut, adminB, lid, map[string]any{"permissions": []string{"library.catalog.view"}}); rec.Code != http.StatusForbidden {
			t.Fatalf("admin edits legacy: status = %d; want 403", rec.Code)
		}
		if rec := callRole(t, h.AssignRoles, http.MethodPut, adminB, map[string]string{"user_id": userB}, map[string]any{"roles": []string{legacyName}}); rec.Code != http.StatusForbidden {
			t.Fatalf("admin assigns legacy: status = %d; want 403", rec.Code)
		}
		if rec := callRole(t, h.AssignRoles, http.MethodPut, ownerClaims(tenantB), map[string]string{"user_id": userB}, map[string]any{"roles": []string{legacyName}}); rec.Code != http.StatusOK {
			t.Fatalf("platform owner assigns legacy: status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if rec := callRole(t, h.DeleteRole, http.MethodDelete, ownerClaims(tenantB), lid, nil); rec.Code != http.StatusOK {
			t.Fatalf("platform owner deletes legacy: status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})
}

// TestStaffCannotManageRoles: library_staff holds library.team.manage (for its own PIN) but the
// role routes also need admin level, so staff get 403 on every role change and assignment.
func TestStaffCannotManageRoles(t *testing.T) {
	db, svc := roleScopeDB(t)
	ctx := context.Background()
	h := NewRBACHandler(svc, zap.NewNop())
	tid, _, _ := newTenant(t, db)
	staffID := roleUser(t, db, tid, rbac.RoleStaff)
	staff := tenantClaims(tid, staffID)
	adminID := roleUser(t, db, tid, rbac.RoleAdmin)
	custom, err := svc.CreateRole(ctx, rbac.Actor{TenantID: tid, UserID: adminID}, uniqueRoleName("shelver"), "", []string{"library.copies.view"})
	if err != nil {
		t.Fatalf("admin creates role: %v", err)
	}

	// Same gates as the router mounts on the role routes.
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authclient.ContextWithClaims(req.Context(), staff)))
		})
	})
	view := libmw.RequireServicePermission(svc, "library.team.view", "library.team.manage")
	manage := libmw.RequireServicePermission(svc, "library.team.manage")
	roleAdmin := libmw.RequireLibraryAdmin(svc)
	r.With(view).Get("/rbac/roles", h.ListRoles)
	r.With(manage, roleAdmin).Post("/rbac/roles", h.CreateRole)
	r.With(manage, roleAdmin).Put("/rbac/roles/{id}", h.UpdateRole)
	r.With(manage, roleAdmin).Delete("/rbac/roles/{id}", h.DeleteRole)
	r.With(manage, roleAdmin).Put("/team/{user_id}/roles", h.AssignRoles)

	do := func(method, path string, body any) int {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		return rec.Code
	}

	if code := do(http.MethodGet, "/rbac/roles", nil); code != http.StatusOK {
		t.Fatalf("staff list roles: %d; want 200", code)
	}
	if code := do(http.MethodPost, "/rbac/roles", map[string]any{"name": uniqueRoleName("x"), "permissions": []string{"library.copies.view"}}); code != http.StatusForbidden {
		t.Fatalf("staff create: %d; want 403", code)
	}
	if code := do(http.MethodPut, "/rbac/roles/"+custom.ID.String(), map[string]any{"permissions": []string{"library.copies.view"}}); code != http.StatusForbidden {
		t.Fatalf("staff update: %d; want 403", code)
	}
	if code := do(http.MethodDelete, "/rbac/roles/"+custom.ID.String(), nil); code != http.StatusForbidden {
		t.Fatalf("staff delete: %d; want 403", code)
	}
	if code := do(http.MethodPut, "/team/"+staffID+"/roles", map[string]any{"roles": []string{rbac.RoleAdmin}}); code != http.StatusForbidden {
		t.Fatalf("staff assigns itself admin: %d; want 403", code)
	}
	if code := do(http.MethodPut, "/rbac/roles/"+systemRole(t, db, rbac.RoleMember).ID.String(), map[string]any{"permissions": []string{"*"}}); code != http.StatusForbidden {
		t.Fatalf("staff edits library_member: %d; want 403", code)
	}

	// The service refuses staff on its own too, in case a route is ever mounted without the gate.
	if _, err := svc.CreateRole(ctx, rbac.Actor{TenantID: tid, UserID: staffID}, uniqueRoleName("y"), "", []string{"library.copies.view"}); !errors.Is(err, rbac.ErrAdminRequired) {
		t.Fatalf("service create by staff: err = %v; want ErrAdminRequired", err)
	}
	if err := svc.DeleteRole(ctx, rbac.Actor{TenantID: tid, UserID: staffID}, custom.ID); !errors.Is(err, rbac.ErrAdminRequired) {
		t.Fatalf("service delete by staff: err = %v; want ErrAdminRequired", err)
	}
	if got := userRoles(t, db, tid, staffID); len(got) != 1 || got[0] != rbac.RoleStaff {
		t.Fatalf("staff roles = %v; want only %s", got, rbac.RoleStaff)
	}
}

// TestCannotGrantPermissionYouLack: no caller may put a permission on a role that the caller does
// not hold, and "*" needs a caller holding "*".
func TestCannotGrantPermissionYouLack(t *testing.T) {
	db, svc := roleScopeDB(t)
	ctx := context.Background()
	tid, _, _ := newTenant(t, db)
	staff := rbac.Actor{TenantID: tid, UserID: roleUser(t, db, tid, rbac.RoleStaff)}
	admin := rbac.Actor{TenantID: tid, UserID: roleUser(t, db, tid, rbac.RoleAdmin)}

	for _, perms := range [][]string{{"*"}, {"library.settings.manage"}, {"library.copies.view", "library.copies.hard_delete"}} {
		_, err := svc.CreateRole(ctx, staff, uniqueRoleName("lack"), "", perms)
		var notHeld rbac.ErrPermissionNotHeld
		if !errors.As(err, &notHeld) {
			t.Fatalf("staff grants %v: err = %v; want ErrPermissionNotHeld", perms, err)
		}
	}

	// A token-only permission counts as held, nothing more.
	tokenOnly := rbac.Actor{TenantID: tid, UserID: uuid.NewString(), ClaimPermissions: []string{"library.reports.view"}}
	var notHeld rbac.ErrPermissionNotHeld
	if _, err := svc.CreateRole(ctx, tokenOnly, uniqueRoleName("tok"), "", []string{"library.reports.view", "*"}); !errors.As(err, &notHeld) || notHeld.Permission != "*" {
		t.Fatalf("token caller grants *: err = %v; want ErrPermissionNotHeld for *", err)
	}

	// Editing an existing custom role is held to the same rule.
	role, err := svc.CreateRole(ctx, admin, uniqueRoleName("desk"), "", []string{"library.copies.view"})
	if err != nil {
		t.Fatalf("admin creates role: %v", err)
	}
	if _, err := svc.UpdateRolePermissions(ctx, staff, role.ID, []string{"*"}, ""); !errors.As(err, &notHeld) {
		t.Fatalf("staff raises role to *: err = %v; want ErrPermissionNotHeld", err)
	}

	// A library admin holds "*" and may grant it; so may a platform owner.
	if _, err := svc.CreateRole(ctx, admin, uniqueRoleName("full"), "", []string{"*"}); err != nil {
		t.Fatalf("admin grants *: %v", err)
	}
	if _, err := svc.CreateRole(ctx, rbac.Actor{TenantID: tid, PlatformOwner: true}, uniqueRoleName("po"), "", []string{"*"}); err != nil {
		t.Fatalf("platform owner grants *: %v", err)
	}

	// Through the handler the refusal is a 403.
	h := NewRBACHandler(svc, zap.NewNop())
	rec := callRole(t, h.CreateRole, http.MethodPost, tenantClaims(tid, staff.UserID), nil, map[string]any{"name": uniqueRoleName("h"), "permissions": []string{"*"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("handler: status = %d, body = %s; want 403", rec.Code, rec.Body.String())
	}
}

// TestOnlyAdminAssignsLibraryAdmin: a staff caller cannot give anyone (itself included)
// library_admin, while a library admin and a platform owner can.
func TestOnlyAdminAssignsLibraryAdmin(t *testing.T) {
	db, svc := roleScopeDB(t)
	h := NewRBACHandler(svc, zap.NewNop())
	tid, _, _ := newTenant(t, db)
	staffID := roleUser(t, db, tid, rbac.RoleStaff)
	memberID := roleUser(t, db, tid, rbac.RoleMember)
	adminID := roleUser(t, db, tid, rbac.RoleAdmin)
	staff := tenantClaims(tid, staffID)

	for _, target := range []string{staffID, memberID} {
		rec := callRole(t, h.AssignRoles, http.MethodPut, staff, map[string]string{"user_id": target}, map[string]any{"roles": []string{rbac.RoleAdmin}})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("staff assigns admin to %s: status = %d, body = %s; want 403", target, rec.Code, rec.Body.String())
		}
	}
	if got := userRoles(t, db, tid, staffID); len(got) != 1 || got[0] != rbac.RoleStaff {
		t.Fatalf("staff roles = %v; want unchanged", got)
	}
	if got := userRoles(t, db, tid, memberID); len(got) != 1 || got[0] != rbac.RoleMember {
		t.Fatalf("member roles = %v; want unchanged", got)
	}

	rec := callRole(t, h.AssignRoles, http.MethodPut, tenantClaims(tid, adminID), map[string]string{"user_id": memberID}, map[string]any{"roles": []string{rbac.RoleAdmin}})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin assigns admin: status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	rec = callRole(t, h.AssignRoles, http.MethodPut, ownerClaims(tid), map[string]string{"user_id": staffID}, map[string]any{"roles": []string{rbac.RoleAdmin}})
	if rec.Code != http.StatusOK {
		t.Fatalf("platform owner assigns admin: status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
}

// TestRoleNameUniquePerTenantAfterLegacyIndex: a production database still carries the old
// global UNIQUE(name) index, because Schema.Create never drops indexes. The pre-migrate step must
// remove it, after which two tenants can each create a role with the same name.
func TestRoleNameUniquePerTenantAfterLegacyIndex(t *testing.T) {
	db, sqlDB := memberTestDB(t)
	ctx := context.Background()
	svc := rbac.NewService(db, zap.NewNop(), "")
	if err := svc.SeedGlobalRoles(ctx); err != nil {
		t.Fatalf("seed roles: %v", err)
	}
	// Put the database back the way the migrator leaves it, whatever happens below.
	t.Cleanup(func() { _ = database.PreMigrate(context.Background(), sqlDB) })

	// Earlier runs of these tests may have left the same generated name in two tenants, which
	// would block the old index. Clear only rows matching the names these tests generate.
	if _, err := sqlDB.ExecContext(ctx, `DELETE FROM "library_roles" WHERE tenant_id IS NOT NULL AND name ~ '^[a-z]+_[0-9a-f]{8}$'`); err != nil {
		t.Fatalf("clear generated test roles: %v", err)
	}
	// Recreate the pre S2-9 index, as a not yet migrated production database has it.
	if _, err := sqlDB.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS "libraryrole_name" ON "library_roles" ("name")`); err != nil {
		t.Skipf("test database already holds the same role name in two tenants, cannot recreate the old index: %v", err)
	}
	var present bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'libraryrole_name')`).Scan(&present); err != nil || !present {
		t.Fatalf("old index not in place: present = %v, err = %v", present, err)
	}

	if err := database.PreMigrate(ctx, sqlDB); err != nil {
		t.Fatalf("pre-migrate: %v", err)
	}
	if err := database.PreMigrate(ctx, sqlDB); err != nil {
		t.Fatalf("pre-migrate is not idempotent: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'libraryrole_name')`).Scan(&present); err != nil || present {
		t.Fatalf("old index still present after pre-migrate: present = %v, err = %v", present, err)
	}

	h := NewRBACHandler(svc, zap.NewNop())
	tenantA, _, _ := newTenant(t, db)
	tenantB, _, _ := newTenant(t, db)
	name := uniqueRoleName("shared")
	t.Cleanup(func() { _, _ = db.LibraryRole.Delete().Where(libraryrole.Name(name)).Exec(context.Background()) })
	for _, tid := range []uuid.UUID{tenantA, tenantB} {
		caller := tenantClaims(tid, roleUser(t, db, tid, rbac.RoleAdmin))
		rec := callRole(t, h.CreateRole, http.MethodPost, caller, nil, map[string]any{"name": name, "permissions": []string{"library.catalog.view"}})
		if rec.Code != http.StatusCreated {
			t.Fatalf("tenant %s creates %q: status = %d, body = %s; want 201", tid, name, rec.Code, rec.Body.String())
		}
	}
}

// TestDuplicateRoleNameIs409: a second role with the same name in one tenant is a clean 409 that
// never carries database text, and any unexpected failure is a generic 500.
func TestDuplicateRoleNameIs409(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		db, svc := roleScopeDB(t)
		h := NewRBACHandler(svc, zap.NewNop())
		tid, _, _ := newTenant(t, db)
		admin := tenantClaims(tid, roleUser(t, db, tid, rbac.RoleAdmin))
		name := uniqueRoleName("dup")
		body := map[string]any{"name": name, "permissions": []string{"library.catalog.view"}}
		if rec := callRole(t, h.CreateRole, http.MethodPost, admin, nil, body); rec.Code != http.StatusCreated {
			t.Fatalf("first create: status = %d, body = %s", rec.Code, rec.Body.String())
		}
		rec := callRole(t, h.CreateRole, http.MethodPost, admin, nil, body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("second create: status = %d, body = %s; want 409", rec.Code, rec.Body.String())
		}
		var got map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got["code"] != "role_name_taken" || got["error"] != "A role with this name already exists in your organisation." {
			t.Fatalf("409 body = %v", got)
		}

		// A unique violation from the database itself (a race past the name check) maps the same way.
		if _, err := db.LibraryRole.Create().SetName(name).SetTenantID(tid).Save(context.Background()); err == nil || !ent.IsConstraintError(err) {
			t.Fatalf("direct duplicate insert: err = %v; want a constraint error", err)
		}
		rec = httptest.NewRecorder()
		_, err := db.LibraryRole.Create().SetName(name).SetTenantID(tid).Save(context.Background())
		h.respondRoleError(rec, err, "create_failed")
		if rec.Code != http.StatusConflict || strings.Contains(strings.ToLower(rec.Body.String()), "duplicate") || strings.Contains(rec.Body.String(), "libraryrole") {
			t.Fatalf("constraint error: status = %d, body = %s; want 409 without database text", rec.Code, rec.Body.String())
		}
	})

	t.Run("unexpected error is generic", func(t *testing.T) {
		db := closedEntClient(t)
		h := NewRBACHandler(rbac.NewService(db, zap.NewNop(), ""), zap.NewNop())
		tid := uuid.New()
		rec := callRole(t, h.GetRole, http.MethodGet, tenantClaims(tid, uuid.NewString()), map[string]string{"id": uuid.NewString()}, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d; want 500", rec.Code)
		}
		var got map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got["error"] != "internal error" {
			t.Fatalf("500 body = %v; want a generic message", got)
		}
	})
}
