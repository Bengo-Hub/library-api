# Library API - REST Reference

This is the grouped endpoint reference for library-api. **Every route below is registered in `internal/http/router/router.go`** under the single mount `/api/v1/{tenant}/library`. `{tenant}` is the tenant **slug**; it must match the tenant claim in the JWT.

**Base URL:** `https://libraryapi.codevertexitsolutions.com/api/v1/{tenant}/library`

---

## Authentication

Every request must include one of:

- **Bearer JWT** (from SSO): `Authorization: Bearer <token>`
- **API key** (S2S): `X-API-Key: <INTERNAL_SERVICE_KEY>`

All routes pass through `RequireAuth` → JIT heal → mutations-only subscription gate → (where mounted) `RequireServicePermission`.

**List responses** use a `listEnvelope`: `{ "data": [...], "total": <int> }`. **Errors** use `{ "error": "...", "code": "..." }` (or `{ "message", "code" }`), HTTP 4xx/5xx.

---

## Health (root, unauthenticated)

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/healthz` | Liveness |
| GET | `/readyz` | Readiness (DB/Redis/NATS) |
| GET | `/metrics` | Metrics |
| GET | `/v1/docs`, `/v1/docs/*` | Swagger UI |
| GET | `/api/v1/openapi.json` | OpenAPI spec |
| GET | `/` | Redirects to `/v1/docs/` |
| GET | `/media/*` | Static media file server (cover images, e-book files) when `MEDIA_ROOT` set |

---

## Auth & Reports

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| GET | `/auth/me` | Service identity + effective permissions (JWT ∪ local RBAC) | → `user_id`, `tenant_id`, `tenant_slug`, `roles[]`, `permissions[]`, `is_platform_owner`, `is_superuser` |
| GET | `/reports/summary` | Dashboard summary counts | → `active_loans`, `overdue_loans`, `holds_ready`, `holds_waiting`, `members`, `titles`, `copies` |

---

## Catalog (`/catalog`)

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| GET | `/catalog/bibs` | List bibliographic records | `?q=` (title/ISBN), `?format=` (PHYSICAL\|EBOOK\|AUDIOBOOK\|PERIODICAL), paging → `listEnvelope` |
| POST | `/catalog/bibs` | Create a bib record | `title`*, `subtitle`, `isbn13`, `isbn10`, `authors[]`, `publisher_name`, `format`, `language`, `ddc_classification`, `lc_call_number`, `publication_year`, `page_count`, `summary`, `cover_image_url` |
| GET | `/catalog/search` | OPAC search | `?q=` → `listEnvelope` |
| GET | `/catalog/isbn/{isbn}` | ISBN lookup (metadata pre-fill) | → bib metadata |
| GET | `/catalog/bibs/{id}` | Get one bib | → `ent.BibRecord` |
| PUT | `/catalog/bibs/{id}` | Update a bib | same body as create |
| DELETE | `/catalog/bibs/{id}` | Delete a bib | — |
| GET | `/catalog/bibs/{id}/copies` | List copies of a bib | → `listEnvelope` |
| POST | `/catalog/copies` | Create a copy (holding) | `bib_record_id`*, `branch_id`*, `barcode`*, `accession_no`, `call_number`, `shelf_location`, `is_reference_only`, `acquisition_cost`, `loan_policy_id` |
| PUT | `/catalog/copies/{id}` | Update a copy | copy fields incl. `status` |
| GET | `/catalog/copies/by-barcode/{barcode}` | Resolve a copy by scanned barcode | → `ent.BookCopy` |
| GET | `/catalog/copies/{id}/label.pdf` | Copy spine-label PDF | → `application/pdf` (Blob) |

---

## Branches

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| GET | `/branches` | List branches | → `listEnvelope` |
| POST | `/branches` | Create a branch | `name`*, `code`*, `address`, `latitude`, `longitude`, `opening_hours`, `is_default` |
| PUT | `/branches/{id}` | Update a branch | branch fields |

---

## Members, Tiers & Policies

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| GET | `/members` | List members | `?q=`, `?status=`, `?tier_id=`, paging → `listEnvelope` |
| POST | `/members` | Register a member (allocates `membership_no`) | `display_name`, `contact_phone`, `contact_email`, `tier_id`, `home_branch_id`, `user_id`, `crm_contact_id`, `is_walk_in` |
| GET | `/members/{id}` | Get one member | → `ent.Member` |
| PUT | `/members/{id}` | Update a member | member fields |
| GET | `/members/{id}/loans` | A member's loans | → loan list |
| GET | `/members/{id}/fines` | A member's fines | → fine list |
| GET | `/member-tiers` | List tiers | → `listEnvelope` |
| POST | `/member-tiers` | Create a tier | `name`*, `max_concurrent_loans`, `loan_period_days`, `max_renewals`, `hold_limit`, `ebook_concurrent_limit`, `daily_fine_rate`, `max_fine_before_block`, `annual_fee`, `is_default` |
| PUT | `/member-tiers/{id}` | Update a tier | tier fields |
| GET | `/loan-policies` | List loan policies | → `listEnvelope` |
| POST | `/loan-policies` | Create a policy | `name`*, `loan_period_days`, `max_renewals`, `holdable`, `fine_per_day`, `grace_days`, `is_default` |

---

## Circulation (`/circulation`)

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| POST | `/circulation/checkout` | Check out a copy to a member (scan-driven) | `member_id`*, `copy_id` **or** `copy_barcode`, `in_house` → `201` `ent.Loan` |
| POST | `/circulation/return` | Check a copy back in | `copy_id` **or** `copy_barcode` → `{ loan, fine?, promoted_hold? }` |
| POST | `/circulation/renew/{loan_id}` | Renew an active loan | → updated `ent.Loan` |
| GET | `/circulation/loans` | List loans | `?status=`, `?member_id=`, `?overdue=true`, paging → `listEnvelope` |

**Circulation error codes (409/404):** `member_not_active`, `member_blocked`, `loan_limit`, `copy_unavailable`, `no_active_loan`, `renew_limit`, `renew_held`.

---

## Holds

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| GET | `/holds` | List holds | `?status=`, `?member_id=`, `?bib_record_id=`, paging → `listEnvelope` |
| POST | `/holds` | Place a hold on a bib | `bib_record_id`*, `member_id`*, `branch_id` |
| DELETE | `/holds/{id}` | Cancel a hold | — |

---

## Fines

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| GET | `/fines` | List fines | `?status=`, `?member_id=` → `listEnvelope` |
| POST | `/fines/{id}/waive` | Waive a fine (sensitive — audited; sets `waived_by`) | → updated `ent.Fine` |
| POST | `/fines/{id}/pay` | Create a treasury payment intent for the outstanding amount | → `{ intent_id, initiate_url, amount }` |

> The pay endpoint creates a treasury intent (`reference_type=library_fine`), stores `treasury_intent_id`, and publishes `library.fine.assessed`. The actual flip to PAID happens asynchronously when the `treasury.payment.succeeded` consumer fires.

---

## E-Books (`/ebooks`)

| Method | Path | Purpose | Key fields |
|--------|------|---------|------------|
| GET | `/ebooks` | List e-books | → `listEnvelope` |
| POST | `/ebooks` | Register an e-book record (file uploaded to the media PVC) | `bib_record_id`*, `file_url`*, `format`, `lending_model`, `max_concurrent_loans`, `loan_duration_days` |
| POST | `/ebooks/{id}/lend` | Borrow (Controlled Digital Lending — concurrency-limited) | `member_id`* → `{ loan_id, access_token, expires_at }`; `409 cdl_limit` when all copies lent |
| GET | `/ebooks/{id}/read` | Open a token-gated reading session | `?token=` → `{ file_url, format, watermark, last_read_position, expires_at }` |
| POST | `/ebooks/loans/{id}/position` | Persist reading progress | `position` (JSON) → `{ saved: true }` |
| POST | `/ebooks/{id}/purchase` | Buy an e-book outright — creates a treasury `ebook_sale` intent | `member_id` → `{ purchase_id, intent_id, initiate_url, amount }`; `409 not_purchasable` |
| GET | `/ebooks/{id}/download` | Download a purchased e-book (requires a PAID purchase) | `?token=` → `{ file_url, format, download_count }`; `403 not_paid` |

---

## RBAC / Team

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/rbac/roles` | List global library roles |
| GET | `/rbac/permissions` | List the permission catalog (`library.{module}.{action}`) |
| GET | `/team` | List the tenant's provisioned library users |
| PUT | `/team/{user_id}/roles` | Assign roles to a team member (`{ roles: [...] }`) |

---

## Notes

- `*` marks a required field.
- All money fields serialize as **strings** (decimal precision), e.g. `"amount": "120.0000"`.
- Swagger UI is served at `/v1/docs` (OpenAPI at `/api/v1/openapi.json`); handlers carry godoc annotations.
- The companion `library-ui` anticipates a few endpoints not yet in `router.go` (e.g. `/reports/popular`, `/reports/circulation`, `/reports/overdue`, `/fines/membership`, `/catalog/authors|publishers|subjects`, `/catalog/bibs/{id}/cover`, copy `DELETE`, `rbac/roles/{id}` PUT). These are Phase-2 additions; the UI degrades gracefully where they are absent.
