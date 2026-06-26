package handlers

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SRU (Search/Retrieve via URL) copy-cataloging import. Unlike binary Z39.50, SRU is plain
// HTTP+XML, so we can query an external bibliographic target (default: Library of Congress)
// and return MARCXML records as importable bib previews. This is the software half of the
// "Z39.50/SRU federation" requirement; the UI then POSTs a chosen record to /import/marc.

const defaultSRUTarget = "http://lx2.loc.gov:210/lcdb"

// sruEnvelope is a minimal SRU 1.1/1.2 searchRetrieve response (recordData holds MARCXML).
type sruEnvelope struct {
	XMLName xml.Name `xml:"searchRetrieveResponse"`
	Records []struct {
		RecordData struct {
			Record marcRecordIn `xml:"record"`
		} `xml:"recordData"`
	} `xml:"records>record"`
}

// marcRecordIn parses an inbound MARCXML record (namespace-agnostic on the local name).
type marcRecordIn struct {
	DataFields []struct {
		Tag       string `xml:"tag,attr"`
		Subfields []struct {
			Code string `xml:"code,attr"`
			Text string `xml:",chardata"`
		} `xml:"subfield"`
	} `xml:"datafield"`
}

type sruPreview struct {
	Title           string `json:"title"`
	Authors         []string `json:"authors"`
	ISBN13          string `json:"isbn13"`
	PublisherName   string `json:"publisher_name"`
	PublicationYear int    `json:"publication_year"`
}

// SRUSearch godoc
// @Summary Search an external SRU catalog (default: Library of Congress) for copy-cataloging
// @Tags Catalog
// @Param q query string true "search terms (title/author/ISBN)"
// @Param target query string false "SRU base URL (defaults to LoC)"
// @Router /{tenant}/library/catalog/sru/search [get]
func (h *CatalogHandler) SRUSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		respondError(w, http.StatusBadRequest, "q is required", "invalid_request")
		return
	}
	target := r.URL.Query().Get("target")
	if target == "" {
		target = defaultSRUTarget
	}
	previews, err := fetchSRU(r.Context(), target, q)
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error(), "sru_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: previews, Total: len(previews)})
}

func fetchSRU(ctx context.Context, target, q string) ([]sruPreview, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("version", "1.1")
	query.Set("operation", "searchRetrieve")
	query.Set("recordSchema", "marcxml")
	query.Set("maximumRecords", "10")
	query.Set("query", q)
	u.RawQuery = query.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var env sruEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	out := make([]sruPreview, 0, len(env.Records))
	for _, rec := range env.Records {
		out = append(out, marcToPreview(rec.RecordData.Record))
	}
	return out, nil
}

func marcToPreview(m marcRecordIn) sruPreview {
	var p sruPreview
	for _, df := range m.DataFields {
		sub := func(code string) string {
			for _, s := range df.Subfields {
				if s.Code == code {
					return strings.TrimSpace(strings.Trim(s.Text, " /:,"))
				}
			}
			return ""
		}
		switch df.Tag {
		case "020":
			if v := sub("a"); v != "" && p.ISBN13 == "" {
				p.ISBN13 = strings.Fields(v)[0]
			}
		case "100", "700":
			if v := sub("a"); v != "" {
				p.Authors = append(p.Authors, v)
			}
		case "245":
			t := sub("a")
			if b := sub("b"); b != "" {
				t = strings.TrimSpace(t) + " : " + b
			}
			p.Title = t
		case "260", "264":
			if v := sub("b"); v != "" {
				p.PublisherName = v
			}
			if v := sub("c"); v != "" {
				if y, err := strconv.Atoi(digitsOnly(v)); err == nil {
					p.PublicationYear = y
				}
			}
		}
	}
	return p
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}
