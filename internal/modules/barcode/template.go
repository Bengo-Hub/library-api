package barcode

import "strings"

// LabelTemplate describes a physical label roll/stock: one lane's size, how many lanes sit
// side-by-side across the roll's width, gaps, DPI, and whether content must be rotated 90° to
// read correctly given how the roll is mounted in the printer.
//
// Mirrors inventory-api's internal/modules/barcode LabelTemplate (same field shapes/semantics)
// — kept as its own local copy rather than a shared package (see docs/barcode-labels.md's scope
// decision), since library's CopyLabel never needs GS1-128/lot/serial. The bug this fixes: a
// librarian printing holding labels to a thermal roll printer (e.g. an Xprinter XP-330B) had NO
// thermal-native output at all — RenderPDF/RenderSheet only ever produced an Avery-sheet-shaped
// PDF meant for a cut-sheet office printer, forcing the operator to force-fit that PDF onto a
// thermal roll via guessed Windows paper presets — exactly the failure mode that produces
// rotated/misaligned prints (see the bulk endpoint's hardcoded "copy-labels.pdf" filename,
// which is the file this bug was originally reported against).
type LabelTemplate struct {
	Name string

	LabelWIn float64 // one lane's label width, inches (content's natural/unrotated orientation)
	LabelHIn float64 // label height along the feed direction, inches
	DPI      int

	Lanes  int     // 1-4 labels side by side across the roll's width ("rows")
	GapXIn float64 // gutter BETWEEN lanes, inches (0 when Lanes==1)
	GapYIn float64 // gap between labels along the feed direction, inches (die-cut gap)

	// Rotate: true when the physical media is mounted so content must print turned 90° to read
	// correctly along the feed direction. See RenderPDF's transform and renderTSPL's per-command
	// rotation param.
	Rotate bool

	Custom bool
}

// RollWidthIn returns the full roll width in inches: all lanes plus the gutters between them.
func (t LabelTemplate) RollWidthIn() float64 {
	lanes := t.laneCount()
	return float64(lanes)*t.LabelWIn + float64(lanes-1)*t.GapXIn
}

func (t LabelTemplate) WidthDots() int     { return int(t.LabelWIn * float64(t.DPI)) }
func (t LabelTemplate) HeightDots() int    { return int(t.LabelHIn * float64(t.DPI)) }
func (t LabelTemplate) RollWidthDots() int { return int(t.RollWidthIn() * float64(t.DPI)) }
func (t LabelTemplate) GapXDots() int      { return int(t.GapXIn * float64(t.DPI)) }

// LaneXOffsetDots returns the x-offset (dots, from the roll's left edge) at which the given
// zero-based lane index starts — used by renderTSPL to tile labels side-by-side across the roll
// width instead of one at a time.
func (t LabelTemplate) LaneXOffsetDots(lane int) int {
	return lane * (t.WidthDots() + t.GapXDots())
}

func (t LabelTemplate) laneCount() int {
	switch {
	case t.Lanes < 1:
		return 1
	case t.Lanes > 4:
		return 4
	default:
		return t.Lanes
	}
}

// namedLabelTemplates are the built-in presets. "1row_62x29" is an exact alias of the
// pre-existing hardcoded RenderPDF size (62mm×29mm) so old callers keep working unchanged. The
// multi-row presets are new — engineering estimates fit within a ≤80mm thermal roll width (the
// Xprinter XP-330B's confirmed media width), NOT vendor-confirmed exact stock. A library with
// different real stock should use "custom" instead of assuming one of these matches exactly.
func namedLabelTemplates() map[string]LabelTemplate {
	return map[string]LabelTemplate{
		"1row_62x29": {
			Name: "1 row — 62x29mm (spine/holding label)", LabelWIn: 62.0 / 25.4, LabelHIn: 29.0 / 25.4,
			DPI: 203, Lanes: 1, GapYIn: 2.0 / 25.4,
		},
		"2row_35x29": {
			Name: "2 rows — 35x29mm each", LabelWIn: 35.0 / 25.4, LabelHIn: 29.0 / 25.4,
			DPI: 203, Lanes: 2, GapXIn: 2.0 / 25.4, GapYIn: 2.0 / 25.4,
		},
		"3row_23x29": {
			Name: "3 rows — 23x29mm each", LabelWIn: 23.0 / 25.4, LabelHIn: 29.0 / 25.4,
			DPI: 203, Lanes: 3, GapXIn: 1.5 / 25.4, GapYIn: 2.0 / 25.4,
		},
		"4row_17x29": {
			Name: "4 rows — 17x29mm each", LabelWIn: 17.0 / 25.4, LabelHIn: 29.0 / 25.4,
			DPI: 203, Lanes: 4, GapXIn: 1.0 / 25.4, GapYIn: 2.0 / 25.4,
		},
	}
}

// LabelTemplateByName resolves a request-supplied template/preset name; unknown/empty falls back
// to the pre-existing 62x29mm single-lane default so old callers with no opinion keep working.
func LabelTemplateByName(name string) LabelTemplate {
	key := strings.ToLower(strings.TrimSpace(name))
	if t, ok := namedLabelTemplates()[key]; ok {
		if t.Lanes < 1 {
			t.Lanes = 1
		}
		return t
	}
	return namedLabelTemplates()["1row_62x29"]
}

// CustomLabelTemplate builds a template from caller-supplied physical dimensions. lanes clamps to 1-4.
func CustomLabelTemplate(wIn, hIn float64, lanes int, gapXIn, gapYIn float64, rotate bool) LabelTemplate {
	if lanes < 1 {
		lanes = 1
	}
	if lanes > 4 {
		lanes = 4
	}
	if wIn <= 0 {
		wIn = 62.0 / 25.4
	}
	if hIn <= 0 {
		hIn = 29.0 / 25.4
	}
	return LabelTemplate{
		Name:     "Custom",
		LabelWIn: wIn, LabelHIn: hIn, DPI: 203,
		Lanes: lanes, GapXIn: gapXIn, GapYIn: gapYIn,
		Rotate: rotate, Custom: true,
	}
}
