// Package barcode renders spine/holding labels (CODE128 barcode + call number + title) as
// PDF, for printing on a standard label printer. Mirrors the inventory-api barcode module's
// approach (boombuler/barcode + go-pdf/fpdf); kept lean for the library label use case.
package barcode

import (
	"bytes"
	"fmt"
	"image/png"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
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
// human-readable barcode, call number and (truncated) title.
func RenderPDF(l CopyLabel) ([]byte, error) {
	if l.Barcode == "" {
		return nil, fmt.Errorf("barcode is required")
	}
	bc, err := code128.Encode(l.Barcode)
	if err != nil {
		return nil, fmt.Errorf("encode barcode: %w", err)
	}
	scaled, err := barcode.Scale(bc, 480, 120)
	if err != nil {
		return nil, fmt.Errorf("scale barcode: %w", err)
	}
	var bcPNG buf
	if err := encodePNG(&bcPNG, scaled); err != nil {
		return nil, err
	}

	pdf := fpdf.NewCustom(&fpdf.InitType{UnitStr: "mm", Size: fpdf.SizeType{Wd: 62, Ht: 29}})
	pdf.SetMargins(2, 2, 2)
	pdf.AddPage()
	opt := fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false}
	pdf.RegisterImageOptionsReader("bc", opt, bytes.NewReader(bcPNG.Bytes()))
	pdf.ImageOptions("bc", 4, 3, 54, 12, false, opt, 0, "")

	pdf.SetXY(2, 16)
	pdf.SetFont("Helvetica", "B", 8)
	pdf.CellFormat(58, 4, l.Barcode, "", 2, "C", false, 0, "")
	pdf.SetFont("Helvetica", "", 7)
	if l.CallNumber != "" {
		pdf.CellFormat(58, 3.5, trunc(l.CallNumber, 40), "", 2, "C", false, 0, "")
	}
	if l.Title != "" {
		pdf.CellFormat(58, 3.5, trunc(l.Title, 42), "", 2, "C", false, 0, "")
	}

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
