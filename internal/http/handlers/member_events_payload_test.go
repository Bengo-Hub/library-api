package handlers

import (
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
)

// library.member.registered for a member linked to the CRM carries ids only.
func TestMemberRegisteredPayloadLinkedHasNoPersonalData(t *testing.T) {
	contactID := uuid.New()
	m := &ent.Member{ID: uuid.New(), MembershipNo: "LIB-1", CrmContactID: &contactID, DisplayName: "Mary", ContactEmail: "mary@example.com"}

	p := memberRegisteredPayload(m)
	if p["crm_contact_id"] != contactID.String() || p["membership_no"] != "LIB-1" || p["member_id"] != m.ID {
		t.Fatalf("ids missing: %v", p)
	}
	for _, k := range []string{"email", "name", "display_name", "phone"} {
		if _, ok := p[k]; ok {
			t.Errorf("linked member payload must not carry %q", k)
		}
	}
}

// An unlinked member keeps the name and email the welcome notice needs.
func TestMemberRegisteredPayloadUnlinkedKeepsFields(t *testing.T) {
	m := &ent.Member{ID: uuid.New(), MembershipNo: "LIB-2", DisplayName: "Mary", ContactEmail: "mary@example.com"}

	p := memberRegisteredPayload(m)
	if p["email"] != "mary@example.com" || p["name"] != "Mary" || p["display_name"] != "Mary" {
		t.Fatalf("got %v, want email, name and display_name", p)
	}
	if _, ok := p["crm_contact_id"]; ok {
		t.Error("unlinked member has no crm_contact_id")
	}
}
