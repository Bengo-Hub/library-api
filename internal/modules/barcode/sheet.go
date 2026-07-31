package barcode

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/go-pdf/fpdf"
)

// AverySpec describes a label-sheet grid (mm) on a single physical sheet (A4 or US Letter).
// Mirrors inventory-api's internal/modules/barcode AverySpec — same field shapes, same
// sheet-preset dimensions — so bulk holding labels lay out on the same real-world Avery
// stock librarians already have on hand for other printed labels.
type AverySpec struct {
	Name    string
	Cols    int
	Rows    int
	LabelW  float64 // mm
	LabelH  float64 // mm
	MarginX float64 // mm — left/right page margin to first column
	MarginY float64 // mm — top page margin to first row
	GutterX float64 // mm — horizontal gap between columns
	GutterY float64 // mm — vertical gap between rows
}

// AveryL7160 is the standard A4 sheet-label layout: 3×7 = 21 labels/sheet, 63.5×38.1mm each.
func AveryL7160() AverySpec {
	return AverySpec{
		Name:    "Avery L7160 (A4, 21/sheet)",
		Cols:    3,
		Rows:    7,
		LabelW:  63.5,
		LabelH:  38.1,
		MarginX: 7.0,
		MarginY: 15.0,
		GutterX: 2.5,
		GutterY: 0,
	}
}

// Avery5160 is the standard US-Letter sheet-label layout (shared by 5160/8160/5260/8460):
// 3×10 = 30 labels/sheet, 2-5/8"×1" (66.7×25.4mm) each.
func Avery5160() AverySpec {
	return AverySpec{
		Name:    "Avery 5160 (US Letter, 30/sheet)",
		Cols:    3,
		Rows:    10,
		LabelW:  66.7,
		LabelH:  25.4,
		MarginX: 4.8,
		MarginY: 12.7,
		GutterX: 3.2,
		GutterY: 0,
	}
}

// AverySpecByName resolves a request-supplied sheet-preset name; unknown/empty falls back
// to AveryL7160.
func AverySpecByName(name string) AverySpec {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "5160", "avery_5160", "avery5160", "us_letter":
		return Avery5160()
	default:
		return AveryL7160()
	}
}

// PerPage returns how many labels fit on one sheet.
func (s AverySpec) PerPage() int { return s.Cols * s.Rows }

// cutDash is the dash pattern (mm) used for the "cut here" guide around each label cell —
// short dashes with a wider gap, matching the perforation-style lines printers/scissors
// guides conventionally use, so it doesn't read as a solid border users might mistake for
// part of the label's own design.
var cutDash = []float64{0.8, 0.8}

// pageSizeFor returns the fpdf page-size string ("A4" or "Letter") matching the given spec,
// keyed off the LabelH used by Avery5160 (US Letter) vs AveryL7160 (A4).
func pageSizeFor(spec AverySpec) string {
	if spec.Name == Avery5160().Name {
		return "Letter"
	}
	return "A4"
}

// RenderSheet lays out copy holding labels onto a sheet of the given Avery grid — one cell
// per copy, with a CODE128 barcode PNG and a dashed cut-guide border — so a librarian can
// print labels for many newly-catalogued copies in one PDF instead of one-at-a-time on the
// single spine-label endpoint. Mirrors inventory-api's renderAveryPDF/drawCutGuide/
// drawLabelCell pattern (duplicated here rather than shared, since this is a separate Go
// module with its own barcode package).
func RenderSheet(labels []CopyLabel, spec AverySpec, tenantName string) ([]byte, error) {
	if len(labels) == 0 {
		return nil, fmt.Errorf("barcode: no labels to render")
	}
	pdf := fpdf.New("P", "mm", pageSizeFor(spec), "")
	pdf.SetAutoPageBreak(false, 0)
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	perPage := spec.PerPage()
	imgSeq := 0

	for i, lbl := range labels {
		posOnPage := i % perPage
		if posOnPage == 0 {
			pdf.AddPage()
		}
		col := posOnPage % spec.Cols
		row := posOnPage / spec.Cols

		x := spec.MarginX + float64(col)*(spec.LabelW+spec.GutterX)
		y := spec.MarginY + float64(row)*(spec.LabelH+spec.GutterY)

		drawCutGuide(pdf, x, y, spec.LabelW, spec.LabelH)
		drawCopyLabelCell(pdf, tr, lbl, tenantName, x, y, spec.LabelW, spec.LabelH, &imgSeq)
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("barcode: render avery pdf: %w", err)
	}
	return out.Bytes(), nil
}

// drawCutGuide draws a dashed rectangle around a label cell as a cutting guide, then
// restores a solid draw style so it never bleeds into the label content drawn after it.
func drawCutGuide(pdf *fpdf.Fpdf, x, y, w, h float64) {
	pdf.SetDrawColor(150, 150, 150)
	pdf.SetLineWidth(0.15)
	pdf.SetDashPattern(cutDash, 0)
	pdf.Rect(x, y, w, h, "D")
	pdf.SetDashPattern([]float64{}, 0) // back to solid for everything drawn after
	pdf.SetDrawColor(0, 0, 0)
}

// drawCopyLabelCell renders a single copy holding label as ONE unified card inside the
// rectangle (x,y,w,h). Layout (top→bottom): tenant name (compact, one line — not a colored
// graphic banner; labels this small can't spare the space for a colored band), title (bold,
// truncated), call number (its own row), barcode image, human-readable barcode text
// (monospace, centered).
func drawCopyLabelCell(pdf *fpdf.Fpdf, tr func(string) string, lbl CopyLabel, tenantName string, x, y, w, h float64, imgSeq *int) {
	const pad = 2.0
	innerW := w - 2*pad
	cursorY := y + pad

	// Tenant/library name — one compact line, not a graphic banner: at label sizes this
	// small, a colored header band would eat the space needed for the barcode's quiet zone
	// and force everything else to shrink below legibility.
	if tenantName != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "B", 6)
		pdf.SetTextColor(90, 98, 110)
		pdf.CellFormat(innerW, 2.6, tr(fitText(pdf, strings.ToUpper(tenantName), innerW)), "", 0, "L", false, 0, "")
		cursorY += 2.9
	}

	pdf.SetTextColor(20, 28, 38)

	// Title — bold, one line, truncated to width.
	if lbl.Title != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.CellFormat(innerW, 3.6, tr(fitText(pdf, lbl.Title, innerW)), "", 0, "L", false, 0, "")
		cursorY += 3.8
	}

	// Call number — its own row.
	if lbl.CallNumber != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "", 6.5)
		pdf.SetTextColor(110, 120, 130)
		pdf.CellFormat(innerW, 3.0, tr(fitText(pdf, lbl.CallNumber, innerW)), "", 0, "L", false, 0, "")
		pdf.SetTextColor(20, 28, 38)
		cursorY += 3.2
	}

	// Barcode image — fills the central band of the cell.
	barH := h - (cursorY - y) - pad - 3.4 // leave room for the human-readable text line
	if barH < 6 {
		barH = 6
	}
	if bc, err := code128.Encode(lbl.Barcode); err == nil {
		if scaled, err := barcode.Scale(bc, 480, 120); err == nil {
			var bcPNG buf
			if err := encodePNG(&bcPNG, scaled); err == nil {
				name := fmt.Sprintf("sheet_bc%d", *imgSeq)
				*imgSeq++
				opt := fpdf.ImageOptions{ImageType: "PNG"}
				pdf.RegisterImageOptionsReader(name, opt, bytes.NewReader(bcPNG.Bytes()))
				pdf.ImageOptions(name, x+pad, cursorY, innerW, barH, false, opt, 0, "")
				cursorY += barH + 0.5
			}
		}
	}

	// Human-readable barcode text (centered, monospace).
	pdf.SetXY(x+pad, cursorY)
	pdf.SetFont("Courier", "", 6)
	pdf.CellFormat(innerW, 2.8, tr(fitText(pdf, lbl.Barcode, innerW)), "", 0, "C", false, 0, "")
}

// fitText truncates s with an ellipsis so it fits within maxW at the pdf's current font.
func fitText(pdf *fpdf.Fpdf, s string, maxW float64) string {
	if pdf.GetStringWidth(s) <= maxW {
		return s
	}
	for len(s) > 1 {
		s = s[:len(s)-1]
		if pdf.GetStringWidth(s+"...") <= maxW {
			return s + "..."
		}
	}
	return s
}
