// Package consumers holds the library NATS/JetStream event consumers. PaymentConsumer
// reconciles treasury payments back to the originating fine/fee/purchase: on
// treasury.payment.succeeded it flips the matching record to PAID (idempotent on the
// treasury intent id).
package consumers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/ebookpurchase"
	"github.com/bengobox/library-service/internal/ent/fine"
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
	ReferenceType string `json:"reference_type"`
	ReferenceID   string `json:"reference_id"`
	IntentID      string `json:"intent_id"`
	ID            string `json:"id"`
	Status        string `json:"status"`
}

// Start subscribes to treasury.payment.succeeded as a durable queue consumer so multiple
// replicas share the work without double-processing.
func (c *PaymentConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	_, err := js.QueueSubscribe(
		"treasury.payment.succeeded",
		"library-payment-reconcile",
		func(m *nats.Msg) { c.handle(ctx, m) },
		nats.Durable("library-payment-reconcile"),
		nats.ManualAck(),
		nats.AckWait(30*time.Second),
		nats.DeliverAll(),
	)
	return err
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
	if intentID == "" && p.ReferenceID == "" {
		return
	}

	switch p.ReferenceType {
	case "library_fine", "":
		c.reconcileFine(ctx, intentID, p.ReferenceID)
	case "membership_fee":
		c.reconcileFee(ctx, intentID, p.ReferenceID)
	case "ebook_sale":
		c.reconcilePurchase(ctx, intentID, p.ReferenceID)
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
	_ = events.Publish(ctx, c.db.OutboxEvent, f.TenantID, f.ID.String(), events.EventFinePaid, map[string]any{
		"fine_id": f.ID, "intent_id": intentID,
	})
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
