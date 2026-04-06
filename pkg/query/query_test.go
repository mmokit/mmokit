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
	q.Init(sys)

	count := 0
	for _, b := range q.All() {
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
	q.Init(sys)

	for _, b := range q.All() {
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
	q.Init(sys, IncludeAll())

	var withVel, withoutVel int
	for _, b := range q.All() {
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
	q.Init(sys)

	count := 0
	for range q.All() {
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
	q.Init(sys, IncludeAll())

	count := 0
	for range q.All() {
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
	q.Init(sys, Without[component.Lifetime]())

	count := 0
	for range q.All() {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 entity (Lifetime excluded), got %d", count)
	}
}

func TestQueryCountAndAny(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	var q Query[struct{ Pos *component.Position }]
	q.Init(sys, IncludeAll())

	if q.Any() {
		t.Error("expected Any() = false for empty world")
	}
	if q.Count() != 0 {
		t.Errorf("expected Count() = 0, got %d", q.Count())
	}

	posMap := ecs.NewMap1[component.Position](world)
	posMap.NewEntity(&component.Position{})
	posMap.NewEntity(&component.Position{})

	if !q.Any() {
		t.Error("expected Any() = true after adding entities")
	}
	if q.Count() != 2 {
		t.Errorf("expected Count() = 2, got %d", q.Count())
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
	q.Init(sys, IncludeAll())

	count := 0
	for range q.All() {
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
	q.Init(sys, IncludeAll())

	count := 0
	for range q.All() {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 iterations, got %d", count)
	}
}

func TestQueryEachCallback(t *testing.T) {
	world := ecs.NewWorld()
	sys := &queryTestSys{world: world}

	posMap := ecs.NewMap1[component.Position](world)
	posMap.NewEntity(&component.Position{X: 5})

	var q Query[struct{ Pos *component.Position }]
	q.Init(sys, IncludeAll())

	called := false
	q.Each(func(e ecs.Entity, b *struct{ Pos *component.Position }) {
		called = true
		if b.Pos.X != 5 {
			t.Errorf("expected X=5, got %.0f", b.Pos.X)
		}
	})
	if !called {
		t.Error("Each callback was not called")
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
	q.Init(sys)
}
