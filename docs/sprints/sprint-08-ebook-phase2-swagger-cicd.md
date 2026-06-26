# Sprint 08 — E-book Purchase/Download, Swagger & CI/CD

**Status:** ✅ Shipped (DRM byte-stream hardening pending)
**Goal:** Turn on the e-book one-time purchase + token-gated download path (treasury `ebook_sale` + reconcile), resolve the member from the JWT, ship Swagger/OpenAPI docs, and finalise the build/deploy pipeline.

---

## Scope

The Phase-2 e-book commerce flow plus developer-experience and deployment plumbing.

---

## Task Checklist

### E-book purchase (`handlers/ebook_purchase.go`)
- [x] `POST /ebooks/{id}/purchase` — guard `is_purchasable` (`409 not_purchasable`) + treasury wired; create a PENDING `EbookPurchase` with a `download_token`; create a treasury intent (`reference_type=ebook_sale`, idempotent on the purchase UUID); store `treasury_intent_id`; return `{purchase_id, intent_id, initiate_url, amount}`.
- [x] `GET /ebooks/{id}/download` — token-gated: require a PAID `EbookPurchase` for the token (`403 not_paid`); increment `download_count`; return the resolved file location.
- [x] `resolveMemberID` — resolve the member from the request body, falling back to the JWT-linked membership (`no_member` when none).

### Treasury reconcile extension (`internal/modules/consumers/payment.go`)
- [x] `reference_type=ebook_sale` → `reconcilePurchase` flips the matching `EbookPurchase` → PAID (by intent id, else reference id; idempotent).

### Swagger / OpenAPI (`handlers/swagger.go`)
- [x] Embedded `swagger.yaml` → `OpenAPIJSON` at `GET /api/v1/openapi.json`.
- [x] `SwaggerUI` at `GET /v1/docs` (+ `/v1/docs/*`); root `/` redirects to `/v1/docs/`.
- [x] godoc `@Summary`/`@Router` annotations across handlers.

### CI/CD & deploy
- [x] `Dockerfile` + `build.sh`.
- [x] `.github/workflows/deploy.yml`.
- [x] `devops-k8s/apps/library-api` (Helm values; HA ≥2 replicas + PDB convention).
- [x] `cmd/seed` idempotent: global roles always; demo data on `SEED_TENANT_ID` (branch, default tier, sample bibs + copies).

### Pending hardening
- [ ] Byte-stream the download from the PVC with a per-purchase watermark; TOKEN_GATED `drm_policy` enforcement; signed short-TTL URLs (current handler returns the resolved file location).
- [ ] `library.ebook.purchased` outbox event.

---

## Acceptance Criteria

- [x] Purchasing a sellable e-book creates one treasury intent + a PENDING purchase, and returns an initiate URL.
- [x] After payment, the reconcile consumer marks the purchase PAID; download then succeeds with the token and increments the count.
- [x] Non-purchasable e-books reject purchase; unpaid tokens reject download.
- [x] Swagger UI loads at `/v1/docs` and serves the spec.
- [x] Service builds, containerises, and deploys via CI.

---

## Dependencies

- Sprint 06 (e-book registry), Sprint 07 (treasury client + reconcile consumer), Sprint 02 (`ebook_purchases` schema).
