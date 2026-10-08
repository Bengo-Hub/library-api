package subscriptions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// gateClient stubs subscriptions-api's tenant subscription endpoint. A nil snapshot answers
// 503 to exercise the fail-open path.
func gateClient(t *testing.T, e *Entitlements) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if e == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(e)
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{ServiceURL: srv.URL, APIKey: "test-key"})
}

// Tenant IDs are distinct per case because the entitlement cache is package-wide.
func TestConsumerHasActiveProduct(t *testing.T) {
	cases := []struct {
		name   string
		tenant string
		ent    *Entitlements
		want   bool
	}{
		{"active", "t-lib-active", &Entitlements{ActiveProducts: []string{"library"}, BillingMode: "recurring"}, true},
		{"switched off", "t-lib-off", &Entitlements{ActiveProducts: []string{"pos"}, BillingMode: "recurring"}, false},
		{"exempt", "t-lib-exempt", &Entitlements{BillingMode: "exempt"}, true},
		{"payg", "t-lib-payg", &Entitlements{ActiveProducts: []string{"pos"}, BillingMode: "service_charge"}, true},
		{"no product lines yet", "t-lib-legacy", &Entitlements{BillingMode: "recurring"}, true},
		{"subscriptions down", "t-lib-down", nil, true},
	}
	for _, c := range cases {
		if got := gateClient(t, c.ent).ConsumerHasActiveProduct(context.Background(), c.tenant, "library"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	var nilClient *Client
	if !nilClient.ConsumerHasActiveProduct(context.Background(), "t-lib-nil", "library") {
		t.Error("nil client must fail open")
	}
}
