package barcode

import (
	"bytes"
	"fmt"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/go-pdf/fpdf"
)

// MemberCard is the data printed on a patron membership card (or, with Kind set, a staff card).
type MemberCard struct {
	Org          string // library / tenant name (header band)
	Kind         string // header subtitle, e.g. "MEMBERSHIP CARD" | "STAFF CARD" (default membership)
	Name         string // member / staff full name
	MembershipNo string // human-readable + encoded in the barcode (membership no | staff serial)
	Tier         string // membership tier or staff role (optional)
	ExpiresAt    string // formatted expiry date (optional)
}

// RenderMemberCard returns a CR80 business-card-sized PDF (85.6mm × 54mm) styled like a
// membership card: brand header band, member name + tier, and a CODE128 barcode of the
// membership number (scannable at the self-checkout kiosk to resolve the member). Uses the same
// go-pdf/fpdf + boombuler/barcode stack as the holding labels / treasury documents.
func RenderMemberCard(c MemberCard) ([]byte, error) {
	if c.MembershipNo == "" {
		return nil, fmt.Errorf("membership number is required")
	}
	bc, err := code128.Encode(c.MembershipNo)
	if err != nil {
		return nil, fmt.Errorf("encode barcode: %w", err)
	}
	scaled, err := barcode.Scale(bc, 560, 120)
	if err != nil {
		return nil, fmt.Errorf("scale barcode: %w", err)
	}
	var bcPNG buf
	if err := encodePNG(&bcPNG, scaled); err != nil {
		return nil, err
	}

	const w, h = 85.6, 54.0
	pdf := fpdf.NewCustom(&fpdf.InitType{UnitStr: "mm", Size: fpdf.SizeType{Wd: w, Ht: h}})
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	// Card background + rounded-ish border.
	pdf.SetFillColor(255, 255, 255)
	pdf.Rect(0, 0, w, h, "F")

	// Brand header band (purple).
	pdf.SetFillColor(124, 58, 237)
	pdf.Rect(0, 0, w, 15, "F")
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(5, 3.5)
	pdf.SetFont("Helvetica", "B", 11)
	pdf.CellFormat(w-10, 5, trunc(orDefault(c.Org, "Library"), 32), "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 7)
	pdf.CellFormat(w-10, 3.5, orDefault(c.Kind, "MEMBERSHIP CARD"), "", 2, "L", false, 0, "")

	// Member name + tier.
	pdf.SetTextColor(17, 24, 39)
	pdf.SetXY(5, 19)
	pdf.SetFont("Helvetica", "B", 12)
	pdf.CellFormat(w-10, 6, trunc(orDefault(c.Name, "Library Member"), 34), "", 2, "L", false, 0, "")
	if c.Tier != "" || c.ExpiresAt != "" {
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(107, 114, 128)
		meta := c.Tier
		if c.ExpiresAt != "" {
			if meta != "" {
				meta += "  •  "
			}
			meta += "Expires " + c.ExpiresAt
		}
		pdf.CellFormat(w-10, 4, meta, "", 2, "L", false, 0, "")
	}

	// Barcode + human-readable number, bottom band.
	opt := fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false}
	pdf.RegisterImageOptionsReader("mcard", opt, bytes.NewReader(bcPNG.Bytes()))
	pdf.ImageOptions("mcard", 5, 34, w-10, 11, false, opt, 0, "")
	pdf.SetXY(5, 45.5)
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(17, 24, 39)
	pdf.CellFormat(w-10, 5, c.MembershipNo, "", 0, "C", false, 0, "")

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return out.Bytes(), nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
