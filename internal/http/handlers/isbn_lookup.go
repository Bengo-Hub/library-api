package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
)

// isbnMetadata is the flat shape the cataloging UI expects from the ISBN lookup endpoint.
// Every field is best-effort: fields not found anywhere stay at their zero value (and are
// omitted from JSON), so the UI can prefill what it has and treat the rest as "no match".
type isbnMetadata struct {
	Title           string   `json:"title,omitempty"`
	Subtitle        string   `json:"subtitle,omitempty"`
	Author          string   `json:"author,omitempty"` // convenience: authors joined by ", "
	Authors         []string `json:"authors,omitempty"`
	Publisher       string   `json:"publisher,omitempty"`
	PublicationYear int      `json:"publication_year,omitempty"`
	ISBN            string   `json:"isbn"`
	Pages           int      `json:"pages,omitempty"`
	CoverURL        string   `json:"cover_url,omitempty"`
	Subjects        []string `json:"subjects,omitempty"`
	Language        string   `json:"language,omitempty"`
	Source          string   `json:"source,omitempty"` // debug-only: where fields came from
}

// ISBNLookup godoc
// @Summary Look up bibliographic metadata by ISBN (local first, then free keyless APIs)
// @Description Cascades local DB -> Google Books -> Open Library -> LoC SRU, merging results
// @Tags Catalog
// @Produce json
// @Param isbn path string true "ISBN-10 or ISBN-13"
// @Success 200 {object} isbnMetadata
// @Router /{tenant}/library/catalog/isbn/{isbn} [get]
func (h *CatalogHandler) ISBNLookup(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	isbn := cleanISBN(chi.URLParam(r, "isbn"))

	// 1. Local hit first (tenant-scoped) — same flat shape so the UI prefill is identical.
	if local, err := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID),
		bibrecord.Or(bibrecord.Isbn13(isbn), bibrecord.Isbn10(isbn))).First(r.Context()); err == nil {
		respondJSON(w, http.StatusOK, localBibToMetadata(local, isbn))
		return
	}

	// 2..4. External free/keyless providers. They are run CONCURRENTLY under one short
	// overall budget so total latency ≈ the slowest single provider (not the sum), and the
	// cataloging UI is never blocked: on a miss/timeout we still return 200 fast with the
	// echoed ISBN so the librarian can type details manually. Never a 5xx for a miss.
	out := isbnMetadata{ISBN: isbn}

	type result struct {
		priority int // lower = preferred source for first-non-empty merge
		name     string
		meta     *isbnMetadata
	}
	// Overall budget across all providers. Each provider call additionally self-caps (~3s)
	// in httpGetJSON/fetchSRU; this ctx guarantees the handler returns even if a host hangs.
	gctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	providers := []struct {
		priority int
		name     string
		fn       func(context.Context, string) *isbnMetadata
	}{
		{1, "google_books", fetchGoogleBooks},
		{2, "open_library", fetchOpenLibrary},
		{3, "loc_sru", fetchSRUMetadata},
	}
	results := make(chan result, len(providers))
	for _, p := range providers {
		p := p
		go func() {
			results <- result{priority: p.priority, name: p.name, meta: p.fn(gctx, isbn)}
		}()
	}
	collected := make([]result, 0, len(providers))
	for range providers {
		select {
		case res := <-results:
			if res.meta != nil {
				collected = append(collected, res)
			}
		case <-gctx.Done():
			// Budget exhausted — respond with whatever has arrived so far. Stragglers are
			// abandoned (their fetch ctx is cancelled), the goroutines drain into the
			// buffered channel and exit without blocking.
			goto merge
		}
	}
merge:
	// Merge in priority order so the preferred provider wins each field (first non-empty).
	sort.SliceStable(collected, func(i, j int) bool { return collected[i].priority < collected[j].priority })
	var sources []string
	for _, res := range collected {
		if mergeMetadata(&out, res.meta) {
			sources = append(sources, res.name)
		}
	}

	out.ISBN = isbn // ensure the echoed ISBN is never overwritten/lost
	out.Author = strings.Join(out.Authors, ", ")
	if len(sources) == 0 {
		out.Source = "none"
	} else {
		out.Source = strings.Join(sources, "+")
	}
	respondJSON(w, http.StatusOK, out)
}

// cleanISBN strips everything that is not a digit or X (case-insensitive).
func cleanISBN(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == 'X' || r == 'x' {
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(b.String())
}

// localBibToMetadata maps a local ent.BibRecord onto the flat lookup shape.
func localBibToMetadata(b *ent.BibRecord, isbn string) isbnMetadata {
	m := isbnMetadata{
		Title:           b.Title,
		Subtitle:        b.Subtitle,
		Authors:         b.Authors,
		Author:          strings.Join(b.Authors, ", "),
		Publisher:       b.PublisherName,
		PublicationYear: b.PublicationYear,
		ISBN:            isbn,
		Pages:           b.PageCount,
		CoverURL:        b.CoverImageURL,
		Language:        b.Language,
		Source:          "local",
	}
	if m.ISBN == "" {
		if b.Isbn13 != "" {
			m.ISBN = b.Isbn13
		} else {
			m.ISBN = b.Isbn10
		}
	}
	return m
}

// mergeMetadata copies non-empty fields from src into dst only where dst is still empty.
// Returns true if it contributed at least one field (so the source can be reported).
func mergeMetadata(dst *isbnMetadata, src *isbnMetadata) bool {
	contributed := false
	if dst.Title == "" && src.Title != "" {
		dst.Title = src.Title
		contributed = true
	}
	if dst.Subtitle == "" && src.Subtitle != "" {
		dst.Subtitle = src.Subtitle
		contributed = true
	}
	if len(dst.Authors) == 0 && len(src.Authors) > 0 {
		dst.Authors = src.Authors
		contributed = true
	}
	if dst.Publisher == "" && src.Publisher != "" {
		dst.Publisher = src.Publisher
		contributed = true
	}
	if dst.PublicationYear == 0 && src.PublicationYear > 0 {
		dst.PublicationYear = src.PublicationYear
		contributed = true
	}
	if dst.Pages == 0 && src.Pages > 0 {
		dst.Pages = src.Pages
		contributed = true
	}
	if dst.CoverURL == "" && src.CoverURL != "" {
		dst.CoverURL = src.CoverURL
		contributed = true
	}
	if len(dst.Subjects) == 0 && len(src.Subjects) > 0 {
		dst.Subjects = src.Subjects
		contributed = true
	}
	if dst.Language == "" && src.Language != "" {
		dst.Language = src.Language
		contributed = true
	}
	return contributed
}

// httpGetJSON performs a short-timeout GET and decodes the JSON body. Fails soft (nil).
// The per-call cap (3s) sits inside the handler's overall provider budget (~4s); whichever
// fires first wins, so a single slow host can never block the cataloging response.
func httpGetJSON(ctx context.Context, url string, v any) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err := json.Unmarshal(body, v); err != nil {
		return false
	}
	return true
}

// --- Google Books (no API key required) ---
// https://www.googleapis.com/books/v1/volumes?q=isbn:{isbn}

type googleBooksResponse struct {
	TotalItems int `json:"totalItems"`
	Items      []struct {
		VolumeInfo struct {
			Title         string   `json:"title"`
			Subtitle      string   `json:"subtitle"`
			Authors       []string `json:"authors"`
			Publisher     string   `json:"publisher"`
			PublishedDate string   `json:"publishedDate"`
			PageCount     int      `json:"pageCount"`
			Categories    []string `json:"categories"`
			Language      string   `json:"language"`
			ImageLinks    struct {
				Thumbnail      string `json:"thumbnail"`
				SmallThumbnail string `json:"smallThumbnail"`
			} `json:"imageLinks"`
		} `json:"volumeInfo"`
	} `json:"items"`
}

func fetchGoogleBooks(ctx context.Context, isbn string) *isbnMetadata {
	var gb googleBooksResponse
	if !httpGetJSON(ctx, "https://www.googleapis.com/books/v1/volumes?q=isbn:"+isbn, &gb) {
		return nil
	}
	if len(gb.Items) == 0 {
		return nil
	}
	vi := gb.Items[0].VolumeInfo
	m := &isbnMetadata{
		Title:           strings.TrimSpace(vi.Title),
		Subtitle:        strings.TrimSpace(vi.Subtitle),
		Authors:         vi.Authors,
		Publisher:       strings.TrimSpace(vi.Publisher),
		PublicationYear: parseYear(vi.PublishedDate),
		Pages:           vi.PageCount,
		Subjects:        vi.Categories,
		Language:        vi.Language,
		CoverURL:        normalizeGoogleCover(vi.ImageLinks.Thumbnail),
	}
	if m.CoverURL == "" {
		m.CoverURL = normalizeGoogleCover(vi.ImageLinks.SmallThumbnail)
	}
	return m
}

// normalizeGoogleCover upgrades a Google Books thumbnail URL: force https, drop the
// curled-page edge effect, and bump the zoom level for a cleaner cover image.
func normalizeGoogleCover(u string) string {
	if u == "" {
		return ""
	}
	u = strings.Replace(u, "http://", "https://", 1)
	u = strings.Replace(u, "&edge=curl", "", 1)
	u = strings.Replace(u, "zoom=1", "zoom=2", 1)
	return u
}

// --- Open Library (jscmd=data is richer than /isbn/{isbn}.json) ---
// https://openlibrary.org/api/books?bibkeys=ISBN:{isbn}&format=json&jscmd=data

type openLibraryData struct {
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	Authors   []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Publishers []struct {
		Name string `json:"name"`
	} `json:"publishers"`
	PublishDate   string `json:"publish_date"`
	NumberOfPages int    `json:"number_of_pages"`
	Subjects      []struct {
		Name string `json:"name"`
	} `json:"subjects"`
	Cover struct {
		Large  string `json:"large"`
		Medium string `json:"medium"`
		Small  string `json:"small"`
	} `json:"cover"`
}

func fetchOpenLibrary(ctx context.Context, isbn string) *isbnMetadata {
	key := "ISBN:" + isbn
	raw := map[string]openLibraryData{}
	url := "https://openlibrary.org/api/books?bibkeys=" + key + "&format=json&jscmd=data"
	if !httpGetJSON(ctx, url, &raw) {
		return nil
	}
	d, ok := raw[key]
	if !ok {
		return nil
	}
	m := &isbnMetadata{
		Title:           strings.TrimSpace(d.Title),
		Subtitle:        strings.TrimSpace(d.Subtitle),
		PublicationYear: parseYear(d.PublishDate),
		Pages:           d.NumberOfPages,
	}
	for _, a := range d.Authors {
		if a.Name != "" {
			m.Authors = append(m.Authors, a.Name)
		}
	}
	if len(d.Publishers) > 0 {
		m.Publisher = d.Publishers[0].Name
	}
	for _, s := range d.Subjects {
		if s.Name != "" {
			m.Subjects = append(m.Subjects, s.Name)
		}
	}
	switch {
	case d.Cover.Large != "":
		m.CoverURL = d.Cover.Large
	case d.Cover.Medium != "":
		m.CoverURL = d.Cover.Medium
	default:
		// Deterministic cover fallback by ISBN.
		m.CoverURL = "https://covers.openlibrary.org/b/isbn/" + isbn + "-L.jpg"
	}
	return m
}

// --- LoC SRU (last resort) — reuses fetchSRU/marcToPreview from sru.go ---

func fetchSRUMetadata(ctx context.Context, isbn string) *isbnMetadata {
	previews, err := fetchSRU(ctx, defaultSRUTarget, "bath.isbn="+isbn)
	if err != nil || len(previews) == 0 {
		return nil
	}
	p := previews[0]
	return &isbnMetadata{
		Title:           p.Title,
		Authors:         p.Authors,
		Publisher:       p.PublisherName,
		PublicationYear: p.PublicationYear,
	}
}

// parseYear extracts a 4-digit year from common date strings ("2009", "2009-05-01",
// "May 2009", "c1998"). Returns 0 if no plausible year is present.
func parseYear(s string) int {
	digits := make([]rune, 0, 4)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
			if len(digits) == 4 {
				break
			}
		} else if len(digits) > 0 && len(digits) < 4 {
			// reset on non-contiguous runs shorter than a year
			digits = digits[:0]
		}
	}
	if len(digits) != 4 {
		return 0
	}
	y, err := strconv.Atoi(string(digits))
	if err != nil {
		return 0
	}
	return y
}
