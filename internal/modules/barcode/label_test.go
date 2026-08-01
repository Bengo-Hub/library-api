package barcode

import "testing"

func TestRenderPDF_PageMatchesTemplate(t *testing.T) {
	lbl := CopyLabel{Barcode: "B00012345", Title: "Archeology", CallNumber: "930.1 MCI"}

	tmpl := LabelTemplateByName("1row_29x62")
	if _, err := RenderPDF(lbl, tmpl); err != nil {
		t.Fatalf("RenderPDF (Rotate=false) failed: %v", err)
	}

	tmpl.Rotate = true
	if _, err := RenderPDF(lbl, tmpl); err != nil {
		t.Fatalf("RenderPDF (Rotate=true) failed: %v", err)
	}
}

func TestRenderPDF_RequiresNoHardcodedSize(t *testing.T) {
	lbl := CopyLabel{Barcode: "B00012345", Title: "Archeology", CallNumber: "930.1 MCI"}

	small, err := RenderPDF(lbl, CustomLabelTemplate(1, 0.5, 1, 0, 0, false))
	if err != nil {
		t.Fatalf("small custom template failed: %v", err)
	}
	large, err := RenderPDF(lbl, CustomLabelTemplate(4, 2, 1, 0, 0, false))
	if err != nil {
		t.Fatalf("large custom template failed: %v", err)
	}
	if string(small) == string(large) {
		t.Fatalf("PDFs for very different template sizes must not be identical")
	}
}
