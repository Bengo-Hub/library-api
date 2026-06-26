package handlers

import (
	"encoding/xml"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
)

// marcRecord is a minimal MARCXML record (subset of the LOC schema): leader +
// controlfields + datafields with subfields. Sufficient for export/interchange of the
// MARC-lite fields the catalog tracks.
type marcRecord struct {
	XMLName      xml.Name          `xml:"record"`
	Xmlns        string            `xml:"xmlns,attr"`
	Leader       string            `xml:"leader"`
	ControlField []marcControl     `xml:"controlfield"`
	DataField    []marcDataField   `xml:"datafield"`
}

type marcControl struct {
	Tag  string `xml:"tag,attr"`
	Text string `xml:",chardata"`
}

type marcDataField struct {
	Tag       string         `xml:"tag,attr"`
	Ind1      string         `xml:"ind1,attr"`
	Ind2      string         `xml:"ind2,attr"`
	Subfields []marcSubfield `xml:"subfield"`
}

type marcSubfield struct {
	Code string `xml:"code,attr"`
	Text string `xml:",chardata"`
}

func bibToMARC(b *ent.BibRecord) marcRecord {
	rec := marcRecord{Xmlns: "http://www.loc.gov/MARC21/slim", Leader: "00000nam a2200000 a 4500"}
	rec.ControlField = append(rec.ControlField, marcControl{Tag: "008", Text: strconv.Itoa(b.PublicationYear)})
	df := func(tag, ind1, ind2 string, subs ...marcSubfield) {
		rec.DataField = append(rec.DataField, marcDataField{Tag: tag, Ind1: ind1, Ind2: ind2, Subfields: subs})
	}
	if b.Isbn13 != "" {
		df("020", " ", " ", marcSubfield{Code: "a", Text: b.Isbn13})
	}
	if len(b.Authors) > 0 {
		df("100", "1", " ", marcSubfield{Code: "a", Text: b.Authors[0]})
	}
	title := b.Title
	if b.Subtitle != "" {
		title += " : " + b.Subtitle
	}
	df("245", "1", "0", marcSubfield{Code: "a", Text: title})
	if b.PublisherName != "" {
		df("260", " ", " ", marcSubfield{Code: "b", Text: b.PublisherName}, marcSubfield{Code: "c", Text: strconv.Itoa(b.PublicationYear)})
	}
	if b.DdcClassification != "" {
		df("082", "0", "4", marcSubfield{Code: "a", Text: b.DdcClassification})
	}
	return rec
}

// MarcXML godoc
// @Summary Export a bibliographic record as MARCXML
// @Tags Catalog
// @Produce application/xml
// @Router /{tenant}/library/catalog/bibs/{id}/marc.xml [get]
func (h *CatalogHandler) MarcXML(w http.ResponseWriter, r *http.Request) {
	b, ok := h.loadBib(w, r)
	if !ok {
		return
	}
	out, err := xml.MarshalIndent(bibToMARC(b), "", "  ")
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "marc_failed")
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

// MarcJSON godoc
// @Summary Export a bibliographic record as MARC-in-JSON
// @Tags Catalog
// @Router /{tenant}/library/catalog/bibs/{id}/marc.json [get]
func (h *CatalogHandler) MarcJSON(w http.ResponseWriter, r *http.Request) {
	b, ok := h.loadBib(w, r)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, bibToMARC(b))
}

// ImportMarc godoc
// @Summary Import a bibliographic record from a MARC-lite JSON payload
// @Tags Catalog
// @Router /{tenant}/library/catalog/import/marc [post]
func (h *CatalogHandler) ImportMarc(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	// Accepts the same flexible shape as bibRequest (the UI maps MARC fields client-side).
	var req bibRequest
	if err := Decode(r, &req); err != nil || req.Title == "" {
		respondError(w, http.StatusBadRequest, "a title is required (245$a)", "invalid_request")
		return
	}
	c := h.db.BibRecord.Create().SetTenantID(tenantID).SetTitle(req.Title)
	applyBibFields(c, req)
	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "import_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

func (h *CatalogHandler) loadBib(w http.ResponseWriter, r *http.Request) (*ent.BibRecord, bool) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return nil, false
	}
	b, err := h.db.BibRecord.Query().Where(bibrecord.IDEQ(id), bibrecord.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return nil, false
	}
	return b, true
}
