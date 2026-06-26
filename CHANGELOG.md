# Changelog

All notable changes to the Library Service (library-api) will be documented in this file.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
