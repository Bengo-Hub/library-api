# Changelog

All notable changes to the Library Service (library-api) will be documented in this file.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Borrowing fee collected at checkout:** the circulation rule's `rental_charge` (per book) is raised as a fee when a book is checked out, and the librarian can mark it paid during checkout (`payment` on `POST /circulation/checkout`). `GET /circulation/checkout-quote` previews the fee, late rate and due date.
- **Late fee tied to the borrowing fee:** new rule field `late_fee_uses_rental_charge` charges each late day at the borrowing fee.
- **Desk payments without treasury:** `POST /fines/{id}/record-payment` and `GET /fines/{id}/payments`, backed by a new `fine_payments` ledger and `internal/modules/fines`. The treasury consumer writes to the same ledger. Dashboard summary gains `collected_today`.
- Migration `20261016071236_add_fee_payments_and_late_fee_flag`: `circulation_rules.late_fee_uses_rental_charge`, the `fine_payments` table and two `fines` indexes.

### Changed
- One rule resolver for checkout, renew, return and lost items; a tier baseline applies when no rule matches (previously a fixed 14 days). Renewals now follow the rule instead of the tier. The Redis rule cache and its `KEYS` scan are gone.
- Rule saves reject malformed or negative amounts and a second rule on the same branch, tier and format.
- `outstanding_fines` on members is now the money owed (it was a count that the UI formatted as money).
- Due dates honour `due_date_mode`, branch hours, holidays and hourly rules through the calendar module.
- Dashboard, report and list queries are grouped in SQL and look names up only for the rows shown (summary, popular titles, circulation trend, item movement, member activity, fine ageing, member list).

### Fixed
- Loans the hourly sweep had marked `OVERDUE` dropped out of the dashboard overdue count, the overdue filter, the loan limit, the copy due dates and the member delete guard.
- A fine could be waived after it was paid. A double click on checkout could lend one copy twice.
- The circulation trend counted a return only when the loan was also checked out inside the window.

### Added (Phase 2, in progress)
- **E-book one-time purchase + download:** `POST /ebooks/{id}/purchase` creates a treasury `ebook_sale` intent + a PENDING `EbookPurchase` (with a `download_token`); `GET /ebooks/{id}/download?token=` serves the file for a PAID purchase and increments `download_count`. `treasury.payment.succeeded` reconcile extended to `ebook_sale` → `EbookPurchase` PAID (`reconcilePurchase`).
- **Swagger/OpenAPI:** Swagger UI at `/v1/docs` + spec at `/api/v1/openapi.json`; root `/` redirects to the docs.

### Planned (Phase 2)
- DRM/watermark hardening (byte-stream download from PVC with per-purchase watermark, TOKEN_GATED enforcement, signed short-TTL URLs); `library.ebook.purchased` event.
- Notifications `library/*` templates + notifications-side consumer; e-book loan-expiry scheduler.
- Membership-fee automation + dunning escalation.
- Reports endpoints (`/reports/popular`, `/reports/circulation`, `/reports/overdue`); catalog authority list + cover-upload endpoints.

## [0.1.0] — 2026-06-26 — Phase 1 MVP

### Added
- **Service bootstrap:** Go 1.26 + chi router + Ent v0.14 + Atlas versioned migrations; pgxpool, Redis, NATS, zap; health/readiness/metrics; graceful shutdown (`internal/app/app.go`).
- **Ent schemas (24+):** `bibrecord`, `author`, `publisher`, `subject`, `collection`, `branch`, `bookcopy`, `member`, `membertier`, `loanpolicy`, `loan`, `hold`, `fine`, `membershipfee`, `ebook`, `ebookloan`, `ebookpurchase`, `libraryrole`, `libraryuser`, `auditlog`, `documentsequence`, `serviceconfig`, `outboxevent`, `tenant`. Decimal money via `moneyField`/`rateField` (`numeric(18,4)`/`numeric(10,4)`).
- **Auth/SSO:** `RequireAuth` (JWKS via `shared-auth-client`) + S2S API-key auth; JIT user provisioning that **heals existing users** on every request.
- **RBAC:** global roles (`library_admin`/`library_staff`/`library_member`) seeded + drift-healed; union RBAC middleware (`RequireServicePermission`); `GET /auth/me` returning JWT ∪ local permissions; `/rbac` + `/team` endpoints.
- **Catalog/OPAC:** BibRecord CRUD + list/search + ISBN lookup (MARC-lite + Dublin Core); BookCopy CRUD + resolve-by-barcode + spine-label PDF; Author/Publisher/Subject/Collection authority schemas.
- **Circulation:** rules engine — checkout (fine-block + loan-limit + hold-aware), return (overdue-fine accrual + hold promotion to READY with 48h expiry), renew (renew-limit + waiting-hold block); in-house/reference sessions; overdue scheduler (idempotent, multi-replica safe).
- **Members & money:** member registry (auth `user_id` + marketflow `crm_contact_id` refs; walk-in support) with tiers + loan policies; fines (list/waive/pay-via-treasury-intent); membership fees via treasury intent; `DocumentSequence` numbering.
- **E-books:** registry + token-gated in-browser PDF/EPUB reader; Controlled Digital Lending with `ForUpdate()` row-locked concurrency cap (`cdl_limit`); reading-position persistence.
- **Events:** transactional outbox + shared-events `OutboxPoller` → NATS (`{aggregate_type}.{event_type}`, `aggregate_type="library"`); full published catalog (member/loan/hold/fine/ebook/membership); `treasury.payment.succeeded` durable queue consumer reconciling fines/fees → PAID (idempotent on intent id).
- **Subscriptions:** mutations-only subscription gate; cached, fail-open S2S entitlement client.
- **Integrations:** treasury (payment intents), notifications (REST fallback), subscriptions, marketflow (CRM ref).
- **Ops:** idempotent seed (global roles always; demo data on `SEED_TENANT_ID`); backup config; HA-ready (≥2 replicas + PDB convention).
- **Docs:** `plan.md`, `architecture.md`, `erd.md`, `integrations.md`, `rbac-and-seed.md`, `api-reference.md`, `docs/sprints/*`, plus README/CONTRIBUTING/SECURITY/SUPPORT/CODE_OF_CONDUCT.
