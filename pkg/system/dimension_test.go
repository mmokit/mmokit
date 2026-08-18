package system

import (
	"strings"
	"testing"
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

// Selecting 3D must fail loudly rather than fall back to 2D. A silent fallback
// produces a server that encodes two coordinates while its operator believes it
// encodes three, and nothing downstream can detect that: one component set
// across profiles means a 2D and a 3D Position have identical type IDs.
func TestEngineBindingsFor3DPanicsRatherThanFallingBack(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("EngineBindingsFor(Dimension3D) returned instead of panicking — " +
				"a 2D fallback here is the exact undetectable mismatch the profile exists to prevent")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is %T, want string", r)
		}
		if !strings.Contains(msg, "not implemented") {
			t.Errorf("panic message does not say the profile is unimplemented: %q", msg)
		}
	}()
	EngineBindingsFor(Dimension3D)
}

func TestEngineBindingsForUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("EngineBindingsFor on an unknown dimension should panic")
		}
	}()
	EngineBindingsFor(Dimension(99))
}
