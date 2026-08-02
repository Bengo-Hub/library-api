package barcode

import (
	"bytes"
	"fmt"

	"github.com/go-pdf/fpdf"
)

// RenderThermalPreviewPDF renders a multi-page PDF preview matching EXACTLY what
// RenderThermalTSPL would print for the same labels/template — one page per label, each page
// sized (and rotated, if tmpl.Rotate) to the SAME physical LabelTemplate dimensions used for the
// real TSPL job, so an operator can visually confirm placement/orientation BEFORE dispatching to
// the printer. This is deliberately NOT the Avery-sheet grid (RenderSheet) — that's a different
// physical format (cut-sheet office paper laid out in columns), and silently substituting it as
// a "preview" for a thermal-roll job showed the operator a misleadingly different layout (a
// multi-column grid) from what would actually print, which is what led to labels being printed
// in columns on a single-lane thermal roll. Reuses the same page-dims-and-rotate-transform
// pattern as RenderPDF (the single-item endpoint), just looped once per label instead of once.
func RenderThermalPreviewPDF(labels []CopyLabel, tmpl LabelTemplate) ([]byte, error) {
	if len(labels) == 0 {
		return nil, fmt.Errorf("barcode: no labels to render")
	}
	w, h := tmpl.LabelWIn*25.4, tmpl.LabelHIn*25.4 // mm
	pageW, pageH := w, h
	if tmpl.Rotate {
		pageW, pageH = h, w
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{UnitStr: "mm", Size: fpdf.SizeType{Wd: pageW, Ht: pageH}})
	pdf.SetAutoPageBreak(false, 0)
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	imgSeq := 0

	for _, lbl := range labels {
		pdf.AddPage()
		if tmpl.Rotate {
			pdf.TransformBegin()
			pdf.TransformRotate(90, pageW/2, pageH/2)
			drawCopyLabelCell(pdf, tr, lbl, "", (pageW-w)/2, (pageH-h)/2, w, h, &imgSeq)
			pdf.TransformEnd()
		} else {
			drawCopyLabelCell(pdf, tr, lbl, "", 0, 0, w, h, &imgSeq)
		}
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("barcode: render thermal preview pdf: %w", err)
	}
	return out.Bytes(), nil
}
