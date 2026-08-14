# Query[T] Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `mmokit.Query[T]` — a bundle-based ECS query abstraction that eliminates raw ark filter boilerplate — then migrate all existing systems to use it.

**Architecture:** `Query[T]` wraps ark's `UnsafeFilter` behind a single generic type parameterized on a component bundle struct. Reflection runs once at init to extract field offsets and component IDs. Per-tick iteration uses `unsafe.Pointer` arithmetic to populate a reusable bundle. Go 1.25 range-over-function iterators provide the primary iteration API. Default Ghost+Replica exclusions cover 90%+ of systems.

**Tech Stack:** Go 1.25, ark ECS v0.7.1 (`UnsafeFilter`, `UnsafeQuery`, `ComponentID`, `TypeID`), `iter.Seq2`, `reflect`, `unsafe`

**Spec:** `docs/superpowers/specs/2026-04-06-query-abstraction-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `pkg/mmokit/query.go` | `Query[T]`, `NewQuery`, `QueryOption`, `fieldMeta`, bundle reflection, `All()`/`Each()`/`Count()`/`Any()` |
| `pkg/mmokit/query_test.go` | All tests for the Query abstraction |

All other changes are modifications to existing system files (replacing raw filter patterns).

---

### Task 1: Core Query[T] type + bundle reflection + tests

**Files:**

- Create: `pkg/mmokit/query.go`
- Create: `pkg/mmokit/query_test.go`

- [ ] **Step 1: Write failing test for basic 2-component bundle iteration**

```go
// pkg/mmokit/query_test.go
package mmokit

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
)

// queryTestSys implements the interface{ ECSWorld() *ecs.World } needed by Query.Init.
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mmokit/ -run TestQuery -v`
Expected: compilation error — `Query` type doesn't exist yet.

- [ ] **Step 3: Implement Query[T] core — types, Init, bundle reflection, All()**

```go
// pkg/mmokit/query.go
package mmokit

import (
	"iter"
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
)

// fieldMeta stores precomputed info for populating one bundle field.
type fieldMeta struct {
	compID   ecs.ID
	offset   uintptr
	optional bool
}

// Query wraps an ark UnsafeFilter and provides ergonomic, arity-independent
// iteration over entities matching a component bundle struct T.
//
// Bundle rules: every exported field must be a pointer to a component struct.
// Fields tagged `ecs:"optional"` are set to nil when the entity lacks that component.
type Query[T any] struct {
	filter ecs.UnsafeFilter
	fields []fieldMeta
	bundle T
	inited bool
}

// QueryOption configures how a Query is built.
type QueryOption struct {
	tp         reflect.Type // non-nil for Without
	includeAll bool         // true for IncludeAll
}

// Without adds a component type to the exclusion set. Multiple calls accumulate.
func Without[T any]() QueryOption {
	return QueryOption{tp: reflect.TypeFor[T]()}
}

// IncludeAll clears all default exclusions (Ghost, Replica).
func IncludeAll() QueryOption {
	return QueryOption{includeAll: true}
}

// Init initializes the Query from a system's ECS world.
// T is inferred from the struct field — no type repetition needed.
func (q *Query[T]) Init(sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) {
	if q.inited {
		panic("mmokit.Query: Init called twice")
	}
	w := sys.ECSWorld()
	q.initFields(w)
	q.initFilter(w, opts)
	q.inited = true
}

// NewQuery creates and returns a Query[T] by value.
func NewQuery[T any](sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) Query[T] {
	var q Query[T]
	q.Init(sys, opts...)
	return q
}

func (q *Query[T]) initFields(w *ecs.World) {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		panic("mmokit.Query: T must be a struct")
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Ptr || f.Type.Elem().Kind() != reflect.Struct {
			panic("mmokit.Query: field " + f.Name + " must be a pointer to a struct")
		}
		compID := ecs.TypeID(w, f.Type.Elem())
		optional := f.Tag.Get("ecs") == "optional"
		q.fields = append(q.fields, fieldMeta{
			compID:   compID,
			offset:   f.Offset,
			optional: optional,
		})
	}
	if len(q.fields) == 0 {
		panic("mmokit.Query: bundle struct has no exported pointer fields")
	}
}

func (q *Query[T]) initFilter(w *ecs.World, opts []QueryOption) {
	// Collect required component IDs (non-optional fields).
	var required []ecs.ID
	for i := range q.fields {
		if !q.fields[i].optional {
			required = append(required, q.fields[i].compID)
		}
	}
	q.filter = ecs.NewUnsafeFilter(w, required...)

	// Parse options.
	includeAll := false
	var extraWithout []ecs.ID
	for _, opt := range opts {
		if opt.includeAll {
			includeAll = true
		}
		if opt.tp != nil {
			extraWithout = append(extraWithout, ecs.TypeID(w, opt.tp))
		}
	}

	// Build exclusion set.
	var withoutIDs []ecs.ID
	if !includeAll {
		withoutIDs = append(withoutIDs,
			ecs.ComponentID[component.Ghost](w),
			ecs.ComponentID[component.Replica](w),
		)
	}
	withoutIDs = append(withoutIDs, extraWithout...)

	if len(withoutIDs) > 0 {
		q.filter = q.filter.Without(withoutIDs...)
	}
}

func (q *Query[T]) populateBundle(uq *ecs.UnsafeQuery) {
	base := unsafe.Pointer(&q.bundle)
	for i := range q.fields {
		fm := &q.fields[i]
		fieldPtr := (*unsafe.Pointer)(unsafe.Add(base, fm.offset))
		if fm.optional && !uq.Has(fm.compID) {
			*fieldPtr = nil
		} else {
			*fieldPtr = uq.Get(fm.compID)
		}
	}
}

// All returns a range iterator over all matching entities.
// Early break via `break` is safe — the query is properly closed.
func (q *Query[T]) All() iter.Seq2[ecs.Entity, *T] {
	return func(yield func(ecs.Entity, *T) bool) {
		uq := q.filter.Query()
		for uq.Next() {
			q.populateBundle(&uq)
			if !yield(uq.Entity(), &q.bundle) {
				uq.Close()
				return
			}
		}
	}
}

// Each iterates all matching entities. Cannot break early.
func (q *Query[T]) Each(fn func(ecs.Entity, *T)) {
	uq := q.filter.Query()
	for uq.Next() {
		q.populateBundle(&uq)
		fn(uq.Entity(), &q.bundle)
	}
}

// Count returns the number of matching entities without full iteration.
func (q *Query[T]) Count() int {
	return q.filter.Query().Count()
}

// Any returns true if at least one entity matches.
func (q *Query[T]) Any() bool {
	return q.Count() > 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/mmokit/ -run TestQuery -v`
Expected: PASS

- [ ] **Step 5: Write tests for optional fields, Without exclusions, IncludeAll, Count/Any, early break, zero entities, and invalid bundles**

```go
// Append to pkg/mmokit/query_test.go

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
	q.Init(sys) // default: excludes Ghost + Replica

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

	// Default exclusions + also without Lifetime
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
```

- [ ] **Step 6: Run all tests**

Run: `go test ./pkg/mmokit/ -run TestQuery -v`
Expected: ALL PASS

- [ ] **Step 7: Run go vet**

Run: `go vet ./pkg/mmokit/...`
Expected: no errors

- [ ] **Step 8: Commit**

```bash
git add pkg/mmokit/query.go pkg/mmokit/query_test.go
git commit -m "feat: add mmokit.Query[T] bundle-based ECS query abstraction"
```

---

### Task 2: Migrate examples/simple

**Files:**

- Modify: `examples/simple/main.go`

- [ ] **Step 1: Migrate OscillateSystem to use Query[T]**

Replace the entire OscillateSystem and its imports:

```go
package main

import (
	"context"

	"github.com/zenion/mmokit/pkg/mmokit"
)

// OscillateSystem moves all entities left and right.
type OscillateSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		Pos *mmokit.Position
	}]
	elapsed float32
	dir     float32
}

func (s *OscillateSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll())
	s.dir = 1
}

func (s *OscillateSystem) Update(dt float32) {
	s.elapsed += dt
	if s.elapsed >= 5.0 {
		s.elapsed = 0
		s.dir = -s.dir
	}
	for _, b := range s.entities.All() {
		b.Pos.X += 100 * s.dir * dt
	}
}

func main() {
	coord := mmokit.NewCoordinator(mmokit.Config{
		CellSize: 8192,
		TickRate: 20,
	})
	coord.OnInit(func(w *mmokit.WorldBase) {
		w.SpawnEntity(mmokit.Position{X: 4096, Y: 4096}, mmokit.WithCollider(20))
	})
	coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
	coord.Start(context.Background())
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./examples/simple/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add examples/simple/main.go
git commit -m "refactor: migrate examples/simple to mmokit.Query[T]"
```

---

### Task 3: Migrate pkg/system/ simple systems (physics, lifetime, wander-pattern)

**Files:**

- Modify: `pkg/system/physics.go`
- Modify: `pkg/system/lifetime.go`

- [ ] **Step 1: Migrate PhysicsSystem**

```go
// pkg/system/physics.go
package system

import (
	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// PhysicsSystem integrates velocity into position each tick.
// Skips Ghost and Replica entities.
type PhysicsSystem struct {
	engine.SystemBase
	entities mmokit.Query[struct {
		Pos *component.Position
		Vel *component.Velocity
	}]
}

func (s *PhysicsSystem) Init() {
	s.entities.Init(s)
}

func (s *PhysicsSystem) Update(dt float32) {
	for _, b := range s.entities.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}
}
```

- [ ] **Step 2: Migrate LifetimeSystem**

```go
// pkg/system/lifetime.go
package system

import (
	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem struct {
	engine.SystemBase
	entities mmokit.Query[struct {
		Lt *component.Lifetime
	}]
}

func (s *LifetimeSystem) Init() {
	s.entities.Init(s)
}

func (s *LifetimeSystem) Update(dt float32) {
	for e, b := range s.entities.All() {
		b.Lt.Remaining -= dt
		if b.Lt.Remaining <= 0 {
			if s.Engine() != nil {
				s.Engine().MarkForRemoval(e)
			}
		}
	}
}
```

- [ ] **Step 3: Run existing tests + vet**

Run: `go vet ./pkg/system/... && go test ./pkg/system/ -v -count=1`
Expected: all pass

- [ ] **Step 4: Commit**

```bash
git add pkg/system/physics.go pkg/system/lifetime.go
git commit -m "refactor: migrate PhysicsSystem + LifetimeSystem to Query[T]"
```

---

### Task 4: Migrate pkg/system/ systems with optional Map lookups

**Files:**

- Modify: `pkg/system/direction_move.go`
- Modify: `pkg/system/click_to_move.go`
- Modify: `pkg/system/spatial_system.go`

- [ ] **Step 1: Migrate DirectionMoveSystem (Map1[MoveParams] → optional field)**

```go
// pkg/system/direction_move.go
package system

import (
	"math"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// DirectionMoveSystem moves entities in the direction of their DirectionInput
// at MoveParams.MaxSpeed. Sets velocity to zero when input is inactive.
// Skips Ghost and Replica entities.
type DirectionMoveSystem struct {
	engine.SystemBase
	entities mmokit.Query[struct {
		Pos    *component.Position
		Vel    *component.Velocity
		DI     *component.DirectionInput
		Params *component.MoveParams `ecs:"optional"`
	}]
}

func (s *DirectionMoveSystem) Init() {
	s.entities.Init(s)
}

func (s *DirectionMoveSystem) Update(dt float32) {
	for _, b := range s.entities.All() {
		if !b.DI.Active {
			b.Vel.X = 0
			b.Vel.Y = 0
			continue
		}

		speed := defaultMaxSpeed
		if b.Params != nil && b.Params.MaxSpeed > 0 {
			speed = b.Params.MaxSpeed
		}

		mag := float32(math.Sqrt(float64(b.DI.X*b.DI.X + b.DI.Y*b.DI.Y)))
		if mag < 0.001 {
			b.Vel.X = 0
			b.Vel.Y = 0
			continue
		}

		b.Vel.X = (b.DI.X / mag) * speed
		b.Vel.Y = (b.DI.Y / mag) * speed
	}
}
```

- [ ] **Step 2: Migrate ClickToMoveSystem (Map1[MoveParams] → optional field)**

```go
// pkg/system/click_to_move.go
package system

import (
	"math"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/coords"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/mmokit"
)

const defaultMaxSpeed float32 = 300

// ClickToMoveSystem moves entities toward their MoveTarget at MoveParams.MaxSpeed.
// Stops when within ~1 unit of the target. Does nothing when MoveTarget.Active is false.
// Skips Ghost and Replica entities.
type ClickToMoveSystem struct {
	engine.SystemBase
	entities mmokit.Query[struct {
		Pos    *component.Position
		Vel    *component.Velocity
		MT     *component.MoveTarget
		CC     *component.CellCoord
		Params *component.MoveParams `ecs:"optional"`
	}]
}

func (s *ClickToMoveSystem) Init() {
	s.entities.Init(s)
}

func (s *ClickToMoveSystem) Update(dt float32) {
	cellSize := coords.CellSize
	for _, b := range s.entities.All() {
		if !b.MT.Active {
			continue
		}

		dx := float32(b.MT.CellX-b.CC.CellX)*cellSize + b.MT.X - b.Pos.X
		dy := float32(b.MT.CellY-b.CC.CellY)*cellSize + b.MT.Y - b.Pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		speed := defaultMaxSpeed
		if b.Params != nil && b.Params.MaxSpeed > 0 {
			speed = b.Params.MaxSpeed
		}

		stepDist := speed * dt
		if dist <= stepDist {
			b.Pos.X = b.MT.X + float32(b.MT.CellX-b.CC.CellX)*cellSize
			b.Pos.Y = b.MT.Y + float32(b.MT.CellY-b.CC.CellY)*cellSize
			b.MT.Active = false
			b.Vel.X = 0
			b.Vel.Y = 0
			continue
		}

		b.Vel.X = (dx / dist) * speed
		b.Vel.Y = (dy / dist) * speed
	}
}

// SetMoveTarget converts world-absolute coordinates to cell-local and activates.
func SetMoveTarget(mt *component.MoveTarget, worldX, worldY float32) {
	SetMoveTargetWithCellSize(mt, worldX, worldY, coords.CellSize)
}

// SetMoveTargetWithCellSize converts world-absolute coordinates to cell-local
// using the given cell size and activates.
func SetMoveTargetWithCellSize(mt *component.MoveTarget, worldX, worldY, cellSize float32) {
	mt.CellX = int32(math.Floor(float64(worldX / cellSize)))
	mt.CellY = int32(math.Floor(float64(worldY / cellSize)))
	mt.X = worldX - float32(mt.CellX)*cellSize
	mt.Y = worldY - float32(mt.CellY)*cellSize
	mt.Active = true
}

// CancelMoveTarget deactivates movement.
func CancelMoveTarget(mt *component.MoveTarget) {
	mt.Active = false
}
```

- [ ] **Step 3: Migrate SpatialSystem (Map1[Rotation] → optional field, no Without → IncludeAll)**

```go
// pkg/system/spatial_system.go
package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/mmokit"
	"github.com/zenion/mmokit/pkg/spatial"
)

// SpatialHooks provides optional per-tick callbacks for game-specific spatial logic.
type SpatialHooks struct {
	PreTick  func()
	OnEntity func(entity ecs.Entity, entry spatial.Entry)
	PostTick func()
}

// SpatialSystem updates the spatial hash grid each tick by querying all entities
// with Position + Collider + NetworkID. Rotation is read if present.
type SpatialSystem struct {
	engine.SystemBase
	grid     *spatial.HashGrid
	entities mmokit.Query[struct {
		Pos *component.Position
		Col *component.Collider
		Net *component.NetworkID
		Rot *component.Rotation `ecs:"optional"`
	}]
	hooks    SpatialHooks
	initHook func(gw any) SpatialHooks
}

// SetInitHook sets a function that runs during Init to produce per-tick hooks.
func (s *SpatialSystem) SetInitHook(fn func(gw any) SpatialHooks) {
	s.initHook = fn
}

func (s *SpatialSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll())

	if sp, ok := s.GameWorld().(interface{ SpatialGrid() *spatial.HashGrid }); ok {
		s.grid = sp.SpatialGrid()
	}
	if s.initHook != nil {
		s.hooks = s.initHook(s.GameWorld())
	}
}

func (s *SpatialSystem) Update(dt float32) {
	if s.hooks.PreTick != nil {
		s.hooks.PreTick()
	}

	for e, b := range s.entities.All() {
		entry := spatial.Entry{
			Entity: e,
			X:      b.Pos.X,
			Y:      b.Pos.Y,
			Radius: b.Col.Radius,
			Width:  b.Col.Width,
			Height: b.Col.Height,
			Layer:  b.Col.Layer,
			Shape:  b.Col.Shape,
		}
		if b.Rot != nil {
			entry.Rotation = b.Rot.Angle
		}

		if s.grid.IsRegistered(e) {
			s.grid.Update(entry)
		} else {
			s.grid.Register(entry)
		}

		if s.hooks.OnEntity != nil {
			s.hooks.OnEntity(e, entry)
		}
	}

	if s.hooks.PostTick != nil {
		s.hooks.PostTick()
	}
}
```

- [ ] **Step 4: Run tests + vet**

Run: `go vet ./pkg/system/... && go test ./pkg/system/ -v -count=1`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add pkg/system/direction_move.go pkg/system/click_to_move.go pkg/system/spatial_system.go
git commit -m "refactor: migrate DirectionMove, ClickToMove, Spatial to Query[T]"
```

---

### Task 5: Migrate pkg/system/replica_dead_reckoning.go (multiple queries)

**Files:**

- Modify: `pkg/system/replica_dead_reckoning.go`

- [ ] **Step 1: Migrate ReplicaDeadReckoningSystem — two queries with IncludeAll**

```go
// pkg/system/replica_dead_reckoning.go
package system

import (
	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// ReplicaDeadReckoningSystem advances replica and ghost entity positions
// each tick using their last-known velocity.
type ReplicaDeadReckoningSystem struct {
	engine.SystemBase
	replicas mmokit.Query[struct {
		Pos *component.Position
		Vel *component.Velocity
		Rep *component.Replica
	}]
	ghosts mmokit.Query[struct {
		Pos   *component.Position
		Vel   *component.Velocity
		Ghost *component.Ghost
	}]
}

func (s *ReplicaDeadReckoningSystem) Init() {
	s.replicas.Init(s, mmokit.IncludeAll())
	s.ghosts.Init(s, mmokit.IncludeAll())
}

func (s *ReplicaDeadReckoningSystem) Update(dt float32) {
	for _, b := range s.replicas.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}

	for _, b := range s.ghosts.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./pkg/system/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/system/replica_dead_reckoning.go
git commit -m "refactor: migrate ReplicaDeadReckoningSystem to Query[T]"
```

---

### Task 6: Migrate examples/4node-basic

**Files:**

- Modify: `examples/4node-basic/system_debug_info.go`

- [ ] **Step 1: Migrate DebugInfoSystem**

```go
// examples/4node-basic/system_debug_info.go
package main

import (
	"github.com/zenion/mmokit/pkg/mmokit"
)

// DebugInfoSystem updates game-specific debug fields each tick.
type DebugInfoSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		DI *DebugInfo
	}]
}

func (s *DebugInfoSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll())
}

func (s *DebugInfoSystem) Update(dt float32) {
	for _, b := range s.entities.All() {
		b.DI.AoIRadius = AoIRadius
	}
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./examples/4node-basic/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/system_debug_info.go
git commit -m "refactor: migrate 4node-basic DebugInfoSystem to Query[T]"
```

---

### Task 7: Migrate examples/slither systems

**Files:**

- Modify: `examples/slither/system_boost.go`
- Modify: `examples/slither/system_decay.go`
- Modify: `examples/slither/system_leaderboard.go`
- Modify: `examples/slither/system_movement.go`
- Modify: `examples/slither/system_eating.go`
- Modify: `examples/slither/system_collision.go`
- Modify: `examples/slither/system_bot.go`

- [ ] **Step 1: Migrate BoostSystem**

Replace the struct + Init + Update iteration. The filter becomes:

```go
type BoostSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		State *SnakeState
		Body  *SnakeBody
	}]
	tick uint32
}

func (s *BoostSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}
```

Update becomes `for _, b := range s.entities.All()` using `b.State` and `b.Body` instead of `state` and `body`.

- [ ] **Step 2: Migrate DecaySystem**

```go
type DecaySystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		State *SnakeState
		NetID *mmokit.NetworkID
		Pos   *mmokit.Position
	}]
}

func (s *DecaySystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` using `b.State`, `b.NetID`, `b.Pos`, and `e` for KillInfo.

- [ ] **Step 3: Migrate LeaderboardSystem (custom Without: Ghost only)**

```go
type LeaderboardSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		State *SnakeState
		NetID *mmokit.NetworkID
	}]
}

func (s *LeaderboardSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	// Include replicas for cross-node coverage, exclude only ghosts
	s.entities.Init(s, mmokit.IncludeAll(), mmokit.Without[mmokit.Ghost]())
}
```

- [ ] **Step 4: Migrate MovementSystem**

```go
type MovementSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		Pos   *mmokit.Position
		Vel   *mmokit.Velocity
		Rot   *mmokit.Rotation
		State *SnakeState
		Body  *SnakeBody
	}]
}

func (s *MovementSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` — replace `pos` → `b.Pos`, `vel` → `b.Vel`, etc. Use `e` with `s.gw.SnakeInputMap.HasAll(e)`.

- [ ] **Step 5: Migrate EatingSystem**

```go
type EatingSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		Pos   *mmokit.Position
		Rot   *mmokit.Rotation
		State *SnakeState
		NetID *mmokit.NetworkID
	}]
	buf []mmokit.SpatialEntry
}

func (s *EatingSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}
```

In Update: move `replicaMap := ecs.NewMap1[mmokit.Replica](gw.ECSWorld())` to a field initialized in Init, or keep as-is since it's used on spatial-query results, not the iterated entity. The iteration becomes `for _, b := range s.entities.All()`.

- [ ] **Step 6: Migrate CollisionSystem**

```go
type CollisionSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		Pos   *mmokit.Position
		Rot   *mmokit.Rotation
		State *SnakeState
		NetID *mmokit.NetworkID
	}]
	buf []mmokit.SpatialEntry
}

func (s *CollisionSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` — collect `snakeInfo` with `entity: e`, `posX: b.Pos.X`, etc.

- [ ] **Step 7: Migrate BotSystem**

```go
type BotSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		Bot   *Bot
		State *SnakeState
		Pos   *mmokit.Position
		Rot   *mmokit.Rotation
		NetID *mmokit.NetworkID
	}]
	buf []mmokit.SpatialEntry
}

func (s *BotSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` — replace `bot` → `b.Bot`, `state` → `b.State`, etc. Use `e` for entity-specific spatial calls.

- [ ] **Step 8: Verify compilation**

Run: `go vet ./examples/slither/...`
Expected: no errors

- [ ] **Step 9: Commit**

```bash
git add examples/slither/system_boost.go examples/slither/system_decay.go examples/slither/system_leaderboard.go examples/slither/system_movement.go examples/slither/system_eating.go examples/slither/system_collision.go examples/slither/system_bot.go
git commit -m "refactor: migrate all slither systems to Query[T]"
```

---

### Task 8: Migrate internal/system/ simple systems

**Files:**

- Modify: `internal/system/shieldregen.go`
- Modify: `internal/system/statuseffect.go`
- Modify: `internal/system/wander.go`
- Modify: `internal/system/targetlock.go`

- [ ] **Step 1: Migrate ShieldRegenSystem**

```go
type ShieldRegenSystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	entities mmokit.Query[struct {
		Shield *mmokit.Shield
	}]
}

func (s *ShieldRegenSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.entities.Init(s)
}
```

Update: `for _, b := range s.entities.All()` with `b.Shield`.

- [ ] **Step 2: Migrate StatusEffectSystem**

```go
type StatusEffectSystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	entities mmokit.Query[struct {
		SE *gamecomp.StatusEffects
	}]
}

func (s *StatusEffectSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` with `b.SE` and `e` for `gw.ApplyDamage(e, ...)`.

- [ ] **Step 3: Migrate WanderSystem**

```go
type WanderSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		W   *gamecomp.Wander
		Vel *mmokit.Velocity
		Rot *mmokit.Rotation
	}]
}

func (s *WanderSystem) Init() {
	s.entities.Init(s)
}
```

Update: `for _, b := range s.entities.All()` with `b.W`, `b.Vel`, `b.Rot`.

- [ ] **Step 4: Migrate TargetLockSystem**

```go
type TargetLockSystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	entities mmokit.Query[struct {
		Input *gamecomp.PlayerInput
		Lock  *mmokit.TargetLock
	}]
}

func (s *TargetLockSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` with `b.Input`, `b.Lock`, and `e` for component lookups.

- [ ] **Step 5: Verify compilation**

Run: `go vet ./internal/system/...`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add internal/system/shieldregen.go internal/system/statuseffect.go internal/system/wander.go internal/system/targetlock.go
git commit -m "refactor: migrate ShieldRegen, StatusEffect, Wander, TargetLock to Query[T]"
```

---

### Task 9: Migrate internal/system/ complex systems

**Files:**

- Modify: `internal/system/shipcontrol.go`
- Modify: `internal/system/mining.go`
- Modify: `internal/system/ability.go`
- Modify: `internal/system/docking.go`
- Modify: `internal/system/economy.go`

- [ ] **Step 1: Migrate ShipControlSystem (Map1[PlayerInput] → optional field)**

```go
type ShipControlSystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	entities mmokit.Query[struct {
		MT    *mmokit.MoveTarget
		Ship  *gamecomp.ShipControl
		Vel   *mmokit.Velocity
		Rot   *mmokit.Rotation
		Input *gamecomp.PlayerInput `ecs:"optional"`
	}]
}

func (s *ShipControlSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` — replace `mt` → `b.MT`, `ship` → `b.Ship`, etc. Replace `s.playerInputMap.HasAll(entity)` check with `if b.Input != nil && b.Input.DirActive { dirInput = b.Input }`.

- [ ] **Step 2: Migrate MiningSystem**

```go
type MiningSystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	entities mmokit.Query[struct {
		Input *gamecomp.PlayerInput
		Laser *gamecomp.MiningLaser
		Pos   *mmokit.Position
		Inv   *gamecomp.Inventory
	}]
}

func (s *MiningSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.entities.Init(s)
}
```

Update: `for e, b := range s.entities.All()` — replace `input` → `b.Input`, `laser` → `b.Laser`, etc.

- [ ] **Step 3: Migrate AbilitySystem**

```go
type AbilitySystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	entities mmokit.Query[struct {
		Input     *gamecomp.PlayerInput
		Lock      *mmokit.TargetLock
		Abilities *gamecomp.AbilitySet
		Equip     *gamecomp.Equipment
	}]
	deferred []abilityAction
}

func (s *AbilitySystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.entities.Init(s)
	s.deferred = make([]abilityAction, 0, 16)
}
```

Update: `for e, b := range s.entities.All()` — replace `input` → `b.Input`, `lock` → `b.Lock`, `abilities` → `b.Abilities`, `equip` → `b.Equip`.

- [ ] **Step 4: Migrate DockingSystem**

```go
type DockingSystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	stations mmokit.Query[struct {
		Station *gamecomp.Station
		Pos     *mmokit.Position
		NetID   *mmokit.NetworkID
	}]
}

func (s *DockingSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.stations.Init(s)
}
```

Update: `for _, b := range s.stations.All()` — replace `_, pos, netID := stationQuery.Get()` with `b.Pos`, `b.NetID`.

- [ ] **Step 5: Migrate EconomySystem (no default exclusions)**

```go
type EconomySystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	stations mmokit.Query[struct {
		Station *gamecomp.Station
		Pos     *mmokit.Position
	}]
}

func (s *EconomySystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.stations.Init(s, mmokit.IncludeAll())
}
```

Update: `for _, b := range s.stations.All()` — replace `_, pos := stationQuery.Get()` with `b.Pos`.

- [ ] **Step 6: Verify compilation**

Run: `go vet ./internal/system/...`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add internal/system/shipcontrol.go internal/system/mining.go internal/system/ability.go internal/system/docking.go internal/system/economy.go
git commit -m "refactor: migrate ShipControl, Mining, Ability, Docking, Economy to Query[T]"
```

---

### Task 10: Migrate internal/system/network.go and pkg/universe/boundary_system.go

**Files:**

- Modify: `internal/system/network.go`
- Modify: `pkg/universe/boundary_system.go`

- [ ] **Step 1: Migrate NetworkSystem lockFilter**

```go
type NetworkSystem struct {
	mmokit.SystemBase
	gw      *game.GameWorld
	replSys *mmokit.ReplicationSystem
	ctx     *gameNetContext

	locks mmokit.Query[struct {
		Lock  *mmokit.TargetLock
		NetID *mmokit.NetworkID
	}]

	pendingChat          []*enginepb.ChatMsg
	pendingAbilityEvents []*gamepb.AbilityCastResultMsg
}
```

In Init: replace `s.lockFilter = ecs.NewFilter2[...]` with `s.locks.Init(s, mmokit.IncludeAll())`.

In `beforeTick()`: replace `lockQuery := s.lockFilter.Query()` loop with `for e, b := range s.locks.All()` using `b.Lock`, `b.NetID`, and `e`.

- [ ] **Step 2: Migrate BoundarySystem (custom Without: Ghost+Replica+Proxy+Dormant+TransferCooldown + optional Map fields)**

```go
type BoundarySystem struct {
	engine.SystemBase
	bw       BoundaryWorld
	entities mmokit.Query[struct {
		Pos    *component.Position
		CC     *component.CellCoord
		Player *component.PlayerConn `ecs:"optional"`
		Vel    *component.Velocity   `ecs:"optional"`
	}]
}

func (s *BoundarySystem) Init() {
	if s.bw == nil {
		if gw, ok := s.GameWorld().(BoundaryWorld); ok {
			s.bw = gw
		}
	}
	s.entities.Init(s,
		mmokit.IncludeAll(),
		mmokit.Without[component.Ghost](),
		mmokit.Without[component.Replica](),
		mmokit.Without[component.Proxy](),
		mmokit.Without[component.Dormant](),
		mmokit.Without[component.TransferCooldown](),
	)
}
```

Update: replace `query := s.filter.Query()` loop with `for e, b := range s.entities.All()`. Replace `s.velMap.HasAll(entity)` → `if b.Vel != nil`. Replace `s.playerMap.HasAll(entity)` → `if b.Player != nil`. Note: some Map lookups in the transfer-processing section (posMap, netIDMap, cellMap) are on different entities — those stay as local Map1 fields.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./internal/system/... ./pkg/universe/...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add internal/system/network.go pkg/universe/boundary_system.go
git commit -m "refactor: migrate NetworkSystem + BoundarySystem to Query[T]"
```

---

### Task 11: Final verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -count=1`
Expected: ALL PASS

- [ ] **Step 2: Run go vet on entire project**

Run: `go vet ./...`
Expected: no errors

- [ ] **Step 3: Verify examples compile**

Run: `go build ./examples/simple/ && go build ./examples/4node-basic/ && go build ./examples/slither/`
Expected: builds successfully (do NOT build in root — build into a temp or just use vet)

Actually: `go vet ./examples/simple/... ./examples/4node-basic/... ./examples/slither/...`

- [ ] **Step 4: Commit any remaining fixes if needed**

---

### Task 12: Update CLAUDE.md

**Files:**

- Modify: `CLAUDE.md`

- [ ] **Step 1: Add Query[T] documentation to the ECS section**

Add after the existing "ECS (Ark v0.7.1)" section:

```markdown
### Query[T] (mmokit)

`mmokit.Query[T]` provides ergonomic ECS iteration over component bundle structs:

\`\`\`go
type MySystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct {
        Pos    *comp.Position
        Vel    *comp.Velocity
        Params *comp.MoveParams `ecs:"optional"` // nil when absent
    }]
}

func (s *MySystem) Init() {
    s.entities.Init(s)                          // default: excludes Ghost + Replica
    // s.entities.Init(s, mmokit.IncludeAll())  // no exclusions
    // s.entities.Init(s, mmokit.Without[X]())  // add extra exclusions
}

func (s *MySystem) Update(dt float32) {
    for e, b := range s.entities.All() {
        b.Pos.X += b.Vel.X * dt
        if b.Params != nil { /* optional component present */ }
    }
}
\`\`\`

Bundle rules: exported fields must be `*ComponentType`. Use `ecs:"optional"` for optional components. `All()` returns `iter.Seq2[ecs.Entity, *T]` — break is safe. Also provides `Each()`, `Count()`, `Any()`.

Prefer `Query[T]` for new systems. Raw `ecs.FilterN` is still available as an escape hatch for max performance.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: add Query[T] documentation to CLAUDE.md"
```
