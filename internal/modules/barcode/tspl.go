package barcode

import (
	"fmt"
	"strings"
)

// renderTSPL emits TSC/TSPL2 commands for a batch of holding/spine labels — the command
// language spoken by TSC-compatible desktop thermal printers (including the Xprinter XP-330B),
// confirmed via Xprinter's own TSPL-emulation spec. Mirrors inventory-api's renderTSPL (same
// SIZE/GAP/CLS/TEXT/BARCODE/PRINT structure and rotation convention), simplified for
// CopyLabel's smaller field set (title/call number/barcode — Code128 only, no GS1/EAN13/price).
//
// SIZE/GAP describe the physical media AS MOUNTED (mm, never swapped to "fix" rotation). When
// tmpl.Rotate is set, each TEXT/BARCODE command's own rotation parameter is 90 instead of 0.
//
// Multi-lane ("rows"): each feed-row draws up to `lanes` consecutive labels side-by-side at
// x-offsets from tmpl.LaneXOffsetDots, then CLS/PRINT advances to the next row — identical
// tiling to inventory-api's renderZPL/renderTSPL, and to RenderSheet's Avery grid tiling, just
// applied to a continuous roll instead of a fixed sheet.
func RenderThermalTSPL(labels []CopyLabel, tmpl LabelTemplate) []byte {
	lanes := tmpl.laneCount()
	rollWmm := tmpl.RollWidthIn() * 25.4
	hMm := tmpl.LabelHIn * 25.4
	gapMm := tmpl.GapYIn * 25.4

	var sb strings.Builder
	for i := 0; i < len(labels); i += lanes {
		fmt.Fprintf(&sb, "SIZE %.2f mm,%.2f mm\n", rollWmm, hMm)
		fmt.Fprintf(&sb, "GAP %.2f mm,0 mm\n", gapMm)
		sb.WriteString("DIRECTION 0\n")
		sb.WriteString("REFERENCE 0,0\n")
		sb.WriteString("CLS\n")

		for lane := 0; lane < lanes && i+lane < len(labels); lane++ {
			writeTSPLLabel(&sb, labels[i+lane], tmpl, tmpl.LaneXOffsetDots(lane))
		}

		sb.WriteString("PRINT 1,1\n")
	}
	return []byte(sb.String())
}

func writeTSPLLabel(sb *strings.Builder, l CopyLabel, tmpl LabelTemplate, xOffset int) {
	w, h := tmpl.WidthDots(), tmpl.HeightDots()
	rot := 0
	if tmpl.Rotate {
		rot = 90
	}
	marginX := xOffset + w/20
	titleMult := clampMult(h / 10 / 24)
	subMult := clampMult(h / 16 / 24)
	lineGap := h / 40

	y := h / 20

	if l.Title != "" {
		fmt.Fprintf(sb, "TEXT %d,%d,\"3\",%d,%d,%d,\"%s\"\n", marginX, y, rot, titleMult, titleMult, tsplEscape(trunc(l.Title, 38)))
		y += (24 * titleMult) + lineGap
	}
	if l.CallNumber != "" {
		fmt.Fprintf(sb, "TEXT %d,%d,\"3\",%d,%d,%d,\"%s\"\n", marginX, y, rot, subMult, subMult, tsplEscape(trunc(l.CallNumber, 42)))
		y += (24 * subMult) + lineGap
	}

	barH := h - y - h/20
	if barH < 60 {
		barH = 60
	}
	fmt.Fprintf(sb, "BARCODE %d,%d,\"128\",%d,1,%d,2,2,\"%s\"\n", marginX+w/40, y, barH, rot, tsplEscape(l.Barcode))
}

func clampMult(n int) int {
	if n < 1 {
		return 1
	}
	if n > 10 {
		return 10
	}
	return n
}

func tsplEscape(s string) string {
	r := strings.NewReplacer("\"", "'", "\n", " ", "\r", " ")
	return r.Replace(s)
}
