# Sprint 09 — Advanced OPAC, MARC & Authority Control (Phase 3)

**Status:** ⏳ Planned
**Goal:** Elevate the catalog from basic search to a research-grade OPAC: faceted full-text search, MARC21 / Z39.50 / SRU interoperability, authority control, and recommendations.

---

## Scope

Phase-3 catalog depth. Builds on the existing `BibRecord` (with `marc`/`dublin_core` JSON), `Author`/`Publisher`/`Subject`/`Collection` authority tables, and copy availability.

---

## Task Checklist

### Faceted full-text OPAC
- [ ] Add a Postgres `tsvector` column (title/subtitle/authors/subjects/summary) with a GIN index + `pg_trgm` for fuzzy/typo-tolerant matching (Atlas migration).
- [ ] `GET /catalog/search` upgraded: ranked full-text + facets (author, subject, collection, format, language, availability, publication year).
- [ ] `GET /catalog/facets` — facet counts for the current query.
- [ ] Availability facet computed from live copy status per branch.

### Authority control
- [ ] `GET/POST/PUT /catalog/authors`, `/catalog/publishers`, `/catalog/subjects` (CRUD + merge/dedupe).
- [ ] Subject hierarchy browse (parent/child) under LCSH/DDC/LOCAL schemes.
- [ ] Link bibs to authority records (replace/augment denormalized `authors` JSON).
- [ ] `POST /catalog/bibs/{id}/cover` — cover image upload to the media PVC.

### MARC / interoperability
- [ ] MARC21 import (`POST /catalog/import/marc`) → BibRecord (+ copies) mapping leader/008/control + data fields.
- [ ] MARC21 export (`GET /catalog/bibs/{id}/marc`).
- [ ] Z39.50 / SRU search endpoints (federated copy-cataloging from external sources).
- [ ] OAI-PMH harvest feed (optional).

### Recommendations
- [ ] "More like this" (subject/author/collection similarity).
- [ ] Borrowing-history-based recommendations (privacy-aware, opt-in).

### Reports (UI-anticipated endpoints)
- [ ] `GET /reports/popular` (top titles by loans, window).
- [ ] `GET /reports/circulation` (checkouts/returns trend).
- [ ] `GET /reports/overdue` (overdue list).

---

## Acceptance Criteria

- [ ] OPAC search returns ranked, faceted results with availability.
- [ ] A MARC21 record round-trips (import → bib → export) without data loss for core fields.
- [ ] Authority records can be created, merged, and linked to bibs.
- [ ] Reports endpoints back the UI dashboard/analytics charts.

---

## Dependencies

- Sprint 04 (catalog baseline), Sprint 02 (`marc`/`dublin_core`/authority schemas), Postgres `pg_trgm` extension.
