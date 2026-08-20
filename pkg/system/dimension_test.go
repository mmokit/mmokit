package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestDimensionString(t *testing.T) {
	for _, c := range []struct {
		d    Dimension
		want string
	}{
		{Dimension2D, "2d"},
		{Dimension3D, "3d"},
		{Dimension(99), "unknown"},
	} {
		if got := c.d.String(); got != c.want {
			t.Errorf("Dimension(%d).String() = %q, want %q", uint8(c.d), got, c.want)
		}
	}
}

// The zero value has to be 2D. Every Config built by a test fixture leaves
// Dimension unset, and a zero value meaning "3d" or "unknown" would make every
// one of them panic at construction.
func TestDimensionZeroValueIs2D(t *testing.T) {
	var d Dimension
	if d != Dimension2D {
		t.Fatalf("zero Dimension = %v, want Dimension2D", d)
	}
	if set := EngineBindingsFor(d); set.Dimension != Dimension2D || set.Bindings == nil {
		t.Fatalf("EngineBindingsFor(zero) = %+v, want the 2D set", set)
	}
}

// The concern this replaces was that selecting 3D must fail loudly rather than
// fall back to 2D, because a silent fallback produces a server that encodes two
// coordinates while its operator believes it encodes three — and nothing
// downstream can detect that, since one component set across profiles means a
// 2D and a 3D Position have identical type IDs.
//
// Phase 2 implemented the profile, so "panics" is no longer the assertion. The
// concern is unchanged, and this is its forward form: selecting 3D must return
// a set that is genuinely 3D. A fallback would now be silent rather than loud,
// which is strictly worse than the panic it replaced — so it is pinned here.
func TestEngineBindingsFor3DIsNotTheTwoDSet(t *testing.T) {
	set := EngineBindingsFor(Dimension3D)
	if set.Dimension != Dimension3D {
		t.Fatalf("EngineBindingsFor(Dimension3D).Dimension = %v, want Dimension3D", set.Dimension)
	}
	if set.Bindings == nil {
		t.Fatal("EngineBindingsFor(Dimension3D) has no bindings")
	}

	world := ecs.NewWorld()
	got3D := set.Bindings(world, 1000, 500, 2000).snapshotFields()
	got2D := EngineBindingsFor(Dimension2D).Bindings(world, 1000, 500, 2000).snapshotFields()
	if len(got3D) == len(got2D) {
		t.Fatalf("3D and 2D emit the same %d fields (%v vs %v) — 3D fell back to 2D",
			len(got3D), got3D, got2D)
	}
}

func TestEngineBindingsForUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("EngineBindingsFor on an unknown dimension should panic")
		}
	}()
	EngineBindingsFor(Dimension(99))
}
