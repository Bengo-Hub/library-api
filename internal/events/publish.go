// Package events provides the transactional-outbox publish helper. Publishing = inserting
// an outbox_events row (within the domain's Ent transaction); the shared-events OutboxPoller
// wired in app.go drains it to NATS. Subject = {aggregate_type}.{event_type}; aggregate_type
// for this service is always "library".
package events

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
)

// AggregateType is the constant aggregate_type for every library event.
const AggregateType = "library"

// Library event types (the {event_type} half of the subject).
const (
	EventMemberRegistered = "member.registered"
	EventLoanCreated      = "loan.created"
	EventLoanRenewed      = "loan.renewed"
	EventLoanReturned     = "loan.returned"
	EventLoanOverdue      = "loan.overdue"
	EventHoldReady        = "hold.ready"
	EventFineAssessed     = "fine.assessed"
	EventFinePaid         = "fine.paid"
	EventEbookLoaned      = "ebook.loaned"
	EventEbookExpired     = "ebook.expired"
	EventMembershipFeeDue = "membership.fee_due"
	EventBibCreated       = "bib.created"
	EventBranchCreated    = "branch.created"
	EventHoldExpired      = "hold.expired"
	EventLoanRecalled     = "loan.recalled"
	EventMemberExpired    = "member.expired"
	EventMemberGraduated  = "member.graduated"
)

// Publisher inserts outbox rows. oc is either client.OutboxEvent or tx.OutboxEvent.
type Publisher interface {
	Create() *ent.OutboxEventCreate
}

// Publish writes one outbox event row. Pass tx.OutboxEvent to publish atomically with the
// domain write, or client.OutboxEvent for a standalone publish.
func Publish(ctx context.Context, oc Publisher, tenantID uuid.UUID, aggregateID, eventType string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = oc.Create().
		SetTenantID(tenantID).
		SetAggregateType(AggregateType).
		SetAggregateID(aggregateID).
		SetEventType(eventType).
		SetPayload(json.RawMessage(b)).
		Save(ctx)
	return err
}
