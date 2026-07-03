package payref

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBuild_FormatAndDeterminism(t *testing.T) {
	tenant := uuid.MustParse("5bce71cd-a29f-484f-adc7-3566aed6d14f")
	entity := uuid.MustParse("b2b59251-8e5d-4993-820e-7b140981d289")

	got := Build("LIB", "Urban Loft Cafe", tenant, entity)
	if got != "LIB-URBANL-B2B592518E5D" {
		t.Fatalf("unexpected reference: %s", got)
	}
	// Deterministic: same inputs → same reference (so treasury dedup prevents duplicate intents).
	if again := Build("LIB", "Urban Loft Cafe", tenant, entity); again != got {
		t.Fatalf("not deterministic: %s != %s", again, got)
	}
	// Three dash-separated segments, uppercased.
	if parts := strings.Split(got, "-"); len(parts) != 3 {
		t.Fatalf("want 3 segments, got %d in %s", len(parts), got)
	}
	if got != strings.ToUpper(got) {
		t.Fatalf("reference not uppercased: %s", got)
	}
}

func TestBuild_SlugFallbackAndSanitising(t *testing.T) {
	tenant := uuid.MustParse("5bce71cd-a29f-484f-adc7-3566aed6d14f")
	entity := uuid.MustParse("b2b59251-8e5d-4993-820e-7b140981d289")

	// Empty slug → falls back to tenant UUID hex (first 6).
	got := Build("POS", "", tenant, entity)
	if !strings.HasPrefix(got, "POS-5BCE71-") {
		t.Fatalf("slug fallback wrong: %s", got)
	}
	// Non-alphanumerics stripped, truncated to 6.
	if got := Build("ORD", "a-b_c!d e f g", tenant, entity); !strings.HasPrefix(got, "ORD-ABCDEF-") {
		t.Fatalf("slug sanitising wrong: %s", got)
	}
}

func TestBuild_DistinctEntitiesDistinctRefs(t *testing.T) {
	tenant := uuid.New()
	a := Build("LIB", "acme", tenant, uuid.New())
	b := Build("LIB", "acme", tenant, uuid.New())
	if a == b {
		t.Fatalf("distinct entities produced identical refs: %s", a)
	}
}
