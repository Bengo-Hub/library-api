package refdata

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/ent/tenant"
)

// DemoTenantSlug is the sandbox tenant that gets seeded demo desk PINs (never a real tenant).
const DemoTenantSlug = "codevertex-demo"

type demoStaff struct {
	UserID string
	Name   string
	Role   string
	PIN    string
}

// demoStaffMembers are the desk-PIN logins surfaced on the demo pin-login card. PINs are stable
// so the UI hint card can list them. Roles span admin/staff/member to demo branch scoping.
var demoStaffMembers = []demoStaff{
	{"demo-librarian", "Demo Librarian", "library_admin", "1111"},
	{"demo-desk", "Demo Desk Assistant", "library_staff", "2222"},
	{"demo-member", "Demo Member", "library_member", "3333"},
}

// SeedDemoStaff idempotently provisions demo desk-PIN staff for the codevertex-demo tenant so the
// PIN-login screen has working demo logins. No-op for any other tenant / when the demo tenant row
// isn't cached yet (it gets seeded on a later boot after the first SSO login).
func SeedDemoStaff(ctx context.Context, client *ent.Client, log *zap.Logger) error {
	t, err := client.Tenant.Query().Where(tenant.Slug(DemoTenantSlug)).First(ctx)
	if err != nil {
		return nil // demo tenant not present locally yet — skip silently
	}
	// Ensure a branch exists so non-admin demo staff have somewhere to log in.
	br, err := client.Branch.Query().Where(branch.TenantID(t.ID), branch.IsActive(true)).First(ctx)
	if err != nil {
		br, err = client.Branch.Create().
			SetTenantID(t.ID).SetName("Main Library").SetCode("HQ").SetIsDefault(true).SetIsActive(true).Save(ctx)
		if err != nil {
			log.Warn("demo seed: create branch failed", zap.Error(err))
			return nil
		}
	}

	for _, d := range demoStaffMembers {
		hash, herr := bcrypt.GenerateFromPassword([]byte(d.PIN), bcrypt.DefaultCost)
		if herr != nil {
			continue
		}
		existing, qerr := client.LibraryUser.Query().
			Where(libraryuser.TenantID(t.ID), libraryuser.UserID(d.UserID)).First(ctx)
		if ent.IsNotFound(qerr) {
			_, _ = client.LibraryUser.Create().
				SetTenantID(t.ID).SetUserID(d.UserID).SetDisplayName(d.Name).
				SetRoles([]string{d.Role}).SetBranchIds([]string{br.ID.String()}).
				SetIsActive(true).SetPinHash(string(hash)).Save(ctx)
			continue
		} else if qerr != nil {
			continue
		}
		// Heal: keep PIN + branch assignment current (idempotent).
		_, _ = client.LibraryUser.UpdateOne(existing).
			SetDisplayName(d.Name).SetRoles([]string{d.Role}).
			SetBranchIds([]string{br.ID.String()}).SetIsActive(true).SetPinHash(string(hash)).Save(ctx)
	}
	log.Info("demo seed: desk PINs ensured for codevertex-demo")
	return nil
}
