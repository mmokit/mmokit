package mmokit

import (
	"reflect"
	"testing"
)

type kindRegTestNameComp struct{ Name string }
type kindRegTestHealthComp struct{ HP float32 }

type kindRegTestBundle struct {
	Name   *kindRegTestNameComp
	Health *kindRegTestHealthComp
}

func TestRegisterKind_BuildsKindSpec(t *testing.T) {
	// Spy on the realize fn — record each component type registered.
	var registered []reflect.Type

	spec := buildKindSpec[kindRegTestBundle](42, "TestKind", nil, func(t reflect.Type) {
		registered = append(registered, t)
	})

	if spec == nil {
		t.Fatal("expected non-nil kind spec")
	}
	if len(registered) != 2 {
		t.Fatalf("expected 2 components registered, got %d (%v)", len(registered), registered)
	}
	want := map[reflect.Type]bool{
		reflect.TypeFor[kindRegTestNameComp]():   true,
		reflect.TypeFor[kindRegTestHealthComp](): true,
	}
	for _, ty := range registered {
		if !want[ty] {
			t.Errorf("unexpected component type %v registered", ty)
		}
	}
}

func TestRegisterKind_RejectsNonStruct(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on non-struct T")
		}
	}()
	buildKindSpec[int](0, "Bad", nil, func(reflect.Type) {})
}

func TestRegisterKind_RejectsNonPointerField(t *testing.T) {
	type badBundle struct {
		Name kindRegTestNameComp // value, not pointer
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on non-pointer field")
		}
	}()
	buildKindSpec[badBundle](0, "Bad", nil, func(reflect.Type) {})
}

func TestRegisterKind_RejectsEmptyBundle(t *testing.T) {
	type emptyBundle struct{}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on bundle with zero exported pointer-to-struct fields")
		}
	}()
	buildKindSpec[emptyBundle](0, "Empty", nil, func(reflect.Type) {})
}

func TestRegisterKind_RejectsAllUnexportedFields(t *testing.T) {
	type unexportedBundle struct {
		name *kindRegTestNameComp // lowercase = unexported
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on bundle with no exported fields")
		}
	}()
	buildKindSpec[unexportedBundle](0, "Unexported", nil, func(reflect.Type) {})
}

func TestRegisterKind_RealizesPerCell(t *testing.T) {
	mmo := New(Config{
		CellsX:   1,
		CellsY:   1,
		CellSize: 1000,
		TickRate: 20,
		AoIRadius: 100,
		Headless: true,
	})
	RegisterKind[kindRegTestBundle](mmo, 100, "TestKind", EngineBindingsConfig{})
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cells := mmo.Cells
	var cell *Cell
	for _, c := range cells {
		cell = c
		break
	}
	if cell == nil {
		t.Fatal("expected at least one cell")
	}
	defs := cell.Stage.EntityKindDefs()
	def, ok := defs[100]
	if !ok {
		t.Fatalf("kind 100 not registered on cell %s", cell.ID)
	}
	if def.Name != "TestKind" {
		t.Errorf("expected kind name TestKind, got %q", def.Name)
	}
}
