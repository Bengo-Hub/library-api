# Library Service (library-api)

The Library Service is the Library Management System backbone for the Codevertex platform — a standalone Go microservice covering the **bibliographic catalog/OPAC**, **circulation** (checkout/return/renew, holds, in-house reading), **members + fines/fees**, and **e-books** (in-browser reader + Controlled Digital Lending). It integrates with the existing fabric (auth/SSO, treasury, notifications, subscriptions, marketflow) **by reference** — no cross-service PII duplication — and is keyed to the shared `tenant` registry that spans every Go microservice.

## Highlights

- Bibliographic master (BibRecord) with ISBN lookup, MARC-lite + Dublin Core payloads, and physical copies + e-books hanging off it.
- Scan-driven circulation rules engine: checkout/return/renew with fine-block and loan-limit enforcement, in-house/reference sessions, and a holds queue that promotes the next waiting hold on return.
- Members with tiers + loan policies; overdue/lost/damage **fines** and periodic **membership fees** settled via treasury payment intents.
- E-books with Controlled Digital Lending (concurrency-capped, token-gated in-browser reader); Phase-2 one-time purchase + secured download.
- Transactional outbox → NATS (`{aggregate_type}.{event_type}`) and a treasury reconcile consumer.

## Stack

- Go 1.26, chi v5 router, Ent v0.14 ORM + **Atlas versioned migrations** (no prod auto-migrate).
- PostgreSQL (pgx / pgbouncer), Redis (`@bengo-hub/cache`), NATS JetStream + transactional outbox (`shared-events`).
- Shared libs: `shared-auth-client` (JWKS + API-key), `httpware`, `shared-events`, `cache`, `pagination`.
- Money via `shopspring/decimal` (`numeric(18,4)`) — never float. Observability via zap.

## Repository Layout

```
cmd/{api,migrate,seed}            entrypoints (server / Atlas migrate / idempotent seed)
internal/config                   envconfig
internal/platform/{database,cache,events,treasury,notifications,subscriptions}  infra + S2S adapters
internal/shared/logger            zap logger
internal/ent/schema               Ent schemas (bib, copy, member, loan, hold, fine, ebook, …)
internal/ent/migrate              Atlas versioned migrations (embedded-FS fallback)
internal/events                   outbox publish helper (aggregate_type = "library")
internal/modules                  circulation · rbac · sequence · barcode · consumers
internal/http/{handlers,middleware,router}   HTTP layer
docs/                             plan, architecture, erd, integrations, rbac-and-seed, api-reference, sprints
```

## Local Development

```bash
# 1. Generate Ent code (after editing internal/ent/schema/*.go)
go generate ./internal/ent/...

# 2. Generate an Atlas migration against local PG17 (db: library, schema: ent_dev)
psql -h localhost -U postgres -d library -c "DROP SCHEMA IF EXISTS ent_dev CASCADE; CREATE SCHEMA ent_dev;"
POSTGRES_URL="postgres://postgres:postgres@localhost:5432/library?sslmode=disable" \
  go run -mod=mod internal/ent/migrate/main.go <migration_name>

# 3. Apply migrations + build + run
go run ./cmd/migrate
go build ./... && go run ./cmd/api

# 4. (optional) seed demo data for a tenant
SEED_TENANT_ID=<tenant-uuid> go run ./cmd/seed
```

Default port is `4010` (`HTTP_PORT`). Production: `https://libraryapi.codevertexitsolutions.com`.

## Environment

Configuration is via env vars (see `internal/config/config.go`). Common keys: `POSTGRES_URL`, `REDIS_ADDR`, `EVENTS_NATS_URL`, `NATS_STREAM=library`, `AUTH_JWKS_URL`, `AUTH_ISSUER`, `AUTH_AUDIENCE`, `INTERNAL_SERVICE_KEY`, `TREASURY_SERVICE_URL`, `NOTIFICATIONS_SERVICE_URL`, `SUBSCRIPTIONS_SERVICE_URL`, `MEDIA_ROOT`, `EBOOK_ROOT`, `HTTP_ALLOWED_ORIGINS`.

> **Never commit secrets.** Use K8s secrets / env at runtime and placeholders (`<REPLACE_ME>`, `${{ secrets.NAME }}`) in any tracked file.

## Key Endpoints

All routes are mounted under `/api/v1/{tenant}/library` (see `docs/api-reference.md`):

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/healthz`, `/readyz`, `/metrics` | Probes (root, unauthenticated) |
| GET | `/auth/me` | Identity + effective permissions (JWT ∪ local RBAC) |
| GET/POST/PUT/DELETE | `/catalog/bibs…`, `/catalog/copies…` | Bibliographic + copy management |
| POST | `/circulation/{checkout,return,renew}` | Circulation desk |
| GET/POST/DELETE | `/holds…` | Holds queue |
| GET/POST/PUT | `/members…`, `/member-tiers…`, `/loan-policies…` | Patron management |
| GET/POST | `/fines…` (waive/pay) | Fines (treasury-settled) |
| GET/POST | `/ebooks…` (lend/read/position) | Digital shelf + CDL reader |
| GET | `/reports/summary` | Dashboard counts |
| GET/PUT | `/rbac…`, `/team…` | Roles + team |

## Documentation

- [`docs/plan.md`](docs/plan.md) — product + technical plan, phased roadmap.
- [`docs/architecture.md`](docs/architecture.md) — service architecture, request lifecycle, circulation engine, outbox.
- [`docs/erd.md`](docs/erd.md) — entity-relationship model (with Mermaid erDiagram).
- [`docs/integrations.md`](docs/integrations.md) — auth/treasury/notifications/subscriptions/marketflow + NATS catalog.
- [`docs/rbac-and-seed.md`](docs/rbac-and-seed.md) — roles, permissions, JIT heal, seed.
- [`docs/api-reference.md`](docs/api-reference.md) — grouped REST reference.
- [`docs/sprints/`](docs/sprints/) — sprint logs.

## Project Status

Phase 1 (MVP) shipped — see `CHANGELOG.md` and `docs/plan.md`.
