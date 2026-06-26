# Sprint 07 — Events & Cross-Service Integrations

**Status:** ✅ Shipped (notifications consumer + subscriptions activation pending — see notes)
**Goal:** Wire the transactional outbox → NATS, publish the full library event catalog, consume `treasury.payment.succeeded` to reconcile money, and stand up the S2S integration clients (treasury, notifications, subscriptions).

---

## Scope

The asynchronous backbone that makes circulation/fines/e-books observable and reconcilable across services. Subject convention `{aggregate_type}.{event_type}`; `aggregate_type` is always `"library"`.

---

## Task Checklist

### Outbox publishing (`internal/events/publish.go`)
- [x] `events.Publish(ctx, oc, tenantID, aggregateID, eventType, payload)` — inserts an `outbox_events` row (pass `tx.OutboxEvent` for atomic publish with the domain write).
- [x] Event-type constants: `member.registered`, `loan.created`, `loan.renewed`, `loan.returned`, `loan.overdue`, `hold.ready`, `fine.assessed`, `fine.paid`, `ebook.loaned`, `ebook.expired`, `membership.fee_due`.
- [x] `OutboxPoller` (shared-events) wired in `app.go` — drains PENDING rows to NATS via the NATS adapter (batch size + poll period from config).

### Treasury client (`internal/platform/treasury/client.go`)
- [x] S2S `CreateIntent` → `POST /api/v1/s2s/{tenant}/payments/intents` (X-API-Key + Idempotency-Key); `IntentResponse.ResolvedID()`.

### Treasury reconcile consumer (`internal/modules/consumers/payment.go`)
- [x] Durable queue subscription `library-payment-reconcile` on `treasury.payment.succeeded` (ManualAck, AckWait 30s, DeliverAll) — replica-safe.
- [x] Switch on `reference_type`: `library_fine`/empty → `reconcileFine`, `membership_fee` → `reconcileFee` (matched by `treasury_intent_id`, else `reference_id`; idempotent on already-PAID); fine reconcile publishes `library.fine.paid`.
- [x] Consumer started in `app.Run` (warns if NATS unavailable).

### Subscriptions client (`internal/platform/subscriptions/client.go`)
- [x] S2S `GetEntitlements` + `ConsumerHasFeature` (cached 60s, fail-open; demo-bypass + PAYG always allowed).
- [x] Feature catalog uses `library_*` codes.
- [ ] Activate consumer-side feature gating across the outbox-consuming workers (client built; not yet invoked in any consumer).

### Notifications client (`internal/platform/notifications/client.go`)
- [x] S2S REST-fallback `Send` → `POST /{tenant}/notifications/messages` (best-effort, never blocks circulation).
- [ ] `library/*` templates + a notifications-side consumer subscribing to the library outbox subjects (Sprint 10 / notifications repo).

---

## Acceptance Criteria

- [x] Domain mutations write outbox rows atomically; the poller publishes them to NATS.
- [x] `treasury.payment.succeeded` flips the matching fine/fee to PAID, idempotently, sharing work across replicas via the queue group.
- [x] Treasury intents are created with idempotency keys.
- [ ] Notifications templates render + deliver from library events (pending — clients ready).

---

## Notes

The notifications + subscriptions **clients exist and are tested**, but as of Phase 1 only the **treasury** client, the **outbox poller**, and the **payment reconcile consumer** are wired into `app.go`. Notifications template delivery and consumer-side subscription gating are tracked in Sprint 10.

---

## Dependencies

- Sprint 02 (`outbox_events` schema), Sprint 05/06 (event producers), treasury-api S2S endpoint.
