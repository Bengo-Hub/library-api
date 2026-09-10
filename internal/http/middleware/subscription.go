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
//
// Delegates to the canonical authclient.RequireFeatureCode. NOTE: the old body also
// bypassed on claims.IsSuperuser(); dropping that is intentional and correct (platform
// SEC-3 policy: tenant superusers do NOT bypass subscription/feature gating).
func RequireFeature(featureCode string) func(http.Handler) http.Handler {
	return authclient.RequireFeatureCode(featureCode)
}

// Note: an inline RequireActiveSubscriptionForMutations used to live here, with an incorrect
// claims.IsSuperuser() bypass (platform SEC-3 policy: tenant superusers do NOT bypass
// subscription/feature gating — see RequireFeature above, already fixed). It was dead code —
// router.go wires authclient.RequireActiveSubscriptionForMutationsWithGrace(7) directly — so it
// was removed outright rather than fixed in place, to stop it being copied as "the pattern" by
// a future service.
