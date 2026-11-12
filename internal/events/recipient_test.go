package events

import (
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
)

// A member linked to the CRM is named by crm_contact_id alone: notifications-api looks up the
// name and email from the CRM at send time, so the event carries no personal data.
func TestWithMemberLinkedSendsOnlyTheContactID(t *testing.T) {
	contactID := uuid.New()
	m := &ent.Member{ID: uuid.New(), CrmContactID: &contactID, ContactEmail: "mary@example.com", ContactPhone: "+254712345678", DisplayName: "Mary"}

	p := WithMember(map[string]any{"member_id": m.ID}, m)
	if p["crm_contact_id"] != contactID.String() {
		t.Fatalf("crm_contact_id = %v, want %s", p["crm_contact_id"], contactID)
	}
	for _, k := range []string{"email", "name", "phone", "display_name"} {
		if _, ok := p[k]; ok {
			t.Errorf("linked member payload must not carry %q", k)
		}
	}
	if p["member_id"] != m.ID {
		t.Error("ids must be kept")
	}
}

// An unlinked member (CRM down, refused, or never linked) keeps email and name so the notice
// still reaches them.
func TestWithMemberUnlinkedKeepsEmailAndName(t *testing.T) {
	m := &ent.Member{ID: uuid.New(), ContactEmail: "mary@example.com", DisplayName: "Mary"}

	p := WithMember(map[string]any{"member_id": m.ID}, m)
	if p["email"] != "mary@example.com" || p["name"] != "Mary" {
		t.Fatalf("got %v, want email and name", p)
	}
	if _, ok := p["crm_contact_id"]; ok {
		t.Error("unlinked member has no crm_contact_id")
	}
}

func TestWithMemberNilLeavesPayload(t *testing.T) {
	p := WithMember(map[string]any{"member_id": "m1"}, nil)
	if len(p) != 1 {
		t.Fatalf("got %v", p)
	}
	nilID := uuid.Nil
	p = WithMember(map[string]any{}, &ent.Member{CrmContactID: &nilID, ContactEmail: "a@example.com"})
	if p["email"] != "a@example.com" {
		t.Fatalf("a nil contact id counts as unlinked: %v", p)
	}
}
