# Library API - Plan

**Service:** library-api
**Language:** Go 1.26
**Production domain:** `libraryapi.codevertexitsolutions.com`
**Last updated:** 2026-06-26
**Status:** Phase 1 (MVP) shipped — full backend + frontend. Catalog/OPAC, circulation (checkout/return/renew + holds + in-house reading), members/tiers/policies, fines + membership fees, and e-books (in-browser reader + Controlled Digital Lending) are live, with auth/SSO, treasury, notifications, subscriptions and outbox plumbing wired.

---

## Product Overview

library-api is the Library Management System backbone for the Codevertex platform. It is a **standalone Go microservice** that integrates with the existing fabric (auth/SSO, treasury, notifications, subscriptions, marketflow) **by reference** — no cross-service PII duplication. It serves four pillars:

1. **Bibliographic catalog / OPAC** — the "work" (BibRecord) and its physical copies + e-books; ISBN lookup, MARC-lite + Dublin Core payloads, faceted search foundations.
2. **Circulation** — scan-driven checkout/return/renew, holds queue with promotion-on-return, in-house/reference reading sessions, overdue accrual.
3. **Members + money** — patron registry with tiers + loan policies; overdue/lost/damage fines and periodic membership fees settled via treasury payment intents.
4. **E-books** — in-browser PDF/EPUB reader with Controlled Digital Lending (CDL, concurrency-limited, no download in Phase 1); Phase-2 one-time purchase + token-gated download.

A multi-tenant SaaS service: every business row carries `tenant_id`; roles/permissions are global reference data. The companion frontend is `library-ui` (Next 16).

---

## Current State (2026-06-26)

Phase 1 is shipped. The actual implemented surface:

**Ent schemas (24+ entities):** `bibrecord`, `author`, `publisher`, `subject`, `collection` (`catalog_refs.go`), `branch`, `bookcopy`, `member`, `membertier`, `loanpolicy` (`policy.go`), `loan`, `hold`, `fine`, `membershipfee`, `ebook`, `ebookloan`, `ebookpurchase` (`ebookloan.go`), `libraryrole`, `libraryuser` (`rbac.go`), `auditlog`, `documentsequence`, `serviceconfig`, `outboxevent`, `tenant`. Money columns are `numeric(18,4)` via `shopspring/decimal` (never float); rate columns are `numeric(10,4)`.

**HTTP handlers (15 files):** `authme.go`, `catalog_bibs.go`, `catalog_copies.go`, `copy_label.go`, `branches.go`, `members.go`, `member_tiers.go`, `circulation.go`, `holds.go`, `fines.go`, `ebooks.go`, `reports.go`, `rbac.go`, `health.go`, plus shared `respond.go`/`reqctx.go`.

**Modules:** `circulation` (rules engine + overdue scheduler), `rbac` (global roles + JIT heal), `sequence` (membership/accession/loan numbering), `barcode` (copy spine-label PDF), `consumers` (treasury payment reconcile).

**RBAC:** 3 global roles (`library_admin`, `library_staff`, `library_member`); dotted permission codes `library.{module}.{action}`; JIT provisioning heals existing users on every request.

**Events (outbox):** `library.member.registered`, `library.loan.created`, `library.loan.renewed`, `library.loan.returned`, `library.loan.overdue`, `library.hold.ready`, `library.fine.assessed`, `library.fine.paid`, `library.ebook.loaned`, `library.ebook.expired`, `library.membership.fee_due` — all `{aggregate_type}.{event_type}` with `aggregate_type = "library"`.

**Consumed events:** `treasury.payment.succeeded` (durable queue consumer `library-payment-reconcile`) flips the matching fine/membership-fee/purchase to PAID (idempotent on the treasury intent id).

**library-ui:** Shipped — SSO/PKCE, dashboard, catalog (OPAC) + cataloging + copies, circulation desk (scan-driven), holds, members + tiers + policies, fines, e-books + in-browser reader, branches, team/roles, reports, settings, platform admin.

---

## Phased Roadmap

### Phase 1 — MVP (shipped)

All four pillars + full platform plumbing.

| # | Capability | Status |
|---|------------|--------|
| 1 | Bibliographic catalog (BibRecord, copies, ISBN lookup, MARC-lite/Dublin Core) | ✅ Done |
| 2 | Circulation rules engine: checkout/return/renew, in-house reading | ✅ Done |
| 3 | Holds queue + promotion-on-return (READY with pickup expiry) | ✅ Done |
| 4 | Members + tiers + loan policies | ✅ Done |
| 5 | Fines (overdue accrual at return) + treasury pay/waive | ✅ Done |
| 6 | Membership fees via treasury intent | ✅ Done |
| 7 | E-books: in-browser PDF/EPUB reader + Controlled Digital Lending (no download) | ✅ Done |
| 8 | Overdue scheduler (idempotent, multi-replica safe) | ✅ Done |
| 9 | Transactional outbox → NATS; treasury reconcile consumer | ✅ Done |
| 10 | RBAC (global roles, JIT heal, `/auth/me` union) | ✅ Done |
| 11 | Atlas versioned migrations | ✅ Done |
| 12 | Copy spine-label PDF | ✅ Done |
| 13 | Idempotent seed (global roles always; demo data on `SEED_TENANT_ID`) | ✅ Done |

### Phase 2 — E-book purchase/download + notifications

| # | Capability | Status |
|---|------------|--------|
| 1 | E-book one-time purchase (`ebookpurchase`) settled via treasury `ebook_sale` intent (`POST /ebooks/{id}/purchase`) | ✅ Done |
| 2 | Token-gated secured download (`GET /ebooks/{id}/download`, PAID-purchase + `download_count`); treasury reconcile extended to `ebook_sale` | ✅ Done (byte-stream + per-purchase watermark hardening pending) |
| 3 | Swagger/OpenAPI handler (`/v1/docs`, `/api/v1/openapi.json`) | ✅ Done |
| 4 | Notifications templates + consumer (overdue notices, hold-ready, fine assessed, fee due) | ⏳ Planned |
| 5 | Membership-fee automation + dunning | ⏳ Planned |

### Phase 3 — Advanced OPAC / MARC

- Advanced faceted OPAC (subject/author/collection facets, availability).
- MARC21 / Z39.50 / SRU import-export; authority control (Author/Subject/Publisher tables already back this).
- Recommendations.

### Phase 4 — RFID / self-checkout

- RFID + self-checkout kiosk; security-gate integration.
- Offline PWA staff desk; copy stocktake + inter-branch transfers (Branch + BookCopy `IN_TRANSIT` status already model this).

---

## Key Decisions

- **By-reference integration, no PII duplication.** Member references the auth `user_id` and an optional marketflow `crm_contact_id` (marketflow is the customer SoT). Only a cached `display_name`/contact is held for desk UX. Walk-in/anonymous patrons are supported via `is_walk_in` with no refs.
- **Money is decimal, never float.** All money columns go through the `moneyField`/`rateField` helpers (`numeric(18,4)` / `numeric(10,4)`).
- **Outbox-only publishing.** Mutations insert an `outbox_events` row in the same Ent transaction; the shared-events `OutboxPoller` drains to NATS. Subject = `{aggregate_type}.{event_type}`.
- **Mutations-only subscription gate.** GET always passes; mutations require an active subscription (superuser / platform-owner / demo / PAYG exempt).
- **Global roles, not tenant-scoped.** `LibraryRole` has no `tenant_id`; the `LibraryUser` projection is per-tenant and JIT-healed on every authenticated request.
- **Atlas versioned migrations** — no Ent auto-migrate in production (`POSTGRES_RUN_MIGRATIONS` applies versioned files only).
- **CDL before purchase.** Phase 1 e-books are read-in-browser only (concurrency-capped); download is deliberately deferred to Phase 2.

---

## Dependencies

| Dependency | Notes |
|------------|-------|
| entgo.io/ent v0.14 | ORM + code generation |
| Atlas | Versioned migrations (embedded-FS fallback) |
| shared-auth-client | JWT/JWKS validation + API-key S2S auth |
| httpware | HTTP middleware, request id, recover, health probes |
| shared-events | NATS JetStream helpers + SQL outbox repository + OutboxPoller |
| `@bengo-hub/cache` (Redis) | Caching / slug resolution |
| pgx/v5 | PostgreSQL driver (pgbouncer in prod) |
| shopspring/decimal | Money/rate precision |

---

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Treasury intent ↔ fine reconciliation drift | Fines stuck UNPAID | Idempotent reconcile keyed on `treasury_intent_id`; fall back to `reference_id` |
| CDL concurrency race under load | Over-lending an e-book | `ForUpdate()` row lock on the `ebook` row inside the lend transaction |
| Hold promotion races on return | Two members promoted for one copy | Promotion runs inside the return transaction; copy flips to `RESERVED` atomically |
| Sequence collisions (membership/accession) | Duplicate human-readable numbers | `DocumentSequence` row-locked allocation per tenant + kind |
