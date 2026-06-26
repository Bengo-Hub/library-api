# Sprint 01 — Foundation & Platform Infra

**Status:** ✅ Shipped
**Goal:** Stand up the library-api Go microservice skeleton — module, shared libs, config, platform adapters (DB/cache/events), logger, health probes, container + Atlas migration scaffolding — so every later domain sprint has a running, observable, deployable host.

---

## Scope

The bootstrap that all four pillars depend on. No domain logic; just a service that starts, connects to its infra, serves health, and is ready for schemas + handlers.

---

## Task Checklist

### Module & dependencies
- [x] `go.mod` (`github.com/bengobox/library-service`), Go 1.26.
- [x] Shared libs wired: `shared-auth-client`, `httpware`, `shared-events`, `@bengo-hub/cache`, `pagination`.
- [x] Core deps: `entgo.io/ent` v0.14, `chi/v5`, `pgx/v5`, `go-redis/v9`, `nats.go`, `zap`, `shopspring/decimal`, `kelseyhightower/envconfig`, `joho/godotenv`.

### Config (`internal/config/config.go`)
- [x] envconfig structs: `App`, `HTTP`, `Postgres`, `Redis`, `Events`, `Telemetry`, `Auth`, `Media`, `Services`, `Backup`, `Subscriptions`.
- [x] Defaults: `HTTP_PORT=4010`, `NATS_STREAM=library`, `NATS_DELIVER_GROUP=library-workers`, production service URLs.
- [x] `Load()` reads `.env` (optional) then env vars; never panics on missing secrets.

### Platform adapters (`internal/platform/`)
- [x] `database/postgres.go` — pgxpool with max-open/idle, conn lifetime, statement + idle-in-transaction timeouts.
- [x] `cache/redis.go` — go-redis client (addr/username/password/db/TLS/dial-timeout).
- [x] `events/nats.go` — NATS connect + `EnsureStream` for the `library` JetStream stream.
- [x] `shared/logger/logger.go` — zap logger keyed to `APP_ENV`.

### App wiring (`internal/app/app.go`, `cmd/api/main.go`)
- [x] `app.New(ctx)` — load config → logger → DB pool → Redis → NATS (+ EnsureStream, warn-not-fatal) → Ent client over pgx → handlers → HTTP server.
- [x] `app.Run(ctx)` — start background workers + HTTP server (HTTP or HTTPS), block until ctx cancelled.
- [x] `app.Close()` — reverse-order resource teardown (outbox → NATS drain → Redis → DB pool → Ent → logger sync).
- [x] Graceful shutdown on signal with a 10s timeout.

### Health & observability
- [x] `GET /healthz` liveness, `GET /readyz` readiness (DB/Redis/NATS), `GET /metrics`.
- [x] httpware middleware stack: `RequestID`, `RealIP`, `Logging`, `Recover`, 30s `Timeout`, 50 MB `RequestSize`, CORS.

### Migrations & ops scaffolding
- [x] `internal/ent/migrate` Atlas versioned-migration dir (embedded-FS fallback pattern).
- [x] `cmd/migrate/main.go` Atlas migrate runner; `POSTGRES_RUN_MIGRATIONS` applies versioned files only (no prod auto-migrate).
- [x] `Dockerfile` + `build.sh` + `.github/workflows/deploy.yml`; `devops-k8s/apps/library-api` (Helm).

---

## Acceptance Criteria

- [x] `go build ./...` and `go run ./cmd/api` start the service on `:4010`.
- [x] `/healthz` returns 200; `/readyz` reflects DB/Redis/NATS reachability.
- [x] Missing optional infra (NATS) degrades to a warning, not a crash.
- [x] Migrations apply via `cmd/migrate`; no online auto-migrate in prod.

---

## Dependencies

- Shared module registry (`shared-auth-client`, `httpware`, `shared-events`, `cache`, `pagination`) available.
- Postgres / Redis / NATS reachable locally for dev.
