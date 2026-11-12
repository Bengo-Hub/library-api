package events

import (
	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
)

// WithMember adds to an event payload who the event is about, for notifications-api.
//
// A member linked to the CRM is named by crm_contact_id alone: notifications-api fetches the
// name and email from the CRM when it sends, so no personal data travels on the bus. A member
// with no CRM link (the CRM was down or refused the contact, or the member predates the link)
// keeps email and name in the payload, which is how their notices still reach them. That
// fallback stays until every member is linked.
//
// The payload is changed in place and returned. A nil member leaves it as it is.
func WithMember(payload map[string]any, m *ent.Member) map[string]any {
	if m == nil {
		return payload
	}
	if m.CrmContactID != nil && *m.CrmContactID != uuid.Nil {
		payload["crm_contact_id"] = m.CrmContactID.String()
		return payload
	}
	payload["email"] = m.ContactEmail
	payload["name"] = m.DisplayName
	return payload
}
