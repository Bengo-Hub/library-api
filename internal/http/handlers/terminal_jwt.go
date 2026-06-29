package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/golang-jwt/jwt/v5"
)

const terminalIssuer = "library-terminal"

// terminalTokenTTL is how long a desk/kiosk PIN session lasts before requiring a re-PIN. Sized
// to a full working day so the desk isn't logged out mid-shift (was 8h — expired too soon).
const terminalTokenTTL = 12 * time.Hour

// terminalClaims are embedded in short-lived HMAC JWTs issued after a library PIN login.
// They mirror the SSO JWT shape so the same downstream middleware (JIT, subscription gate,
// RBAC) treats a PIN/terminal session exactly like an SSO session.
type terminalClaims struct {
	UserID               string         `json:"user_id"`
	TenantID             string         `json:"tenant_id"`
	TenantSlug           string         `json:"tenant_slug"`
	Email                string         `json:"email"`
	Name                 string         `json:"name"`
	Roles                []string       `json:"roles"`
	Permissions          []string       `json:"permissions"`
	IsPlatformOwner      bool           `json:"is_platform_owner,omitempty"`
	IsDemo               bool           `json:"is_demo,omitempty"`
	BillingMode          string         `json:"billing_mode,omitempty"`
	SubscriptionStatus   string         `json:"sub_status,omitempty"`
	SubscriptionFeatures []string       `json:"subscription_features,omitempty"`
	SubscriptionLimits   map[string]int `json:"sub_limits,omitempty"`
	jwt.RegisteredClaims
}

// issueTerminalJWT signs an 8-hour HMAC-SHA256 terminal JWT for a library PIN session.
func issueTerminalJWT(secret []byte, tc terminalClaims) (string, error) {
	now := time.Now()
	tc.RegisteredClaims = jwt.RegisteredClaims{
		Subject:   tc.UserID,
		Issuer:    terminalIssuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(terminalTokenTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, tc).SignedString(secret)
}

// validateTerminalJWT parses and validates an HMAC-signed terminal JWT.
func validateTerminalJWT(tokenStr string, secret []byte) (*terminalClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &terminalClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*terminalClaims)
	if !ok || !token.Valid || claims.Issuer != terminalIssuer {
		return nil, fmt.Errorf("invalid terminal JWT")
	}
	return claims, nil
}

// terminalToAuthClaims converts terminal JWT claims into the shared authclient.Claims so
// downstream middleware (JIT provisioning, RequireServicePermission, subscription gate) work
// uniformly for PIN sessions.
func terminalToAuthClaims(tc *terminalClaims) *authclient.Claims {
	return &authclient.Claims{
		TenantID:             tc.TenantID,
		TenantSlug:           tc.TenantSlug,
		Email:                tc.Email,
		Roles:                tc.Roles,
		Permissions:          tc.Permissions,
		IsPlatformOwner:      tc.IsPlatformOwner,
		IsDemo:               tc.IsDemo,
		BillingMode:          tc.BillingMode,
		SubscriptionStatus:   tc.SubscriptionStatus,
		SubscriptionFeatures: tc.SubscriptionFeatures,
		SubscriptionLimits:   tc.SubscriptionLimits,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: tc.UserID,
			Issuer:  tc.Issuer,
		},
	}
}

// RequireAnyAuth accepts either a library terminal PIN JWT (HMAC) or a standard SSO JWT.
// Terminal JWTs are validated first; on failure the request falls through to SSO RequireAuth.
func RequireAnyAuth(jwtSecret []byte, ssoAuth *authclient.AuthMiddleware) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") && len(jwtSecret) > 0 {
				tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
				if tc, err := validateTerminalJWT(tokenStr, jwtSecret); err == nil {
					ctx := authclient.ContextWithClaims(r.Context(), terminalToAuthClaims(tc))
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			if ssoAuth != nil {
				ssoAuth.RequireAuth(next).ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
