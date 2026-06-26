# Library Service — Integration Guide

This document maps every cross-service integration for library-api: the auth/SSO fabric, treasury (payments), notifications, subscriptions (entitlement gating), and marketflow (CRM). The guiding rule is **integration by reference, no PII duplication** — library-api holds foreign references (`user_id`, `crm_contact_id`, `treasury_intent_id`), never copies of another service's owned data.

---

## Table of Contents

1. [Auth / SSO](#auth--sso)
2. [Treasury](#treasury)
3. [Notifications](#notifications)
4. [Subscriptions](#subscriptions)
5. [Marketflow (CRM)](#marketflow-crm)
6. [Data-ownership boundaries](#data-ownership-boundaries)
7. [NATS subject catalog](#nats-subject-catalog)
8. [Integration security](#integration-security)

---

## Auth / SSO

**Integration type:** OIDC/OAuth2 (PKCE for the UI) + JWT validation + S2S API key.

- **JWT validation** via `shared-auth-client`: JWKS fetched from `AUTH_JWKS_URL` (`sso.codevertexitsolutions.com/api/v1/.well-known/jwks.json`), cached 1h, refreshed 5m. Issuer/audience from `AUTH_ISSUER`/`AUTH_AUDIENCE` (`codevertex`).
- **All `/api/v1/{tenant}/library` routes** require a Bearer JWT (or an `X-API-Key` for S2S when `AUTH_ENABLE_API_KEY_AUTH=true`).
- **JIT provisioning (heal-existing-users):** on every authenticated request, `rbac.EnsureUserFromToken` upserts the local `LibraryUser` from claims and re-applies mapped roles. Existing users self-heal when a role mapping changes (treasury #30 gotcha) — there is no "first-login only" gap.
- **Role mapping:** SSO global roles → library roles (`MapGlobalRoles`): `superuser`/`admin`/`owner`/`platform_owner` → `library_admin`; `staff`/`cashier`/`manager` → `library_staff`; everything else → `library_member`.
- **`GET /auth/me`** returns the effective identity: **JWT permissions ∪ local RBAC permissions** (the UI bootstraps RBAC from this after SSO login).

**Env:** `AUTH_SERVICE_URL`, `AUTH_ISSUER`, `AUTH_AUDIENCE`, `AUTH_JWKS_URL`, `INTERNAL_SERVICE_KEY`.

---

## Treasury

**Integration type:** S2S REST (charge) + NATS event (reconcile).

Library charges (overdue/lost/damage **fines**, **membership fees**, and **e-book sales**) are never settled inside library-api — they create a **treasury payment intent** and let treasury own the gateway flow.

**Charge path (S2S):** `treasury.Client.CreateIntent` → `POST /api/v1/s2s/{tenant}/payments/intents`

```json
{
  "source_service": "library",
  "reference_id":   "<fine|fee|purchase UUID>",
  "reference_type": "library_fine | membership_fee | ebook_sale",
  "amount":         120.00,
  "currency":       "KES",
  "payment_method": "pending",
  "description":    "Library fine payment"
}
```

- Sent with `X-API-Key: ${INTERNAL_SERVICE_KEY}` (no user JWT on the S2S path) and an `Idempotency-Key` (the fine/fee UUID) to prevent duplicate intents on retries.
- The returned `intent_id` is stored on the fine/fee (`treasury_intent_id`); the response `initiate_url` is handed to the shared pay page.

**Reconcile path (NATS):** the `library-payment-reconcile` durable queue consumer subscribes to **`treasury.payment.succeeded`** and, switching on `reference_type`, flips the matching **fine** (`library_fine`), **membership fee** (`membership_fee`), or **e-book purchase** (`ebook_sale`) to **PAID** — matched by `treasury_intent_id`, else `reference_id`; idempotent on the intent id (already-PAID is a no-op). For fines it then publishes `library.fine.paid`.

**Env:** `TREASURY_SERVICE_URL` (default `booksapi.codevertexitsolutions.com`), `INTERNAL_SERVICE_KEY`.

---

## Notifications

**Integration type:** Events (primary) + REST fallback.

- **Primary (event-driven):** library outbox events are consumed by a notifications worker that renders `library/*` templates (overdue notice, hold-ready, fine-assessed, fee-due). This keeps circulation actions non-blocking.
- **Fallback (REST):** `notifications.Client.Send` → `POST /{tenant}/notifications/messages` for synchronous, on-demand sends. Best-effort — a failure is logged and ignored (notifications must **never** block a circulation action).
- Email is rate-limited by plan on the notifications side; SMS/push/WhatsApp are never blocked.

**Template ↔ event map (Phase 2 wiring):**

| Library event | Notification template |
|---------------|------------------------|
| `library.loan.overdue` | `library/overdue-notice` |
| `library.hold.ready` | `library/hold-ready` |
| `library.fine.assessed` | `library/fine-assessed` |
| `library.membership.fee_due` | `library/membership-fee-due` |
| `library.ebook.loaned` | `library/ebook-loaned` |

**Env:** `NOTIFICATIONS_SERVICE_URL`, `INTERNAL_SERVICE_KEY`.

---

## Subscriptions

**Integration type:** mutations-only gate (UI/API) + S2S entitlement client (consumers).

- **Mutations-only gate:** `RequireActiveSubscriptionForMutations` lets all GET/HEAD/OPTIONS through; mutations require an active subscription. Superuser / platform-owner / demo-bypass / PAYG (`IsGatingExempt`) tenants always pass. The 403 envelope is `{error,code:"subscription_inactive",upgrade:true}` (frontends open the upgrade flow).
- **Consumer gating (S2S):** `subscriptions.Client.ConsumerHasFeature(tenant_id, feature_code)` mirrors the inventory-api client — cached (60s) and **fail-open** (a subscriptions outage never drops event processing). Demo-bypass and `billing_mode=service_charge` (PAYG) tenants are always allowed.
- Feature catalog uses `library_*` codes (e.g. `library_circulation`, `library_ebooks`).

**Env:** `SUBSCRIPTIONS_SERVICE_URL` (default `pricingapi.codevertexitsolutions.com`), `INTERNAL_SERVICE_KEY`. Endpoint: `GET /api/v1/tenants/{id}/subscription` (tenant-scoped S2S — not `/subscription`).

---

## Marketflow (CRM)

**Integration type:** reference link (optional enrich).

- A `Member` may carry a `crm_contact_id` referencing a marketflow contact. **Marketflow is the customer SoT** (`crm_data_ownership_sync`); library-api stores only the reference plus a cached `display_name`/contact for desk UX.
- No PII is duplicated: phone/email cached on `Member` are a convenience cache, not the source of truth.

**Env:** `MARKETFLOW_SERVICE_URL` (default `marketflowapi.codevertexitsolutions.com`).

---

## Data-Ownership Boundaries

| Data | Owner (SoT) | library-api holds |
|------|-------------|-------------------|
| User identity / login | auth-api | `Member.user_id`, `LibraryUser.user_id` (refs) |
| Tenant + branding | auth-api | thin `Tenant` projection (slug/name); branding via Redis cache |
| Customer / contact | marketflow | `Member.crm_contact_id` (ref) + cached display name |
| Payments / GL | treasury | `treasury_intent_id` on fine/fee/purchase |
| Roles/permissions (codes) | **library-api** | `LibraryRole` (global) — library owns its own `library.{module}.{action}` codes |
| Catalog / circulation / members | **library-api** | full ownership |

---

## NATS Subject Catalog

All subjects follow `{aggregate_type}.{event_type}`; library's `aggregate_type` is always `"library"`. Stream `library`, deliver group `library-workers`.

### Published (via transactional outbox)

| Subject | Payload (key fields) |
|---------|----------------------|
| `library.member.registered` | `member_id`, `membership_no`, `tier_id` |
| `library.loan.created` | `loan_id`, `member_id`, `copy_id`, `due_at`, `in_house` |
| `library.loan.renewed` | `loan_id`, `new_due_at`, `renewals` |
| `library.loan.returned` | `loan_id`, `member_id`, `copy_id` |
| `library.loan.overdue` | `loan_id`, `member_id`, `due_at` |
| `library.hold.ready` | `hold_id`, `member_id`, `bib_record_id`, `expires_at` |
| `library.fine.assessed` | `fine_id`, `member_id`, `amount`, `reason` |
| `library.fine.paid` | `fine_id`, `intent_id` |
| `library.ebook.loaned` | `ebook_loan_id`, `ebook_id`, `member_id`, `expires_at` |
| `library.ebook.expired` | `ebook_loan_id`, `ebook_id`, `member_id` |
| `library.membership.fee_due` | `fee_id`, `member_id`, `period_end`, `amount` |

### Consumed

| Subject | Consumer (durable) | Action |
|---------|--------------------|--------|
| `treasury.payment.succeeded` | `library-payment-reconcile` (queue group) | Reconcile matching fine / membership-fee / e-book purchase → PAID by `reference_type` (`library_fine` / `membership_fee` / `ebook_sale`), idempotent on `treasury_intent_id` (fallback `reference_id`). Replica-safe via the queue group. |

---

## Integration Security

- **JWT** validated via JWKS; claims carry `tenant_id` for scoping.
- **S2S** uses the shared `INTERNAL_SERVICE_KEY` via `X-API-Key` (never a per-service key, never a user JWT on the S2S path).
- **Tenant isolation** is enforced at the query level (every business query is `tenant_id`-scoped); global reference data (roles) is the only exception.
- **Idempotency** on charge (`Idempotency-Key` = fine/fee UUID) and reconcile (keyed on `treasury_intent_id`).
- **Secrets** come from K8s secrets / env — never committed. Tracked files use placeholders (`<REPLACE_ME>`, `${{ secrets.NAME }}`).

---

## References

- Auth integration: `auth-service/.../docs/integrations.md`
- Treasury integration: `finance-service/treasury-api/docs/integrations.md`
- Notifications integration: `notifications-service/.../docs/integrations.md`
- RBAC + `/auth/me` union pattern: `library-api/docs/rbac-and-seed.md`
