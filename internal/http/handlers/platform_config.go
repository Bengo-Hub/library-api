package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/platform/secrets"
)

// PlatformConfigHandler exposes platform-owner endpoints to manage the credential-encryption
// key and integration secrets (e.g. the ISBNdb API key). Raw key material / secret values are
// NEVER returned in responses or logged — only a sha256 fingerprint + source/updated_at.
type PlatformConfigHandler struct {
	store *secrets.Store
	log   *zap.Logger
}

// NewPlatformConfigHandler builds the handler over the shared secrets store (so a key change
// invalidates the same provider the ISBN lookup decrypts with).
func NewPlatformConfigHandler(store *secrets.Store, log *zap.Logger) *PlatformConfigHandler {
	return &PlatformConfigHandler{store: store, log: log}
}

// RegisterRoutes mounts the routes under the caller's (already platform-owner-gated) router.
func (h *PlatformConfigHandler) RegisterRoutes(r chi.Router) {
	r.Get("/encryption-key", h.GetEncryptionKey)
	r.Put("/encryption-key", h.PutEncryptionKey)
	r.Get("/integrations/isbndb", h.GetISBNdb)
	r.Put("/integrations/isbndb", h.PutISBNdb)
}

type encryptionKeyResponse struct {
	Configured     bool    `json:"configured"`
	Source         string  `json:"source"`
	KeyFingerprint string  `json:"key_fingerprint"`
	UpdatedAt      *string `json:"updated_at"`
}

// GetEncryptionKey godoc
// @Summary Platform credential-encryption key status (masked)
// @Tags Platform
// @Router /{tenant}/library/platform/encryption-key [get]
func (h *PlatformConfigHandler) GetEncryptionKey(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.encryptionStatus(r))
}

type putEncryptionKeyRequest struct {
	Key      string `json:"key"`
	Generate bool   `json:"generate"`
}

// PutEncryptionKey godoc
// @Summary Set or generate the platform credential-encryption key
// @Tags Platform
// @Router /{tenant}/library/platform/encryption-key [put]
func (h *PlatformConfigHandler) PutEncryptionKey(w http.ResponseWriter, r *http.Request) {
	var req putEncryptionKeyRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body", "invalid_request")
		return
	}
	var keyBytes []byte
	if req.Generate || req.Key == "" {
		keyBytes = make([]byte, 32)
		if _, err := rand.Read(keyBytes); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to generate key", "key_gen_failed")
			return
		}
	} else {
		decoded, err := base64.StdEncoding.DecodeString(req.Key)
		if err != nil || len(decoded) != 32 {
			respondError(w, http.StatusBadRequest, "key must be base64 of exactly 32 bytes", "invalid_request")
			return
		}
		keyBytes = decoded
	}
	if err := h.store.SetEncryptionKey(r.Context(), keyBytes); err != nil {
		h.log.Error("failed to save encryption key", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to save key", "save_failed")
		return
	}
	h.log.Info("platform credential-encryption key rotated") // no key material logged
	respondJSON(w, http.StatusOK, h.encryptionStatus(r))
}

func (h *PlatformConfigHandler) encryptionStatus(r *http.Request) encryptionKeyResponse {
	st := h.store.EncryptionStatus(r.Context())
	var updatedAt *string
	if st.Source == "db" && st.DBUpdatedAt != nil {
		ts := st.DBUpdatedAt.Format("2006-01-02T15:04:05Z")
		updatedAt = &ts
	}
	return encryptionKeyResponse{
		Configured:     st.Configured,
		Source:         st.Source,
		KeyFingerprint: st.Fingerprint,
		UpdatedAt:      updatedAt,
	}
}

type integrationResponse struct {
	Configured     bool    `json:"configured"`
	KeyFingerprint string  `json:"key_fingerprint"`
	UpdatedAt      *string `json:"updated_at"`
}

type putISBNdbRequest struct {
	APIKey string `json:"api_key"`
}

// GetISBNdb godoc
// @Summary ISBNdb integration status (masked)
// @Tags Platform
// @Router /{tenant}/library/platform/integrations/isbndb [get]
func (h *PlatformConfigHandler) GetISBNdb(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.isbndbStatus(r))
}

// PutISBNdb godoc
// @Summary Set or clear the ISBNdb API key (encrypted at rest; empty clears)
// @Tags Platform
// @Router /{tenant}/library/platform/integrations/isbndb [put]
func (h *PlatformConfigHandler) PutISBNdb(w http.ResponseWriter, r *http.Request) {
	var req putISBNdbRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body", "invalid_request")
		return
	}
	if err := h.store.SetSecret(r.Context(), secrets.KeyISBNdbAPIKey, req.APIKey, "ISBNdb API key (book metadata enrichment)"); err != nil {
		h.log.Error("failed to save ISBNdb key", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to save key", "save_failed")
		return
	}
	respondJSON(w, http.StatusOK, h.isbndbStatus(r))
}

func (h *PlatformConfigHandler) isbndbStatus(r *http.Request) integrationResponse {
	configured, fp, updatedAt := h.store.SecretStatus(r.Context(), secrets.KeyISBNdbAPIKey)
	var ts *string
	if updatedAt != nil {
		s := updatedAt.Format("2006-01-02T15:04:05Z")
		ts = &s
	}
	return integrationResponse{Configured: configured, KeyFingerprint: fp, UpdatedAt: ts}
}
