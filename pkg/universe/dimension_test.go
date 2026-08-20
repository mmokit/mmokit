package universe

import (
	"reflect"
	"testing"

	"github.com/mmokit/mmokit/pkg/system"
)

// Dimension must be an ALIAS of system.Dimension, not a mirror of it. A mirror
// plus a cast at the package boundary is two sources of truth for one value,
// and reflect.Type identity is what tells the two apart: an alias is the same
// type, a defined type with the same underlying kind is not.
func TestDimensionIsAnAliasNotAMirror(t *testing.T) {
	if got, want := reflect.TypeFor[Dimension](), reflect.TypeFor[system.Dimension](); got != want {
		t.Fatalf("universe.Dimension is %v, want it to BE %v — mirroring the enum "+
			"reintroduces the two-sources-of-truth defect part A deleted", got, want)
	}
	if Dimension2D != system.Dimension2D || Dimension3D != system.Dimension3D {
		t.Error("dimension constants disagree with pkg/system")
	}
}

// New defaults to 2D and exposes it. Every Config a fixture builds leaves
// Dimension unset, so the zero value has to be the working profile.
func TestNewDefaultsTo2D(t *testing.T) {
	c := New(Config{CellsX: 1, CellsY: 1, Headless: true})
	if got := c.Dimension(); got != Dimension2D {
		t.Fatalf("Process.Dimension() = %v, want 2d", got)
	}
}

// The concern this replaces was that an unimplemented profile must fail at
// construction, where the panic names the config field, rather than several
// frames into schema assembly. Phase 2 implemented 3D, so the assertion
// becomes its forward form: constructing with Dimension3D succeeds and the
// process reports the profile it was actually given.
//
// The loud-failure guarantee itself is not lost — TestEngineBindingsForUnknown
// Panics in pkg/system still covers a dimension with no binding set, which is
// what a future profile would be.
func TestNewAcceptsDimension3D(t *testing.T) {
	c := New(Config{CellsX: 1, CellsY: 1, Headless: true, Dimension: Dimension3D})
	if got := c.Dimension(); got != Dimension3D {
		t.Fatalf("Process.Dimension() = %v, want 3d", got)
	}
}
