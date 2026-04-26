package engine

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

type stubWorld struct{ tag string }

type testSystem struct {
	SystemBase[*stubWorld]
}

func (s *testSystem) Update(dt float32) {}

func TestSystemBase_TypedWorld(t *testing.T) {
	s := &testSystem{}
	w := &stubWorld{tag: "hello"}
	s.SetDeps(ecs.NewWorld(), nil, w)

	got := s.World()
	if got == nil || got.tag != "hello" {
		t.Fatalf("World() returned %+v, want stubWorld{tag:\"hello\"}", got)
	}
}

func TestSystemBase_TypeMismatch_Panics(t *testing.T) {
	type wrongWorld struct{}
	s := &testSystem{}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	s.SetDeps(ecs.NewWorld(), nil, &wrongWorld{})
}

func TestSystemBase_NilGameWorld_OK_ForAny(t *testing.T) {
	type anySystem struct{ SystemBase[any] }
	s := &anySystem{}
	// Should NOT panic.
	s.SetDeps(ecs.NewWorld(), nil, nil)
	if s.World() != nil {
		t.Fatalf("expected nil World, got %v", s.World())
	}
}

func TestSystemBase_NilGameWorld_OK_ForPointer(t *testing.T) {
	s := &testSystem{}
	// Should NOT panic — typed nil → *stubWorld is fine.
	s.SetDeps(ecs.NewWorld(), nil, (*stubWorld)(nil))
	if s.World() != nil {
		t.Fatalf("expected nil World, got %v", s.World())
	}
}
