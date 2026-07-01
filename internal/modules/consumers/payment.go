// Package consumers holds the library NATS/JetStream event consumers. PaymentConsumer
// reconciles treasury payments back to the originating fine/fee/purchase: on
// treasury.payment.succeeded it flips the matching record to PAID (idempotent on the
// treasury intent id).
package consumers

import (
	"context"
	"encoding/json"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/acquisitioninvoice"
	"github.com/bengobox/library-service/internal/ent/ebookpurchase"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membershipfee"
	"github.com/bengobox/library-service/internal/events"
)

// PaymentConsumer reconciles treasury payment events.
type PaymentConsumer struct {
	db  *ent.Client
	log *zap.Logger
}

// NewPaymentConsumer builds the payment consumer.
func NewPaymentConsumer(db *ent.Client, log *zap.Logger) *PaymentConsumer {
	return &PaymentConsumer{db: db, log: log}
}

// envelope is the shared-events wire envelope (a subset).
type envelope struct {
	TenantID  string          `json:"tenant_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

// paymentPayload is the treasury payment payload (best-effort, tolerant of extra fields).
type paymentPayload struct {
	ReferenceType string         `json:"reference_type"`
	ReferenceID   string         `json:"reference_id"`
	IntentID      string         `json:"intent_id"`
	ID            string         `json:"id"`
	Status        string         `json:"status"`
	Metadata      map[string]any `json:"metadata"`
}

// entityIDFromReference recovers the originating record's UUID for the by-reference
// fallback path. Modern references are prefixed (LIB-{slug}-{hex}, not a parseable
// UUID), so the entity UUID travels in metadata.entity_id; legacy references were the
// bare entity UUID. Returns "" when neither yields a UUID (then we rely on intent_id).
func entityIDFromReference(referenceID string, meta map[string]any) string {
	if meta != nil {
		if v, ok := meta["entity_id"].(string); ok {
			if _, err := uuid.Parse(v); err == nil {
				return v
			}
		}
	}
	if _, err := uuid.Parse(referenceID); err == nil {
		return referenceID // legacy: reference_id WAS the entity UUID
	}
	return ""
}

// Start subscribes to treasury.payment.succeeded as a durable deliver-group consumer so
// multiple replicas share the work without double-processing or "already bound" conflicts.
// Uses the shared-events canonical primitive (queue == durable name) which adds the
// one-time self-heal of any stale non-deliver-group durable plus rebind resilience.
func (c *PaymentConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"treasury",
		"treasury.payment.succeeded",
		"library-payment-reconcile",
		func(m *nats.Msg) { c.handle(ctx, m) },
		nats.Durable("library-payment-reconcile"),
		nats.ManualAck(),
		nats.AckWait(30*time.Second),
		nats.DeliverAll(),
	)
	return nil
}

func (c *PaymentConsumer) handle(ctx context.Context, m *nats.Msg) {
	defer func() { _ = m.Ack() }()

	var env envelope
	if err := json.Unmarshal(m.Data, &env); err != nil {
		c.log.Warn("payment event: bad envelope", zap.Error(err))
		return
	}
	var p paymentPayload
	src := env.Payload
	if len(src) == 0 {
		src = m.Data // some publishers send the flat payload
	}
	if err := json.Unmarshal(src, &p); err != nil {
		return
	}
	intentID := p.IntentID
	if intentID == "" {
		intentID = p.ID
	}
	// entityID is the by-reference fallback key (recovered from metadata or a legacy
	// bare-UUID reference); intentID remains the primary, robust match.
	entityID := entityIDFromReference(p.ReferenceID, p.Metadata)
	if intentID == "" && entityID == "" {
		return
	}

	switch p.ReferenceType {
	case "library_fine", "":
		c.reconcileFine(ctx, intentID, entityID)
	case "membership_fee":
		c.reconcileFee(ctx, intentID, entityID)
	case "ebook_sale":
		c.reconcilePurchase(ctx, intentID, entityID)
	case "acquisition_invoice":
		c.reconcileAcqInvoice(ctx, intentID, entityID)
	}
}

func (c *PaymentConsumer) reconcilePurchase(ctx context.Context, intentID, referenceID string) {
	q := c.db.EbookPurchase.Query()
	switch {
	case intentID != "":
		q = q.Where(ebookpurchase.TreasuryIntentID(intentID))
	case referenceID != "":
		if id, err := uuid.Parse(referenceID); err == nil {
			q = q.Where(ebookpurchase.IDEQ(id))
		} else {
			return
		}
	default:
		return
	}
	p, err := q.Only(ctx)
	if err != nil || p.Status == ebookpurchase.StatusPAID {
		return
	}
	_, _ = c.db.EbookPurchase.UpdateOneID(p.ID).SetStatus(ebookpurchase.StatusPAID).SetPurchasedAt(time.Now()).Save(ctx)
	c.log.Info("ebook purchase reconciled to PAID", zap.String("purchase", p.ID.String()))
}

// reconcileFine marks the matching fine PAID (by intent id, else by fine id reference).
func (c *PaymentConsumer) reconcileFine(ctx context.Context, intentID, referenceID string) {
	q := c.db.Fine.Query()
	switch {
	case intentID != "":
		q = q.Where(fine.TreasuryIntentID(intentID))
	case referenceID != "":
		if id, err := uuid.Parse(referenceID); err == nil {
			q = q.Where(fine.IDEQ(id))
		} else {
			return
		}
	default:
		return
	}
	f, err := q.Only(ctx)
	if err != nil {
		return // not ours / not found — ignore
	}
	if f.Status == fine.StatusPAID {
		return // idempotent
	}
	if _, err := c.db.Fine.UpdateOneID(f.ID).
		SetStatus(fine.StatusPAID).SetAmountPaid(f.Amount).SetPaidAt(time.Now()).Save(ctx); err != nil {
		c.log.Warn("fine reconcile failed", zap.Error(err))
		return
	}
	payload := map[string]any{
		"fine_id": f.ID, "intent_id": intentID, "amount": f.Amount.String(),
	}
	if mem, merr := c.db.Member.Query().Where(member.IDEQ(f.MemberID)).Only(ctx); merr == nil {
		payload["email"] = mem.ContactEmail
		payload["name"] = mem.DisplayName
	}
	_ = events.Publish(ctx, c.db.OutboxEvent, f.TenantID, f.ID.String(), events.EventFinePaid, payload)
	c.log.Info("fine reconciled to PAID", zap.String("fine", f.ID.String()))
}

func (c *PaymentConsumer) reconcileFee(ctx context.Context, intentID, referenceID string) {
	q := c.db.MembershipFee.Query()
	switch {
	case intentID != "":
		q = q.Where(membershipfee.TreasuryIntentID(intentID))
	case referenceID != "":
		if id, err := uuid.Parse(referenceID); err == nil {
			q = q.Where(membershipfee.IDEQ(id))
		} else {
			return
		}
	default:
		return
	}
	fee, err := q.Only(ctx)
	if err != nil || fee.Status == membershipfee.StatusPAID {
		return
	}
	_, _ = c.db.MembershipFee.UpdateOneID(fee.ID).SetStatus(membershipfee.StatusPAID).SetPaidAt(time.Now()).Save(ctx)
}

func (c *PaymentConsumer) reconcileAcqInvoice(ctx context.Context, intentID, referenceID string) {
	// Primary match: treasury_invoice_id == intentID (UUID stored at invoice creation).
	if intentID != "" {
		if tid, err := uuid.Parse(intentID); err == nil {
			inv, err := c.db.AcquisitionInvoice.Query().
				Where(acquisitioninvoice.TreasuryInvoiceIDEQ(tid), acquisitioninvoice.StatusEQ(acquisitioninvoice.StatusPENDING)).
				Only(ctx)
			if err == nil {
				_, _ = c.db.AcquisitionInvoice.UpdateOneID(inv.ID).SetStatus(acquisitioninvoice.StatusPAID).Save(ctx)
				c.log.Info("acquisition invoice reconciled to PAID", zap.String("invoice", inv.ID.String()))
				return
			}
		}
	}
	// Fallback: reference_id is a legacy bare entity UUID.
	if referenceID != "" {
		if id, err := uuid.Parse(referenceID); err == nil {
			inv, err := c.db.AcquisitionInvoice.Query().
				Where(acquisitioninvoice.IDEQ(id), acquisitioninvoice.StatusEQ(acquisitioninvoice.StatusPENDING)).
				Only(ctx)
			if err == nil {
				_, _ = c.db.AcquisitionInvoice.UpdateOneID(inv.ID).SetStatus(acquisitioninvoice.StatusPAID).Save(ctx)
				c.log.Info("acquisition invoice reconciled to PAID (ref-fallback)", zap.String("invoice", inv.ID.String()))
			}
		}
	}
}
