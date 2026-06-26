# Library API - Architecture

**Service:** library-api
**Language:** Go 1.26
**ORM:** Ent (entgo.io/ent v0.14) + Atlas versioned migrations
**HTTP Router:** chi/v5
**Port:** 4010
**Production:** `libraryapi.codevertexitsolutions.com`
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
│   │   │   └── subscription.go # RequireActiveSubscriptionForMutations
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
2. **`RequireAuth`** (`shared-auth-client`) — validates the Bearer JWT against JWKS (`sso.codevertexitsolutions.com`), or an `X-API-Key` for S2S. Claims land in request context.
3. **JIT heal** — `rbac.Service.EnsureUserFromToken` upserts the local `LibraryUser` from JWT claims and re-applies mapped roles **on every request** (not only first-create), so a user provisioned before a role mapping existed self-heals (treasury #30 gotcha).
4. **Mutations-only subscription gate** — `RequireActiveSubscriptionForMutations`: GET/HEAD/OPTIONS always pass; mutations require an active subscription, with superuser / platform-owner / demo / PAYG (`IsGatingExempt`) bypass. Emits the standard `{error,code:"subscription_inactive",upgrade:true}` envelope frontends parse.
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

Loan/policy resolution precedence is **copy → bib → tier → tenant default**. The implemented baseline resolves from the member's **tier** (`MemberTier`: `loan_period_days`, `max_concurrent_loans`, `max_renewals`, `daily_fine_rate`, `max_fine_before_block`). `LoanPolicy` is the reusable policy backing per-copy (`BookCopy.loan_policy_id`) and per-bib (`BibRecord.default_loan_policy_id`) overrides.

### Checkout

1. Member must be `ACTIVE`.
2. **Fine-block** check: outstanding (`amount − amount_paid`) across `UNPAID`/`PARTIAL` fines must be below the tier's `max_fine_before_block`.
3. **Loan-limit** check: active take-home loans `< tier.max_concurrent_loans` (in-house sessions are exempt and do not count).
4. Copy must be `AVAILABLE`, or `RESERVED` **only if** this member holds a READY hold on the bib.
5. In a transaction: flip copy to `ON_LOAN` (or `IN_HOUSE` for reference sessions), create the `Loan` (due = now + `loan_period_days`), fulfill the member's READY hold if present, publish `library.loan.created`.

### Return

1. Find the `ACTIVE` loan for the copy (else `no_active_loan`).
2. In a transaction: mark loan `RETURNED`.
3. If overdue, assess an `OVERDUE` fine = `days_overdue × tier.daily_fine_rate` and publish `library.fine.assessed`.
4. **Hold promotion:** the next `WAITING` hold on the bib (ordered by `queue_position`, then `placed_at`) is promoted to `READY` with a 48h pickup expiry; the copy flips to `RESERVED` and `library.hold.ready` is published. If no hold waits, the copy returns to `AVAILABLE`.
5. Publish `library.loan.returned`.

### Renew

- Blocked by `tier.max_renewals` (`renew_limit`) or by any `WAITING` hold on the bib (`renew_held`). On success, `due_at += loan_period_days`, `renewals_count += 1`, publish `library.loan.renewed`.

### Overdue scheduler

`StartOverdueScheduler` (default hourly) flips past-due `ACTIVE` take-home loans to `OVERDUE` and emits `library.loan.overdue`. It is **idempotent** (only non-OVERDUE loans are touched) so it is safe to run on every replica. Fine accrual itself happens at return time, not in the sweep.

### CDL concurrency

E-book lending (`ebooks.go`) takes a `ForUpdate()` row lock on the `ebook` row, counts active (`returned_at IS NULL`) `EbookLoan`s, and rejects (`cdl_limit`, 409) when at `max_concurrent_loans` — keeping simultaneous reader sessions consistent under load. A successful lend mints a short-lived `access_token` gating `GET /ebooks/{id}/read`. Outright purchase (`ebook_purchase.go`) creates a treasury `ebook_sale` intent + a PENDING `EbookPurchase`; once the reconcile consumer flips it PAID, `GET /ebooks/{id}/download?token=` serves the file (token-gated, `download_count`-tracked).

---

## Transactional Outbox & Event Catalog

**Transport:** NATS JetStream (stream `library`, deliver group `library-workers`).

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

- **JWT validation** via `shared-auth-client` (JWKS from `sso.codevertexitsolutions.com`, cached 1h, refreshed 5m).
- **API-key auth** for S2S calls (`AUTH_ENABLE_API_KEY_AUTH`, shared `INTERNAL_SERVICE_KEY` via `X-API-Key`).
- All `/api/v1/{tenant}/library` routes require auth; health/metrics/media are open.

---

## Infrastructure

| Component | Config Env (uniform keys) | Default |
|-----------|----------------------------|---------|
| PostgreSQL | `POSTGRES_URL`, `POSTGRES_MAX_OPEN_CONNS`, `POSTGRES_RUN_MIGRATIONS` | `localhost:5432/library` |
| Redis | `REDIS_ADDR`, `REDIS_PASSWORD`, … | `localhost:6380` |
| NATS JetStream | `EVENTS_NATS_URL`, `NATS_STREAM=library`, `NATS_DELIVER_GROUP=library-workers` | `nats://localhost:4222` |
| Auth/JWKS | `AUTH_JWKS_URL`, `AUTH_ISSUER`, `AUTH_AUDIENCE`, `INTERNAL_SERVICE_KEY` | `sso.codevertexitsolutions.com` |
| Treasury | `TREASURY_SERVICE_URL` | `booksapi.codevertexitsolutions.com` |
| Notifications | `NOTIFICATIONS_SERVICE_URL` | `notificationsapi.codevertexitsolutions.com` |
| Subscriptions | `SUBSCRIPTIONS_SERVICE_URL` | `pricingapi.codevertexitsolutions.com` |
| Media / E-books | `MEDIA_ROOT`, `MEDIA_URL_BASE`, `EBOOK_ROOT` | per-tenant PVC |
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
