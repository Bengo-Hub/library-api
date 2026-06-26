# Sprint 04 — Catalog / OPAC, Copies & Branches

**Status:** ✅ Shipped
**Goal:** Ship the bibliographic catalog (BibRecord CRUD, OPAC search with availability, ISBN lookup), physical copies (CRUD + accession sequence + barcode label PDF), branches, and the supporting reference data.

---

## Scope

The "what the library owns" layer: titles (works), their physical copies at branches, and the OPAC read surface.

---

## Task Checklist

### Bibliographic records (`handlers/catalog_bibs.go`)
- [x] `GET /catalog/bibs` — list with `?q=` (title/ISBN fold search), `?format=` filter, pagination → `listEnvelope`.
- [x] `POST /catalog/bibs` — create (title, subtitle, isbn13/10, authors[], publisher_name, format, language, ddc, lc_call_number, publication_year, page_count, summary, cover_image_url).
- [x] `GET /catalog/bibs/{id}`, `PUT /catalog/bibs/{id}`, `DELETE /catalog/bibs/{id}`.
- [x] `GET /catalog/search` — OPAC search (`?q=`).
- [x] `GET /catalog/isbn/{isbn}` — local-first lookup, then **OpenLibrary** fallback (`fetchOpenLibrary`) returning `{source, metadata}`.

### Copies / holdings (`handlers/catalog_copies.go`, `copy_label.go`)
- [x] `GET /catalog/bibs/{id}/copies` — list copies of a bib.
- [x] `POST /catalog/copies` — create copy (bib_record_id, branch_id, barcode, accession_no, call_number, shelf_location, is_reference_only, acquisition_cost, loan_policy_id).
- [x] `PUT /catalog/copies/{id}` — update (incl. status).
- [x] `GET /catalog/copies/by-barcode/{barcode}` — resolve a scanned copy.
- [x] Accession-number allocation via the document-sequence allocator.
- [x] `GET /catalog/copies/{id}/label.pdf` — spine-label PDF.

### Barcode module (`internal/modules/barcode/label.go`)
- [x] Code-128 barcode (`boombuler/barcode` + `code128`) rendered to a label PDF (`go-pdf/fpdf`, 62×29mm), title/call-number truncation.

### Branches (`handlers/branches.go`)
- [x] `GET /branches`, `POST /branches` (name, code, address, lat/long, opening_hours, is_default), `PUT /branches/{id}`.

### Reference data
- [x] Author / Publisher / Subject / Collection schemas available for authority + browse (CRUD endpoints are Phase-2/UI-anticipated).

---

## Acceptance Criteria

- [x] Bibs can be created, searched by title/ISBN, and filtered by format.
- [x] ISBN lookup pre-fills metadata from OpenLibrary when not held locally.
- [x] Copies carry a unique barcode (per tenant) and a sequenced accession number.
- [x] A scannable Code-128 spine-label PDF renders for any copy.
- [x] Branches drive copy location + (future) due-date rollover via opening hours.

---

## Dependencies

- Sprint 02 (catalog/copy/branch schemas), Sprint 03 (auth + sequence allocator).
