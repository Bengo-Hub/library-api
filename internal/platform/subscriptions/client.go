// Package subscriptions is the S2S client for subscriptions-api. All calls use the
// shared INTERNAL_SERVICE_KEY via X-API-Key (never a per-service key). It mirrors the
// inventory-api client: cached, fail-open entitlement checks for event consumers.
package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const entitlementCacheTTL = 60 * time.Second

// Config holds configuration for the subscriptions S2S client.
type Config struct {
	ServiceURL     string
	APIKey         string
	RequestTimeout time.Duration
}

// Entitlements is the tenant's subscription snapshot fetched from subscriptions-api.
type Entitlements struct {
	Features     []string `json:"features"`
	Status       string   `json:"status"`
	BillingMode  string   `json:"billing_mode"`
	IsDemoBypass bool     `json:"is_demo_bypass"`
}

// Client interacts with subscriptions-api over S2S (X-API-Key, no user JWT).
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient creates a subscriptions S2S client. nil-safe: a nil *Client fails open.
func NewClient(cfg Config) *Client {
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// GetEntitlements fetches the tenant snapshot from the S2S tenant-scoped endpoint
// (/api/v1/tenants/{id}/subscription — NOT /subscription). Returns nil on any error so
// callers fail open.
func (c *Client) GetEntitlements(ctx context.Context, tenantID string) *Entitlements {
	if c == nil || c.cfg.ServiceURL == "" || tenantID == "" {
		return nil
	}
	url := fmt.Sprintf("%s/api/v1/tenants/%s/subscription", c.cfg.ServiceURL, tenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var e Entitlements
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return nil
	}
	return &e
}

type cachedEntitlements struct {
	ent     *Entitlements
	fetched time.Time
}

var (
	entCacheMu sync.Mutex
	entCache   = map[string]cachedEntitlements{}
)

// ConsumerHasFeature reports whether a tenant is entitled to featureCode, for NATS event
// consumers (tenant_id, no JWT). Demo-bypass and service-charge (PAYG) tenants are always
// allowed. FAILS OPEN when unwired/unreachable so a subscriptions outage never drops sync.
func (c *Client) ConsumerHasFeature(ctx context.Context, tenantID, featureCode string) bool {
	if c == nil || tenantID == "" {
		return true
	}
	e := c.cachedEntitlements(ctx, tenantID)
	if e == nil {
		return true
	}
	if e.IsDemoBypass || e.BillingMode == "service_charge" {
		return true
	}
	for _, f := range e.Features {
		if f == featureCode {
			return true
		}
	}
	return false
}

func (c *Client) cachedEntitlements(ctx context.Context, tenantID string) *Entitlements {
	entCacheMu.Lock()
	if hit, ok := entCache[tenantID]; ok && time.Since(hit.fetched) < entitlementCacheTTL {
		entCacheMu.Unlock()
		return hit.ent
	}
	entCacheMu.Unlock()

	e := c.GetEntitlements(ctx, tenantID)
	if e == nil {
		return nil
	}
	entCacheMu.Lock()
	entCache[tenantID] = cachedEntitlements{ent: e, fetched: time.Now()}
	entCacheMu.Unlock()
	return e
}
