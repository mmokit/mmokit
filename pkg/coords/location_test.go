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
