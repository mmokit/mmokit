package engine

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/query"
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

type autoBindSystem struct {
	SystemBase[*stubWorld]
	pos query.Query[struct {
		Pos *component.Position
	}]
}

func (s *autoBindSystem) Update(dt float32) {}

func TestSystemBase_AutoBindsQueries(t *testing.T) {
	s := &autoBindSystem{}
	w := ecs.NewWorld()
	s.SetDeps(w, nil, &stubWorld{})

	// Mimic framework lifecycle.
	s.BindQueries(s)
	s.Init()
	s.BuildQueries()

	// Range without panic; default exclusions apply (Ghost + Replica).
	count := 0
	for range s.pos.Iter {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

type ghostExclusionSystem struct {
	SystemBase[*stubWorld]
	q query.Query[struct {
		Pos *component.Position
	}]
}

func (s *ghostExclusionSystem) Update(dt float32) {}

func TestSystemBase_DefaultExclusions(t *testing.T) {
	s := &ghostExclusionSystem{}
	w := ecs.NewWorld()
	s.SetDeps(w, nil, &stubWorld{})
	s.BindQueries(s)
	s.Init()
	s.BuildQueries()

	// Spawn one entity with a Ghost component — it should be excluded.
	mapper := ecs.NewMap2[component.Position, component.Ghost](w)
	mapper.NewEntity(&component.Position{}, &component.Ghost{})

	count := 0
	for range s.q.Iter {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 (Ghost excluded by default), got %d", count)
	}
}
