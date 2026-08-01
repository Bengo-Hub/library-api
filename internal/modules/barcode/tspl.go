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

// textFont is TSPL's smallest built-in bitmap font ("1", documented as 8×12 dots at multiplier
// 1) — switched from the larger "3" font (16×24) after a bench print showed title/call-number
// text at "3" dominating a 29mm-wide label, leaving little room to balance against the barcode.
const textFont = "1"
const fontCharWDots = 8
const fontCharHDots = 12

func writeTSPLLabel(sb *strings.Builder, l CopyLabel, tmpl LabelTemplate, xOffset int) {
	w, h := tmpl.WidthDots(), tmpl.HeightDots()
	rot := 0
	if tmpl.Rotate {
		rot = 90
	}
	marginX := xOffset + w/20
	availW := w - 2*(w/20)
	if availW < fontCharWDots {
		availW = fontCharWDots
	}

	// Font multiplier is sized off the label's SMALLER dimension, not height alone — a tall-but-
	// narrow label (this module's real 29x62mm spine-label roll) has plenty of height to spare
	// but almost no width, so a height-only formula inflated text well past what the width could
	// actually hold, dominating the label and leaving little room to balance against the
	// barcode. Still clamped down further until a reasonably short (~12-char) string would fit.
	minDim := h
	if w < minDim {
		minDim = w
	}
	titleMult := clampMult(minDim / 10 / fontCharHDots)
	for titleMult > 1 && titleMult*fontCharWDots*12 > availW {
		titleMult--
	}
	subMult := clampMult(minDim / 16 / fontCharHDots)
	for subMult > 1 && subMult*fontCharWDots*12 > availW {
		subMult--
	}
	lineGap := h / 40

	// Start with more clearance than a bare margin (h/20) — content is drawn from low y upward,
	// and on this printer/roll combination low-y content lands closest to the physical edge the
	// operator tears along, so a title/first line placed flush against y=h/20 was getting clipped
	// by the tear rather than sitting fully inside the printable label.
	y := h / 10

	if l.Title != "" {
		maxChars := maxInt(availW/(fontCharWDots*titleMult), 4)
		text := trunc(l.Title, maxChars)
		x := marginX + centerOffsetDots(availW, len([]rune(text))*fontCharWDots*titleMult)
		fmt.Fprintf(sb, "TEXT %d,%d,\"%s\",%d,%d,%d,\"%s\"\n", x, y, textFont, rot, titleMult, titleMult, tsplEscape(text))
		y += (fontCharHDots * titleMult) + lineGap
	}
	if l.CallNumber != "" {
		maxChars := maxInt(availW/(fontCharWDots*subMult), 4)
		text := trunc(l.CallNumber, maxChars)
		x := marginX + centerOffsetDots(availW, len([]rune(text))*fontCharWDots*subMult)
		fmt.Fprintf(sb, "TEXT %d,%d,\"%s\",%d,%d,%d,\"%s\"\n", x, y, textFont, rot, subMult, subMult, tsplEscape(text))
		y += (fontCharHDots * subMult) + lineGap
	}

	// Barcode height: capped relative to the label's WIDTH (not just "half the remaining
	// height") — on a tall/narrow label (this module's real 29x62mm roll), sizing off height
	// alone produced bars taller than the barcode's own natural width, reading as a dense
	// vertical column instead of the normal short-and-wide look a Code128 symbol should have.
	remaining := h - y - h/20
	barH := remaining / 2
	if maxBarH := w * 2 / 5; barH > maxBarH {
		barH = maxBarH
	}
	if barH < 60 {
		barH = 60
	}
	if barH > remaining {
		barH = remaining
	}
	const narrowBarDots = 2
	barX := marginX + centerOffsetDots(availW, estimateCode128WidthDots(l.Barcode, narrowBarDots))
	fmt.Fprintf(sb, "BARCODE %d,%d,\"128\",%d,1,%d,%d,%d,\"%s\"\n", barX, y, barH, rot, narrowBarDots, narrowBarDots, tsplEscape(l.Barcode))
}

// estimateCode128WidthDots approximates a Code128 symbol's printed width in dots: (start +
// data-and-check symbol characters, 11 modules each + stop, 13 modules) × the narrow-bar module
// width. This treats every input byte as one symbol character (Code128's Set-C digit-pairing can
// make real barcodes narrower than this estimate for numeric-heavy content) — an intentional
// over-estimate so the centering computed from it never pushes the symbol's actual (narrower)
// render past the label's right edge.
func estimateCode128WidthDots(content string, narrowBarDots int) int {
	modules := (len(content)+2)*11 + 13
	return modules * narrowBarDots
}

// centerOffsetDots returns how far to shift right from the left margin to center content of
// contentWidthDots within availWidthDots — 0 (flush-left, not negative) if the content is
// already at least as wide as the available space.
func centerOffsetDots(availWidthDots, contentWidthDots int) int {
	off := (availWidthDots - contentWidthDots) / 2
	if off < 0 {
		return 0
	}
	return off
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func tsplEscape(s string) string {
	r := strings.NewReplacer("\"", "'", "\n", " ", "\r", " ")
	return r.Replace(s)
}
