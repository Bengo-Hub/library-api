# Sprint 02 — Data Model (Ent Schemas + Migrations)

**Status:** ✅ Shipped
**Goal:** Define the full library domain model as Ent schemas (`internal/ent/schema/`), with shared mixins, fixed-precision decimal money, tenant scoping, and the first Atlas versioned migration.

---

## Scope

All 24+ entities backing catalog, circulation, members, money, e-books, RBAC, and platform/infra tables. Every business row carries `id` (UUID), `tenant_id`, `created_at`, `updated_at`; indexes lead with `tenant_id`; money is `decimal`, never float.

---

## Task Checklist

### Mixins & helpers
- [x] `mixins.go` — `BaseMixin` (UUID PK + timestamps), `TenantMixin` (`tenant_id` + leading index), `uuidType()` helper.
- [x] `money.go` — `moneyField` (`numeric(18,4)`), `moneyFieldOptional` (nullable), `rateField` (`numeric(10,4)`) backed by `shopspring/decimal`.

### Catalog schemas
- [x] `bibrecord.go` — title/subtitle, isbn10/13, issn, lccn, edition, language, ddc/lc call numbers, publication_year, page_count, publisher (name + id), primary_subject_id, collection_id, `format` enum (PHYSICAL/EBOOK/AUDIOBOOK/PERIODICAL), `record_status` enum (DRAFT/ACTIVE/ARCHIVED/WITHDRAWN), summary, cover_image_url, `authors` JSON, `dublin_core` JSON, `marc` JSON, default_loan_policy_id. Indexes on isbn13/isbn10/format/title.
- [x] `catalog_refs.go` — `Author` (name/sort_name/biography), `Publisher` (name/place), `Subject` (name/code/scheme enum LCSH/DDC/LOCAL/parent_id self-ref), `Collection` (name/code/parent_id/is_reference_only).

### Holdings & branch
- [x] `branch.go` — name/code (unique per tenant), address, lat/long, outlet_id, `opening_hours` JSON, is_default, is_active.
- [x] `bookcopy.go` — bib_record_id, branch_id, barcode (unique per tenant), accession_no, call_number, shelf_location, `status` enum (AVAILABLE/ON_LOAN/RESERVED/IN_HOUSE/IN_TRANSIT/LOST/DAMAGED/REPAIR/WITHDRAWN), condition, is_reference_only, acquisition_cost (money), acquisition_date, loan_policy_id override.

### Members, tiers, policies
- [x] `member.go` — membership_no, user_id (auth ref), crm_contact_id (marketflow ref), tier_id, home_branch_id, display_name/contact_phone/contact_email (cache), `status` enum (ACTIVE/SUSPENDED/EXPIRED/BLOCKED), is_walk_in, joined_at, expires_at.
- [x] `policy.go` — `MemberTier` (max_concurrent_loans, loan_period_days, max_renewals, hold_limit, ebook_concurrent_limit, daily_fine_rate, max_fine_before_block, annual_fee, is_default) + `LoanPolicy` (loan_period_days, max_renewals, holdable, fine_per_day, grace_days, is_default).

### Circulation
- [x] `loan.go` — loan_no, copy_id, member_id, branch_id, checkout_at, due_at, returned_at, renewals_count, `status` enum (ACTIVE/RETURNED/OVERDUE/LOST/CLAIMED_RETURNED), in_house, checked_out_by, returned_by. Indexes on (member,status), (copy,status), (status,due_at).
- [x] `hold.go` — bib_record_id, member_id, branch_id, copy_id (set at fulfillment), queue_position, `status` enum (WAITING/READY/FULFILLED/CANCELLED/EXPIRED), placed_at, ready_at, expires_at.

### Money
- [x] `fine.go` — member_id, loan_id, `reason` enum (OVERDUE/LOST/DAMAGE/MEMBERSHIP/OTHER), description, amount + amount_paid (money), `status` enum (UNPAID/PARTIAL/PAID/WAIVED), treasury_intent_id, waived_by, assessed_at, paid_at.
- [x] `membershipfee.go` — member_id, period_start/end, amount (money), `status` enum (PENDING/PAID/WAIVED/CANCELLED), treasury_intent_id, paid_at.

### E-books
- [x] `ebook.go` — bib_record_id, file_url (relative PVC), `format` enum (PDF/EPUB/AUDIO), `drm_policy` enum (NONE/WATERMARK/TOKEN_GATED), `lending_model` enum (CONTROLLED_DIGITAL/ONE_COPY_ONE_USER/PURCHASE/OPEN), max_concurrent_loans, loan_duration_days, is_purchasable, price (money), file_size, checksum.
- [x] `ebookloan.go` — `EbookLoan` (ebook_id, member_id, `mode` enum ONLINE_READ/DOWNLOAD, issued_at, expires_at, returned_at, access_token, last_read_position JSON) + `EbookPurchase` (ebook_id, member_id, treasury_intent_id, amount, `status` enum PENDING/PAID/REFUNDED, download_token, download_count, purchased_at).

### RBAC & platform/infra
- [x] `rbac.go` — `LibraryRole` (global, no tenant_id; name unique, permissions JSON, is_system) + `LibraryUser` (tenant_id + user_id unique, email, display_name, roles JSON, is_active).
- [x] `auditlog.go` — user_id, aggregate_type, aggregate_id, action, changes JSON, ip_address.
- [x] `documentsequence.go` — kind (unique per tenant), prefix, next_value, pad_width.
- [x] `serviceconfig.go` — tenant_id nullable (platform default vs override), config_key (+ unique with tenant), config_value, config_type, description, is_secret.
- [x] `outboxevent.go` — column layout matching shared-events SQL outbox repo exactly (no mixins): tenant_id, aggregate_type, aggregate_id, event_type, payload (raw JSON), status, attempts, last_attempt_at, published_at, error_message, created_at.
- [x] `tenant.go` — id (mirrors auth UUID), slug (unique), name, region, is_active, timestamps.

### Generation & migration
- [x] `go generate ./internal/ent/...` produces the Ent client.
- [x] First Atlas versioned migration generated and applied.
- [x] `SELECT … FOR UPDATE` row-lock capability confirmed for the sequence allocator + CDL lend.

---

## Acceptance Criteria

- [x] All schemas compile and generate; FK columns use UUID.
- [x] Money columns are `numeric(18,4)`; rates `numeric(10,4)`; never float.
- [x] `LibraryRole` has no `tenant_id`; every other business table does.
- [x] `outbox_events` matches the shared-events repository column layout.
- [x] Migration applies cleanly on a fresh DB.

---

## Dependencies

- Sprint 01 (Ent client + migrate scaffolding).
