// Package marketflow provides an S2S client for the MarketFlow CRM API. Library member
// create/update calls UpsertContactByPhone to sync the member into the CRM and store
// the returned contact ID on the Member record (best-effort; never blocks the request).
package marketflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Client is an S2S client for the MarketFlow CRM API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	log        *zap.Logger
}

// NewClient creates a new MarketFlow S2S client.
// baseURL is the MARKETFLOW_SERVICE_URL env var; apiKey is the shared INTERNAL_SERVICE_KEY.
func NewClient(baseURL, apiKey string, log *zap.Logger) *Client {
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		log:        log.Named("marketflow-client"),
	}
}

// Enabled returns false when the client has no base URL configured.
func (c *Client) Enabled() bool { return c.baseURL != "" }

type upsertContactRequest struct {
	TenantID  string `json:"tenant_id"`
	Phone     string `json:"phone"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type upsertContactResponse struct {
	ID string `json:"id"`
}

// UpsertContactByPhone creates or returns the existing MarketFlow contact for the given phone.
// Returns uuid.Nil on any error or when the client is disabled — callers must handle gracefully.
func (c *Client) UpsertContactByPhone(ctx context.Context, tenantID uuid.UUID, phone, fullName string) uuid.UUID {
	if !c.Enabled() {
		return uuid.Nil
	}
	firstName, lastName := splitName(fullName)
	payload, _ := json.Marshal(upsertContactRequest{
		TenantID:  tenantID.String(),
		Phone:     phone,
		FirstName: firstName,
		LastName:  lastName,
	})
	url := fmt.Sprintf("%s/api/v1/internal/contacts/upsert", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		c.log.Warn("marketflow: build request failed", zap.Error(err))
		return uuid.Nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Warn("marketflow: upsert contact failed", zap.Error(err))
		return uuid.Nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.log.Warn("marketflow: unexpected status", zap.Int("status", resp.StatusCode))
		return uuid.Nil
	}
	var result upsertContactResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.log.Warn("marketflow: decode response failed", zap.Error(err))
		return uuid.Nil
	}
	id, err := uuid.Parse(result.ID)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// splitName splits "First Last" into (firstName, lastName). Single words go to firstName.
func splitName(fullName string) (string, string) {
	for i, ch := range fullName {
		if ch == ' ' {
			return fullName[:i], fullName[i+1:]
		}
	}
	return fullName, ""
}
