package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/ent/tenant"
	"github.com/bengobox/library-service/internal/modules/barcode"
	"github.com/bengobox/library-service/internal/modules/rbac"
	"github.com/bengobox/library-service/internal/platform/subscriptions"
)

// PINAuthHandler implements terminal/PIN login that supplements SSO for the circulation
// desk and self-checkout kiosk. Adapted from pos-api: branch (outlet) is chosen first, then a
// PIN; staff may only log in to a branch they're assigned to (admins → any branch). Lockout
// after repeated wrong PINs mirrors the POS lockout policy.
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

// PIN lockout policy (mirrors pos-api).
const (
	maxFailedPINAttempts = 5
	pinLockoutDuration   = 15 * time.Minute
)

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

// isLibraryAdmin reports whether the user's roles grant cross-branch (any-branch) access.
func isLibraryAdmin(roles []string) bool {
	for _, r := range roles {
		if r == rbac.RoleAdmin {
			return true
		}
	}
	return false
}

// branchAllowed reports whether a user may log in to branchID (admins → any active branch).
func branchAllowed(u *ent.LibraryUser, branchID string) bool {
	if isLibraryAdmin(u.Roles) {
		return true
	}
	for _, b := range u.BranchIds {
		if b == branchID {
			return true
		}
	}
	return false
}

// activeBranches returns the tenant's active branches (auto-provisions a default HQ when none).
func (h *PINAuthHandler) activeBranches(ctx context.Context, tenantID uuid.UUID) []*ent.Branch {
	rows, _ := h.db.Branch.Query().Where(branch.TenantID(tenantID), branch.IsActive(true)).All(ctx)
	if len(rows) == 0 {
		if b := EnsureDefaultBranch(ctx, h.db, tenantID); b != nil {
			rows = []*ent.Branch{b}
		}
	}
	return rows
}

func branchJSON(b *ent.Branch) map[string]any {
	return map[string]any{"id": b.ID.String(), "name": b.Name, "code": b.Code, "is_default": b.IsDefault}
}

// PINBranches godoc
// @Summary Active branches for the PIN-login branch picker (public per tenant)
// @Tags Auth
// @Router /{tenant}/library/auth/pin/branches [get]
func (h *PINAuthHandler) PINBranches(w http.ResponseWriter, r *http.Request) {
	t, ok := h.tenantBySlug(r)
	if !ok {
		respondError(w, http.StatusNotFound, "unknown tenant", "not_found")
		return
	}
	rows := h.activeBranches(r.Context(), t.ID)
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		out = append(out, branchJSON(b))
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

func (h *PINAuthHandler) terminalClaimsFor(ctx context.Context, t *ent.Tenant, u *ent.LibraryUser) terminalClaims {
	tc := terminalClaims{
		UserID:             u.UserID,
		TenantID:           t.ID.String(),
		TenantSlug:         t.Slug,
		Email:              u.Email,
		Name:               u.DisplayName,
		Roles:              u.Roles,
		Permissions:        h.rbac.ListPermissions(ctx, t.ID, u.UserID),
		SubscriptionStatus: "active",
	}
	// Baseline demo/platform bypass from the tenant slug (mirrors pos-api): the demo tenant and
	// the platform owner are gating-exempt even if the subscriptions S2S call is unavailable, so
	// PIN/terminal sessions can use feature-gated routes without a fragile dependency.
	tc.IsDemo = t.Slug == "codevertex-demo"
	tc.IsPlatformOwner = t.Slug == "codevertex"
	if e := h.subs.GetEntitlements(ctx, t.ID.String()); e != nil {
		tc.SubscriptionStatus = e.Status
		tc.SubscriptionFeatures = e.Features
		tc.BillingMode = e.BillingMode
		tc.IsDemo = tc.IsDemo || e.IsDemoBypass
	}
	return tc
}

// loginResponse builds the shared PIN-login success body (token + user + selected branch).
func (h *PINAuthHandler) loginResponse(ctx context.Context, t *ent.Tenant, u *ent.LibraryUser, br *ent.Branch) (map[string]any, error) {
	token, err := issueTerminalJWT(h.jwtSecret, h.terminalClaimsFor(ctx, t, u))
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"access_token": token,
		"token_type":   "terminal",
		"user_id":      u.UserID,
		"name":         u.DisplayName,
		"roles":        u.Roles,
		"is_admin":     isLibraryAdmin(u.Roles),
		"expires_in":   int(terminalTokenTTL.Seconds()),
	}
	if br != nil {
		body["branch_id"] = br.ID.String()
		body["branch_name"] = br.Name
	}
	return body, nil
}

// resolveLoginBranch picks/validates the branch for a PIN login: explicit branch_id (validated),
// else the sole allowed branch, else error. Returns the branch + an error message ("" = ok).
func (h *PINAuthHandler) resolveLoginBranch(ctx context.Context, tenantID uuid.UUID, u *ent.LibraryUser, branchID string) (*ent.Branch, string) {
	all := h.activeBranches(ctx, tenantID)
	byID := map[string]*ent.Branch{}
	for _, b := range all {
		byID[b.ID.String()] = b
	}
	if branchID != "" {
		b, ok := byID[branchID]
		if !ok {
			return nil, "unknown branch"
		}
		if !branchAllowed(u, branchID) {
			return nil, "you are not assigned to this branch"
		}
		return b, ""
	}
	// No branch supplied — admins default to the default/first; others to their sole allowed one.
	if isLibraryAdmin(u.Roles) {
		for _, b := range all {
			if b.IsDefault {
				return b, ""
			}
		}
		if len(all) > 0 {
			return all[0], ""
		}
		return nil, "no branches configured"
	}
	allowed := make([]*ent.Branch, 0)
	for _, b := range all {
		if branchAllowed(u, b.ID.String()) {
			allowed = append(allowed, b)
		}
	}
	if len(allowed) == 1 {
		return allowed[0], ""
	}
	if len(allowed) == 0 {
		return nil, "no branch assigned — ask an admin to assign you a branch"
	}
	return nil, "select a branch"
}

// checkLockout returns a non-empty message when the user's PIN is currently locked.
func lockoutMessage(u *ent.LibraryUser) string {
	if u.PinLockedUntil != nil && time.Now().Before(*u.PinLockedUntil) {
		return "PIN locked. Try again in " + time.Until(*u.PinLockedUntil).Round(time.Second).String()
	}
	return ""
}

func (h *PINAuthHandler) registerPINFailure(ctx context.Context, u *ent.LibraryUser) {
	attempts := u.PinFailedAttempts + 1
	upd := h.db.LibraryUser.UpdateOne(u).SetPinFailedAttempts(attempts)
	if attempts >= maxFailedPINAttempts {
		upd = upd.SetPinLockedUntil(time.Now().Add(pinLockoutDuration))
	}
	_ = upd.Exec(ctx)
}

// Login godoc
// @Summary PIN login by profile — validate a staff PIN at a branch and return a terminal JWT
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
		UserID   string `json:"user_id"`
		PIN      string `json:"pin"`
		BranchID string `json:"branch_id"`
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
	if msg := lockoutMessage(u); msg != "" {
		respondError(w, http.StatusTooManyRequests, msg, "pin_locked")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(*u.PinHash), []byte(body.PIN)) != nil {
		h.registerPINFailure(r.Context(), u)
		respondError(w, http.StatusUnauthorized, "invalid PIN", "invalid_pin")
		return
	}
	br, msg := h.resolveLoginBranch(r.Context(), t.ID, u, body.BranchID)
	if msg != "" {
		respondError(w, http.StatusBadRequest, msg, "branch_required")
		return
	}
	_ = h.db.LibraryUser.UpdateOne(u).SetPinFailedAttempts(0).ClearPinLockedUntil().Exec(r.Context())
	resp, err := h.loginResponse(r.Context(), t, u, br)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "token_failed")
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

// IdentifyByPIN godoc
// @Summary PIN-first login — identify staff by PIN at a branch (no profile picker)
// @Tags Auth
// @Router /{tenant}/library/auth/pin/identify [post]
func (h *PINAuthHandler) IdentifyByPIN(w http.ResponseWriter, r *http.Request) {
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
		PIN      string `json:"pin"`
		BranchID string `json:"branch_id"`
	}
	if err := Decode(r, &body); err != nil || body.PIN == "" || body.BranchID == "" {
		respondError(w, http.StatusBadRequest, "pin and branch_id are required", "invalid_request")
		return
	}
	br, err := h.db.Branch.Query().Where(branch.IDEQ(uuid.MustParse(orZeroUUID(body.BranchID))), branch.TenantID(t.ID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusBadRequest, "unknown branch", "invalid_request")
		return
	}
	// Candidates: active staff with a PIN who are allowed at this branch (admins always).
	candidates, _ := h.db.LibraryUser.Query().
		Where(libraryuser.TenantID(t.ID), libraryuser.IsActive(true), libraryuser.PinHashNotNil()).
		All(r.Context())
	var matched *ent.LibraryUser
	for _, u := range candidates {
		if !branchAllowed(u, br.ID.String()) {
			continue
		}
		if msg := lockoutMessage(u); msg != "" {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(*u.PinHash), []byte(body.PIN)) == nil {
			matched = u
			break
		}
	}
	if matched == nil {
		respondError(w, http.StatusUnauthorized, "invalid PIN for this branch", "invalid_pin")
		return
	}
	_ = h.db.LibraryUser.UpdateOne(matched).SetPinFailedAttempts(0).ClearPinLockedUntil().Exec(r.Context())
	resp, err := h.loginResponse(r.Context(), t, matched, br)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "token_failed")
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

// IdentifyByCard resolves a staff member from a scanned staff-card serial (the LibraryUser UserID,
// encoded in the card barcode) and issues a terminal JWT — badge login for the desk/kiosk. Branch
// scoping (admins → any branch) is enforced exactly like PIN login.
// @Router /{tenant}/library/auth/pin/card [post]
func (h *PINAuthHandler) IdentifyByCard(w http.ResponseWriter, r *http.Request) {
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
		Card     string `json:"card"`
		BranchID string `json:"branch_id"`
	}
	if err := Decode(r, &body); err != nil || strings.TrimSpace(body.Card) == "" || body.BranchID == "" {
		respondError(w, http.StatusBadRequest, "card and branch_id are required", "invalid_request")
		return
	}
	br, err := h.db.Branch.Query().Where(branch.IDEQ(uuid.MustParse(orZeroUUID(body.BranchID))), branch.TenantID(t.ID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusBadRequest, "unknown branch", "invalid_request")
		return
	}
	u, err := h.db.LibraryUser.Query().
		Where(libraryuser.TenantID(t.ID), libraryuser.UserID(strings.TrimSpace(body.Card)), libraryuser.IsActive(true)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusUnauthorized, "card not recognised", "invalid_card")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "lookup_failed")
		return
	}
	if !branchAllowed(u, br.ID.String()) {
		respondError(w, http.StatusForbidden, "you are not assigned to this branch", "branch_forbidden")
		return
	}
	resp, err := h.loginResponse(r.Context(), t, u, br)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "token_failed")
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

// MyCard renders the authenticated staff member's own card (PDF) — "print my card" from the profile.
// @Router /{tenant}/library/auth/me/card.pdf [get]
func (h *PINAuthHandler) MyCard(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	uid := UserIDFrom(r)
	if uid == "" {
		respondError(w, http.StatusUnauthorized, "no user", "unauthorized")
		return
	}
	u, err := h.db.LibraryUser.Query().Where(libraryuser.TenantID(tenantID), libraryuser.UserID(uid)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "staff record not found", "not_found")
		return
	}
	h.writeStaffCard(w, r, tenantID, u)
}

// StaffCard renders a given staff member's card (PDF) — admin prints it from Team & Roles.
// @Router /{tenant}/library/team/{user_id}/card.pdf [get]
func (h *PINAuthHandler) StaffCard(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	uid := chi.URLParam(r, "user_id")
	u, err := h.db.LibraryUser.Query().Where(libraryuser.TenantID(tenantID), libraryuser.UserID(uid)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "staff record not found", "not_found")
		return
	}
	h.writeStaffCard(w, r, tenantID, u)
}

func (h *PINAuthHandler) writeStaffCard(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, u *ent.LibraryUser) {
	role := ""
	if len(u.Roles) > 0 {
		role = strings.ToUpper(strings.ReplaceAll(u.Roles[0], "library_", ""))
	}
	card := barcode.MemberCard{Kind: "STAFF CARD", Name: u.DisplayName, MembershipNo: u.UserID, Tier: role}
	if t, terr := h.db.Tenant.Get(r.Context(), tenantID); terr == nil {
		card.Org = t.Name
	}
	pdf, err := barcode.RenderMemberCard(card)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "render_failed")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=\"staff-card-"+u.UserID+".pdf\"")
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	_, _ = w.Write(pdf)
}

// orZeroUUID returns s if it's a valid UUID, else the nil UUID string (so MustParse never panics).
func orZeroUUID(s string) string {
	if _, err := uuid.Parse(s); err != nil {
		return uuid.Nil.String()
	}
	return s
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
		SetPinHash(string(hash)).SetPinFastHash(pinFastHash(tenantID, u.UserID, body.PIN)).
		SetPinFailedAttempts(0).ClearPinLockedUntil().Save(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "save_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
}

// StaffProfiles godoc
// @Summary List staff with PINs for the keypad picker (public; optional ?branch_id scoping)
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
	branchID := r.URL.Query().Get("branch_id")
	out := make([]map[string]any, 0, len(rows))
	for _, u := range rows {
		if branchID != "" && !branchAllowed(u, branchID) {
			continue
		}
		out = append(out, map[string]any{
			"user_id": u.UserID, "name": u.DisplayName, "roles": u.Roles,
			"has_pin": true, "is_admin": isLibraryAdmin(u.Roles),
		})
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}
