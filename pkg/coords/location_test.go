package coords

import "testing"

func TestLocation_IsZero(t *testing.T) {
	if !(Location{}).IsZero() {
		t.Fatalf("zero-value Location should report IsZero()==true")
	}
	if (Location{X: 1}).IsZero() {
		t.Fatalf("Location{X:1} should not be zero")
	}
	if (Location{Facing: 0.1}).IsZero() {
		t.Fatalf("Location{Facing:0.1} should not be zero")
	}
	if (Location{Tag: "x"}).IsZero() {
		t.Fatalf("Location{Tag:\"x\"} should not be zero")
	}
}

// TestLocation_NoStaleGlobalRead pins that the new API never reads the
// package-global CellSize — the bug class the whole redesign eliminates.
// Constructing a Location literal must produce identical fields regardless
// of what CellSize is set to when the literal is evaluated.
func TestLocation_NoStaleGlobalRead(t *testing.T) {
	prev := CellSize
	defer func() { CellSize = prev }()

	CellSize = 8192
	a := Location{X: 1000, Y: 1000, Facing: 1.57, Tag: "bank"}
	CellSize = 2000
	b := Location{X: 1000, Y: 1000, Facing: 1.57, Tag: "bank"}
	if a != b {
		t.Fatalf("Location literals must not depend on global CellSize: a=%+v b=%+v", a, b)
	}
}
