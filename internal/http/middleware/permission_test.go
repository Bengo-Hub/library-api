package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestRequirePlatformOwnerRejectsSuperuser locks in that the platform gate (encryption key,
// platform integrations) takes only IsPlatformOwner. A superuser role is a tenant role and must
// be refused, whether it came on an SSO token or a library PIN token.
func TestRequirePlatformOwnerRejectsSuperuser(t *testing.T) {
	gate := RequirePlatformOwner()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func(claims *authclient.Claims) int {
		req := httptest.NewRequest(http.MethodGet, "/platform/encryption-key", nil)
		if claims != nil {
			req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
		}
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, req)
		return rec.Code
	}
	mk := func(issuer string, platformOwner bool, roles ...string) *authclient.Claims {
		return &authclient.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: uuid.NewString(), Issuer: issuer},
			TenantID:         uuid.NewString(),
			Roles:            roles,
			IsPlatformOwner:  platformOwner,
		}
	}

	if code := call(nil); code != http.StatusForbidden {
		t.Fatalf("no claims: status = %d; want 403", code)
	}
	if code := call(mk("library-terminal", false, "superuser")); code != http.StatusForbidden {
		t.Fatalf("PIN token with superuser: status = %d; want 403", code)
	}
	if code := call(mk("https://sso.example", false, "superuser", "admin")); code != http.StatusForbidden {
		t.Fatalf("SSO tenant superuser: status = %d; want 403", code)
	}
	if code := call(mk("https://sso.example", true)); code != http.StatusOK {
		t.Fatalf("platform owner: status = %d; want 200", code)
	}
}
