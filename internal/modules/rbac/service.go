// Package rbac owns library-service roles + permissions (the service is the source of
// truth for its own library.{module}.{action} codes per reference_service_rbac_authme_sync).
// Effective permissions = SSO JWT ∪ this local RBAC. JIT provisioning heals EXISTING
// users on every request (treasury #30 gotcha), not only on first creation.
package rbac

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/libraryrole"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/ent/predicate"
	"github.com/bengobox/library-service/internal/ent/tenant"
)

// Library service roles (global; seeded once).
const (
	RoleAdmin  = "library_admin"
	RoleStaff  = "library_staff"
	RoleMember = "library_member"
)

// Service provides RBAC operations backed by Ent.
type Service struct {
	db             *ent.Client
	log            *zap.Logger
	authServiceURL string
	httpClient     *http.Client
	entitle        *authclient.EntitlementClient
}

// SetEntitlement attaches the plan check that stops JIT provisioning (tenant projection and user
// rows) for a tenant whose plan does not include the library service. Nil leaves it off.
func (s *Service) SetEntitlement(c *authclient.EntitlementClient) { s.entitle = c }

// NewService creates the RBAC service. authServiceURL (auth-api's public base URL) is used
// best-effort to backfill the local tenant projection's display name — never on the request's
// own auth path, so a slow/unreachable auth-api never blocks login.
func NewService(db *ent.Client, log *zap.Logger, authServiceURL string) *Service {
	return &Service{db: db, log: log, authServiceURL: authServiceURL, httpClient: &http.Client{Timeout: 3 * time.Second}}
}

// crudCodes returns the Django-style CRUD permission codes for a module
// (library.{module}.{view|add|change|delete|manage}).
func crudCodes(mod string) []string {
	return []string{
		"library." + mod + ".view",
		"library." + mod + ".add",
		"library." + mod + ".change",
		"library." + mod + ".delete",
		"library." + mod + ".manage",
	}
}

// staffPermissions is the library_staff grant: full CRUD on day-to-day modules + the custom
// circulation/holds/fines/transfers/stocktake actions, but NOT admin-only config (tiers/policies
// manage, branches, team, settings).
func staffPermissions() []string {
	out := []string{}
	for _, m := range []string{"catalog", "copies", "collections", "members", "ebooks"} {
		out = append(out, crudCodes(m)...)
	}
	out = append(out,
		"library.member_tiers.view", "library.loan_policies.view",
		"library.circulation.view", "library.circulation.checkout", "library.circulation.return", "library.circulation.renew",
		"library.holds.view", "library.holds.place", "library.holds.change", "library.holds.delete", "library.holds.manage",
		"library.fines.view", "library.fines.assess", "library.fines.waive", "library.fines.pay",
		"library.ebooks.lend",
		"library.transfers.view", "library.transfers.add", "library.transfers.receive", "library.transfers.manage",
		"library.stocktake.view", "library.stocktake.add", "library.stocktake.scan", "library.stocktake.finalize", "library.stocktake.manage",
		"library.membership_fees.view", "library.membership_fees.add", "library.membership_fees.pay",
		"library.reports.view",
		// Circulation-desk staff must be able to view their own Team page and set their own PIN
		// without needing a full library_admin (tenant-admin) grant.
		"library.team.view", "library.team.manage",
	)
	return out
}

// SeedGlobalRoles idempotently upserts the global library roles + their permission sets.
func (s *Service) SeedGlobalRoles(ctx context.Context) error {
	defaults := []struct {
		name  string
		desc  string
		perms []string
	}{
		{RoleAdmin, "Full library administration", []string{"*"}},
		{RoleStaff, "Circulation desk + cataloging", staffPermissions()},
		{RoleMember, "Patron self-service (browse catalog + e-books, place holds)", []string{
			"library.catalog.view", "library.ebooks.view", "library.holds.place",
		}},
	}
	for _, d := range defaults {
		existing, err := s.db.LibraryRole.Query().Where(libraryrole.Name(d.name), libraryrole.TenantIDIsNil()).Only(ctx)
		if ent.IsNotFound(err) {
			if _, cerr := s.db.LibraryRole.Create().
				SetName(d.name).SetDescription(d.desc).SetPermissions(d.perms).SetIsSystem(true).
				Save(ctx); cerr != nil {
				return cerr
			}
			continue
		} else if err != nil {
			return err
		}
		// Heal permission drift on existing system roles. The seed is the only writer of
		// system roles; the API refuses to change them.
		if _, err := s.db.LibraryRole.UpdateOne(existing).SetPermissions(d.perms).SetDescription(d.desc).SetIsSystem(true).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// MapGlobalRoles maps SSO global roles to library service roles. Privileged globals →
// library_admin; explicit staff/cashier → library_staff; everything else → library_member.
func MapGlobalRoles(global []string) []string {
	out := map[string]bool{}
	for _, g := range global {
		switch strings.ToLower(strings.TrimSpace(g)) {
		case "superuser", "admin", "owner", "platform_owner", "superusers":
			out[RoleAdmin] = true
		case "staff", "cashier", "manager":
			out[RoleStaff] = true
		}
	}
	if len(out) == 0 {
		out[RoleMember] = true
	}
	roles := make([]string, 0, len(out))
	for r := range out {
		roles = append(roles, r)
	}
	return roles
}

// EnsureUserFromToken upserts the local LibraryUser from JWT claims and (re)assigns the
// mapped roles. Runs on EVERY authenticated request so a user created before a role
// mapping existed still self-heals.
func (s *Service) EnsureUserFromToken(ctx context.Context, claims *authclient.Claims) error {
	if claims == nil || claims.Subject == "" || claims.TenantID == "" {
		return nil
	}
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return nil
	}
	if s.entitle.Require(ctx, claims.TenantID, "library") != nil {
		return nil // not in the library plan: nothing is provisioned (the module gate answers 403)
	}
	// Cache the tenant slug→id mapping locally so the PUBLIC PIN-login routes (which have no
	// JWT) can resolve the org slug. Best-effort, get-or-create by the auth tenant UUID.
	if slug := claims.GetTenantSlug(); slug != "" {
		existingTenant, err := s.db.Tenant.Query().Where(tenant.IDEQ(tenantID)).Only(ctx)
		switch {
		case ent.IsNotFound(err):
			// JWT claims never carry the tenant's display name (only its slug), so fetch it
			// from auth-api's public by-id lookup -- the same mechanism notifications-api's
			// own event-driven tenant resolver uses to sync a local projection. Best-effort:
			// a name-fetch failure still leaves slug resolution working, just blank display
			// name until the next self-heal below picks it up.
			name := s.fetchTenantName(ctx, tenantID)
			create := s.db.Tenant.Create().SetID(tenantID).SetSlug(slug)
			if name != "" {
				create.SetName(name)
			}
			_, _ = create.Save(ctx)
		case err == nil && existingTenant.Name == "":
			// Self-heal a tenant provisioned before this backfill existed (or whose earlier
			// name-fetch attempt failed) -- runs on every authenticated request per this
			// function's own doc comment, so this recovers on the next login either way.
			if name := s.fetchTenantName(ctx, tenantID); name != "" {
				_, _ = s.db.Tenant.UpdateOneID(tenantID).SetName(name).Save(ctx)
			}
		}
	}
	roles := MapGlobalRoles(claims.Roles)
	// If SSO scoped the user to a single outlet, link that to the matching library branch so
	// branch-scoped PIN login works without manual assignment ("sync from SSO outlet links").
	ssoBranchID := s.resolveSSOBranchID(ctx, tenantID, claims)

	existing, err := s.db.LibraryUser.Query().
		Where(libraryuser.TenantID(tenantID), libraryuser.UserID(claims.Subject)).
		Only(ctx)
	if ent.IsNotFound(err) {
		c := s.db.LibraryUser.Create().
			SetTenantID(tenantID).
			SetUserID(claims.Subject).
			SetEmail(claims.Email).
			SetRoles(roles)
		if ssoBranchID != "" {
			c.SetBranchIds([]string{ssoBranchID})
		}
		_, cerr := c.Save(ctx)
		return cerr
	} else if err != nil {
		return err
	}
	// Heal roles (merge mapped roles into any explicitly-granted ones).
	merged := mergeUnique(existing.Roles, roles)
	u := s.db.LibraryUser.UpdateOne(existing).SetRoles(merged).SetEmail(claims.Email)
	if ssoBranchID != "" {
		u.SetBranchIds(mergeUnique(existing.BranchIds, []string{ssoBranchID}))
	}
	_, err = u.Save(ctx)
	return err
}

// fetchTenantName resolves a tenant's display name via auth-api's public by-id lookup
// (GET /api/v1/tenants/by-id/{id}, no auth required -- same trust tier as the by-slug
// endpoint, safe fields only). Best-effort: any failure (network, non-200, bad JSON)
// returns "" rather than an error, since this only ever backfills a display-only field
// and must never block the login path it's called from.
func (s *Service) fetchTenantName(ctx context.Context, tenantID uuid.UUID) string {
	if s.authServiceURL == "" {
		return ""
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	url := strings.TrimRight(s.authServiceURL, "/") + "/api/v1/tenants/by-id/" + tenantID.String()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.log.Warn("fetchTenantName: request failed", zap.String("tenant_id", tenantID.String()), zap.Error(err))
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.log.Warn("fetchTenantName: non-200 response", zap.String("tenant_id", tenantID.String()), zap.Int("status", resp.StatusCode))
		return ""
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	return body.Name
}

// resolveSSOBranchID maps the SSO-selected outlet (claims OutletID/OutletCode) to a library
// Branch id for non-admin users, so an outlet linked in SSO scopes the user's library branch.
// Returns "" for admins/HQ users (unrestricted) or when no branch matches.
func (s *Service) resolveSSOBranchID(ctx context.Context, tenantID uuid.UUID, claims *authclient.Claims) string {
	if claims.CanAccessAllOutlets() {
		return ""
	}
	if oid := claims.GetOutletID(); oid != "" {
		if u, err := uuid.Parse(oid); err == nil {
			if b, err := s.db.Branch.Query().Where(branch.TenantID(tenantID), branch.OutletID(u)).First(ctx); err == nil {
				return b.ID.String()
			}
		}
	}
	if claims.OutletCode != "" {
		if b, err := s.db.Branch.Query().Where(branch.TenantID(tenantID), branch.Code(claims.OutletCode)).First(ctx); err == nil {
			return b.ID.String()
		}
	}
	return ""
}

// SyncUserFromEvent upserts the local LibraryUser from an auth.user.{created,updated,pin_set}
// NATS event, mapping global roles and reconciling the SSO-linked branch. Mirrors
// EnsureUserFromToken's upsert/merge logic but takes already-unpacked event fields instead of
// JWT claims, since the auth-events consumer has no JWT to read (see reference_library_pin_sso_gap
// for why library-api previously had no consumer for these events at all).
//
// globalRoles empty means the event carried no role info (e.g. a profile-only update) — roles
// are left untouched rather than downgraded. outletID follows auth-api's pointer convention:
// nil = no change, non-nil-empty = clear, non-nil-non-empty = set.
func (s *Service) SyncUserFromEvent(ctx context.Context, tenantID uuid.UUID, userID, email, displayName string, globalRoles []string, outletID *string) error {
	var roles []string
	if len(globalRoles) > 0 {
		roles = MapGlobalRoles(globalRoles)
	}

	var branchID string
	if outletID != nil && *outletID != "" {
		if oid, err := uuid.Parse(*outletID); err == nil {
			if b, berr := s.db.Branch.Query().Where(branch.TenantID(tenantID), branch.OutletID(oid)).First(ctx); berr == nil {
				branchID = b.ID.String()
			}
		}
	}

	existing, err := s.db.LibraryUser.Query().
		Where(libraryuser.TenantID(tenantID), libraryuser.UserID(userID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		c := s.db.LibraryUser.Create().SetTenantID(tenantID).SetUserID(userID)
		if email != "" {
			c.SetEmail(email)
		}
		if displayName != "" {
			c.SetDisplayName(displayName)
		}
		if len(roles) > 0 {
			c.SetRoles(roles)
		}
		if branchID != "" {
			c.SetBranchIds([]string{branchID})
		}
		// auth.user.created and auth.user.pin_set are independent NATS
		// subjects/durable consumers that can be processed concurrently (e.g.
		// back-to-back events for a tenant's owner/admin during registration),
		// both racing to create the same LibraryUser row on the (tenant_id,
		// user_id) unique index. Upsert so the loser of the race no-ops
		// instead of failing the whole handler on the constraint violation —
		// callers (handlePinSet) re-query afterward for the authoritative row.
		return c.
			OnConflictColumns(libraryuser.FieldTenantID, libraryuser.FieldUserID).
			DoNothing().
			Exec(ctx)
	} else if err != nil {
		return err
	}

	u := s.db.LibraryUser.UpdateOne(existing)
	if email != "" {
		u.SetEmail(email)
	}
	if displayName != "" {
		u.SetDisplayName(displayName)
	}
	if len(roles) > 0 {
		u.SetRoles(mergeUnique(existing.Roles, roles))
	}
	if outletID != nil {
		if *outletID == "" {
			u.SetBranchIds([]string{})
		} else if branchID != "" {
			u.SetBranchIds(mergeUnique(existing.BranchIds, []string{branchID}))
		}
	}
	_, err = u.Save(ctx)
	return err
}

// HasAnyPermission resolves the user's local role permissions and reports whether any of
// perms is granted (wildcard "*" grants all).
func (s *Service) HasAnyPermission(ctx context.Context, tenantID uuid.UUID, userID string, perms ...string) bool {
	u, err := s.db.LibraryUser.Query().
		Where(libraryuser.TenantID(tenantID), libraryuser.UserID(userID)).
		Only(ctx)
	if err != nil || len(u.Roles) == 0 {
		return false
	}
	roleDefs, err := s.db.LibraryRole.Query().Where(libraryrole.NameIn(u.Roles...), visibleTo(tenantID)).All(ctx)
	if err != nil {
		return false
	}
	granted := map[string]bool{}
	for _, rd := range roleDefs {
		for _, p := range rd.Permissions {
			if p == "*" {
				return true
			}
			granted[p] = true
		}
	}
	for _, want := range perms {
		if granted[want] {
			return true
		}
	}
	return false
}

// ListPermissions returns the user's effective local permission codes (flattened from
// their assigned roles; a wildcard role yields just ["*"]).
func (s *Service) ListPermissions(ctx context.Context, tenantID uuid.UUID, userID string) []string {
	u, err := s.db.LibraryUser.Query().
		Where(libraryuser.TenantID(tenantID), libraryuser.UserID(userID)).
		Only(ctx)
	if err != nil || len(u.Roles) == 0 {
		return nil
	}
	roleDefs, err := s.db.LibraryRole.Query().Where(libraryrole.NameIn(u.Roles...), visibleTo(tenantID)).All(ctx)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, rd := range roleDefs {
		for _, p := range rd.Permissions {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// visibleTo limits a role query to the roles a tenant can see: system roles and legacy custom
// roles (no tenant) plus the tenant's own custom roles. Another tenant's custom role never
// matches, so it can neither be read nor resolve permissions here.
func visibleTo(tenantID uuid.UUID) predicate.LibraryRole {
	return libraryrole.Or(libraryrole.TenantIDIsNil(), libraryrole.TenantIDEQ(tenantID))
}

// IsLegacyRole reports whether a role is a custom role created before roles were tenant scoped.
// Its tenant is unknown, so only a platform owner may edit or assign it.
func IsLegacyRole(r *ent.LibraryRole) bool {
	return r != nil && !r.IsSystem && r.TenantID == nil
}

// Actor is the caller of a role management call.
type Actor struct {
	TenantID      uuid.UUID
	UserID        string
	PlatformOwner bool
	// ClaimPermissions are the permission codes carried on the caller's token.
	ClaimPermissions []string
}

// Role management errors. The handler maps each one to its HTTP status.
var (
	ErrRoleNotFound       = stringError("role not found")
	ErrSystemRoleReadOnly = stringError("system roles are read only")
	ErrLegacyRoleLocked   = stringError("this role predates tenant scoping and only a platform owner may change it")
	ErrRoleNameTaken      = stringError("A role with this name already exists in your organisation.")
	ErrAdminRequired      = stringError("only a library admin or platform owner may manage or assign roles")
)

// ErrPermissionNotHeld is returned when a caller tries to put a permission on a role that the
// caller does not hold.
type ErrPermissionNotHeld struct{ Permission string }

func (e ErrPermissionNotHeld) Error() string {
	return "you cannot grant a permission you do not hold: " + e.Permission
}

type stringError string

func (e stringError) Error() string { return string(e) }

// IsLibraryAdmin reports whether the user holds library_admin in the tenant.
func (s *Service) IsLibraryAdmin(ctx context.Context, tenantID uuid.UUID, userID string) bool {
	if s == nil || s.db == nil || userID == "" {
		return false
	}
	u, err := s.db.LibraryUser.Query().
		Where(libraryuser.TenantID(tenantID), libraryuser.UserID(userID)).
		Only(ctx)
	if err != nil {
		return false
	}
	for _, r := range u.Roles {
		if r == RoleAdmin {
			return true
		}
	}
	return false
}

// isAdminActor reports whether the actor may manage roles: a real platform owner or a
// library_admin of the tenant.
func (s *Service) isAdminActor(ctx context.Context, a Actor) bool {
	return a.PlatformOwner || s.IsLibraryAdmin(ctx, a.TenantID, a.UserID)
}

// checkGrantable refuses any permission the actor does not hold. "*" is only grantable by a
// caller holding "*"; a platform owner holds everything.
func (s *Service) checkGrantable(ctx context.Context, a Actor, permissions []string) error {
	if a.PlatformOwner {
		return nil
	}
	held := map[string]bool{}
	for _, p := range a.ClaimPermissions {
		held[p] = true
	}
	for _, p := range s.ListPermissions(ctx, a.TenantID, a.UserID) {
		held[p] = true
	}
	if held["*"] {
		return nil
	}
	for _, p := range permissions {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !held[p] {
			return ErrPermissionNotHeld{Permission: p}
		}
	}
	return nil
}

// ListRoles returns the roles the tenant can see: system roles, legacy custom roles and the
// tenant's own custom roles.
func (s *Service) ListRoles(ctx context.Context, tenantID uuid.UUID) ([]*ent.LibraryRole, error) {
	return s.db.LibraryRole.Query().Where(visibleTo(tenantID)).All(ctx)
}

// GetRole returns a role the tenant can see. Another tenant's custom role is ErrRoleNotFound.
func (s *Service) GetRole(ctx context.Context, tenantID, id uuid.UUID) (*ent.LibraryRole, error) {
	role, err := s.db.LibraryRole.Query().Where(libraryrole.IDEQ(id), visibleTo(tenantID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrRoleNotFound
	}
	return role, err
}

// CreateRole creates a custom role owned by the actor's tenant. A reserved platform role name is
// refused, a seeded system role name is read only, and a name the tenant can already see is
// taken. Every permission must be one the actor holds, and the actor must be admin level.
func (s *Service) CreateRole(ctx context.Context, a Actor, name, description string, permissions []string) (*ent.LibraryRole, error) {
	name = strings.TrimSpace(name)
	if IsReservedRole(name) {
		return nil, ErrRoleNotAssignable{Role: name}
	}
	if IsSystemRoleName(name) {
		return nil, ErrSystemRoleReadOnly
	}
	if a.TenantID == uuid.Nil {
		return nil, ErrRoleNotFound
	}
	if err := s.checkGrantable(ctx, a, permissions); err != nil {
		return nil, err
	}
	if !s.isAdminActor(ctx, a) {
		return nil, ErrAdminRequired
	}
	taken, err := s.db.LibraryRole.Query().Where(libraryrole.Name(name), visibleTo(a.TenantID)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrRoleNameTaken
	}
	row, err := s.db.LibraryRole.Create().
		SetName(name).SetDescription(description).SetPermissions(permissions).
		SetIsSystem(false).SetTenantID(a.TenantID).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// A concurrent create of the same name in the tenant. Never echo the database text.
		return nil, ErrRoleNameTaken
	}
	return row, err
}

// editableRole loads a role for a change by the actor. System roles are read only, legacy roles
// are for a platform owner only, and another tenant's role is not found.
func (s *Service) editableRole(ctx context.Context, a Actor, id uuid.UUID) (*ent.LibraryRole, error) {
	role, err := s.GetRole(ctx, a.TenantID, id)
	if err != nil {
		return nil, err
	}
	if role.IsSystem || (role.TenantID == nil && systemRoleNames[role.Name]) {
		return nil, ErrSystemRoleReadOnly
	}
	if IsLegacyRole(role) && !a.PlatformOwner {
		return nil, ErrLegacyRoleLocked
	}
	return role, nil
}

// UpdateRolePermissions replaces a custom role's permission set (and optionally its
// description). Every permission must be one the actor holds, and the actor must be admin level.
func (s *Service) UpdateRolePermissions(ctx context.Context, a Actor, id uuid.UUID, permissions []string, description string) (*ent.LibraryRole, error) {
	role, err := s.editableRole(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if err := s.checkGrantable(ctx, a, permissions); err != nil {
		return nil, err
	}
	if !s.isAdminActor(ctx, a) {
		return nil, ErrAdminRequired
	}
	u := s.db.LibraryRole.UpdateOne(role).SetPermissions(permissions)
	if description != "" {
		u.SetDescription(description)
	}
	row, err := u.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, ErrRoleNameTaken
	}
	return row, err
}

// DeleteRole removes a custom role of the actor's tenant (or a legacy role, platform owner only).
func (s *Service) DeleteRole(ctx context.Context, a Actor, id uuid.UUID) error {
	role, err := s.editableRole(ctx, a, id)
	if err != nil {
		return err
	}
	if !s.isAdminActor(ctx, a) {
		return ErrAdminRequired
	}
	return s.db.LibraryRole.DeleteOne(role).Exec(ctx)
}

// ListUsers returns the tenant's provisioned library users (the team).
func (s *Service) ListUsers(ctx context.Context, tenantID uuid.UUID) ([]*ent.LibraryUser, error) {
	return s.db.LibraryUser.Query().Where(libraryuser.TenantID(tenantID)).All(ctx)
}

// reservedRoleNames are platform or auth-level role names. They are never a library role, so
// they can never be assigned to a library user, created as a custom role or carried on a PIN
// token. A "superuser" stored on a LibraryUser used to ride into the PIN token, where
// claims.IsSuperuser() bypassed every permission check and passed RequirePlatformOwner.
var reservedRoleNames = map[string]bool{
	"superuser": true, "superusers": true, "super_admin": true, "superadmin": true,
	"owner": true, "platform_owner": true, "platform_admin": true, "platform": true,
	"admin": true, "administrator": true, "system_admin": true, "tenant_admin": true, "root": true,
}

// systemRoleNames are the seeded library roles (see SeedGlobalRoles). Always assignable.
var systemRoleNames = map[string]bool{RoleAdmin: true, RoleStaff: true, RoleMember: true}

// IsSystemRoleName reports whether name is a seeded system role name (compared lowercase and
// trimmed), so a custom role cannot be "Library_Admin" either.
func IsSystemRoleName(name string) bool {
	return systemRoleNames[strings.ToLower(strings.TrimSpace(name))]
}

// IsReservedRole reports whether name is a platform or auth-level role that must never be
// granted through library-api (compared lowercase and trimmed).
func IsReservedRole(name string) bool {
	return reservedRoleNames[strings.ToLower(strings.TrimSpace(name))]
}

// ErrRoleNotAssignable is returned when a role is reserved or not a defined library role.
type ErrRoleNotAssignable struct{ Role string }

func (e ErrRoleNotAssignable) Error() string {
	return "role " + strings.TrimSpace(e.Role) + " cannot be assigned"
}

// AssignableRoles returns the roles from the list that are defined library roles the tenant can
// see (the seeded system roles, a legacy custom role or the tenant's own custom role) and not
// reserved, plus the ones it dropped. Another tenant's custom role is dropped. When the role
// table cannot be read, only the seeded system roles pass.
func (s *Service) AssignableRoles(ctx context.Context, tenantID uuid.UUID, roles []string) (allowed, rejected []string) {
	candidates := []string{}
	for _, r := range roles {
		name := strings.TrimSpace(r)
		if name == "" {
			continue
		}
		if IsReservedRole(name) {
			rejected = append(rejected, r)
			continue
		}
		candidates = append(candidates, name)
	}
	defined := map[string]bool{}
	if len(candidates) > 0 && s.db != nil {
		if rows, err := s.db.LibraryRole.Query().Where(libraryrole.NameIn(candidates...), visibleTo(tenantID)).All(ctx); err == nil {
			for _, row := range rows {
				defined[row.Name] = true
			}
		}
	}
	for _, name := range candidates {
		if systemRoleNames[name] || defined[name] {
			allowed = append(allowed, name)
		} else {
			rejected = append(rejected, name)
		}
	}
	return mergeUnique(allowed, nil), rejected
}

// AssignRoles sets a user's roles in the actor's tenant (replacing the current set). Every role
// must be a library role the tenant can see; reserved platform roles such as superuser are
// refused before anything is read. Only a library admin or platform owner may assign roles,
// library_admin needs an admin caller, and a legacy custom role the user does not already hold
// can only be added by a platform owner.
func (s *Service) AssignRoles(ctx context.Context, a Actor, userID string, roles []string) error {
	for _, r := range roles {
		if IsReservedRole(r) {
			return ErrRoleNotAssignable{Role: r}
		}
	}
	allowed, rejected := s.AssignableRoles(ctx, a.TenantID, roles)
	if len(rejected) > 0 {
		return ErrRoleNotAssignable{Role: rejected[0]}
	}
	if !s.isAdminActor(ctx, a) {
		return ErrAdminRequired
	}
	u, err := s.db.LibraryUser.Query().Where(libraryuser.TenantID(a.TenantID), libraryuser.UserID(userID)).Only(ctx)
	if err != nil {
		return err
	}
	if !a.PlatformOwner {
		held := map[string]bool{}
		for _, r := range u.Roles {
			held[r] = true
		}
		var added []string
		for _, r := range allowed {
			if !held[r] && !systemRoleNames[r] {
				added = append(added, r)
			}
		}
		if len(added) > 0 {
			rows, err := s.db.LibraryRole.Query().Where(libraryrole.NameIn(added...), libraryrole.TenantIDIsNil()).All(ctx)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if IsLegacyRole(row) {
					return ErrLegacyRoleLocked
				}
			}
		}
	}
	_, err = s.db.LibraryUser.UpdateOne(u).SetRoles(allowed).Save(ctx)
	return err
}

// AssignBranches sets the branches a user may log in to (replacing the current set). An empty
// list clears the restriction (the user then has no branch unless they're an admin).
func (s *Service) AssignBranches(ctx context.Context, tenantID uuid.UUID, userID string, branchIDs []string) error {
	u, err := s.db.LibraryUser.Query().Where(libraryuser.TenantID(tenantID), libraryuser.UserID(userID)).Only(ctx)
	if err != nil {
		return err
	}
	_, err = s.db.LibraryUser.UpdateOne(u).SetBranchIds(branchIDs).Save(ctx)
	return err
}

// PermissionCatalog is the full Django-style list of library.{module}.{action} codes the team
// matrix renders (grouped by module in the UI). Keep in sync with the role grants + route guards.
func PermissionCatalog() []map[string]string {
	type spec struct{ code, label string }
	specs := []spec{}
	addCrud := func(mod, label string) {
		specs = append(specs,
			spec{"library." + mod + ".view", "View " + label},
			spec{"library." + mod + ".add", "Add " + label},
			spec{"library." + mod + ".change", "Edit " + label},
			spec{"library." + mod + ".delete", "Delete " + label},
			spec{"library." + mod + ".manage", "Manage " + label},
		)
	}
	addCrud("catalog", "catalog")
	addCrud("copies", "copies")
	specs = append(specs, spec{"library.copies.hard_delete", "Permanently delete withdrawn copies (irreversible)"})
	addCrud("collections", "collections")
	addCrud("members", "members")
	addCrud("member_tiers", "member tiers")
	addCrud("loan_policies", "loan policies")
	addCrud("branches", "branches")
	addCrud("ebooks", "e-books")
	specs = append(specs,
		spec{"library.ebooks.lend", "Lend e-books (CDL)"},
		spec{"library.circulation.view", "View circulation"},
		spec{"library.circulation.checkout", "Check out"},
		spec{"library.circulation.return", "Check in / return"},
		spec{"library.circulation.renew", "Renew loans"},
		spec{"library.holds.view", "View holds"},
		spec{"library.holds.place", "Place holds"},
		spec{"library.holds.change", "Edit holds"},
		spec{"library.holds.delete", "Cancel holds"},
		spec{"library.holds.manage", "Manage holds"},
		spec{"library.fines.view", "View fines"},
		spec{"library.fines.assess", "Assess fines"},
		spec{"library.fines.waive", "Waive fines"},
		spec{"library.fines.pay", "Take fine payments"},
		spec{"library.transfers.view", "View transfers"},
		spec{"library.transfers.add", "Create transfers"},
		spec{"library.transfers.receive", "Receive transfers"},
		spec{"library.transfers.manage", "Manage transfers"},
		spec{"library.stocktake.view", "View stocktake"},
		spec{"library.stocktake.add", "Start stocktake"},
		spec{"library.stocktake.scan", "Scan stocktake"},
		spec{"library.stocktake.finalize", "Finalize stocktake"},
		spec{"library.stocktake.manage", "Manage stocktake"},
		spec{"library.membership_fees.view", "View membership fees"},
		spec{"library.membership_fees.add", "Charge membership fees"},
		spec{"library.membership_fees.pay", "Take membership fee payments"},
		spec{"library.reports.view", "View reports"},
		spec{"library.team.view", "View team"},
		spec{"library.team.manage", "Manage team & roles"},
		spec{"library.settings.manage", "Manage settings"},
	)
	out := make([]map[string]string, 0, len(specs))
	for _, c := range specs {
		out = append(out, map[string]string{"code": c.code, "label": c.label})
	}
	return out
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range append(append([]string{}, a...), b...) {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
