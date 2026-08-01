package barcode

import (
	"strconv"
	"strings"
	"testing"
)

func sampleCopyLabels(n int) []CopyLabel {
	labels := make([]CopyLabel, n)
	for i := range labels {
		labels[i] = CopyLabel{Barcode: "B00012345", Title: "Archeology", CallNumber: "930.1 MCI"}
	}
	return labels
}

func TestRenderThermalTSPL_RotationParam(t *testing.T) {
	tmpl := LabelTemplateByName("1row_62x29")

	tmpl.Rotate = false
	out := string(RenderThermalTSPL(sampleCopyLabels(1), tmpl))
	if strings.Contains(out, ",90,") {
		t.Fatalf("Rotate=false should not emit a 90 rotation param:\n%s", out)
	}

	tmpl.Rotate = true
	out = string(RenderThermalTSPL(sampleCopyLabels(1), tmpl))
	if !strings.Contains(out, ",90,") {
		t.Fatalf("Rotate=true should emit a 90 rotation param:\n%s", out)
	}
}

func TestRenderThermalTSPL_MultiRowTilesOneBlockPerRow(t *testing.T) {
	tmpl := LabelTemplateByName("3row_23x29")
	out := string(RenderThermalTSPL(sampleCopyLabels(3), tmpl))

	if got := strings.Count(out, "CLS"); got != 1 {
		t.Fatalf("3 labels at 3 lanes should be ONE CLS/PRINT block (one feed-row), got %d", got)
	}
	wantW := strconv.FormatFloat(tmpl.RollWidthIn()*25.4, 'f', 2, 64)
	if !strings.Contains(out, "SIZE "+wantW+" mm,") {
		t.Fatalf("SIZE should use the full roll width (%s mm):\n%s", wantW, out)
	}
	if got := strings.Count(out, "BARCODE"); got != 3 {
		t.Fatalf("expected 3 barcode commands (one per lane), got %d", got)
	}

	out2 := string(RenderThermalTSPL(sampleCopyLabels(4), tmpl))
	if got := strings.Count(out2, "CLS"); got != 2 {
		t.Fatalf("4 labels at 3 lanes should span 2 feed-rows, got %d CLS blocks", got)
	}
}
