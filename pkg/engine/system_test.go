package engine

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/query"
)

type testSystem struct {
	SystemBase
}

type autoBindSystem struct {
	SystemBase
	pos query.Query[struct {
		Pos *component.Position
	}]
}

func (s *autoBindSystem) Update(dt float32) {}

func TestSystemBase_AutoBindsQueries(t *testing.T) {
	s := &autoBindSystem{}
	w := ecs.NewWorld()
	s.SetDeps(w, nil)

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
	SystemBase
	q query.Query[struct {
		Pos *component.Position
	}]
}

func (s *ghostExclusionSystem) Update(dt float32) {}

func TestSystemBase_DefaultExclusions(t *testing.T) {
	s := &ghostExclusionSystem{}
	w := ecs.NewWorld()
	s.SetDeps(w, nil)
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

func TestSystemBase_DefaultUpdate_NoOp(t *testing.T) {
	// testSystem embeds SystemBase and doesn't override Update — should not panic.
	s := &testSystem{}
	s.SetDeps(ecs.NewWorld(), nil)
	s.Update(0.05)
}
