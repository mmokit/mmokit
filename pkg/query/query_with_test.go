package query

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

type withTestSystem struct {
	w *ecs.World
	q Query[struct {
		Pos *component.Position
	}]
}

func (s *withTestSystem) ECSWorld() *ecs.World { return s.w }

func TestQuery_With_AccumulatesOptions(t *testing.T) {
	w := ecs.NewWorld()
	s := &withTestSystem{w: w}

	// Configure: include-all + exclude Velocity.
	s.q.With(IncludeAll())
	s.q.With(Without[component.Velocity]())

	// Test exercises the manual build path; the framework auto-builds via
	// SystemBase.BindQueries/BuildQueries in production.
	s.q.BuildFromECS(w)

	// No exclusion of Ghost/Replica because IncludeAll cleared them.
	// Should still range without panic.
	count := 0
	for range s.q.Iter {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 entities, got %d", count)
	}
}

func TestQuery_With_AfterBuild_Panics(t *testing.T) {
	w := ecs.NewWorld()
	s := &withTestSystem{w: w}
	s.q.With(IncludeAll())
	s.q.BuildFromECS(w)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	s.q.With(Without[component.Velocity]())
}
