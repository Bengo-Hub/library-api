# Sprint 10 — RFID, Self-Checkout, Dunning & Notifications (Phase 4)

**Status:** ⏳ Planned
**Goal:** Close out the platform: RFID + self-checkout kiosk, security-gate integration, offline staff desk, inter-branch transfers/stocktake, membership-fee dunning, notification delivery, and e-book download hardening.

---

## Scope

Phase-4 operational maturity + the deferred items from earlier sprints (notifications consumer, subscription gating activation, DRM watermark).

---

## Task Checklist

### RFID & self-checkout
- [ ] RFID tag ↔ copy mapping (tag id on `BookCopy` or a join table).
- [ ] Self-checkout kiosk endpoints: member self-identify → bulk checkout/return via RFID antenna read.
- [ ] Security-gate integration: emit a gate-alert event when an un-checked-out copy passes (`library.security.alert`).

### Offline staff desk
- [ ] Offline circulation queue: idempotent checkout/return with a `client_reference`/Idempotency-Key end-to-end (mirror the POS offline pattern).
- [ ] Conflict resolution + dead-letter for un-syncable transactions.

### Inter-branch logistics
- [ ] Copy transfer between branches (`IN_TRANSIT` status flow) + transfer receipts.
- [ ] Stocktake / shelf-reading reconciliation (expected vs scanned), missing-copy report.

### Membership-fee dunning & member emails
- [ ] Periodic membership-fee assessment scheduler emitting `library.membership.fee_due`.
- [ ] Dunning escalation (notice → reminder → suspend `Member.status`).
- [ ] Member-direct emails (last-9-digit phone / email match) for receipts + reminders.

### Notifications (deferred from Sprint 07)
- [ ] `library/*` templates (overdue-notice, hold-ready, fine-assessed, fee-due, ebook-loaned).
- [ ] Notifications-side consumer subscribing to the library outbox subjects.
- [ ] E-book loan-expiry scheduler emitting `library.ebook.expired`.

### Subscription gating (deferred from Sprint 07)
- [ ] Invoke `ConsumerHasFeature` (`library_*`) in the outbox-consuming workers / gated endpoints.

### E-book download hardening (deferred from Sprint 08)
- [ ] Byte-stream download from the PVC with a per-purchase watermark.
- [ ] TOKEN_GATED `drm_policy` enforcement + signed short-TTL URLs.
- [ ] `library.ebook.purchased` event.

---

## Acceptance Criteria

- [ ] A self-checkout kiosk completes checkout/return via RFID without staff.
- [ ] Security gate raises an alert for an un-checked-out copy.
- [ ] Offline desk transactions sync exactly-once on reconnect.
- [ ] Copies transfer between branches with an auditable `IN_TRANSIT` trail.
- [ ] Membership-fee dunning escalates and suspends per policy.
- [ ] Library events render + deliver notifications; e-book downloads are watermarked.

---

## Dependencies

- Sprint 05 (circulation), Sprint 06/08 (e-books), Sprint 07 (events + notifications/subscriptions clients), RFID hardware + notifications-api template support.
