package refdata

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/ent/tenant"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// demoPatron is a standalone demo library patron (not tied to a staff PIN account).
type demoPatron struct {
	name, email, phone, tier, status string
}

var demoPatrons = []demoPatron{
	{"Wanjiku Kamau", "wanjiku.kamau@example.co.ke", "+254712000001", "Standard", "ACTIVE"},
	{"Brian Otieno", "brian.otieno@example.co.ke", "+254712000002", "Student", "ACTIVE"},
	{"Amina Hassan", "amina.hassan@example.co.ke", "+254712000003", "Senior Citizen", "ACTIVE"},
	{"David Mwangi", "david.mwangi@example.co.ke", "+254712000004", "Standard", "SUSPENDED"},
}

// SeedDemoMembers idempotently provisions demo library patrons for the codevertex-demo tenant so
// the Members list isn't empty: a Member (patron) record for every LibraryUser holding the
// library_member role (staff who are also patrons), plus a handful of standalone patrons. Numbers
// are allocated through the document-sequence so they match the configured format. No-op elsewhere.
func SeedDemoMembers(ctx context.Context, client *ent.Client, log *zap.Logger) error {
	t, err := client.Tenant.Query().Where(tenant.Slug(DemoTenantSlug)).First(ctx)
	if err != nil {
		return nil
	}
	// Default tier (tenant default → global default → any).
	tierID, ok := defaultTierID(ctx, client, t.ID)
	if !ok {
		log.Warn("demo seed members: no tier available yet — skipping")
		return nil
	}
	tierByName := map[string]string{}
	tiers, _ := client.MemberTier.Query().
		Where(membertier.Or(membertier.TenantID(t.ID), membertier.TenantID(GlobalTenantID))).All(ctx)
	for _, tr := range tiers {
		tierByName[tr.Name] = tr.ID.String()
	}

	// LibraryUsers with the member role → patron records.
	memberUsers, _ := client.LibraryUser.Query().Where(libraryuser.TenantID(t.ID)).All(ctx)
	created := 0
	ensure := func(name, email, phone, tierName, status string) {
		if name == "" {
			return
		}
		exists, _ := client.Member.Query().
			Where(member.TenantID(t.ID), member.DisplayName(name)).Exist(ctx)
		if exists {
			return
		}
		tid := tierID
		if id, ok := tierByName[tierName]; ok {
			if parsed, perr := uuid.Parse(id); perr == nil {
				tid = parsed
			}
		}
		no, nerr := allocMemberNo(ctx, client, t.ID)
		if nerr != nil {
			log.Warn("demo seed members: alloc number failed", zap.Error(nerr))
			return
		}
		c := client.Member.Create().
			SetTenantID(t.ID).SetMembershipNo(no).SetTierID(tid).
			SetDisplayName(name).SetStatus(member.Status(status))
		if email != "" {
			c.SetContactEmail(email)
		}
		if phone != "" {
			c.SetContactPhone(phone)
		}
		if _, err := c.Save(ctx); err != nil {
			log.Warn("demo seed members: create failed", zap.String("name", name), zap.Error(err))
			return
		}
		created++
	}

	for _, u := range memberUsers {
		if hasRole(u.Roles, "library_member") {
			ensure(u.DisplayName, "", "", "Standard", "ACTIVE")
		}
	}
	for _, p := range demoPatrons {
		ensure(p.name, p.email, p.phone, p.tier, p.status)
	}
	if created > 0 {
		log.Info("demo seed members: patrons ensured", zap.Int("created", created))
	}
	return nil
}

// defaultTierID resolves the tenant's default tier → global default → any tier.
func defaultTierID(ctx context.Context, client *ent.Client, tenantID uuid.UUID) (uuid.UUID, bool) {
	if t, err := client.MemberTier.Query().Where(membertier.TenantID(tenantID), membertier.IsDefault(true)).First(ctx); err == nil {
		return t.ID, true
	}
	if t, err := client.MemberTier.Query().Where(membertier.TenantID(GlobalTenantID), membertier.IsDefault(true)).First(ctx); err == nil {
		return t.ID, true
	}
	if t, err := client.MemberTier.Query().
		Where(membertier.Or(membertier.TenantID(tenantID), membertier.TenantID(GlobalTenantID))).First(ctx); err == nil {
		return t.ID, true
	}
	return uuid.UUID{}, false
}

// allocMemberNo allocates a membership number through the document-sequence (matching configured format).
func allocMemberNo(ctx context.Context, client *ent.Client, tenantID uuid.UUID) (string, error) {
	tx, err := client.Tx(ctx)
	if err != nil {
		return "", err
	}
	no, err := sequence.Next(ctx, tx, tenantID, sequence.KindMembership, "MBR", 5)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return no, nil
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if strings.EqualFold(r, want) {
			return true
		}
	}
	return false
}
