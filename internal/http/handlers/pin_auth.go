package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/ent/tenant"
	"github.com/bengobox/library-service/internal/modules/rbac"
	"github.com/bengobox/library-service/internal/platform/subscriptions"
)

// PINAuthHandler implements terminal/PIN login that supplements SSO for the circulation
// desk and self-checkout kiosk (quick staff switching without a full SSO round-trip).
type PINAuthHandler struct {
	db        *ent.Client
	rbac      *rbac.Service
	subs      *subscriptions.Client
	jwtSecret []byte
	log       *zap.Logger
}

// NewPINAuthHandler builds the PIN auth handler.
func NewPINAuthHandler(db *ent.Client, rbacSvc *rbac.Service, subs *subscriptions.Client, jwtSecret string, log *zap.Logger) *PINAuthHandler {
	return &PINAuthHandler{db: db, rbac: rbacSvc, subs: subs, jwtSecret: []byte(jwtSecret), log: log}
}

// Secret exposes the HMAC secret so the router can build RequireAnyAuth.
func (h *PINAuthHandler) Secret() []byte { return h.jwtSecret }

func pinFastHash(tenantID uuid.UUID, userID, pin string) string {
	sum := sha256.Sum256([]byte(tenantID.String() + ":" + userID + ":" + pin))
	return hex.EncodeToString(sum[:])
}

func (h *PINAuthHandler) tenantBySlug(r *http.Request) (*ent.Tenant, bool) {
	slug := chi.URLParam(r, "tenant")
	t, err := h.db.Tenant.Query().Where(tenant.Slug(slug)).Only(r.Context())
	if err != nil {
		return nil, false
	}
	return t, true
}

// Login godoc
// @Summary PIN login — validate a staff PIN and return a short-lived terminal JWT
// @Tags Auth
// @Router /{tenant}/library/auth/pin [post]
func (h *PINAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if len(h.jwtSecret) == 0 {
		respondError(w, http.StatusServiceUnavailable, "PIN auth not configured", "pin_unconfigured")
		return
	}
	t, ok := h.tenantBySlug(r)
	if !ok {
		respondError(w, http.StatusNotFound, "unknown tenant", "not_found")
		return
	}
	var body struct {
		UserID string `json:"user_id"`
		PIN    string `json:"pin"`
	}
	if err := Decode(r, &body); err != nil || body.UserID == "" || body.PIN == "" {
		respondError(w, http.StatusBadRequest, "user_id and pin are required", "invalid_request")
		return
	}
	u, err := h.db.LibraryUser.Query().Where(libraryuser.TenantID(t.ID), libraryuser.UserID(body.UserID)).Only(r.Context())
	if err != nil || u.PinHash == nil || *u.PinHash == "" {
		respondError(w, http.StatusUnauthorized, "invalid PIN", "invalid_pin")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(*u.PinHash), []byte(body.PIN)) != nil {
		respondError(w, http.StatusUnauthorized, "invalid PIN", "invalid_pin")
		return
	}

	tc := terminalClaims{
		UserID:      u.UserID,
		TenantID:    t.ID.String(),
		TenantSlug:  t.Slug,
		Email:       u.Email,
		Name:        u.DisplayName,
		Roles:       u.Roles,
		Permissions: h.rbac.ListPermissions(r.Context(), t.ID, u.UserID),
		// Subscription snapshot (so the mutations gate treats PIN sessions like SSO).
		SubscriptionStatus: "active",
	}
	if e := h.subs.GetEntitlements(r.Context(), t.ID.String()); e != nil {
		tc.SubscriptionStatus = e.Status
		tc.SubscriptionFeatures = e.Features
		tc.BillingMode = e.BillingMode
		tc.IsDemo = e.IsDemoBypass
	}
	token, err := issueTerminalJWT(h.jwtSecret, tc)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "token_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "terminal",
		"user_id":      u.UserID,
		"name":         u.DisplayName,
		"roles":        u.Roles,
		"expires_in":   8 * 3600,
	})
}

// SetPIN godoc
// @Summary Set/replace a staff member's desk PIN (SSO-authed; manager action)
// @Tags Auth
// @Router /{tenant}/library/auth/pin/set [post]
func (h *PINAuthHandler) SetPIN(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var body struct {
		UserID string `json:"user_id"`
		PIN    string `json:"pin"`
	}
	if err := Decode(r, &body); err != nil || body.UserID == "" || len(body.PIN) < 4 {
		respondError(w, http.StatusBadRequest, "user_id and a 4+ digit pin are required", "invalid_request")
		return
	}
	u, err := h.db.LibraryUser.Query().Where(libraryuser.TenantID(tenantID), libraryuser.UserID(body.UserID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "user not found", "not_found")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.PIN), bcrypt.DefaultCost)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "hash_failed")
		return
	}
	if _, err := h.db.LibraryUser.UpdateOne(u).
		SetPinHash(string(hash)).SetPinFastHash(pinFastHash(tenantID, u.UserID, body.PIN)).Save(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "save_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
}

// StaffProfiles godoc
// @Summary List staff with PINs for the keypad picker (public; no PINs returned)
// @Tags Auth
// @Router /{tenant}/library/auth/pin/profiles [get]
func (h *PINAuthHandler) StaffProfiles(w http.ResponseWriter, r *http.Request) {
	t, ok := h.tenantBySlug(r)
	if !ok {
		respondError(w, http.StatusNotFound, "unknown tenant", "not_found")
		return
	}
	rows, err := h.db.LibraryUser.Query().
		Where(libraryuser.TenantID(t.ID), libraryuser.IsActive(true), libraryuser.PinHashNotNil()).
		All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		out = append(out, map[string]any{"user_id": u.UserID, "name": u.DisplayName, "roles": u.Roles, "has_pin": true})
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}
