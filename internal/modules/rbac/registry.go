package rbac

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PushRolesToAuthRegistry POSTs the library role catalogue to auth-api
// POST /api/v1/s2s/roles/sync so auth-ui can assign service-level library roles to members
// and global→service role resolution stays correct. Idempotent on the auth side (upsert by
// unique role_code) — safe to call on every startup/deploy. Best-effort: never fatal.
func (s *Service) PushRolesToAuthRegistry(ctx context.Context, authURL, apiKey string) error {
	authURL = strings.TrimRight(strings.TrimSpace(authURL), "/")
	if authURL == "" || strings.TrimSpace(apiKey) == "" {
		return nil // unwired — skip silently
	}
	type roleEntry struct {
		RoleCode    string `json:"role_code"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
	}
	roles := []roleEntry{
		{RoleCode: RoleAdmin, Name: "Library Admin", Description: "Full library administration", Scope: "library"},
		{RoleCode: RoleStaff, Name: "Library Staff", Description: "Circulation desk + cataloging", Scope: "library"},
		{RoleCode: RoleMember, Name: "Library Member", Description: "Patron self-service", Scope: "library"},
	}
	body, _ := json.Marshal(map[string]any{"service": "library", "roles": roles})

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, authURL+"/api/v1/s2s/roles/sync", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("auth registry returned %d", resp.StatusCode)
	}
	return nil
}
