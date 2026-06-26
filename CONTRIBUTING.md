# Contributing Guide

We welcome contributions to the Library Service. Please review this guide before submitting changes.

## Environment Setup

1. Install Go 1.26, Docker, and make.
2. Provision PostgreSQL, Redis, and NATS (see future `docker-compose.yml`).
3. Copy sample environment variables when available; never commit a populated `.env`.
4. Run `go generate ./internal/ent/...` whenever schema files change, then generate an Atlas migration.

## Workflow

1. Branch from `main`.
2. Implement changes with clear, self-contained commits.
3. Run `go fmt ./...`, `golangci-lint run`, and `go test ./...`.
4. Update docs (`docs/plan.md`, `docs/erd.md`, `docs/api-reference.md`, READMEs) as needed.
5. Open a pull request describing the changes, rationale, and testing.

## Coding Standards

- Follow idiomatic Go patterns and clean architecture boundaries.
- Keep module interfaces small; prefer dependency injection over globals.
- Money is always `shopspring/decimal` via the `moneyField`/`rateField` helpers — never float.
- Mutations publish via the transactional outbox (`events.Publish` with `tx.OutboxEvent`), never directly to NATS.
- Use table-driven tests; leverage Testcontainers for DB/Redis integration tests.
- Ensure migrations are reversible and reviewed together with schema changes.

## Commit Style

- Use descriptive messages (`library: add hold promotion on return`).
- Reference task/issue IDs where applicable.
- Avoid large mixed commits; keep concerns separated.

## Issue Reporting

- Provide reproduction steps, expected vs actual behaviour, service logs.
- Tag severity (`bug`, `enhancement`, `question`, `security`).
- For security concerns, follow the guidance in `SECURITY.md`.

## Communication

- Slack channel: `#bengobox-library`.
- Weekly triage: Wednesdays 14:00 EAT.
- Architecture decisions recorded as ADRs in `docs/`.

Thanks for helping build a world-class library platform!
