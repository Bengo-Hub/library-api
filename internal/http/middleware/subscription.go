package middleware

import (
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

// RequireActiveSubscriptionForMutations passes all GET/OPTIONS through; for mutations it
// requires an active subscription. Superuser / platform-owner / gating-exempt (demo, PAYG)
// tenants always pass. Emits the standard {error,code,upgrade} envelope the frontends parse.
func RequireActiveSubscriptionForMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodOptions || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		claims, ok := authclient.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			next.ServeHTTP(w, r) // auth middleware already gates; don't double-fail here
			return
		}
		if claims.IsSuperuser() || claims.IsPlatformOwner || claims.IsGatingExempt() || claims.IsSubscriptionActive() {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Your subscription is not active.","code":"subscription_inactive","upgrade":true}`))
	})
}
