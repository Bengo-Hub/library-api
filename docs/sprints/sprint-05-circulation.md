# Sprint 05 — Circulation Engine & Holds

**Status:** ✅ Shipped
**Goal:** Build the circulation rules engine (checkout/return/renew with policy precedence, fine-block and loan-limit enforcement, in-house sessions), the holds queue with promotion-on-return, and the idempotent overdue scheduler.

---

## Scope

The core staff workflow. All state transitions run inside Ent transactions and publish outbox events.

---

## Task Checklist

### Members, tiers, policies (`handlers/members.go`, `member_tiers.go`)
- [x] `GET/POST /members`, `GET/PUT /members/{id}` (membership_no via sequence; auth `user_id` + marketflow `crm_contact_id` refs; walk-in).
- [x] `GET /members/{id}/loans`, `GET /members/{id}/fines`.
- [x] `GET/POST /member-tiers`, `PUT /member-tiers/{id}`; `GET/POST /loan-policies`.

### Circulation service (`internal/modules/circulation/service.go`)
- [x] **Policy precedence** copy → bib → tier → tenant default (tier baseline implemented).
- [x] `Checkout` — member ACTIVE check; **fine-block** (outstanding ≥ tier `max_fine_before_block`); **loan-limit** (active take-home < `max_concurrent_loans`, in-house exempt); copy must be AVAILABLE (or RESERVED for this member's READY hold); flip copy ON_LOAN/IN_HOUSE; create loan (due = now + period); fulfill READY hold; publish `library.loan.created`.
- [x] `Return` — mark loan RETURNED; assess overdue fine (`days × tier.daily_fine_rate`); **promote next WAITING hold** → READY (copy RESERVED, 48h expiry, publish `library.hold.ready`) else copy AVAILABLE; publish `library.loan.returned`.
- [x] `Renew` — blocked by `max_renewals` (`renew_limit`) or a WAITING hold (`renew_held`); else `due_at += period`, `renewals_count += 1`, publish `library.loan.renewed`.
- [x] `assessOverdueFine`, `fineBlocked`, `readyHoldFor` helpers.

### Circulation handlers (`handlers/circulation.go`)
- [x] `POST /circulation/checkout` (member_id + copy_id|copy_barcode + in_house), `POST /circulation/return`, `POST /circulation/renew/{loan_id}`, `GET /circulation/loans` (status/member/overdue filters).
- [x] Domain-error → HTTP mapping: `member_not_active`, `member_blocked`, `loan_limit`, `copy_unavailable`, `no_active_loan`, `renew_limit`, `renew_held`.
- [x] `resolveCopyID` — accept copy id or scanned barcode.

### Holds (`handlers/holds.go`)
- [x] `GET /holds` (status/member/bib filters), `POST /holds` (place on a bib, queue_position), `DELETE /holds/{id}` (cancel).

### Overdue scheduler (`internal/modules/circulation/scheduler.go`)
- [x] `StartOverdueScheduler` (hourly) flips past-due ACTIVE take-home loans → OVERDUE + emits `library.loan.overdue`; idempotent (only non-OVERDUE touched) → safe on every replica; batch limit 500.

### Tests
- [x] `circulation/service_test.go` — checkout/return/renew + hold-promotion coverage.

---

## Acceptance Criteria

- [x] Checkout enforces ACTIVE member, fine-block, and loan-limit; in-house sessions bypass the take-home limit.
- [x] Return assesses an overdue fine and promotes the next hold atomically.
- [x] Renew respects the renewal cap and waiting holds.
- [x] Overdue sweep is idempotent and replica-safe.
- [x] Every transition publishes the correct outbox event.

---

## Dependencies

- Sprint 02 (loan/hold/member/tier schemas), Sprint 03 (auth + sequence), Sprint 04 (copies/branches), Sprint 07 (outbox transport — events buffer until the poller drains).
