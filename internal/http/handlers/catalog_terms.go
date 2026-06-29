package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/catalogterm"
	"github.com/bengobox/library-service/internal/modules/refdata"
)

// validTermKinds are the cataloging dictionaries exposed to the pickers.
var validTermKinds = map[string]bool{"author": true, "publisher": true, "place": true, "subject": true}

type termResponse struct {
	Value string `json:"value"`
}

// ListTerms returns dictionary suggestions for a cataloging field (author/publisher/place/subject),
// filtered by an optional ?q= substring. Includes both tenant and global (shared) terms.
// @Router /{tenant}/library/catalog/terms [get]
func (h *CatalogHandler) ListTerms(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	if !validTermKinds[kind] {
		respondError(w, http.StatusBadRequest, "unknown kind", "invalid_request")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	query := h.db.CatalogTerm.Query().Where(
		catalogterm.KindEQ(kind),
		catalogterm.Or(catalogterm.TenantID(tenantID), catalogterm.TenantID(refdata.GlobalTenantID)),
	)
	if q != "" {
		query = query.Where(catalogterm.ValueContainsFold(q))
	}
	rows, err := query.Order(ent.Asc(catalogterm.FieldValue)).Limit(50).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	// De-dup values (a value may exist both globally and per-tenant).
	seen := map[string]bool{}
	out := make([]termResponse, 0, len(rows))
	for _, t := range rows {
		if seen[t.Value] {
			continue
		}
		seen[t.Value] = true
		out = append(out, termResponse{Value: t.Value})
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

// CreateTerm adds a dictionary value (idempotent) so it appears in future pickers.
// @Router /{tenant}/library/catalog/terms [post]
func (h *CatalogHandler) CreateTerm(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	value := strings.TrimSpace(req.Value)
	if !validTermKinds[kind] || value == "" {
		respondError(w, http.StatusBadRequest, "kind and value are required", "invalid_request")
		return
	}
	upsertCatalogTerms(r.Context(), h.db, tenantID, kind, []string{value})
	respondJSON(w, http.StatusCreated, termResponse{Value: value})
}

// upsertCatalogTerms idempotently records dictionary values for a kind (best-effort; ignores
// duplicates). Called when terms are explicitly added and whenever a bib is saved so the pickers
// stay populated from real cataloging activity.
func upsertCatalogTerms(ctx context.Context, db *ent.Client, tenantID uuid.UUID, kind string, values []string) {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		exists, _ := db.CatalogTerm.Query().Where(
			catalogterm.TenantID(tenantID), catalogterm.KindEQ(kind), catalogterm.ValueEQ(v),
		).Exist(ctx)
		if exists {
			continue
		}
		_, _ = db.CatalogTerm.Create().SetTenantID(tenantID).SetKind(kind).SetValue(v).Save(ctx)
	}
}
