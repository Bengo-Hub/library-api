package handlers

import (
	"context"
	"net/http"
	"strings"

	sharedcache "github.com/Bengo-Hub/cache"
	sharedpagination "github.com/Bengo-Hub/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/collection"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/predicate"
	"github.com/bengobox/library-service/internal/ent/subject"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/refdata"
	"github.com/bengobox/library-service/internal/platform/secrets"
)

// CatalogHandler serves bibliographic + copy endpoints.
type CatalogHandler struct {
	db        *ent.Client
	secrets   *secrets.Store     // platform secret store; supplies the optional ISBNdb key for lookups
	mediaRoot string             // on-disk media root (cover uploads); "" disables uploads
	cache     *sharedcache.Aside // may be nil (Redis unconfigured) — callers fall back to a live fetch
	log       *zap.Logger
}

// NewCatalogHandler builds the catalog handler. secretStore may be nil (ISBNdb enrichment off);
// mediaRoot may be "" (cover upload disabled); cache may be nil (Redis unconfigured).
func NewCatalogHandler(db *ent.Client, secretStore *secrets.Store, mediaRoot string, cache *sharedcache.Aside, log *zap.Logger) *CatalogHandler {
	return &CatalogHandler{db: db, secrets: secretStore, mediaRoot: mediaRoot, cache: cache, log: log}
}

// bibRequest is the create/update payload for a bibliographic record.
type bibRequest struct {
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle"`
	ISBN13        string   `json:"isbn13"`
	ISBN10        string   `json:"isbn10"`
	Authors       []string `json:"authors"`
	PublisherName string   `json:"publisher_name"`
	Format        string   `json:"format"`
	Language      string   `json:"language"`
	DDC           string   `json:"ddc_classification"`
	// CallNumber is the DEFAULT call number a new copy of this title starts with (CreateCopy /
	// ReceiveLine fall back to it when a caller doesn't send one) — not the authoritative per-copy
	// value, which stays on BookCopy.call_number and can still diverge per copy.
	CallNumber        string   `json:"lc_call_number"`
	PublishYear       int      `json:"publication_year"`
	PageCount         int      `json:"page_count"`
	Summary           string   `json:"summary"`
	CoverImageURL     string   `json:"cover_image_url"`
	CoverBackImageURL string   `json:"cover_back_image_url"`
	Edition           string   `json:"edition"`
	ISSN              string   `json:"issn"`
	CollectionID      string   `json:"collection_id"`
	PublicationPlace  string   `json:"publication_place"`
	Subjects          []string `json:"subjects"`
	OtherISBNs        []string `json:"other_isbns"`
	Force             bool     `json:"force"` // bypass a soft duplicate-title warning; never bypasses an ISBN conflict
}

// ListBibs godoc
// @Summary List bibliographic records
// @Tags Catalog
// @Produce json
// @Param q query string false "Title search"
// @Param format query string false "PHYSICAL|EBOOK|AUDIOBOOK|PERIODICAL"
// @Success 200 {object} sharedpagination.Response
// @Router /{tenant}/library/catalog/bibs [get]
func (h *CatalogHandler) ListBibs(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	params := sharedpagination.Parse(r)
	q := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID))
	if s := r.URL.Query().Get("q"); s != "" {
		q = q.Where(bibrecord.Or(
			bibrecord.TitleContainsFold(s),
			bibrecord.Isbn13ContainsFold(s),
			bibrecord.Isbn10ContainsFold(s),
			bibrecord.IDIn(bibIDsMatchingCopyIdentifier(r.Context(), h.db, tenantID, s)...),
		))
	}
	if f := r.URL.Query().Get("format"); f != "" {
		q = q.Where(bibrecord.FormatEQ(bibrecord.Format(f)))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(bibrecord.FieldCreatedAt)).Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(rows, total, params))
}

// bibIDsMatchingCopyIdentifier resolves BibRecord IDs whose copies carry a barcode or accession
// number matching term, so a scanned copy label (the library's own accession barcode, distinct from
// the manufacturer ISBN barcode printed on the book) resolves to its title on ListBibs/Search —
// mirrors ListAllCopies' bib-title lookup (catalog_copies.go) in the opposite direction. An empty
// result is safe: ent compiles IDIn() with zero ids to a predicate that matches nothing, the same
// convention ListAllCopies already relies on for its own possibly-empty ID slice.
func bibIDsMatchingCopyIdentifier(ctx context.Context, db *ent.Client, tenantID uuid.UUID, term string) []uuid.UUID {
	ids, _ := db.BookCopy.Query().
		Where(bookcopy.TenantID(tenantID), bookcopy.Or(
			bookcopy.BarcodeContainsFold(term),
			bookcopy.AccessionNoContainsFold(term),
		)).
		Select(bookcopy.FieldBibRecordID).Strings(ctx)
	out := make([]uuid.UUID, 0, len(ids))
	for _, s := range ids {
		if u, err := uuid.Parse(s); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// CreateBib godoc
// @Summary Create a bibliographic record
// @Tags Catalog
// @Accept json
// @Produce json
// @Success 201 {object} ent.BibRecord
// @Router /{tenant}/library/catalog/bibs [post]
func (h *CatalogHandler) CreateBib(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req bibRequest
	if err := Decode(r, &req); err != nil || req.Title == "" {
		respondError(w, http.StatusBadRequest, "title is required", "invalid_request")
		return
	}
	if blocked := h.rejectDuplicateBib(r.Context(), w, tenantID, req, nil); blocked {
		return
	}
	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "tx_failed")
		return
	}
	c := tx.BibRecord.Create().SetTenantID(tenantID).SetTitle(req.Title)
	applyBibFields(c, req)
	row, err := c.Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	// Publish bib.created on the transactional outbox (atomic with the write), mirroring
	// member.registered / loan.created etc.
	_ = events.Publish(r.Context(), tx.OutboxEvent, tenantID, row.ID.String(), events.EventBibCreated, map[string]any{
		"bib_id": row.ID, "title": row.Title, "isbn13": row.Isbn13, "isbn10": row.Isbn10,
	})
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "commit_failed")
		return
	}
	h.recordBibTerms(r.Context(), tenantID, req)
	respondJSON(w, http.StatusCreated, row)
}

// recordBibTerms grows the cataloging dictionaries (authors/publisher/place/subjects) from a saved
// bib so the searchable pickers stay populated from real cataloging activity (best-effort).
func (h *CatalogHandler) recordBibTerms(ctx context.Context, tenantID uuid.UUID, req bibRequest) {
	upsertCatalogTerms(ctx, h.db, tenantID, "author", req.Authors)
	upsertCatalogTerms(ctx, h.db, tenantID, "subject", req.Subjects)
	if req.PublisherName != "" {
		upsertCatalogTerms(ctx, h.db, tenantID, "publisher", []string{req.PublisherName})
	}
	if req.PublicationPlace != "" {
		upsertCatalogTerms(ctx, h.db, tenantID, "place", []string{req.PublicationPlace})
	}
}

// GetBib returns a single bib record.
// @Router /{tenant}/library/catalog/bibs/{id} [get]
func (h *CatalogHandler) GetBib(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	row, err := h.db.BibRecord.Query().Where(bibrecord.IDEQ(id), bibrecord.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "get_failed")
		return
	}
	// Enrich with live copy/hold counts the same way Search's opacRow does — GetBib previously
	// returned the bare row with no total_copies/available_copies/on_hold, so the title-detail
	// page's AvailabilityBadge always rendered "No copies" regardless of real holdings.
	total, available, onHold := h.copyCountsByBib(r.Context(), tenantID, []*ent.BibRecord{row})
	respondJSON(w, http.StatusOK, opacRow{BibRecord: row, TotalCopies: total[row.ID], AvailableCopies: available[row.ID], OnHold: onHold[row.ID]})
}

// UpdateBib updates a bib record.
// @Router /{tenant}/library/catalog/bibs/{id} [put]
func (h *CatalogHandler) UpdateBib(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.BibRecord.Query().Where(bibrecord.IDEQ(id), bibrecord.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req bibRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	if blocked := h.rejectDuplicateBib(r.Context(), w, tenantID, req, &id); blocked {
		return
	}
	u := h.db.BibRecord.UpdateOneID(id)
	if req.Title != "" {
		u.SetTitle(req.Title)
	}
	applyBibUpdate(u, req)
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	h.recordBibTerms(r.Context(), tenantID, req)
	respondJSON(w, http.StatusOK, row)
}

// DeleteBib removes a bib record (and is blocked if copies exist).
// @Router /{tenant}/library/catalog/bibs/{id} [delete]
func (h *CatalogHandler) DeleteBib(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	n, _ := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordID(id)).Count(r.Context())
	if n > 0 {
		respondError(w, http.StatusConflict, "remove copies first", "has_copies")
		return
	}
	if _, err := h.db.BibRecord.Delete().Where(bibrecord.IDEQ(id), bibrecord.TenantID(tenantID)).Exec(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "delete_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// bibDuplicateMatch is the lightweight shape returned for a possible-duplicate hit — just enough
// for the cataloging UI to show "this already exists" and link to it, without the cost of a full
// opacRow (copy/hold counts) for what is usually a live-typing check.
type bibDuplicateMatch struct {
	ID       uuid.UUID `json:"id"`
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle,omitempty"`
	Authors  []string  `json:"authors,omitempty"`
	Format   string    `json:"format"`
	Isbn13   string    `json:"isbn13,omitempty"`
	Isbn10   string    `json:"isbn10,omitempty"`
	CoverURL string    `json:"cover_image_url,omitempty"`
}

func toBibDuplicateMatch(b *ent.BibRecord) bibDuplicateMatch {
	return bibDuplicateMatch{
		ID: b.ID, Title: b.Title, Subtitle: b.Subtitle, Authors: b.Authors,
		Format: string(b.Format), Isbn13: b.Isbn13, Isbn10: b.Isbn10, CoverURL: b.CoverImageURL,
	}
}

// findDuplicateBibs looks for existing tenant bib records that collide with the given ISBN(s) or
// title (case/whitespace-insensitive exact match), excluding excludeID (the record being edited,
// if any) so an unrelated field edit never flags a title against itself. isbn13/isbn10 checks are
// exact-field matches only (no ISBN-10<->13 cross-conversion) — that already covers the reported
// bug (the same ISBN typed twice), and keeps the query index-friendly.
func (h *CatalogHandler) findDuplicateBibs(ctx context.Context, tenantID uuid.UUID, isbn13, isbn10, title string, excludeID *uuid.UUID) (isbnMatches, titleMatches []*ent.BibRecord) {
	isbn13 = strings.TrimSpace(isbn13)
	isbn10 = strings.TrimSpace(isbn10)
	title = strings.TrimSpace(title)

	if isbn13 != "" || isbn10 != "" {
		var isbnPreds []predicate.BibRecord
		if isbn13 != "" {
			isbnPreds = append(isbnPreds, bibrecord.Isbn13EQ(isbn13))
		}
		if isbn10 != "" {
			isbnPreds = append(isbnPreds, bibrecord.Isbn10EQ(isbn10))
		}
		q := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID), bibrecord.Or(isbnPreds...))
		if excludeID != nil {
			q = q.Where(bibrecord.IDNEQ(*excludeID))
		}
		isbnMatches, _ = q.Limit(5).All(ctx)
	}
	if title != "" {
		q := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID), bibrecord.TitleEqualFold(title))
		if excludeID != nil {
			q = q.Where(bibrecord.IDNEQ(*excludeID))
		}
		titleMatches, _ = q.Limit(5).All(ctx)
	}
	return isbnMatches, titleMatches
}

// rejectDuplicateBib runs findDuplicateBibs for a create/update request and, if it finds a
// collision, writes the 409 response and returns true (caller must stop). An ISBN match is always
// rejected — the same ISBN identifies the same edition, so the fix is to open the existing title
// and add a copy, never a second bib record. A title-only match is rejected UNLESS the caller set
// Force (librarians do legitimately catalog same-titled but genuinely distinct works/editions).
func (h *CatalogHandler) rejectDuplicateBib(ctx context.Context, w http.ResponseWriter, tenantID uuid.UUID, req bibRequest, excludeID *uuid.UUID) bool {
	isbnMatches, titleMatches := h.findDuplicateBibs(ctx, tenantID, req.ISBN13, req.ISBN10, req.Title, excludeID)
	if len(isbnMatches) > 0 {
		out := make([]bibDuplicateMatch, len(isbnMatches))
		for i, b := range isbnMatches {
			out[i] = toBibDuplicateMatch(b)
		}
		respondJSON(w, http.StatusConflict, map[string]any{
			"error":   "a title with this ISBN already exists in your catalog — open it and add a copy instead of creating a new title",
			"code":    "duplicate_isbn",
			"matches": out,
		})
		return true
	}
	if len(titleMatches) > 0 && !req.Force {
		out := make([]bibDuplicateMatch, len(titleMatches))
		for i, b := range titleMatches {
			out[i] = toBibDuplicateMatch(b)
		}
		respondJSON(w, http.StatusConflict, map[string]any{
			"error":   "a title with this exact name already exists in your catalog — confirm this is a different work/edition to continue",
			"code":    "duplicate_title",
			"matches": out,
		})
		return true
	}
	return false
}

// CheckDuplicate godoc
// @Summary Live pre-flight duplicate check while cataloging (title/ISBN), non-blocking
// @Tags Catalog
// @Param title query string false "Working title"
// @Param isbn13 query string false "ISBN-13 typed so far"
// @Param isbn10 query string false "ISBN-10 typed so far"
// @Param exclude_id query string false "Bib id to exclude (editing an existing title)"
// @Success 200 {object} map[string]any
// @Router /{tenant}/library/catalog/bibs/check-duplicate [get]
func (h *CatalogHandler) CheckDuplicate(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	qp := r.URL.Query()
	var excludeID *uuid.UUID
	if s := qp.Get("exclude_id"); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			excludeID = &id
		}
	}
	isbnMatches, titleMatches := h.findDuplicateBibs(r.Context(), tenantID, qp.Get("isbn13"), qp.Get("isbn10"), qp.Get("title"), excludeID)
	isbnOut := make([]bibDuplicateMatch, len(isbnMatches))
	for i, b := range isbnMatches {
		isbnOut[i] = toBibDuplicateMatch(b)
	}
	titleOut := make([]bibDuplicateMatch, len(titleMatches))
	for i, b := range titleMatches {
		titleOut[i] = toBibDuplicateMatch(b)
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"isbn_matches":  isbnOut,
		"title_matches": titleOut,
	})
}

// opacRow is one OPAC search hit with live availability.
type opacRow struct {
	*ent.BibRecord
	TotalCopies     int `json:"total_copies"`
	AvailableCopies int `json:"available_copies"`
	OnHold          int `json:"on_hold"`
}

// Search godoc
// @Summary OPAC search with live availability
// @Tags Catalog
// @Param q query string true "search terms"
// @Router /{tenant}/library/catalog/search [get]
func (h *CatalogHandler) Search(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	params := sharedpagination.Parse(r)
	qp := r.URL.Query()
	s := qp.Get("q")

	q := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID))
	// Full-text-ish multi-field match (title/subtitle/summary/publisher/ISBN), plus any copy whose
	// own barcode/accession number matches — a scanned copy label is a library-assigned identifier
	// distinct from the manufacturer ISBN barcode, so it can never match the fields above directly.
	if s != "" {
		q = q.Where(bibrecord.Or(
			bibrecord.TitleContainsFold(s),
			bibrecord.SubtitleContainsFold(s),
			bibrecord.SummaryContainsFold(s),
			bibrecord.PublisherNameContainsFold(s),
			bibrecord.Isbn13ContainsFold(s),
			bibrecord.Isbn10ContainsFold(s),
			bibrecord.IDIn(bibIDsMatchingCopyIdentifier(r.Context(), h.db, tenantID, s)...),
		))
	}
	// Facets.
	if f := qp.Get("format"); f != "" {
		q = q.Where(bibrecord.FormatEQ(bibrecord.Format(f)))
	}
	if lang := qp.Get("language"); lang != "" {
		q = q.Where(bibrecord.Language(lang))
	}
	if sid := qp.Get("subject_id"); sid != "" {
		if id, err := uuid.Parse(sid); err == nil {
			q = q.Where(bibrecord.PrimarySubjectID(id))
		}
	}
	if cid := qp.Get("collection_id"); cid != "" {
		if id, err := uuid.Parse(cid); err == nil {
			q = q.Where(bibrecord.CollectionID(id))
		}
	}
	// Branch facet: restrict to bibs that have a copy at the branch.
	if bid := qp.Get("branch_id"); bid != "" {
		if id, err := uuid.Parse(bid); err == nil {
			bibIDs, _ := h.db.BookCopy.Query().
				Where(bookcopy.TenantID(tenantID), bookcopy.BranchID(id)).
				Select(bookcopy.FieldBibRecordID).Strings(r.Context())
			ids := make([]uuid.UUID, 0, len(bibIDs))
			for _, s := range bibIDs {
				if u, e := uuid.Parse(s); e == nil {
					ids = append(ids, u)
				}
			}
			q = q.Where(bibrecord.IDIn(ids...))
		}
	}

	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(bibrecord.FieldCreatedAt)).Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "search_failed")
		return
	}
	onlyAvailable := qp.Get("available") == "true"
	totalByBib, availByBib, onHoldByBib := h.copyCountsByBib(r.Context(), tenantID, rows)
	out := make([]opacRow, 0, len(rows))
	for _, b := range rows {
		avail := availByBib[b.ID]
		if onlyAvailable && avail == 0 {
			continue
		}
		out = append(out, opacRow{BibRecord: b, TotalCopies: totalByBib[b.ID], AvailableCopies: avail, OnHold: onHoldByBib[b.ID]})
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(out, total, params))
}

// copyCountsByBib batch-resolves total copies, available copies, and active (WAITING/READY)
// hold counts for a page of bib rows via grouped-count queries (instead of a Count() call per
// row per metric, which previously scaled with page size — see reference_library_pin_sso_gap
// for the full N+1 writeup). on_hold counts active Hold records, not a BookCopy status — a hold
// is a member queued waiting for ANY copy of the bib to free up, not a property of one copy.
func (h *CatalogHandler) copyCountsByBib(ctx context.Context, tenantID uuid.UUID, rows []*ent.BibRecord) (total, available, onHold map[uuid.UUID]int) {
	total, available, onHold = map[uuid.UUID]int{}, map[uuid.UUID]int{}, map[uuid.UUID]int{}
	if len(rows) == 0 {
		return total, available, onHold
	}
	bibIDs := make([]uuid.UUID, 0, len(rows))
	for _, b := range rows {
		bibIDs = append(bibIDs, b.ID)
	}

	type countRow struct {
		BibRecordID uuid.UUID `json:"bib_record_id"`
		Count       int       `json:"count"`
	}
	var totalCounts []countRow
	if err := h.db.BookCopy.Query().
		Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordIDIn(bibIDs...)).
		GroupBy(bookcopy.FieldBibRecordID).
		Aggregate(ent.Count()).
		Scan(ctx, &totalCounts); err == nil {
		for _, c := range totalCounts {
			total[c.BibRecordID] = c.Count
		}
	}
	var availCounts []countRow
	if err := h.db.BookCopy.Query().
		Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordIDIn(bibIDs...), bookcopy.StatusEQ(bookcopy.StatusAVAILABLE)).
		GroupBy(bookcopy.FieldBibRecordID).
		Aggregate(ent.Count()).
		Scan(ctx, &availCounts); err == nil {
		for _, c := range availCounts {
			available[c.BibRecordID] = c.Count
		}
	}
	var holdCounts []countRow
	if err := h.db.Hold.Query().
		Where(hold.TenantID(tenantID), hold.BibRecordIDIn(bibIDs...), hold.StatusIn(hold.StatusWAITING, hold.StatusREADY)).
		GroupBy(hold.FieldBibRecordID).
		Aggregate(ent.Count()).
		Scan(ctx, &holdCounts); err == nil {
		for _, c := range holdCounts {
			onHold[c.BibRecordID] = c.Count
		}
	}
	return total, available, onHold
}

// facetsCacheKey scopes the cached facet payload per tenant.
func facetsCacheKey(tenantID uuid.UUID) string { return "library:catalog:facets:" + tenantID.String() }

// Facets godoc
// @Summary Facet values for the OPAC filter UI (formats + collections + subjects + languages)
// @Tags Catalog
// @Router /{tenant}/library/catalog/facets [get]
func (h *CatalogHandler) Facets(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()

	fetch := func(ctx context.Context) (map[string]any, error) {
		subjects, _ := h.db.Subject.Query().Where(subject.TenantID(tenantID)).All(ctx)
		collections, _ := h.db.Collection.Query().
			Where(collection.Or(collection.TenantID(tenantID), collection.TenantID(refdata.GlobalTenantID))).
			Order(ent.Asc(collection.FieldName)).All(ctx)
		branches, _ := h.db.Branch.Query().Where(branch.TenantID(tenantID)).All(ctx)
		languages, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID)).
			GroupBy(bibrecord.FieldLanguage).Strings(ctx)
		return map[string]any{
			"formats":     []string{"PHYSICAL", "EBOOK", "AUDIOBOOK", "PERIODICAL"},
			"subjects":    subjects,
			"collections": collections,
			"branches":    branches,
			"languages":   languages,
		}, nil
	}

	var (
		out map[string]any
		err error
	)
	// Reference-ish data (subjects/collections/branches/languages): cache-aside instead of 4
	// live queries on every catalog page load. Collection changes invalidate below; a newly
	// added branch/subject can take up to the TTL to appear in facets (best-effort — both are
	// admin-configured infrequently, unlike collections which cataloging staff edit routinely).
	if h.cache != nil {
		out, err = sharedcache.GetOrSet(ctx, h.cache, facetsCacheKey(tenantID), sharedcache.TTLReference, fetch)
	} else {
		out, err = fetch(ctx)
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "facets_failed")
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// ISBNLookup is implemented in isbn_lookup.go (local DB -> Google Books -> Open Library ->
// LoC SRU cascade, returning the flat isbnMetadata shape the cataloging UI expects).

func applyBibFields(c *ent.BibRecordCreate, req bibRequest) {
	if req.Subtitle != "" {
		c.SetSubtitle(req.Subtitle)
	}
	if req.ISBN13 != "" {
		c.SetIsbn13(req.ISBN13)
	}
	if req.ISBN10 != "" {
		c.SetIsbn10(req.ISBN10)
	}
	if len(req.Authors) > 0 {
		c.SetAuthors(req.Authors)
	}
	if req.PublisherName != "" {
		c.SetPublisherName(req.PublisherName)
	}
	if req.Format != "" {
		c.SetFormat(bibrecord.Format(req.Format))
	}
	if req.Language != "" {
		c.SetLanguage(req.Language)
	}
	if req.DDC != "" {
		c.SetDdcClassification(req.DDC)
	}
	if req.CallNumber != "" {
		c.SetLcCallNumber(req.CallNumber)
	}
	if req.PublishYear > 0 {
		c.SetPublicationYear(req.PublishYear)
	}
	if req.PageCount > 0 {
		c.SetPageCount(req.PageCount)
	}
	if req.Summary != "" {
		c.SetSummary(req.Summary)
	}
	if req.CoverImageURL != "" {
		c.SetCoverImageURL(req.CoverImageURL)
	}
	if req.CoverBackImageURL != "" {
		c.SetCoverBackImageURL(req.CoverBackImageURL)
	}
	if req.Edition != "" {
		c.SetEdition(req.Edition)
	}
	if req.ISSN != "" {
		c.SetIssn(req.ISSN)
	}
	if req.PublicationPlace != "" {
		c.SetPublicationPlace(req.PublicationPlace)
	}
	if len(req.Subjects) > 0 {
		c.SetSubjects(req.Subjects)
	}
	if len(req.OtherISBNs) > 0 {
		c.SetOtherIsbns(req.OtherISBNs)
	}
	if id, ok := parseOptionalUUID(req.CollectionID); ok {
		c.SetCollectionID(id)
	}
}

// applyBibUpdate mirrors applyBibFields for the update builder so editing a title can change
// any field the create form sets (previously it silently ignored year/pages/ddc/call-number/etc).
func applyBibUpdate(u *ent.BibRecordUpdateOne, req bibRequest) {
	if req.Subtitle != "" {
		u.SetSubtitle(req.Subtitle)
	}
	if req.ISBN13 != "" {
		u.SetIsbn13(req.ISBN13)
	}
	if req.ISBN10 != "" {
		u.SetIsbn10(req.ISBN10)
	}
	if len(req.Authors) > 0 {
		u.SetAuthors(req.Authors)
	}
	if req.PublisherName != "" {
		u.SetPublisherName(req.PublisherName)
	}
	if req.Format != "" {
		u.SetFormat(bibrecord.Format(req.Format))
	}
	if req.Language != "" {
		u.SetLanguage(req.Language)
	}
	if req.DDC != "" {
		u.SetDdcClassification(req.DDC)
	}
	if req.CallNumber != "" {
		u.SetLcCallNumber(req.CallNumber)
	}
	if req.PublishYear > 0 {
		u.SetPublicationYear(req.PublishYear)
	}
	if req.PageCount > 0 {
		u.SetPageCount(req.PageCount)
	}
	if req.Summary != "" {
		u.SetSummary(req.Summary)
	}
	if req.CoverImageURL != "" {
		u.SetCoverImageURL(req.CoverImageURL)
	}
	if req.CoverBackImageURL != "" {
		u.SetCoverBackImageURL(req.CoverBackImageURL)
	}
	if req.Edition != "" {
		u.SetEdition(req.Edition)
	}
	if req.ISSN != "" {
		u.SetIssn(req.ISSN)
	}
	if req.PublicationPlace != "" {
		u.SetPublicationPlace(req.PublicationPlace)
	}
	if len(req.Subjects) > 0 {
		u.SetSubjects(req.Subjects)
	}
	if len(req.OtherISBNs) > 0 {
		u.SetOtherIsbns(req.OtherISBNs)
	}
	if id, ok := parseOptionalUUID(req.CollectionID); ok {
		u.SetCollectionID(id)
	}
}

// parseOptionalUUID parses a non-empty UUID string, returning ok=false for empty/invalid input.
func parseOptionalUUID(s string) (uuid.UUID, bool) {
	if s == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}
