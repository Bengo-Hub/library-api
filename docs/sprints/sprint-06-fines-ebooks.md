# Sprint 06 — Fines, Membership Fees & E-books (CDL)

**Status:** ✅ Shipped
**Goal:** Settle money via treasury payment intents (fines + membership fees), and ship the digital shelf: e-book registry, Controlled Digital Lending (concurrency-capped), token-gated in-browser reader, and reading-position persistence.

---

## Scope

The money + digital-lending surfaces. Phase-1 e-books are read-in-browser only (no download — that lands in Sprint 08).

---

## Task Checklist

### Fines (`handlers/fines.go`)
- [x] `GET /fines` — list with `?status=` / `?member_id=` filters → `listEnvelope`.
- [x] `POST /fines/{id}/waive` — set WAIVED + `waived_by` (sensitive, audited).
- [x] `POST /fines/{id}/pay` — create a treasury intent (`reference_type=library_fine`) for the outstanding amount; store `treasury_intent_id`; publish `library.fine.assessed`; return `{intent_id, initiate_url, amount}`; guard already-settled (`already_settled`) + treasury-unwired (`treasury_unwired`).

### Membership fees
- [x] Membership-fee assessment (period + amount via treasury intent, `reference_type=membership_fee`); status PENDING/PAID/WAIVED/CANCELLED.

### E-books — registry & CDL (`handlers/ebooks.go`)
- [x] `GET /ebooks` — list → `listEnvelope`.
- [x] `POST /ebooks` — register record (bib_record_id, file_url, format, lending_model, max_concurrent_loans, loan_duration_days); file uploaded separately to the media PVC.
- [x] `POST /ebooks/{id}/lend` — **CDL**: `ForUpdate()` row-lock on the ebook row, count active (`returned_at IS NULL`) loans, reject `409 cdl_limit` at cap; mint short-lived `access_token` + expiry; publish `library.ebook.loaned`.
- [x] `GET /ebooks/{id}/read` — token-gated session: validate `access_token` + not-expired; return `{file_url, format, watermark (member id), last_read_position, expires_at}`.
- [x] `POST /ebooks/loans/{id}/position` — persist `last_read_position` JSON.
- [x] `randomToken()` crypto-random token helper.

---

## Acceptance Criteria

- [x] A fine's pay action creates a single treasury intent (idempotent on the fine UUID) and hands back an initiate URL.
- [x] Waive is recorded with the acting user and is auditable.
- [x] CDL lending never exceeds `max_concurrent_loans` under concurrent load (row lock).
- [x] The reader is only accessible with a valid, unexpired token; reading position persists across sessions.
- [x] No e-book download path in Phase 1 (CDL only).

---

## Dependencies

- Sprint 02 (fine/fee/ebook schemas), Sprint 03 (auth), Sprint 07 (treasury client + outbox transport).
