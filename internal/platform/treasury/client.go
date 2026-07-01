// Package treasury is a thin S2S client for treasury-api payment operations. All calls
// use the shared INTERNAL_SERVICE_KEY via X-API-Key (the S2S path needs no user JWT).
// Library charges (fines, e-book sales, membership fees) flow through CreateIntent.
package treasury

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the treasury S2S client.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient creates a treasury S2S client.
func NewClient(serviceURL, internalServiceKey string, timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Client{baseURL: serviceURL, apiKey: internalServiceKey, http: &http.Client{Timeout: timeout}}
}

// CreateIntentRequest is the body for POST /api/v1/s2s/{tenant}/payments/intents.
type CreateIntentRequest struct {
	SourceService string         `json:"source_service"` // always "library"
	ReferenceID   string         `json:"reference_id"`   // service-identifiable ref: LIB-{slug}-{hex} (see internal/payref); becomes the Paystack reference
	ReferenceType string         `json:"reference_type"` // library_fine | membership_fee | ebook_sale
	Amount        float64        `json:"amount"`
	Currency      string         `json:"currency"`
	PaymentMethod string         `json:"payment_method"` // "pending" — payer picks gateway on the pay page
	Description   string         `json:"description,omitempty"`
	CustomerEmail string         `json:"customer_email,omitempty"`
	CustomerPhone string         `json:"customer_phone,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// IntentResponse is the response from POST /api/v1/s2s/{tenant}/payments/intents.
// Amount is a string because treasury serializes decimal.Decimal as a quoted JSON string.
type IntentResponse struct {
	IntentID    string `json:"intent_id"`
	ID          string `json:"id"`
	Status      string `json:"status"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	InitiateURL string `json:"initiate_url,omitempty"`
}

// ResolvedID returns IntentID if non-empty, falling back to ID.
func (r *IntentResponse) ResolvedID() string {
	if r.IntentID != "" {
		return r.IntentID
	}
	return r.ID
}

// CreateIntent calls POST /api/v1/s2s/{tenant}/payments/intents. idempotencyKey (e.g. the
// fine/fee UUID) is sent as Idempotency-Key to prevent duplicate intents on retries.
func (c *Client) CreateIntent(ctx context.Context, tenantSlug, idempotencyKey string, req CreateIntentRequest) (*IntentResponse, error) {
	url := fmt.Sprintf("%s/api/v1/s2s/%s/payments/intents", c.baseURL, tenantSlug)
	headers := map[string]string{}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}
	return doRequest[IntentResponse](ctx, c.http, http.MethodPost, url, c.apiKey, headers, req)
}

// InvoiceLine is one line item on a vendor invoice.
type InvoiceLine struct {
	Description string  `json:"description"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
}

// CreateInvoiceRequest is the body for POST /api/v1/s2s/{tenant}/invoices.
type CreateInvoiceRequest struct {
	SourceService string        `json:"source_service"` // "library"
	ReferenceID   string        `json:"reference_id"`
	ReferenceType string        `json:"reference_type"` // "acquisition_invoice"
	InvoiceType   string        `json:"invoice_type"`   // "vendor_bill"
	Amount        float64       `json:"amount"`
	Currency      string        `json:"currency"`
	Description   string        `json:"description,omitempty"`
	VendorName    string        `json:"vendor_name,omitempty"`
	Lines         []InvoiceLine `json:"lines,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// InvoiceResponse is the response from POST /api/v1/s2s/{tenant}/invoices.
type InvoiceResponse struct {
	InvoiceID string `json:"invoice_id"`
	ID        string `json:"id"`
	Status    string `json:"status"`
}

// ResolvedID returns InvoiceID if non-empty, falling back to ID.
func (r *InvoiceResponse) ResolvedID() string {
	if r.InvoiceID != "" {
		return r.InvoiceID
	}
	return r.ID
}

// CreateInvoice calls POST /api/v1/s2s/{tenant}/invoices to create a vendor bill.
func (c *Client) CreateInvoice(ctx context.Context, tenantSlug string, req CreateInvoiceRequest) (*InvoiceResponse, error) {
	url := fmt.Sprintf("%s/api/v1/s2s/%s/invoices", c.baseURL, tenantSlug)
	return doRequest[InvoiceResponse](ctx, c.http, http.MethodPost, url, c.apiKey, nil, req)
}

func doRequest[T any](ctx context.Context, client *http.Client, method, url, apiKey string, headers map[string]string, body any) (*T, error) {
	var req *http.Request
	var err error
	if body != nil {
		b, merr := json.Marshal(body)
		if merr != nil {
			return nil, fmt.Errorf("treasury: marshal request: %w", merr)
		}
		req, err = http.NewRequestWithContext(ctx, method, url, bytes.NewReader(b))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, http.NoBody)
	}
	if err != nil {
		return nil, fmt.Errorf("treasury: build request: %w", err)
	}
	req.Header.Set("X-API-Key", apiKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("treasury: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("treasury: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("treasury: upstream error %d: %s", resp.StatusCode, string(respBody))
	}

	var result T
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("treasury: decode response: %w", err)
	}
	return &result, nil
}
