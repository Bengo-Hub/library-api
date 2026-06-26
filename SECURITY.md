# Security Policy

The Library Service holds sensitive operational and patron data. Please follow these guidelines to keep it secure.

## Supported Versions

| Version | Supported |
|---------|-----------|
| `main` branch | ✅ |
| Tagged releases (future) | ✅ |
| Older branches/forks | ❌ |

## Reporting Vulnerabilities

1. Email `security@bengobox.com` with a detailed description (do not open a public issue).
2. Include reproduction steps, impact assessment, and suggested mitigations if possible.
3. Encrypt communications when feasible; public PGP keys are available on request.

We will acknowledge reports within 48 hours and coordinate remediation and disclosure.

## Secure Development Practices

- Never commit credentials or production data. Scan staged changes for secret patterns before every commit.
- Use parameterised queries; Ent handles this by default.
- Validate input rigorously, especially for e-book upload and bulk endpoints.
- Enforce least privilege in database roles and service accounts.
- S2S calls use the shared `INTERNAL_SERVICE_KEY` via `X-API-Key` — never embed it in tracked files.
- Patron PII is held by reference (auth `user_id`, marketflow `crm_contact_id`); do not duplicate it.
- E-book reading sessions are token-gated and short-lived; treat access tokens as secrets.
- Run `govulncheck` / dependency scanners regularly.

## Infrastructure Considerations

- Enable point-in-time recovery for PostgreSQL.
- Protect NATS with TLS and authentication.
- Monitor audit logs (`audit_logs`) for suspicious waivers, withdrawals, or overrides.

Thank you for helping keep the Codevertex platform secure.
