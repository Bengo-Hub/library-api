package consumers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	entlibraryuser "github.com/bengobox/library-service/internal/ent/libraryuser"
	"github.com/bengobox/library-service/internal/modules/rbac"
)

const authEventsStream = "auth"

// AuthEventsConsumer consumes auth-service user events so Library PIN provisioning and role
// mapping work the same way pos-api/inventory-api's already do. Before this consumer existed,
// library-api never subscribed to any auth.user.* subject at all, so an admin using auth-ui's
// central "Set Service PIN" dialog to provision a library PIN had the event published but never
// consumed — the PIN was silently never stored.
type AuthEventsConsumer struct {
	log     *zap.Logger
	rbacSvc *rbac.Service
	orm     *ent.Client
}

func NewAuthEventsConsumer(log *zap.Logger, rbacSvc *rbac.Service, orm *ent.Client) *AuthEventsConsumer {
	return &AuthEventsConsumer{
		log:     log.Named("consumers.auth_events"),
		rbacSvc: rbacSvc,
		orm:     orm,
	}
}

// Start subscribes to auth.user.* via JetStream durable consumers.
func (c *AuthEventsConsumer) Start(ctx context.Context, nc *nats.Conn) error {
	if nc == nil {
		c.log.Warn("NATS not available, skipping auth user event subscriptions")
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("auth events: jetstream init: %w", err)
	}

	// Ensure auth stream exists (guard against startup race with auth-api).
	if _, err := js.StreamInfo(authEventsStream); err != nil {
		if _, addErr := js.AddStream(&nats.StreamConfig{
			Name:      authEventsStream,
			Subjects:  []string{"auth.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); addErr != nil && addErr != nats.ErrStreamNameAlreadyInUse {
			c.log.Warn("auth events: ensure auth stream failed", zap.Error(addErr))
		}
	}

	type sub struct {
		subject string
		durable string
		handler func(context.Context, *sharedevents.Event) error
	}
	subs := []sub{
		{"auth.user.created", "lib-auth-user-created", c.handleUserCreated},
		{"auth.user.updated", "lib-auth-user-updated", c.handleUserUpdated},
		// Terminal/PIN provisioning: store the bcrypt PIN hash so /library/auth/pin/* works.
		{"auth.user.pin_set", "lib-auth-user-pin-set", c.handlePinSet},
	}

	for _, s := range subs {
		s := s
		sharedevents.SubscribeQueueWithRebind(c.log, js, "auth", s.subject, s.durable, func(msg *nats.Msg) {
			evt, err := sharedevents.FromJSON(msg.Data)
			if err != nil {
				c.log.Error("failed to unmarshal auth user event",
					zap.String("subject", s.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			if err := s.handler(context.Background(), evt); err != nil {
				c.log.Error("failed to handle auth user event",
					zap.String("subject", s.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
		},
			nats.Durable(s.durable),
			nats.AckExplicit(),
			nats.AckWait(30*time.Second),
			nats.MaxDeliver(5),
			nats.DeliverAll(),
		)
	}

	c.log.Info("auth event subscriptions active",
		zap.String("subjects", "auth.user.created, auth.user.updated, auth.user.pin_set"))
	return nil
}

func (c *AuthEventsConsumer) handleUserCreated(ctx context.Context, evt *sharedevents.Event) error {
	if err := c.syncUser(ctx, evt); err != nil {
		return fmt.Errorf("sync user from auth.user.created: %w", err)
	}
	return nil
}

func (c *AuthEventsConsumer) handleUserUpdated(ctx context.Context, evt *sharedevents.Event) error {
	if err := c.syncUser(ctx, evt); err != nil {
		return fmt.Errorf("sync user from auth.user.updated: %w", err)
	}
	return nil
}

// handlePinSet stores the bcrypt PIN hash carried on an auth.user.pin_set event so the user can
// authenticate at a library terminal. Only acts on events for service "library". The payload
// also carries the raw pin (see auth-api's provisionMemberPIN), which lets us keep the O(1)
// fast-hash lookup used by the local PIN-login handler (pin_auth.go's pinFastHash) in sync.
func (c *AuthEventsConsumer) handlePinSet(ctx context.Context, evt *sharedevents.Event) error {
	service, _ := evt.Payload["service"].(string)
	if service != "" && service != "library" {
		return nil // PIN provisioned for a different service (pos/inventory/…)
	}

	// Ensure the local user exists first (pin_set may arrive before user.created is processed).
	if err := c.syncUser(ctx, evt); err != nil {
		return fmt.Errorf("sync user for pin_set: %w", err)
	}

	userIDStr, _ := evt.Payload["user_id"].(string)
	pinHash, _ := evt.Payload["pin_hash"].(string)
	rawPin, _ := evt.Payload["pin"].(string)
	if evt.TenantID == uuid.Nil || pinHash == "" || userIDStr == "" {
		return fmt.Errorf("missing tenant_id, user_id or pin_hash in pin_set event")
	}

	u, err := c.orm.LibraryUser.Query().
		Where(entlibraryuser.TenantID(evt.TenantID), entlibraryuser.UserID(userIDStr)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("load library user for pin_set: %w", err)
	}
	upd := c.orm.LibraryUser.UpdateOne(u).
		SetPinHash(pinHash).
		SetPinFailedAttempts(0).
		ClearPinLockedUntil()
	if rawPin != "" {
		upd.SetPinFastHash(pinFastHash(evt.TenantID, userIDStr, rawPin))
	}
	if _, err := upd.Save(ctx); err != nil {
		return fmt.Errorf("store pin hash: %w", err)
	}
	c.log.Info("stored terminal PIN hash",
		zap.String("user_id", userIDStr), zap.String("tenant_id", evt.TenantID.String()))
	return nil
}

// syncUser upserts the LibraryUser row + role mapping + SSO branch link carried on an
// auth.user event, via rbac.Service.SyncUserFromEvent.
func (c *AuthEventsConsumer) syncUser(ctx context.Context, evt *sharedevents.Event) error {
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in auth user event")
	}
	userID, _ := evt.Payload["user_id"].(string)
	if userID == "" {
		return fmt.Errorf("missing user_id in auth user event")
	}
	email, _ := evt.Payload["email"].(string)
	fullName, _ := evt.Payload["full_name"].(string)
	roles := stringSliceFromPayload(evt.Payload["roles"])

	var outletID *string
	if raw, ok := evt.Payload["outlet_id"]; ok {
		s, _ := raw.(string)
		outletID = &s
	}

	return c.rbacSvc.SyncUserFromEvent(ctx, evt.TenantID, userID, email, fullName, roles, outletID)
}

// pinFastHash mirrors internal/http/handlers/pin_auth.go's unexported helper of the same name
// (duplicated rather than cross-imported to avoid a consumers→handlers layering dependency).
func pinFastHash(tenantID uuid.UUID, userID, pin string) string {
	sum := sha256.Sum256([]byte(tenantID.String() + ":" + userID + ":" + pin))
	return hex.EncodeToString(sum[:])
}

// stringSliceFromPayload coerces a JSON array payload value (decoded as []any of strings,
// or already []string) into []string, dropping non-string / empty entries.
func stringSliceFromPayload(v any) []string {
	switch t := v.(type) {
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
