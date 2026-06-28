// Package rbac owns library-service roles + permissions (the service is the source of
// truth for its own library.{module}.{action} codes per reference_service_rbac_authme_sync).
// Effective permissions = SSO JWT ∪ this local RBAC. JIT provisioning heals EXISTING
// users on every request (treasury #30 gotcha), not only on first creation.
package rbac

import (
	"context"
	"strings"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/libraryrole"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
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
	db  *ent.Client
	log *zap.Logger
}

// NewService creates the RBAC service.
func NewService(db *ent.Client, log *zap.Logger) *Service {
	return &Service{db: db, log: log}
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
		existing, err := s.db.LibraryRole.Query().Where(libraryrole.Name(d.name)).Only(ctx)
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
		// Heal permission drift on existing system roles.
		if _, err := s.db.LibraryRole.UpdateOne(existing).SetPermissions(d.perms).SetDescription(d.desc).Save(ctx); err != nil {
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
	// Cache the tenant slug→id mapping locally so the PUBLIC PIN-login routes (which have no
	// JWT) can resolve the org slug. Best-effort, get-or-create by the auth tenant UUID.
	if slug := claims.GetTenantSlug(); slug != "" {
		if exists, _ := s.db.Tenant.Query().Where(tenant.IDEQ(tenantID)).Exist(ctx); !exists {
			_, _ = s.db.Tenant.Create().SetID(tenantID).SetSlug(slug).Save(ctx)
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

// HasAnyPermission resolves the user's local role permissions and reports whether any of
// perms is granted (wildcard "*" grants all).
func (s *Service) HasAnyPermission(ctx context.Context, tenantID uuid.UUID, userID string, perms ...string) bool {
	u, err := s.db.LibraryUser.Query().
		Where(libraryuser.TenantID(tenantID), libraryuser.UserID(userID)).
		Only(ctx)
	if err != nil || len(u.Roles) == 0 {
		return false
	}
	roleDefs, err := s.db.LibraryRole.Query().Where(libraryrole.NameIn(u.Roles...)).All(ctx)
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
	roleDefs, err := s.db.LibraryRole.Query().Where(libraryrole.NameIn(u.Roles...)).All(ctx)
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

// ListRoles returns all global library roles.
func (s *Service) ListRoles(ctx context.Context) ([]*ent.LibraryRole, error) {
	return s.db.LibraryRole.Query().All(ctx)
}

// CreateRole creates a custom (non-system) role.
func (s *Service) CreateRole(ctx context.Context, name, description string, permissions []string) (*ent.LibraryRole, error) {
	return s.db.LibraryRole.Create().
		SetName(name).SetDescription(description).SetPermissions(permissions).SetIsSystem(false).
		Save(ctx)
}

// UpdateRolePermissions replaces a role's permission set (and optionally its description).
func (s *Service) UpdateRolePermissions(ctx context.Context, id uuid.UUID, permissions []string, description string) (*ent.LibraryRole, error) {
	u := s.db.LibraryRole.UpdateOneID(id).SetPermissions(permissions)
	if description != "" {
		u.SetDescription(description)
	}
	return u.Save(ctx)
}

// DeleteRole removes a custom role (system roles are protected).
func (s *Service) DeleteRole(ctx context.Context, id uuid.UUID) error {
	role, err := s.db.LibraryRole.Get(ctx, id)
	if err != nil {
		return err
	}
	if role.IsSystem {
		return errSystemRoleLocked
	}
	return s.db.LibraryRole.DeleteOneID(id).Exec(ctx)
}

var errSystemRoleLocked = stringError("system roles cannot be deleted")

type stringError string

func (e stringError) Error() string { return string(e) }

// ListUsers returns the tenant's provisioned library users (the team).
func (s *Service) ListUsers(ctx context.Context, tenantID uuid.UUID) ([]*ent.LibraryUser, error) {
	return s.db.LibraryUser.Query().Where(libraryuser.TenantID(tenantID)).All(ctx)
}

// AssignRoles sets a user's roles (replacing the current set).
func (s *Service) AssignRoles(ctx context.Context, tenantID uuid.UUID, userID string, roles []string) error {
	u, err := s.db.LibraryUser.Query().Where(libraryuser.TenantID(tenantID), libraryuser.UserID(userID)).Only(ctx)
	if err != nil {
		return err
	}
	_, err = s.db.LibraryUser.UpdateOne(u).SetRoles(roles).Save(ctx)
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
