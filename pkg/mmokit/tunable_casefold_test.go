package mmokit

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/tunable"
)

// Operators type field names however they like; findDef resolves them
// case-insensitively and returns the canonical (PascalCase) Name so the
// registry key and per-cell Source.Set stay exact.
func TestFindDef_CaseInsensitive(t *testing.T) {
	defs := []tunable.Def{{Name: "MaxRadius"}, {Name: "Rate"}}
	if d, ok := findDef(defs, "maxradius"); !ok || d.Name != "MaxRadius" {
		t.Fatalf("maxradius should resolve to MaxRadius: got %q ok=%v", d.Name, ok)
	}
	if d, ok := findDef(defs, "RATE"); !ok || d.Name != "Rate" {
		t.Fatalf("RATE should resolve to Rate: got %q ok=%v", d.Name, ok)
	}
	if _, ok := findDef(defs, "nope"); ok {
		t.Fatal("unknown field must not match")
	}
}
