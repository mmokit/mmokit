package engine

import (
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestInputBinding_BasicFields(t *testing.T) {
	b := &InputBinding{}
	b.SetCode(42)
	b.SetProtoName("test.FooMsg")
	b.SetStateMask(States(StateActive))

	if b.Code() != 42 {
		t.Errorf("Code() = %d, want 42", b.Code())
	}
	if b.ProtoName() != "test.FooMsg" {
		t.Errorf("ProtoName() = %q, want test.FooMsg", b.ProtoName())
	}
	expected := StateMask(1) << StateMask(StateActive)
	if b.StateMask() != expected {
		t.Errorf("StateMask() = %d, want %d", b.StateMask(), expected)
	}
}

type testDeps struct {
	A *struct{ X int }
	B *struct{ Y int } `ecs:"optional"`
}

func TestBuildDepsLayout_Basic(t *testing.T) {
	layout := BuildDepsLayout(reflect.TypeOf(testDeps{}))
	if layout == nil {
		t.Fatal("BuildDepsLayout returned nil for valid struct")
	}
	if len(layout.Fields()) != 2 {
		t.Fatalf("len(Fields) = %d, want 2", len(layout.Fields()))
	}
	if layout.Fields()[0].Name != "A" || layout.Fields()[0].Optional {
		t.Errorf("field[0] = %+v, want Name=A Optional=false", layout.Fields()[0])
	}
	if layout.Fields()[1].Name != "B" || !layout.Fields()[1].Optional {
		t.Errorf("field[1] = %+v, want Name=B Optional=true", layout.Fields()[1])
	}
}

type badDeps struct {
	A struct{ X int } // not a pointer — should panic
}

func TestBuildDepsLayout_NonPointerField_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for non-pointer field")
		}
	}()
	BuildDepsLayout(reflect.TypeOf(badDeps{}))
}

type emptyDeps struct{}

func TestBuildDepsLayout_EmptyStruct_ReturnsNil(t *testing.T) {
	layout := BuildDepsLayout(reflect.TypeOf(emptyDeps{}))
	if layout != nil {
		t.Errorf("BuildDepsLayout(empty) = %v, want nil", layout)
	}
}

type dispCompA struct{ X int }
type dispCompB struct{ Y int }

type dispPopulateDeps struct {
	A *dispCompA
	B *dispCompB `ecs:"optional"`
}

func TestCellBinding_PopulateDeps_Required(t *testing.T) {
	w := ecs.NewWorld(64)
	layout := BuildDepsLayout(reflect.TypeOf(dispPopulateDeps{}))
	cb := &CellBinding{layout: layout}
	cb.pooledDeps = reflect.New(layout.StructType()).UnsafePointer()

	aMap := ecs.NewMap1[dispCompA](w)
	e := aMap.NewEntity(&dispCompA{X: 7})

	if !cb.PopulateDeps(w, e) {
		t.Fatal("PopulateDeps returned false for required-only deps that are present")
	}
	bundle := (*dispPopulateDeps)(cb.pooledDeps)
	if bundle.A == nil || bundle.A.X != 7 {
		t.Errorf("bundle.A = %+v, want X=7", bundle.A)
	}
	if bundle.B != nil {
		t.Errorf("bundle.B should be nil (optional, absent), got %+v", bundle.B)
	}
}

func TestCellBinding_PopulateDeps_RequiredAbsent(t *testing.T) {
	w := ecs.NewWorld(64)
	layout := BuildDepsLayout(reflect.TypeOf(dispPopulateDeps{}))
	cb := &CellBinding{layout: layout}
	cb.pooledDeps = reflect.New(layout.StructType()).UnsafePointer()

	// New entity without A.
	bMap := ecs.NewMap1[dispCompB](w)
	e := bMap.NewEntity(&dispCompB{Y: 1})

	if cb.PopulateDeps(w, e) {
		t.Error("PopulateDeps returned true when required field A absent")
	}
}

func TestInputDispatcher_AddBinding_DuplicateCode_Panics(t *testing.T) {
	eng := New(Config{TickRate: 20}, nil, nil)
	d := NewInputDispatcher(eng)

	b1 := &InputBinding{}
	b1.SetCode(1)
	d.AddBinding(b1)

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate code")
		}
	}()
	b2 := &InputBinding{}
	b2.SetCode(1)
	d.AddBinding(b2)
}

func TestInputDispatcher_Lookup(t *testing.T) {
	eng := New(Config{TickRate: 20}, nil, nil)
	d := NewInputDispatcher(eng)

	b := &InputBinding{}
	b.SetCode(42)
	d.AddBinding(b)

	if got := d.Lookup(42); got == nil {
		t.Error("Lookup(42) returned nil after AddBinding")
	}
	if got := d.Lookup(99); got != nil {
		t.Errorf("Lookup(99) = %v, want nil", got)
	}
}
