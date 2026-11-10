package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/modules/rbac"
	"github.com/bengobox/library-service/internal/platform/subscriptions"
)

// closedEntClient returns an ent client whose queries all fail at once, so the RBAC lookup in
// terminalClaimsFor yields no permissions without needing a database.
func closedEntClient(t *testing.T) *ent.Client {
	t.Helper()
	sqlDB, err := sql.Open("pgx", "postgres://127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatalf("open sql: %v", err)
	}
	_ = sqlDB.Close()
	return ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
}

// TestTerminalTokenNeverPlatformOwner locks in that a PIN session in the platform tenant
// ("codevertex") never carries platform owner powers. It is gating-exempt only through the
// sub_exempt flag that subscriptions-api returns for that tenant.
func TestTerminalTokenNeverPlatformOwner(t *testing.T) {
	db := closedEntClient(t)
	tn := &ent.Tenant{ID: uuid.New(), Slug: "codevertex", Name: "Codevertex"}
	u := &ent.LibraryUser{UserID: uuid.NewString(), Email: "desk@example.test", DisplayName: "Desk", Roles: []string{rbac.RoleAdmin}}
	rbacSvc := rbac.NewService(db, zap.NewNop(), "")

	check := func(t *testing.T, tc terminalClaims) {
		t.Helper()
		if tc.IsPlatformOwner {
			t.Fatal("terminal claims must never set IsPlatformOwner")
		}
		if tc.IsDemo {
			t.Fatal("the platform tenant is not the demo tenant")
		}
		token, err := issueTerminalJWT([]byte("secret"), tc)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		parsed, err := validateTerminalJWT(token, []byte("secret"))
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if terminalToAuthClaims(parsed).IsPlatformOwner {
			t.Fatal("signed terminal token must never carry is_platform_owner")
		}
	}

	t.Run("subscriptions unreachable", func(t *testing.T) {
		h := NewPINAuthHandler(db, rbacSvc, nil, "secret", zap.NewNop())
		tc := h.terminalClaimsFor(context.Background(), tn, u)
		check(t, tc)
		if tc.SubscriptionStatus != "active" {
			t.Fatalf("SubscriptionStatus = %q, want active", tc.SubscriptionStatus)
		}
	})

	t.Run("subscriptions returns exempt", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ACTIVE", "billing_mode": "exempt", "exempt": true,
				"features": []string{"circulation"}, "active_service_tags": []string{"library"},
			})
		}))
		defer srv.Close()
		subs := subscriptions.NewClient(subscriptions.Config{ServiceURL: srv.URL})
		h := NewPINAuthHandler(db, rbacSvc, subs, "secret", zap.NewNop())
		tc := h.terminalClaimsFor(context.Background(), tn, u)
		check(t, tc)
		if !tc.SubscriptionExempt {
			t.Fatal("exempt tenant must carry sub_exempt")
		}
		token, _ := issueTerminalJWT([]byte("secret"), tc)
		parsed, _ := validateTerminalJWT(token, []byte("secret"))
		if !terminalToAuthClaims(parsed).IsGatingExempt() {
			t.Fatal("exempt tenant must stay gating-exempt on a terminal token")
		}
	})
}
