package middleware

import (
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

// RequireFeature returns middleware that blocks a route group when the tenant's
// subscription does not include featureCode. Exemption funnels through the shared
// claims.IsGatingExempt() (platform owner, demo, service-charge, sub-exempt); tenant
// superusers are NOT exempt. PIN/terminal sessions carry the same feature snapshot as
// SSO tokens (see pin_auth.go), so gating is uniform across both. When no claims are
// present the request passes through so the outer auth layer decides. Emits the standard
// {error,code,upgrade} envelope the library-ui parses.
func RequireFeature(featureCode string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok || claims == nil {
				next.ServeHTTP(w, r)
				return
			}
			if claims.IsSuperuser() || claims.IsGatingExempt() || claims.HasFeature(featureCode) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"feature_not_available","code":"feature_not_available","required_feature":"` + featureCode + `","upgrade":true}`))
		})
	}
}

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
