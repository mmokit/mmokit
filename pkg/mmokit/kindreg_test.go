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
		reflect.TypeOf(kindRegTestNameComp{}):   true,
		reflect.TypeOf(kindRegTestHealthComp{}): true,
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
