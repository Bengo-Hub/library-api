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
	PhotoPNG     []byte // optional passport photo (PNG/JPEG bytes); a silhouette is drawn when empty
}

// RenderMemberCard renders a CR80 ID/access-card-sized PDF (85.6mm × 54mm): brand header band, a
// passport photo (or a drawn user-silhouette placeholder), member/staff name + tier/role + number,
// and a CODE128 barcode of the number (scannable at the kiosk / pin-login). Same go-pdf/fpdf +
// boombuler/barcode stack as the holding labels / treasury documents.
func RenderMemberCard(c MemberCard) ([]byte, error) {
	if c.MembershipNo == "" {
		return nil, fmt.Errorf("membership number is required")
	}
	bc, err := code128.Encode(c.MembershipNo)
	if err != nil {
		return nil, fmt.Errorf("encode barcode: %w", err)
	}
	scaled, err := barcode.Scale(bc, 560, 110)
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

	// Card background.
	pdf.SetFillColor(255, 255, 255)
	pdf.Rect(0, 0, w, h, "F")

	// Brand header band (purple) + a lighter accent stripe.
	pdf.SetFillColor(124, 58, 237)
	pdf.Rect(0, 0, w, 13, "F")
	pdf.SetFillColor(167, 139, 250)
	pdf.Rect(0, 13, w, 1.2, "F")
	pdf.SetTextColor(255, 255, 255)
	pdf.SetXY(5, 2.3)
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(w-10, 5, trunc(orDefault(c.Org, "Library"), 34), "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 6.5)
	pdf.CellFormat(w-10, 3, orDefault(c.Kind, "MEMBERSHIP CARD"), "", 2, "L", false, 0, "")

	// Photo box (passport) on the left.
	px, py, pw, ph := 5.0, 17.0, 17.0, 21.0
	drawPhoto(pdf, c.PhotoPNG, px, py, pw, ph)

	// Details to the right of the photo.
	dx := px + pw + 4
	dw := w - dx - 5
	pdf.SetTextColor(17, 24, 39)
	pdf.SetXY(dx, 17.5)
	pdf.SetFont("Helvetica", "B", 11.5)
	pdf.CellFormat(dw, 6, trunc(orDefault(c.Name, "Library Member"), 26), "", 2, "L", false, 0, "")
	if c.Tier != "" {
		pdf.SetFont("Helvetica", "", 8.5)
		pdf.SetTextColor(107, 114, 128)
		pdf.CellFormat(dw, 4.5, trunc(c.Tier, 30), "", 2, "L", false, 0, "")
	}
	pdf.SetXY(dx, 30)
	pdf.SetFont("Helvetica", "", 6.5)
	pdf.SetTextColor(124, 58, 237)
	label := "MEMBER NO."
	if c.Kind == "STAFF CARD" {
		label = "STAFF NO."
	}
	pdf.CellFormat(dw, 3, label, "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(17, 24, 39)
	pdf.CellFormat(dw, 4.5, trunc(c.MembershipNo, 24), "", 2, "L", false, 0, "")
	if c.ExpiresAt != "" {
		pdf.SetFont("Helvetica", "", 7)
		pdf.SetTextColor(107, 114, 128)
		pdf.CellFormat(dw, 3.5, "Expires "+c.ExpiresAt, "", 2, "L", false, 0, "")
	}

	// Barcode strip across the bottom + human-readable number.
	opt := fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false}
	pdf.RegisterImageOptionsReader("mcard_bc", opt, bytes.NewReader(bcPNG.Bytes()))
	pdf.ImageOptions("mcard_bc", 5, 40, w-10, 8.5, false, opt, 0, "")
	pdf.SetXY(5, 48.8)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(17, 24, 39)
	pdf.CellFormat(w-10, 4, c.MembershipNo, "", 0, "C", false, 0, "")

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return out.Bytes(), nil
}

// drawPhoto renders the passport photo into the box, or a user-silhouette placeholder when none.
func drawPhoto(pdf *fpdf.Fpdf, photo []byte, x, y, pw, ph float64) {
	// Light placeholder background + rounded frame.
	pdf.SetFillColor(243, 244, 246) // gray-100
	pdf.RoundedRect(x, y, pw, ph, 2, "1234", "F")

	if len(photo) > 0 {
		t := "PNG"
		if len(photo) > 3 && photo[0] == 0xFF && photo[1] == 0xD8 {
			t = "JPG"
		}
		opt := fpdf.ImageOptions{ImageType: t, ReadDpi: false}
		pdf.ClipRoundedRect(x, y, pw, ph, 2, false)
		pdf.RegisterImageOptionsReader("mcard_photo", opt, bytes.NewReader(photo))
		pdf.ImageOptions("mcard_photo", x, y, pw, ph, false, opt, 0, "")
		pdf.ClipEnd()
	} else {
		// User silhouette (head + shoulders), clipped to the rounded box.
		pdf.ClipRoundedRect(x, y, pw, ph, 2, false)
		pdf.SetFillColor(203, 213, 225) // slate-300
		cx := x + pw/2
		pdf.Circle(cx, y+pw*0.42, pw*0.22, "F")               // head
		pdf.Ellipse(cx, y+ph+pw*0.05, pw*0.42, pw*0.42, 0, "F") // shoulders (bottom clipped)
		pdf.ClipEnd()
	}
	// Frame.
	pdf.SetDrawColor(209, 213, 219)
	pdf.SetLineWidth(0.3)
	pdf.RoundedRect(x, y, pw, ph, 2, "1234", "D")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
