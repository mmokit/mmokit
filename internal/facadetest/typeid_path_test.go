package facadetest

import (
	"hash/fnv"
	"reflect"
	"strings"
	"testing"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/internal/wasmfixtures/podcomp"
)

// fnv32aOf is the wire-ID hash restated independently of the implementation,
// so this file pins the contract rather than mirroring whatever the production
// code happens to do.
func fnv32aOf(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// TestTypeIDOf_IgnoresImportPath pins the property that makes relocating a
// registered type's package between directories wire-neutral.
//
// The client wire identity of a registered event, input, broadcast or
// operation type is fnv32a(reflect.Type.String()), and reflect.Type.String()
// qualifies by package NAME. podcomp is imported from
// .../internal/wasmfixtures/podcomp — five path segments — and still
// stringifies to "podcomp.Shield".
//
// If TypeIDOf is ever switched to PkgPath()+"."+Name() (as
// pkg/service.EventTypeName does for the server-internal event bus), every
// client wire ID in the tree changes silently, every generated SDK and wire
// golden needs regenerating, and moving any package becomes a protocol break.
// docs/roadmap.md non-goal 4, AGENTS.md and README.md all describe this
// mechanism; they were wrong about it until this test existed.
func TestTypeIDOf_IgnoresImportPath(t *testing.T) {
	typ := reflect.TypeOf(podcomp.Shield{})

	if got, want := typ.String(), "podcomp.Shield"; got != want {
		t.Fatalf("reflect.Type.String() = %q, want %q", got, want)
	}
	if strings.Contains(typ.String(), "/") {
		t.Fatalf("import path leaked into the wire identity: %q", typ.String())
	}
	if pkgPath := typ.PkgPath(); !strings.Contains(pkgPath, "/") {
		t.Fatalf("precondition failed: %q is not a nested import path, so this test proves nothing", pkgPath)
	}

	if got, want := mmokit.TypeIDOf(typ), fnv32aOf("podcomp.Shield"); got != want {
		t.Fatalf("TypeIDOf = %#x, want fnv32a(%q) = %#x", got, "podcomp.Shield", want)
	}
	if got := mmokit.TypeIDOf(typ); got == fnv32aOf(typ.PkgPath()+"."+typ.Name()) {
		t.Fatalf("TypeIDOf hashes the full import path %q — moving a package is now a wire break", typ.PkgPath())
	}
}
