// Package notifications is the REST fallback client for notifications-api. The PRIMARY
// path is event-driven (library outbox events consumed by a notifications worker); this
// client is used for synchronous, on-demand sends. Email is rate-limited by plan on the
// notifications side; SMS/push/WhatsApp are never blocked.
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client is the notifications-api S2S client.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient creates a notifications S2S client.
func NewClient(serviceURL, internalServiceKey string, timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &Client{baseURL: serviceURL, apiKey: internalServiceKey, http: &http.Client{Timeout: timeout}}
}

// Message is the body for POST /{tenant}/notifications/messages.
type Message struct {
	Channel  string         `json:"channel"` // email | sms | whatsapp | push
	Template string         `json:"template"`
	To       []string       `json:"to"`
	Data     map[string]any `json:"data,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Send posts a notification message. Best-effort: returns an error the caller may log and
// ignore (notifications must never block a circulation action).
func (c *Client) Send(ctx context.Context, tenantID string, msg Message) error {
	if c == nil || c.baseURL == "" {
		return nil
	}
	url := fmt.Sprintf("%s/%s/notifications/messages", c.baseURL, tenantID)
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("notifications: status %d", resp.StatusCode)
	}
	return nil
}
