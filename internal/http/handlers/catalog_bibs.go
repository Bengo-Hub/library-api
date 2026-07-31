package handlers

import (
	"context"
	"net/http"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/collection"
	"github.com/bengobox/library-service/internal/ent/subject"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/modules/refdata"
	"github.com/bengobox/library-service/internal/platform/secrets"
)

// CatalogHandler serves bibliographic + copy endpoints.
type CatalogHandler struct {
	db        *ent.Client
	secrets   *secrets.Store // platform secret store; supplies the optional ISBNdb key for lookups
	mediaRoot string         // on-disk media root (cover uploads); "" disables uploads
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
	PublishYear   int      `json:"publication_year"`
	PageCount     int      `json:"page_count"`
	Summary           string   `json:"summary"`
	CoverImageURL     string   `json:"cover_image_url"`
	CoverBackImageURL string   `json:"cover_back_image_url"`
	Edition           string   `json:"edition"`
	ISSN              string   `json:"issn"`
	CollectionID      string   `json:"collection_id"`
	PublicationPlace  string   `json:"publication_place"`
	Subjects          []string `json:"subjects"`
	OtherISBNs        []string `json:"other_isbns"`
}

// ListBibs godoc
// @Summary List bibliographic records
// @Tags Catalog
// @Produce json
// @Param q query string false "Title search"
// @Param format query string false "PHYSICAL|EBOOK|AUDIOBOOK|PERIODICAL"
// @Success 200 {object} listEnvelope
// @Router /{tenant}/library/catalog/bibs [get]
func (h *CatalogHandler) ListBibs(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	limit, offset := PageParams(r)
	q := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID))
	if s := r.URL.Query().Get("q"); s != "" {
		q = q.Where(bibrecord.Or(
			bibrecord.TitleContainsFold(s),
			bibrecord.Isbn13ContainsFold(s),
			bibrecord.Isbn10ContainsFold(s),
		))
	}
	if f := r.URL.Query().Get("format"); f != "" {
		q = q.Where(bibrecord.FormatEQ(bibrecord.Format(f)))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(bibrecord.FieldCreatedAt)).Limit(limit).Offset(offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: total})
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
	respondJSON(w, http.StatusOK, row)
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

// opacRow is one OPAC search hit with live availability.
type opacRow struct {
	*ent.BibRecord
	TotalCopies     int `json:"total_copies"`
	AvailableCopies int `json:"available_copies"`
}

// Search godoc
// @Summary OPAC search with live availability
// @Tags Catalog
// @Param q query string true "search terms"
// @Router /{tenant}/library/catalog/search [get]
func (h *CatalogHandler) Search(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	limit, offset := PageParams(r)
	qp := r.URL.Query()
	s := qp.Get("q")

	q := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID))
	// Full-text-ish multi-field match (title/subtitle/summary/publisher/ISBN).
	if s != "" {
		q = q.Where(bibrecord.Or(
			bibrecord.TitleContainsFold(s),
			bibrecord.SubtitleContainsFold(s),
			bibrecord.SummaryContainsFold(s),
			bibrecord.PublisherNameContainsFold(s),
			bibrecord.Isbn13ContainsFold(s),
			bibrecord.Isbn10ContainsFold(s),
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
	rows, err := q.Order(ent.Desc(bibrecord.FieldCreatedAt)).Limit(limit).Offset(offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "search_failed")
		return
	}
	onlyAvailable := qp.Get("available") == "true"
	totalByBib, availByBib := h.copyCountsByBib(r.Context(), tenantID, rows)
	out := make([]opacRow, 0, len(rows))
	for _, b := range rows {
		avail := availByBib[b.ID]
		if onlyAvailable && avail == 0 {
			continue
		}
		out = append(out, opacRow{BibRecord: b, TotalCopies: totalByBib[b.ID], AvailableCopies: avail})
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: total})
}

// copyCountsByBib batch-resolves total and available copy counts for a page of bib rows via two
// grouped-count queries (instead of two Count() queries per row, which previously scaled 2x with
// page size — see reference_library_pin_sso_gap for the full N+1 writeup).
func (h *CatalogHandler) copyCountsByBib(ctx context.Context, tenantID uuid.UUID, rows []*ent.BibRecord) (total, available map[uuid.UUID]int) {
	total, available = map[uuid.UUID]int{}, map[uuid.UUID]int{}
	if len(rows) == 0 {
		return total, available
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
	return total, available
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
