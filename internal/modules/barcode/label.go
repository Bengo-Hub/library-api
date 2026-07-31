// Package barcode renders spine/holding labels (CODE128 barcode + call number + title) as
// PDF, for printing on a standard label printer. Mirrors the inventory-api barcode module's
// approach (boombuler/barcode + go-pdf/fpdf); kept lean for the library label use case.
//
// Single-label (RenderPDF) and bulk-sheet (RenderSheet, in sheet.go) printing share ONE card
// layout — drawCopyLabelCell in sheet.go — so a layout/content fix only needs to happen once.
// Before this, RenderPDF had its own separate encode+draw logic that had silently drifted (it
// never disabled fpdf's auto page-break, which on a page this small caused each row — barcode
// image, human-readable text, call number, title — to land on its own PAGE instead of stacking
// in one label; see docs/barcode-labels.md).
package barcode

import (
	"bytes"
	"fmt"
	"image/png"

	"github.com/boombuler/barcode"
	"github.com/go-pdf/fpdf"
)

// CopyLabel is the data printed on one holding label.
type CopyLabel struct {
	Barcode    string
	Title      string
	CallNumber string
	Branch     string
}

// RenderPDF returns a single-label PDF (62mm × 29mm) with a CODE128 barcode and the
// human-readable barcode, call number and (truncated) title — drawn by the same
// drawCopyLabelCell used for every cell of a bulk sheet (see RenderSheet in sheet.go), with no
// tenant-name row (real spine labels carry no branding — see docs/barcode-labels.md).
func RenderPDF(l CopyLabel) ([]byte, error) {
	if l.Barcode == "" {
		return nil, fmt.Errorf("barcode is required")
	}
	const w, h = 62.0, 29.0
	pdf := fpdf.NewCustom(&fpdf.InitType{UnitStr: "mm", Size: fpdf.SizeType{Wd: w, Ht: h}})
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0) // the actual bug: without this, fpdf's default break margin
	// exceeds this label's own height, so every row after the first silently started a new page.
	pdf.AddPage()
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	imgSeq := 0
	drawCopyLabelCell(pdf, tr, l, "", 0, 0, w, h, &imgSeq)

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return out.Bytes(), nil
}

type buf struct{ bytes.Buffer }

func encodePNG(w *buf, img barcode.Barcode) error {
	return png.Encode(w, img)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
