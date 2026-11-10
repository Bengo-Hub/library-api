package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/modules/rbac"
)

// TestAssignRolesRejectsSuperuser locks in the C2 fix: the team roles endpoint accepts only
// defined library roles, so a tenant admin can no longer store "superuser" (or any platform role)
// on a staff member, and a PIN token never carries one.
func TestAssignRolesRejectsSuperuser(t *testing.T) {
	assign := func(t *testing.T, db *ent.Client, tid uuid.UUID, userID string, roles []string) *httptest.ResponseRecorder {
		t.Helper()
		h := NewRBACHandler(rbac.NewService(db, zap.NewNop(), ""), zap.NewNop())
		buf, _ := json.Marshal(map[string]any{"roles": roles})
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(buf))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("user_id", userID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = authclient.ContextWithClaims(ctx, &authclient.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: uuid.NewString(), Issuer: terminalIssuer},
			TenantID:         tid.String(),
			Roles:            []string{rbac.RoleAdmin},
		})
		rec := httptest.NewRecorder()
		h.AssignRoles(rec, req.WithContext(ctx))
		return rec
	}

	// Reserved names are refused before any database read, so no database is needed here.
	t.Run("reserved roles refused", func(t *testing.T) {
		db := closedEntClient(t)
		for _, roles := range [][]string{
			{"superuser"},
			{" SuperUser "},
			{rbac.RoleStaff, "platform_owner"},
			{"owner"},
			{"super_admin"},
			{"admin"},
		} {
			rec := assign(t, db, uuid.New(), uuid.NewString(), roles)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("roles %v: status = %d, body = %s; want 400", roles, rec.Code, rec.Body.String())
			}
		}
		if _, err := rbac.NewService(db, zap.NewNop(), "").CreateRole(context.Background(), "superuser", "", []string{"*"}); err == nil {
			t.Fatal("a custom role may not take a reserved name")
		}
	})

	t.Run("PIN token drops reserved roles", func(t *testing.T) {
		db := closedEntClient(t)
		h := NewPINAuthHandler(db, rbac.NewService(db, zap.NewNop(), ""), nil, "secret", zap.NewNop())
		tn := &ent.Tenant{ID: uuid.New(), Slug: "some-library", Name: "Some Library"}
		u := &ent.LibraryUser{UserID: uuid.NewString(), Roles: []string{rbac.RoleStaff, "superuser", "platform_owner"}}
		tc := h.terminalClaimsFor(context.Background(), tn, u)
		token, err := issueTerminalJWT([]byte("secret"), tc)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := validateTerminalJWT(token, []byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		claims := terminalToAuthClaims(parsed)
		if claims.IsSuperuser() || claims.IsPlatformOwner {
			t.Fatalf("PIN token must not carry superuser or platform owner, roles = %v", claims.Roles)
		}
		if len(claims.Roles) != 1 || claims.Roles[0] != rbac.RoleStaff {
			t.Fatalf("PIN token roles = %v; want only %s", claims.Roles, rbac.RoleStaff)
		}
	})

	// Unknown roles and the stored result need the real role table.
	t.Run("only defined roles are stored", func(t *testing.T) {
		db, _ := memberTestDB(t)
		ctx := context.Background()
		svc := rbac.NewService(db, zap.NewNop(), "")
		if err := svc.SeedGlobalRoles(ctx); err != nil {
			t.Fatalf("seed roles: %v", err)
		}
		tid, _, _ := newTenant(t, db)
		userID := uuid.NewString()
		if _, err := db.LibraryUser.Create().SetTenantID(tid).SetUserID(userID).SetRoles([]string{rbac.RoleMember}).Save(ctx); err != nil {
			t.Fatalf("create user: %v", err)
		}

		if rec := assign(t, db, tid, userID, []string{"made_up_role"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("unknown role: status = %d; want 400", rec.Code)
		}
		if rec := assign(t, db, tid, userID, []string{rbac.RoleStaff, "superuser"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("staff plus superuser: status = %d; want 400", rec.Code)
		}
		if rec := assign(t, db, tid, userID, []string{rbac.RoleStaff}); rec.Code != http.StatusOK {
			t.Fatalf("library_staff: status = %d, body = %s; want 200", rec.Code, rec.Body.String())
		}
		got, err := db.LibraryUser.Query().Where(libraryuser.TenantID(tid), libraryuser.UserID(userID)).Only(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Roles) != 1 || got.Roles[0] != rbac.RoleStaff {
			t.Fatalf("stored roles = %v; want only %s", got.Roles, rbac.RoleStaff)
		}
	})
}
