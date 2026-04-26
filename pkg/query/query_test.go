package query

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

type queryTestSys struct {
	world *ecs.World
}

func (s *queryTestSys) ECSWorld() *ecs.World { return s.world }

func TestQueryBasicIteration(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	mapper := ecs.NewMap2[component.Position, component.Velocity](world)
	mapper.NewEntity(&component.Position{X: 10, Y: 20}, &component.Velocity{X: 1, Y: 2})
	mapper.NewEntity(&component.Position{X: 30, Y: 40}, &component.Velocity{X: 3, Y: 4})

	var q Query[struct {
		Pos *component.Position
		Vel *component.Velocity
	}]
	q.With()
	q.BuildFromECS(sys.ECSWorld())

	count := 0
	for _, b := range q.Iter {
		if b.Pos == nil || b.Vel == nil {
			t.Fatal("bundle fields should not be nil")
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 entities, got %d", count)
	}
}

func TestQueryMutatesComponents(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	mapper := ecs.NewMap2[component.Position, component.Velocity](world)
	entity := mapper.NewEntity(&component.Position{X: 10, Y: 20}, &component.Velocity{X: 1, Y: 2})

	var q Query[struct {
		Pos *component.Position
		Vel *component.Velocity
	}]
	q.With()
	q.BuildFromECS(sys.ECSWorld())

	for _, b := range q.Iter {
		b.Pos.X += b.Vel.X
		b.Pos.Y += b.Vel.Y
	}

	posMap := ecs.NewMap1[component.Position](world)
	pos := posMap.Get(entity)
	if pos.X != 11 || pos.Y != 22 {
		t.Errorf("expected (11, 22), got (%.0f, %.0f)", pos.X, pos.Y)
	}
}

func TestQueryOptionalField(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	m2 := ecs.NewMap2[component.Position, component.Velocity](world)
	m1 := ecs.NewMap1[component.Position](world)

	m2.NewEntity(&component.Position{X: 1}, &component.Velocity{X: 10})
	m1.NewEntity(&component.Position{X: 2})

	var q Query[struct {
		Pos *component.Position
		Vel *component.Velocity `ecs:"optional"`
	}]
	q.With(IncludeAll())
	q.BuildFromECS(sys.ECSWorld())

	var withVel, withoutVel int
	for _, b := range q.Iter {
		if b.Vel != nil {
			withVel++
		} else {
			withoutVel++
		}
	}
	if withVel != 1 || withoutVel != 1 {
		t.Errorf("expected 1 with vel, 1 without; got %d, %d", withVel, withoutVel)
	}
}

func TestQueryDefaultExclusions(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	posMap := ecs.NewMap1[component.Position](world)
	posGhostMap := ecs.NewMap2[component.Position, component.Ghost](world)
	posReplicaMap := ecs.NewMap2[component.Position, component.Replica](world)

	posMap.NewEntity(&component.Position{X: 1})
	posGhostMap.NewEntity(&component.Position{X: 2}, &component.Ghost{})
	posReplicaMap.NewEntity(&component.Position{X: 3}, &component.Replica{})

	var q Query[struct{ Pos *component.Position }]
	q.With()
	q.BuildFromECS(sys.ECSWorld())

	count := 0
	for range q.Iter {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 entity (ghost+replica excluded), got %d", count)
	}
}

func TestQueryIncludeAll(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	posMap := ecs.NewMap1[component.Position](world)
	posGhostMap := ecs.NewMap2[component.Position, component.Ghost](world)

	posMap.NewEntity(&component.Position{X: 1})
	posGhostMap.NewEntity(&component.Position{X: 2}, &component.Ghost{})

	var q Query[struct{ Pos *component.Position }]
	q.With(IncludeAll())
	q.BuildFromECS(sys.ECSWorld())

	count := 0
	for range q.Iter {
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 entities (IncludeAll), got %d", count)
	}
}

func TestQueryCustomWithout(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	posMap := ecs.NewMap1[component.Position](world)
	posLifetimeMap := ecs.NewMap2[component.Position, component.Lifetime](world)

	posMap.NewEntity(&component.Position{X: 1})
	posLifetimeMap.NewEntity(&component.Position{X: 2}, &component.Lifetime{})

	var q Query[struct{ Pos *component.Position }]
	q.With(Without[component.Lifetime]())
	q.BuildFromECS(sys.ECSWorld())

	count := 0
	for range q.Iter {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 entity (Lifetime excluded), got %d", count)
	}
}

func TestQueryEarlyBreak(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	posMap := ecs.NewMap1[component.Position](world)
	for i := range 10 {
		posMap.NewEntity(&component.Position{X: float32(i)})
	}

	var q Query[struct{ Pos *component.Position }]
	q.With(IncludeAll())
	q.BuildFromECS(sys.ECSWorld())

	count := 0
	for range q.Iter {
		count++
		if count == 3 {
			break
		}
	}
	if count != 3 {
		t.Errorf("expected 3 iterations before break, got %d", count)
	}
}

func TestQueryZeroEntities(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	var q Query[struct{ Pos *component.Position }]
	q.With(IncludeAll())
	q.BuildFromECS(sys.ECSWorld())

	count := 0
	for range q.Iter {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 iterations, got %d", count)
	}
}

func TestQueryPanicsOnInvalidBundle(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for non-pointer field")
		}
	}()

	var q Query[struct{ X int }]
	q.With()
	q.BuildFromECS(sys.ECSWorld())
}

// TestQueryIter_PanicInBody_UnlocksWorld verifies that a panic inside the
// rangefunc body still releases the world's read-lock. Without the deferred
// Close() in build()'s iter closure, the panic propagates up without
// re-entering Next(), so uq.Close() never runs and the world's lock counter
// stays > 0 — every subsequent NewEntity panics with "world locked".
//
// Repro for the production panic seen at PostSystems → drainPendingPromotes
// → SpawnLiveFromTransfer → SpawnFromTransferCore → NewEntity in the merge
// scenario: any system whose Update body panics (recovered by
// processAdminCmds or simply because some game code under the iter does a
// nil-deref) leaves the world stuck-locked, and the next mutation —
// typically several lines later — surfaces the symptom.
func TestQueryIter_PanicInBody_UnlocksWorld(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	mapper := ecs.NewMap1[component.Position](world)
	mapper.NewEntity(&component.Position{X: 1, Y: 2})

	var q Query[struct {
		Pos *component.Position
	}]
	q.With()
	q.BuildFromECS(sys.ECSWorld())

	func() {
		defer func() {
			_ = recover()
		}()
		for range q.Iter {
			panic("simulated body panic")
		}
	}()

	if world.IsLocked() {
		t.Fatal("world remained locked after a panic in the rangefunc body — defer Close() leak in pkg/query/query.go build()")
	}

	// Subsequent mutation must succeed. Pre-fix, this NewEntity panicked
	// with "cannot modify a locked world".
	mapper.NewEntity(&component.Position{X: 10, Y: 20})
}
