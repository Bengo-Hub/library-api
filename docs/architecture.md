# Library API - Architecture

**Service:** library-api
**Language:** Go 1.26
**ORM:** Ent (entgo.io/ent v0.14) + Atlas versioned migrations
**HTTP Router:** chi/v5
**Port:** 4010
**Production:** `libraryapi.codevertexafrica.com`
**Last updated:** 2026-06-26
**Status:** Phase 1 shipped. 24+ Ent schemas, 15 handler files, 3 global RBAC roles, transactional outbox publishing, treasury reconcile consumer + overdue scheduler wired.

---

## High-Level Overview

```
        ┌──────────────┐   OIDC/PKCE   ┌──────────────┐
        │   auth-ui    │◄──────────────│  library-ui   │
        │   (SSO)      │──────JWT──────►│  Next 16     │
        └──────────────┘                └──────┬───────┘
                                               │ REST (Bearer JWT)
                                        ┌──────▼─────────────────┐
                                        │      library-api  :4010 │
                                        ├─────────────────────────┤
                                        │ chi router + httpware    │
                                        │ RequireAuth (JWKS)       │
                                        │ JIT heal (RBAC)          │
                                        │ mutations-only sub gate  │
                                        │ RequireServicePermission │
                                        ├─────────────────────────┤
                                        │ handlers → modules        │
                                        │  circulation rules engine │
                                        │  rbac · sequence · barcode│
                                        ├─────────────────────────┤
                                        │ Ent ORM (Postgres/pgx)    │
                                        │ Redis (cache)             │
                                        │ NATS JetStream + outbox   │
                                        └──────┬────────────┬──────┘
                              treasury intent  │            │  outbox events
                           (S2S X-API-Key)     ▼            ▼
                                        ┌────────────┐  ┌──────────────┐
                                        │ treasury   │  │ notifications │
                                        └─────┬──────┘  └──────────────┘
                          treasury.payment.succeeded (NATS consumer)
                                              │
                                              ▼  reconcile fine/fee → PAID
```

---

## Project Layout

```
library-api/
├── cmd/
│   ├── api/main.go             # Application entrypoint (app.New → app.Run)
│   ├── migrate/main.go         # Atlas migrate runner
│   └── seed/main.go            # Idempotent seed (global roles + demo data on SEED_TENANT_ID)
├── internal/
│   ├── app/app.go              # Bootstrap: DB, Redis, NATS, outbox, RBAC seed, handlers, HTTP server
│   ├── config/config.go        # envconfig (HTTP/Postgres/Redis/Events/Auth/Media/Services/Subscriptions/Backup)
│   ├── ent/
│   │   ├── schema/             # Ent schema definitions (source of truth)
│   │   └── migrate/            # Atlas versioned migrations (embedded-FS fallback)
│   ├── events/publish.go       # Outbox publish helper (aggregate_type = "library")
│   ├── http/
│   │   ├── handlers/           # catalog, circulation, members, fines, ebooks (+ ebook_purchase), reports, rbac, swagger, …
│   │   ├── middleware/
│   │   │   ├── permission.go   # RequireServicePermission (union RBAC)
│   │   │   └── subscription.go # RequireFeature (per-feature lock; the mutations gate is authclient.RequireActiveSubscriptionForMutationsWithGrace, wired directly in router.go)
│   │   └── router/router.go    # chi route registration (single mount under /api/v1/{tenant}/library)
│   ├── modules/
│   │   ├── circulation/        # service.go (rules engine) + scheduler.go (overdue sweep)
│   │   ├── rbac/service.go     # global roles, JIT heal, permission resolution
│   │   ├── sequence/           # membership_no / accession_no / loan_no allocation
│   │   ├── barcode/label.go    # copy spine-label PDF
│   │   └── consumers/payment.go# treasury.payment.succeeded reconcile
│   ├── platform/
│   │   ├── cache/redis.go      # Redis client
│   │   ├── database/postgres.go# pgxpool
│   │   ├── events/nats.go      # NATS connect + EnsureStream
│   │   ├── treasury/client.go  # S2S payment-intent client
│   │   ├── notifications/client.go # S2S REST fallback
│   │   └── subscriptions/client.go # S2S entitlement (cached, fail-open)
│   └── shared/logger/          # zap logger
├── docs/                       # Documentation (this set)
└── go.mod
```

---

## Request Lifecycle

Every tenant request flows through the same ordered middleware stack mounted once under `/api/v1/{tenant}/library` (`router.go`):

1. **Global middleware** — `RequestID`, `RealIP`, `httpware.Logging`/`Recover`, 30s `Timeout`, 50 MB `RequestSize` (e-book uploads), CORS.
2. **`RequireAuth`** (`shared-auth-client`) — validates the Bearer JWT against JWKS (`sso.codevertexafrica.com`), or an `X-API-Key` for S2S. Claims land in request context.
3. **JIT heal** — `rbac.Service.EnsureUserFromToken` upserts the local `LibraryUser` from JWT claims and re-applies mapped roles **on every request** (not only first-create), so a user provisioned before a role mapping existed self-heals (treasury #30 gotcha).
4. **Mutations-only subscription gate** — `authclient.RequireActiveSubscriptionForMutationsWithGrace(7)` (shared package, wired directly in `router.go`): GET/HEAD/OPTIONS always pass; mutations require an active subscription (7-day post-expiry grace), with platform-owner / demo / PAYG / explicitly-exempt-tenant (`IsGatingExempt`) bypass. **Tenant superuser does NOT bypass this** — a tenant admin is still a paying customer, not the platform owner. Emits the standard `{error,code:"subscription_inactive",upgrade:true}` envelope frontends parse.
5. **`RequireServicePermission(perms…)`** (where mounted) — union RBAC: (a) superuser/platform-owner bypass → (b) JWT-carried permission → (c) local RBAC fallback (`HasAnyPermission`) → else 403 `permission_denied`.

`GET /auth/me` returns the effective identity: **SSO JWT permissions ∪ local RBAC permissions** (`reference_service_rbac_authme_sync`).

---

## Module Layout & Responsibilities

| Module | Responsibility |
|--------|----------------|
| `circulation` | Checkout / return / renew, hold promotion on return, overdue-fine accrual, in-house sessions, overdue scheduler |
| `rbac` | Global role seed + heal, JWT→role mapping, JIT user upsert, permission resolution, team management |
| `sequence` | Row-locked monotonic `DocumentSequence` allocation for `membership_no` / `accession_no` / `loan_no` |
| `barcode` | Copy spine-label PDF generation |
| `consumers` | `treasury.payment.succeeded` reconcile (fine/fee → PAID, idempotent) |

---

## Circulation Rules Engine

The rules engine lives in `internal/modules/circulation/service.go`.

### Policy precedence

One resolver (`rule_resolver.go`) decides the terms for every checkout, renewal, return and lost item. It reads the tenant's `CirculationRule` rows that match the copy's branch, the member's tier and the title's format, in one indexed query, and the most specific match wins (branch outranks tier, tier outranks format; `null` means all). When no rule matches it falls back to the member's **tier** (`MemberTier`: `loan_period_days`, `max_renewals`, `daily_fine_rate`). The member limits that are not part of a rule (`max_concurrent_loans`, `max_fine_before_block`) always come from the tier. The older `LoanPolicy` table is not read by circulation.

The rule carries the money terms as well as the loan terms:

| Rule field | Used for |
|------------|----------|
| `rental_charge` | The borrowing fee, charged per book at checkout |
| `fine_per_day`, `grace_days`, `max_fine_cap` | The late fee for each day a book is returned late |
| `late_fee_uses_rental_charge` | When true each late day costs the borrowing fee, so one number drives both |
| `replacement_cost`, `processing_fee` | The charge when a loan is marked lost |

Due dates go through the `calendar` module, so closed days and holidays are honoured according to each rule's `due_date_mode`. Hourly rules add hours instead.

### Checkout

1. Member must be `ACTIVE`.
2. **Fine-block** check: outstanding (`amount − amount_paid`, one SUM query) across `UNPAID`/`PARTIAL` fines and fees must be below the tier's `max_fine_before_block`.
3. **Loan-limit** check: take-home loans still out (`ACTIVE` or `OVERDUE`) `< tier.max_concurrent_loans`. In-house sessions are exempt.
4. Copy must be `AVAILABLE`, or `RESERVED` **only if** this member holds a READY hold on the bib.
5. In one transaction: lock the copy row and re-check it, flip it to `ON_LOAN` (or `IN_HOUSE` for reference sessions), create the `Loan` with the due date from the resolved rule, fulfill the member's READY hold, raise the **borrowing fee** and publish `library.loan.created`.

**Borrowing fee.** When the resolved rule has a `rental_charge` and the loan is not in-house, a `RENTAL` fine is created in the same transaction. If the request carries `payment.collected`, the fee is settled in that transaction through `fines.ApplyPayment`, so the librarian marks it paid while checking out. Otherwise it stays `UNPAID` on the member's account and can be paid later from the Fines page. A failure raising the fee cancels the checkout. `GET /circulation/checkout-quote` previews the fee, late rate and due date without changing anything.

**Payments.** Every settlement goes through `internal/modules/fines`: the desk, the checkout and the treasury consumer. `ApplyPayment` locks the fine row, writes a `fine_payments` ledger row, updates `amount_paid` and the status (`PARTIAL` or `PAID`), and publishes `library.fine.paid` once settled, when the member has an email on file. Desk payments need no treasury. Treasury stays the optional online route (`POST /fines/{id}/pay`).

### Return

1. Find the `ACTIVE` loan for the copy (else `no_active_loan`).
2. In a transaction: mark loan `RETURNED`.
3. If overdue, assess an `OVERDUE` fine = `late days × late rate` and publish `library.fine.assessed`. Every started day counts, minus the rule's `grace_days`. The late rate is the rule's `fine_per_day`, or the borrowing fee when `late_fee_uses_rental_charge` is set, or the tier's `daily_fine_rate` when the rule has none. `max_fine_cap` and the replacement-price cap apply on top.
4. **Hold promotion:** the next `WAITING` hold on the bib (ordered by `queue_position`, then `placed_at`) is promoted to `READY` with a 48h pickup expiry; the copy flips to `RESERVED` and `library.hold.ready` is published. If no hold waits, the copy returns to `AVAILABLE`.
5. Publish `library.loan.returned`.

### Renew

- Blocked by the resolved rule's `max_renewals` (`renew_limit`), by any `WAITING` hold on the bib (`renew_held`) or by a pending recall. On success the due date moves forward by one loan period under the same rule, `renewals_count += 1`, and `library.loan.renewed` is published.

### Overdue scheduler

`StartOverdueScheduler` (default hourly) flips past-due `ACTIVE` take-home loans to `OVERDUE` and emits `library.loan.overdue`. Each pass works through the backlog in batches of 500 (up to 40 batches), and every batch flips its loans and queues their notices in one transaction, so a loan is never flipped without its event. It is **idempotent** (only `ACTIVE` loans are touched) and an hourly claim lets only one replica sweep. Fine accrual itself happens at return time, not in the sweep. Because the sweep moves loans to `OVERDUE`, every count of loans that are still out (dashboard, member list, copy list, member delete guard, loan limit) uses `ACTIVE` plus `OVERDUE`.

### CDL concurrency

E-book lending (`ebooks.go`) takes a `ForUpdate()` row lock on the `ebook` row, counts active (`returned_at IS NULL`) `EbookLoan`s, and rejects (`cdl_limit`, 409) when at `max_concurrent_loans` — keeping simultaneous reader sessions consistent under load. A successful lend mints a short-lived `access_token` gating `GET /ebooks/{id}/read`. Outright purchase (`ebook_purchase.go`) creates a treasury `ebook_sale` intent + a PENDING `EbookPurchase`; once the reconcile consumer flips it PAID, `GET /ebooks/{id}/download?token=` serves the file (token-gated, `download_count`-tracked).

---

## Transactional Outbox & Event Catalog

**Transport:** NATS JetStream (stream `library`, deliver group `library-workers`).

**Retention:** the stream keeps events for 7 days (`MaxAge`, set on new and existing streams at startup in `platform/events/nats.go`), like the other service streams. Limits retention keeps acked events because several consumers read the same subjects, so the age limit is what clears them. Before 2026-10-03 the stream had no age limit and kept every event since July.

**Outbox pattern:** Domain mutations call `events.Publish(ctx, tx.OutboxEvent, …)` to insert an `outbox_events` row inside the same Ent transaction. The shared-events `OutboxPoller` (started in `app.go`) drains `PENDING` rows to NATS. Subject = `{aggregate_type}.{event_type}`; `aggregate_type` is always `"library"`.

**Published events:**

| Event | Trigger |
|-------|---------|
| `library.member.registered` | Member created |
| `library.loan.created` | Checkout |
| `library.loan.renewed` | Renew |
| `library.loan.returned` | Return |
| `library.loan.overdue` | Overdue scheduler sweep |
| `library.hold.ready` | Hold promoted to READY on return |
| `library.fine.assessed` | Overdue fine assessed / pay intent created |
| `library.fine.paid` | Treasury reconcile flips fine → PAID |
| `library.ebook.loaned` | CDL lend |
| `library.ebook.expired` | E-book loan expiry (scheduler — Phase 2 notice path) |
| `library.membership.fee_due` | Membership fee assessed |

**Consumed events:**

| Event | Action |
|-------|--------|
| `treasury.payment.succeeded` | Durable queue consumer `library-payment-reconcile` flips the matching `fine`/`membership_fee` (by `treasury_intent_id`, else `reference_id`) to PAID — idempotent on the intent id. Multiple replicas share work via the queue group. |

---

## Multi-Tenancy

- Every business entity carries `tenant_id` (via `TenantMixin`) with a leading index; queries are always tenant-scoped.
- **Global reference data** (`LibraryRole`) has no `tenant_id` — roles/permissions are shared across all tenants.
- `Tenant` is a thin local projection of the auth-api tenant (SoT): caches `slug` + display name for slug→UUID resolution and JIT provisioning. Branding is **not** stored here (auth-api owns branding).
- The URL carries `{tenant}` (slug); claims carry the tenant UUID. The frontend `apiClient` also sends `X-Tenant-ID`/`X-Tenant-Slug` (suppressed for platform owners) + `X-Outlet-ID`.

---

## Authentication

- **JWT validation** via `shared-auth-client` (JWKS from `sso.codevertexafrica.com`, cached 1h, refreshed 5m).
- **API-key auth** for S2S calls (`AUTH_ENABLE_API_KEY_AUTH`, shared `INTERNAL_SERVICE_KEY` via `X-API-Key`).
- All `/api/v1/{tenant}/library` routes require auth; health/metrics/media are open.

---

## Infrastructure

| Component | Config Env (uniform keys) | Default |
|-----------|----------------------------|---------|
| PostgreSQL | `POSTGRES_URL`, `POSTGRES_MAX_OPEN_CONNS`, `POSTGRES_RUN_MIGRATIONS` | `localhost:5432/library` |
| Redis | `REDIS_ADDR`, `REDIS_PASSWORD`, … | `localhost:6380` |
| NATS JetStream | `EVENTS_NATS_URL`, `NATS_STREAM=library`, `NATS_DELIVER_GROUP=library-workers` | `nats://localhost:4222` |
| Auth/JWKS | `AUTH_JWKS_URL`, `AUTH_ISSUER`, `AUTH_AUDIENCE`, `INTERNAL_SERVICE_KEY` | `sso.codevertexafrica.com` |
| Treasury | `TREASURY_SERVICE_URL` | `booksapi.codevertexafrica.com` |
| Notifications | `NOTIFICATIONS_SERVICE_URL` | `notificationsapi.codevertexafrica.com` |
| Subscriptions | `SUBSCRIPTIONS_SERVICE_URL` | `pricingapi.codevertexafrica.com` |
| Media / E-books | `OBJECT_STORAGE_*` from the `object-storage` Secret (httpware/objectstore); `MEDIA_ROOT` only for local development | R2: covers public at `library/<tenant>/covers/<bib>/`, e-books private at `library/<tenant>/ebooks/` |
| HTTP | `HTTP_HOST`, `HTTP_PORT`, `HTTP_ALLOWED_ORIGINS` | `0.0.0.0:4010` |
| Backups | `BACKUP_DIR`, `BACKUP_SCHEDULE_ENABLED`, `BACKUP_RETENTION_DAYS` | PVC fallback |

Secrets are **never** committed — they come from K8s secrets / env. Use placeholders (`<REPLACE_ME>`, `${{ secrets.NAME }}`) in any tracked file.

---

## Migration Strategy

Atlas versioned migrations are the source of truth (no production auto-migrate):

1. Edit `internal/ent/schema/*.go`, then `go generate ./internal/ent/...`.
2. Generate a versioned migration against local PG (`go run internal/ent/migrate/main.go <name>`).
3. Apply via `go run ./cmd/migrate` (or `POSTGRES_RUN_MIGRATIONS=true` on startup, which applies the embedded versioned dir — never an online additive create).

---

## High Availability

Per platform convention, library-api runs **≥2 replicas + a PDB (`minAvailable: 1`)** to avoid rollout/drain 503s. The overdue scheduler and the treasury reconcile consumer are both replica-safe: the scheduler is idempotent, and the consumer is a durable **queue** subscription so work is shared, not duplicated.
