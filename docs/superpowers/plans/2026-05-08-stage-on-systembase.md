# Stage on SystemBase Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse mmokit's typed-world plumbing (`Config.OnInit`, `Config.World`, `*onInitWorld` wrapper, `GameWorld`/`BoundaryWorld` interfaces, `W` type parameter on `SystemBase`) so every game system embeds non-generic `mmokit.SystemBase` and gets `s.Stage()` for free. Per-cell typed game state is fetched explicitly via the existing `mmokit.State[T](stage)` accessor and cached in each system's `Init()`.

**Architecture:** Two-layer split. `pkg/engine.SystemBase` becomes a non-generic concrete type with default no-op `Update`. `pkg/mmokit.SystemBase` becomes a wrapper struct (was a type alias) that embeds `engine.SystemBase` and holds a `*universe.Stage`. Universe wiring calls `SetDeps` → `InitStage` → `BindQueries` in order.

**Tech Stack:** Go 1.23+, ark v0.7.1 (ECS), reflect for field discovery, no external deps.

**Source spec:** [`docs/superpowers/specs/2026-05-08-stage-on-systembase-design.md`](../specs/2026-05-08-stage-on-systembase-design.md)

---

## File Structure

**New files:**
- `pkg/mmokit/system.go` — wrapper `SystemBase` struct, `Stage()`, `InitStage()`.
- `pkg/mmokit/system_test.go` — tests for the wrapper and stage injection.
- `examples/simple/system_field_spawner.go` — replaces the deleted `Config.OnInit` body.

**Heavily modified:**
- `pkg/engine/system.go` — drop `[W]` generic, drop `World()`/`GameWorld()`, simplify `SetDeps`, add default `Update`.
- `pkg/engine/system_test.go` — drop typed-world tests, adapt remaining tests.
- `pkg/mmokit/mmokit.go` — drop the `SystemBase[W any] = engine.SystemBase[W]` alias.
- `pkg/universe/coordinator.go` — rewire system construction loop, delete `Config.OnInit` field, `Config.World` field, the `*onInitWorld` wrapper, the world-creation block, the `BoundaryWorld` type-check.
- `pkg/universe/host.go` — drop `onInit`/`worldFactory` fields and propagation.
- `pkg/universe/stage.go` — drop `world any` field, `GameWorld()`, `SetGameWorld()`.
- `pkg/universe/cell.go` — drop `Cell.World GameWorld` field.
- `pkg/universe/world.go` — delete `GameWorld` and `BoundaryWorld` interfaces.
- `pkg/universe/boundary_system.go` — drop `[BoundaryWorld]` generic, add private `stage *Stage` field, change `s.World()` → `s.stage`.
- `pkg/universe/coordinator_test.go`, `partition_test.go`, `roles_test.go`, `auth_cookie_e2e_test.go`, others — drop `Config.OnInit` boilerplate.
- `pkg/system/{physics,click_to_move,direction_move,lifetime,spatial_system}.go` — `engine.SystemBase[any]` → `engine.SystemBase` (5 mechanical edits).
- `examples/simple/main.go`, `system_sinewave.go` — drop `OnInit`, register new system, drop `[any]`.
- `examples/4node-basic/system_bots.go` — `[*mmokit.Stage]` → `(none)`, `s.World()` → `s.Stage()`.
- `internal/game/gameworld.go` — stop embedding `*Stage`, add unexported `stage *mmokit.Stage` field, set by `AddState` factory.
- `internal/game/factory.go` — `Config.World = ...` → `mmokit.AddState(mmo, NewGameWorldState)`.
- `internal/game/system_*.go` (~13 files) — `mmokit.SystemBase[*GameWorld]` → `mmokit.SystemBase`, add private `gw *GameWorld` field plus an `Init()` method that caches `mmokit.State[GameWorld](s.Stage())`, rewrite all `s.World().X` callsites by category (Stage method vs GameWorld field).

---

## Migration Reference Lists

These two lists drive the mechanical rewrite of `s.World().X` callsites in `internal/game/system_*.go`. Build them first (Task 18) so every callsite-rewrite task can dispatch by name match.

**Stage-method list** (callsite `s.World().X` → `s.Stage().X`): every public method on `*universe.Stage` per `pkg/universe/stage.go` — e.g. `Spawn`, `SpawnAtLocation`, `SpawnPlayer`, `SpawnFromTransfer`, `SerializeEntity`, `MarkForRemoval`, `SendEvent`, `Cell`, `CellID`, `CellSize`, `Bridge`, `Engine`, `LookupNetID`, `RegisterLiveNetID`, `RegisterReplicaNetID`, `IsGhost`, `SetDrainingForMerge`, `IsDrainingForMerge`, `Process`, `UpdateCellBounds`, `RegisterTickCallback`, `TickCallbacks`, `ECS`, `Now`, etc.

**GameWorld-field list** (callsite `s.World().X` → `s.gw.X`): every exported field on `internal/game.GameWorld` per `gameworld.go:77` — e.g. `Config`, `Spatial`, `FullRefreshInterval`, `Queue`, `Players`, `PlayerDB`, etc., plus unexported `eng` (used as `gw.eng.Log` — becomes `s.gw.eng.Log`). Each system declares a private `gw *GameWorld` field and caches it in `Init()` via `s.gw = mmokit.State[GameWorld](s.Stage())` — see Task 21.

**Disambiguation rule**: a field always wins over a same-named method (Go would accept either via promotion, but we're standardizing on the explicit access path). If a name appears in both lists, treat it as field access.

---

## Phase A — Foundation (additive, no callers broken yet)

### Task 1: ~~Add `StateRef[T]` marker type~~ — REMOVED

**Skip this task entirely.** Originally added a `StateRef[T]` marker type for declarative field-discovery of per-stage state. Go disallows embedding a pointer to a type parameter (`type StateRef[T any] struct { *T }` fails with `embedded field type cannot be a (pointer to a) type parameter`), which kills the only ergonomic version of that pattern.

The replacement: each system declares a private `gw *GameWorld` field and caches the lookup in `Init()` via `s.gw = mmokit.State[GameWorld](s.Stage())`. Three lines of boilerplate per system, but direct `s.gw.X` access at every callsite — see Task 21's revised migration pattern. No new public API needed in `pkg/mmokit`.

**Action:** No code change. Move directly to Task 2.

---

## Phase B — Engine Layer Collapse

### Task 2: Collapse engine.SystemBase[W] generic and add default Update

**Files:**
- Modify: `pkg/engine/system.go`

- [ ] **Step 1: Replace the contents of `pkg/engine/system.go` with the non-generic version**

```go
package engine

import (
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
)

// queryBuildable is implemented by *query.Query[T] (defined in pkg/query).
// SystemBase only sees this minimal contract — it doesn't import pkg/query
// (would be a cycle); discovery records pointers via reflection at SetDeps.
type queryBuildable interface {
	BuildFromECS(w *ecs.World)
}

// System is the interface all game systems implement.
// Embed SystemBase for automatic dependency injection via SetDeps/Init.
type System interface {
	Update(dt float32)
}

// SystemBase provides dependency injection for systems. Embed it to get
// ECSWorld(), Engine(), default no-op Init() and Update(), and automatic
// query-field discovery via BindQueries.
//
// Game-side systems should embed mmokit.SystemBase, which wraps this base
// and additionally exposes Stage() (per-cell *universe.Stage accessor).
type SystemBase struct {
	ecsWorld *ecs.World
	eng      *Engine
	queries  []queryBuildable // populated in BindQueries
}

// ECSWorld returns the ECS world for this cell.
func (b *SystemBase) ECSWorld() *ecs.World { return b.ecsWorld }

// Engine returns the engine for this cell.
func (b *SystemBase) Engine() *Engine { return b.eng }

// Init is called once after SetDeps. Override to create filters, configure
// queries via Query.With(opts...), etc. Default is a no-op.
func (b *SystemBase) Init() {}

// Update is called every tick by the engine. Default is a no-op so systems
// that only need Init() may omit Update entirely.
func (b *SystemBase) Update(dt float32) {}

// BindQueries discovers query.Query[T] fields on the outer system struct
// via reflection and records them for the build phase.
func (b *SystemBase) BindQueries(outer any) {
	v := reflect.ValueOf(outer)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		panic("engine.SystemBase: BindQueries requires *Struct")
	}
	v = v.Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		fp := unsafe.Pointer(v.Field(i).UnsafeAddr())
		field := reflect.NewAt(ft.Type, fp).Interface()
		if qb, ok := field.(queryBuildable); ok {
			b.queries = append(b.queries, qb)
		}
	}
}

// BuildQueries materializes each discovered query's ECS filter using the
// options the user accumulated during Init(). Called by the framework after
// Init() returns.
func (b *SystemBase) BuildQueries() {
	for _, q := range b.queries {
		q.BuildFromECS(b.ecsWorld)
	}
}

// SetDeps is called by the framework to inject dependencies.
func (b *SystemBase) SetDeps(w *ecs.World, eng *Engine) {
	b.ecsWorld = w
	b.eng = eng
}

// SystemDef pairs a name with a factory that creates a fresh system instance.
type SystemDef struct {
	Name    string
	Factory func() System
}

// Named overrides the auto-derived system name.
//
//	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}).Named("AILogic"))
func (d SystemDef) Named(name string) SystemDef {
	d.Name = name
	return d
}
```

- [ ] **Step 2: Verify pkg/engine compiles** (downstream packages will still fail — that's expected; they get fixed in later tasks)

Run: `go vet ./pkg/engine/`
Expected: no errors. (Do NOT run the broader `./...` — it will fail on downstream callers, which is expected at this checkpoint.)

- [ ] **Step 3: Commit (broken downstream — OK, fixed in subsequent tasks)**

```bash
git add pkg/engine/system.go
git commit -m "refactor(engine): collapse SystemBase[W] to non-generic; add default Update"
```

### Task 3: Update engine.SystemBase tests for the new shape

**Files:**
- Modify: `pkg/engine/system_test.go`

- [ ] **Step 1: Read the existing test file**

Run: `grep -n "^func Test" pkg/engine/system_test.go` to enumerate.

- [ ] **Step 2: Replace test file contents with the adapted version**

```go
package engine

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/query"
)

// AutoBindsQueries: Query[T] fields on the outer struct are picked up by
// BindQueries and built into ECS filters during BuildQueries.
type queriesTestSystem struct {
	SystemBase
	q query.Query[struct {
		E *ecs.Entity
	}]
}

func TestSystemBase_AutoBindsQueries(t *testing.T) {
	w := ecs.NewWorld()
	s := &queriesTestSystem{}
	s.SetDeps(w, nil)
	s.BindQueries(s)
	if len(s.queries) != 1 {
		t.Fatalf("BindQueries should pick up 1 query field, got %d", len(s.queries))
	}
	s.BuildQueries()
}

// DefaultUpdateIsNoop: a system that embeds SystemBase and defines Init but
// not Update can still be invoked as a System.
type updateNoopSystem struct {
	SystemBase
	initCalled bool
}

func (s *updateNoopSystem) Init() { s.initCalled = true }

func TestSystemBase_DefaultUpdateIsNoop(t *testing.T) {
	var s System = &updateNoopSystem{}
	s.Update(0.05) // must not panic
}

// SetDepsAssignsFields: SetDeps stores ecsWorld and engine on the base.
func TestSystemBase_SetDepsAssignsFields(t *testing.T) {
	w := ecs.NewWorld()
	s := &queriesTestSystem{}
	s.SetDeps(w, nil)
	if s.ECSWorld() != w {
		t.Error("SetDeps did not store ecsWorld")
	}
}
```

- [ ] **Step 3: Run the engine tests**

Run: `go test ./pkg/engine/ -v`
Expected: PASS for `TestSystemBase_AutoBindsQueries`, `TestSystemBase_DefaultUpdateIsNoop`, `TestSystemBase_SetDepsAssignsFields`.

- [ ] **Step 4: Commit**

```bash
git add pkg/engine/system_test.go
git commit -m "test(engine): adapt SystemBase tests to non-generic shape"
```

### Task 4: Migrate pkg/system internal systems to non-generic SystemBase

**Files:**
- Modify: `pkg/system/physics.go`
- Modify: `pkg/system/click_to_move.go`
- Modify: `pkg/system/direction_move.go`
- Modify: `pkg/system/lifetime.go`
- Modify: `pkg/system/spatial_system.go`

- [ ] **Step 1: Mechanical replacement in all 5 files**

In each file, change `engine.SystemBase[any]` → `engine.SystemBase`. Run:

```bash
sed -i 's/engine\.SystemBase\[any\]/engine.SystemBase/g' \
  pkg/system/physics.go \
  pkg/system/click_to_move.go \
  pkg/system/direction_move.go \
  pkg/system/lifetime.go \
  pkg/system/spatial_system.go
```

- [ ] **Step 2: Verify no stragglers**

Run: `grep -rn 'engine\.SystemBase\[' pkg/system/`
Expected: no output (all generic forms gone).

- [ ] **Step 3: Verify pkg/system compiles**

Run: `go vet ./pkg/system/`
Expected: no errors.

- [ ] **Step 4: Run pkg/system tests**

Run: `go test ./pkg/system/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/system/
git commit -m "refactor(system): drop [any] from engine.SystemBase embeds"
```

---

## Phase C — Mmokit Wrapper

### Task 5: Create pkg/mmokit/system.go with the wrapper SystemBase

**Files:**
- Create: `pkg/mmokit/system.go`

- [ ] **Step 1: Create the file**

```go
package mmokit

import (
	"reflect"
	"unsafe"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/universe"
)

// SystemBase is the canonical base for all game systems. Embed it to get:
//   - engine.SystemBase methods (ECSWorld, Engine, Init, default Update,
//     BindQueries, BuildQueries, SetDeps)
//   - Stage() — direct access to the per-cell *universe.Stage
//
// Replaces the previous generic mmokit.SystemBase[W] alias. Game systems no
// longer parameterize on a typed game world; typed per-cell state is fetched
// explicitly via mmokit.State[T](s.Stage()) and cached in each system's Init().
type SystemBase struct {
	engine.SystemBase
	stage *universe.Stage
}

// Stage returns the per-cell stage this system is wired to. Available
// after the framework has called InitStage (i.e. inside Init() and Update()
// — never in struct construction).
func (b *SystemBase) Stage() *universe.Stage { return b.stage }

// InitStage is called by the universe framework after SetDeps. Game code
// must not call this directly — the framework owns stage lifecycle.
func (b *SystemBase) InitStage(s *universe.Stage) { b.stage = s }
```

Note: `pkg/mmokit/system.go` does not need to import `reflect` or `unsafe` — those were only required by the abandoned `BindStateFields` field-discovery prototype.

- [ ] **Step 2: Verify pkg/mmokit compiles** (universe wiring not yet updated — pkg/universe will still fail; this checkpoint only proves mmokit's new types are syntactically OK)

Run: `go vet ./pkg/mmokit/`
Expected: no errors. (May fail because the alias `mmokit.SystemBase[W]` in mmokit.go now collides with the new struct. If so, proceed to Task 6 to remove the alias before re-running.)

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/system.go
git commit -m "feat(mmokit): add SystemBase wrapper with Stage() accessor"
```

### Task 6: Remove the SystemBase[W] type alias from pkg/mmokit/mmokit.go

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Find and delete the alias and its surrounding doc comment**

Locate `pkg/mmokit/mmokit.go` line ~96-102:

```go
// SystemBase is the generic base for all systems. Embed it with the game's
// typed world: `mmokit.SystemBase[*MyWorld]`. Engine-side systems that don't
// need world methods use `mmokit.SystemBase[any]`.
type SystemBase[W any] = engine.SystemBase[W]
```

Delete the entire block (doc comment + alias).

- [ ] **Step 2: Verify pkg/mmokit compiles**

Run: `go vet ./pkg/mmokit/`
Expected: no errors. (The new wrapper struct from Task 5 now resolves the `SystemBase` symbol uniquely.)

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "refactor(mmokit): drop SystemBase[W] alias (replaced by wrapper struct)"
```

### Task 7: Migrate defaultNetworkSystem and delete networkSystem[W] in mmokit.go

`pkg/mmokit/mmokit.go` defines two replication-system factories that used the old typed-world plumbing. After Task 2 (engine collapse) and Task 6 (alias removal), these no longer compile. This task migrates them onto the new `mmokit.SystemBase` + `Stage()` API and deletes the dead `[W]`-typed variant. Without this, `pkg/mmokit` cannot build.

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Update `defaultNetworkSystem` to embed mmokit.SystemBase and use Stage()**

Replace the struct (around line 1122):

```go
type defaultNetworkSystem struct {
	engine.SystemBase[any]
	replSys *ReplicationSystem
}
```

With:

```go
type defaultNetworkSystem struct {
	SystemBase
	replSys *ReplicationSystem
}
```

Then replace the `Init()` method (around line 1127) with:

```go
func (s *defaultNetworkSystem) Init() {
	stage := s.Stage()
	cfg := DefaultReplicationConfig(s.Engine(), stage.SpatialGrid(), stage.Process().ClusterClock)
	cfg.AoIRadius = stage.GetAoIRadius()
	wireBlinkDetector(&cfg, stage.Process(), s.Engine().Log)
	autoDiscoverReplicators(stage, &cfg)
	if cfg.Replicators == nil {
		return // no entity kinds registered — nothing to replicate
	}
	s.replSys = NewReplicationSystem(cfg)
}
```

(`Update` and `ReplicationSystem` methods stay unchanged.)

- [ ] **Step 2: Update `autoDiscoverReplicators` to take `*universe.Stage` directly**

Replace the function signature and body (around line 1263):

```go
// autoDiscoverReplicators populates cfg.Replicators from registered EntityKindDefs
// if no replicators were set explicitly.
func autoDiscoverReplicators(stage *universe.Stage, cfg *ReplicationConfig) {
	if cfg.Replicators != nil {
		return
	}
	defs := stage.EntityKindDefs()
	if len(defs) == 0 {
		return
	}
	defSlice := make([]universe.EntityKindDef, 0, len(defs))
	for _, d := range defs {
		defSlice = append(defSlice, *d)
	}
	cfg.Replicators = BuildReplicators(stage.ECSWorld(), stage.Process(), defSlice...)
}
```

- [ ] **Step 3: Delete `networkSystem[W]`, `NewNetworkSystemWith[W]`, and `clockFromGameWorld`**

These have no remaining callers (verified: `grep -rn 'NewNetworkSystemWith\|clockFromGameWorld' pkg/ internal/ examples/ cmd/` returns nothing outside of mmokit.go itself).

Delete the entire `NewNetworkSystemWith[W]` function and its doc comment (around lines 1169-1183).
Delete the entire `networkSystem[W]` struct definition and all its methods (around lines 1185-1220).
Delete the entire `clockFromGameWorld` helper (around lines 1146-1157).

- [ ] **Step 4: Verify pkg/mmokit compiles**

Run: `go vet ./pkg/mmokit/`
Expected: no errors. (`pkg/universe` may still fail because Phase D hasn't landed — that's expected. This step verifies pkg/mmokit's local consistency.)

- [ ] **Step 5: Verify no stragglers**

Run: `grep -n 'engine\.SystemBase\[\|s\.GameWorld()\|clockFromGameWorld\|NewNetworkSystemWith\|networkSystem\[W' pkg/mmokit/mmokit.go`
Expected: no output (every old-shape reference removed).

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "refactor(mmokit): migrate defaultNetworkSystem to Stage(); delete dead networkSystem[W]"
```

### Task 8: Add tests for mmokit.SystemBase wrapper

**Files:**
- Create: `pkg/mmokit/system_test.go`

- [ ] **Step 1: Create the test file**

```go
package mmokit

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/universe"
)

// TestSystemBase_StageInjection: a system embedding mmokit.SystemBase gets
// its stage populated by InitStage and surfaces it via Stage().
type stageInjectionTestSystem struct {
	SystemBase
}

func TestSystemBase_StageInjection(t *testing.T) {
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	var stage *universe.Stage
	for _, c := range mmo.Cells {
		stage = c.Stage
		break
	}

	s := &stageInjectionTestSystem{}
	s.InitStage(stage)
	if s.Stage() != stage {
		t.Errorf("Stage() returned %p, expected %p", s.Stage(), stage)
	}
}

// TestSystemBase_StateLookupCache: the canonical pattern for game systems —
// cache mmokit.State[T] in Init() and access state directly via the cached
// pointer.
type cacheTestState struct{ Tag string }
type cacheTestSystem struct {
	SystemBase
	gw *cacheTestState
}

func (s *cacheTestSystem) Init() {
	s.gw = State[cacheTestState](s.Stage())
}

func TestSystemBase_StateLookupCache(t *testing.T) {
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	AddState(mmo, func(*universe.Stage) *cacheTestState {
		return &cacheTestState{Tag: "ok"}
	})
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	var stage *universe.Stage
	for _, c := range mmo.Cells {
		stage = c.Stage
		break
	}

	s := &cacheTestSystem{}
	s.InitStage(stage)
	s.Init()

	if s.gw == nil {
		t.Fatal("Init() did not populate s.gw")
	}
	if s.gw.Tag != "ok" {
		t.Errorf("expected Tag=\"ok\", got %q", s.gw.Tag)
	}
}
```

- [ ] **Step 2: Run the new tests**

Run: `go test ./pkg/mmokit/ -run 'TestSystemBase_(StageInjection|StateLookupCache)' -v`
Expected: PASS for both. (Test runs `mmo.Build()` and pulls a stage from the cells map, so `pkg/universe` must compile — if Phase D hasn't landed yet, this test will fail at link time. In that case defer this task to after Phase D and verify here.)

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/system_test.go
git commit -m "test(mmokit): SystemBase Stage() injection and State[T] caching"
```

---

## Phase D — Universe Layer Rewire

### Task 9: Strip world plumbing from pkg/universe/stage.go

**Files:**
- Modify: `pkg/universe/stage.go`

- [ ] **Step 1: Delete the `world any` field and its doc comment**

Locate the struct around line 257 (`// an Entity (via Entity.Stage().GameWorld()) to game-side helpers.`) and the field that follows it. Delete the comment and the field.

- [ ] **Step 2: Delete `Stage.GameWorld()` method**

Locate around line 424:
```go
func (b *Stage) GameWorld() any { return b.world }
```
Delete the method and any leading doc comment.

- [ ] **Step 3: Delete `Stage.SetGameWorld()` method**

Locate around line 430:
```go
func (b *Stage) SetGameWorld(world any) { b.world = world }
```
Delete the method and any leading doc comment.

- [ ] **Step 4: Verify pkg/universe still parses (full compile will still fail until later tasks)**

Run: `gofmt -l pkg/universe/stage.go`
Expected: no output (file parses cleanly).

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/stage.go
git commit -m "refactor(universe): drop Stage.world field, GameWorld(), SetGameWorld()"
```

### Task 10: Delete GameWorld and BoundaryWorld interfaces

**Files:**
- Modify: `pkg/universe/world.go`

- [ ] **Step 1: Replace pkg/universe/world.go with the trimmed version**

```go
package universe

// NeighborInfo describes a neighbor cell's offset relative to the current cell.
type NeighborInfo struct {
	CellID string
	DX, DY int32 // cell offset from this cell
}
```

(Both `GameWorld` and `BoundaryWorld` interfaces removed. Imports collapse since the remaining type doesn't reference `ecs` or `engine`.)

- [ ] **Step 2: Verify file parses**

Run: `gofmt -l pkg/universe/world.go`
Expected: no output.

- [ ] **Step 3: Commit (downstream still broken — fixed in next tasks)**

```bash
git add pkg/universe/world.go
git commit -m "refactor(universe): delete GameWorld and BoundaryWorld interfaces"
```

### Task 11: Rewire BoundarySystem to non-generic SystemBase + private stage field

**Files:**
- Modify: `pkg/universe/boundary_system.go`

- [ ] **Step 1: Read the existing file to identify all `s.World()` callsites**

Run: `grep -n 'World()\|gw\.' pkg/universe/boundary_system.go`

- [ ] **Step 2: Update the struct definition**

Replace:
```go
type BoundarySystem struct {
	engine.SystemBase[BoundaryWorld]
	entities query.Query[struct {
		// ...
	}]
}
```

With:
```go
type BoundarySystem struct {
	engine.SystemBase
	stage *Stage // injected directly during construction (not via InitStage interface — universe-internal type)
	entities query.Query[struct {
		// ...
	}]
}
```

- [ ] **Step 3: Replace `gw := s.World()` and `s.World()` callsites**

In `Update(dt float32)`:
- `gw := s.World()` → `gw := s.stage`
- `s.World().X` → `s.stage.X`
- `gw.Bridge()`, `gw.Cell()`, `gw.CellID()`, `gw.CellSize()`, `gw.Engine()`, `gw.QueueCrossing(...)` — all become `s.stage.X` (Stage already has these methods).

- [ ] **Step 4: Verify file parses**

Run: `gofmt -l pkg/universe/boundary_system.go`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/boundary_system.go
git commit -m "refactor(universe): drop BoundaryWorld param, inject stage directly"
```

### Task 12: Rewire pkg/universe/coordinator.go system construction loop

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Delete `Config.OnInit` and `Config.World` fields**

In the `Config` struct (around line ~290), find and delete both:
```go
World func(base *Stage) GameWorld   // factory pattern
OnInit func(base *Stage)            // escape hatch
```
Plus their doc comments.

- [ ] **Step 2: Delete the mutual-exclusion panic and the world-factory init**

Locate around line 1554:
```go
if c.cfg.World != nil && c.cfg.OnInit != nil {
    panic("mmokit: Config.World and Config.OnInit are mutually exclusive — pick one")
}
c.worldFactory = c.cfg.World
// ...
c.onInit = c.cfg.OnInit

if roles.Has(RoleHost) && c.worldFactory == nil && c.onInit == nil {
    c.worldFactory = func(base *Stage) GameWorld { return base }
}
```

Delete the entire block.

- [ ] **Step 3: Delete the `c.worldFactory` and `c.onInit` field declarations on `Process`**

Locate around line 478:
```go
worldFactory func(*Stage) GameWorld
onInit       func(w *Stage)
```
Delete both.

- [ ] **Step 4: Delete the `*onInitWorld` wrapper type**

Locate around line 1440:
```go
type onInitWorld struct {
    *Stage
    initFn func(w *Stage)
}

func (w *onInitWorld) Init() {
    if w.initFn != nil {
        w.initFn(w.Stage)
    }
}
```
Delete the struct and its method.

- [ ] **Step 5: Delete the world-creation block**

Locate the system-construction phase around line 2151:
```go
var world GameWorld
if c.worldFactory != nil {
    world = c.worldFactory(base)
} else if c.onInit != nil {
    world = &onInitWorld{Stage: base, initFn: c.onInit}
} else {
    world = base
}
base.SetGameWorld(world)
```

Delete the entire block. (`base` — the Stage — is now used directly from this point forward.)

- [ ] **Step 6: Delete the `s.cell.World.Init()` call site**

Locate around line 1811:
```go
s.cell.World.Init()
```
Delete it. (Was where `onInitWorld.Init()` fired the `Config.OnInit` callback. No longer needed.)

- [ ] **Step 7: Update the system wiring loop**

Locate around line 2225:
```go
for i, def := range c.systemDefs {
    sys := def.Factory()

    type depsInjectable interface {
        SetDeps(w *ecs.World, eng *engine.Engine, gw any)
    }
    if di, ok := sys.(depsInjectable); ok {
        di.SetDeps(eng.ECS, eng, world)
    }

    type queryBinder interface {
        BindQueries(outer any)
    }
    if qb, ok := sys.(queryBinder); ok {
        qb.BindQueries(sys)
    }

    gameSystems[i] = sys
    systemNames[i] = def.Name
}
```

Replace with:
```go
for i, def := range c.systemDefs {
    sys := def.Factory()

    type depsInjectable interface {
        SetDeps(w *ecs.World, eng *engine.Engine)
    }
    if di, ok := sys.(depsInjectable); ok {
        di.SetDeps(eng.ECS, eng)
    }

    type stageInjectable interface {
        InitStage(s *Stage)
    }
    if si, ok := sys.(stageInjectable); ok {
        si.InitStage(base)
    }

    type queryBinder interface {
        BindQueries(outer any)
    }
    if qb, ok := sys.(queryBinder); ok {
        qb.BindQueries(sys)
    }

    gameSystems[i] = sys
    systemNames[i] = def.Name
}
```

- [ ] **Step 8: Update the BoundarySystem construction block**

Locate around line 2256:
```go
if _, ok := world.(BoundaryWorld); ok {
    bs := &BoundarySystem{}
    bs.SetDeps(eng.ECS, eng, world)
    bs.BindQueries(bs)
    gameSystems = append(gameSystems, bs)
    systemNames = append(systemNames, "CellBoundary")
}
```

Replace with (BoundarySystem is universe-internal — direct injection):
```go
{
    bs := &BoundarySystem{stage: base}
    bs.SetDeps(eng.ECS, eng)
    bs.BindQueries(bs)
    gameSystems = append(gameSystems, bs)
    systemNames = append(systemNames, "CellBoundary")
}
```

(BoundarySystem now installs unconditionally — every cell has a boundary detector.)

- [ ] **Step 9: Update the `node := &Cell{...}` initializer**

Locate around line 2264:
```go
node := &Cell{
    MeshID:    cell.MeshID(),
    Cell:      cell,
    Engine:    eng,
    World:     world,
    Stage:     base,
    // ...
}
```

Remove the `World:` line (kept `Stage:`):
```go
node := &Cell{
    MeshID:    cell.MeshID(),
    Cell:      cell,
    Engine:    eng,
    Stage:     base,
    // ...
}
```

- [ ] **Step 10: Find and replace `cell.World.UpdateCellBounds`**

Run: `grep -n 'cell\.World' pkg/universe/coordinator.go`
For each match (e.g. line 2787 `cell.World.UpdateCellBounds(...)`), replace `cell.World` with `cell.Stage`.

- [ ] **Step 11: Verify file parses**

Run: `gofmt -l pkg/universe/coordinator.go`
Expected: no output.

- [ ] **Step 12: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "refactor(universe): rewire system construction to two-arg SetDeps + InitStage"
```

### Task 13: Drop Cell.World field from pkg/universe/cell.go and update references

**Files:**
- Modify: `pkg/universe/cell.go`

- [ ] **Step 1: Find the World field**

Run: `grep -n 'World ' pkg/universe/cell.go`

- [ ] **Step 2: Delete the `Cell.World GameWorld` field declaration**

In the `Cell` struct, delete the line:
```go
World GameWorld
```
(plus any leading comment.)

- [ ] **Step 3: Find and update remaining `cell.World` callsites across pkg/universe**

Run: `grep -rn 'cell\.World\|\.World\b' pkg/universe/ | grep -v "_test\|GameWorld\b"`

For each callsite, replace `cell.World.X` with `cell.Stage.X`. Same goes for `c.World` patterns.

- [ ] **Step 4: Verify pkg/universe parses**

Run: `gofmt -l pkg/universe/`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/cell.go pkg/universe/
git commit -m "refactor(universe): drop Cell.World field; cell.World.X -> cell.Stage.X"
```

### Task 14: Drop onInit/worldFactory propagation from pkg/universe/host.go

**Files:**
- Modify: `pkg/universe/host.go`

- [ ] **Step 1: Find and delete the fields**

Run: `grep -n 'onInit\|worldFactory' pkg/universe/host.go`

Delete the lines (around line 52 in current code):
```go
onInit       func(w *Stage)
worldFactory func(*Stage) GameWorld
```

- [ ] **Step 2: Find and delete the propagation lines**

Look for `h.onInit = c.onInit` and `h.worldFactory = c.worldFactory` style assignments. Delete them. Same for any `host.onInit = ...` / `host.worldFactory = ...`.

Run: `grep -n 'onInit\|worldFactory' pkg/universe/host.go pkg/universe/coordinator.go`
Expected after edits: no remaining matches.

- [ ] **Step 3: Verify the package compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors. (At this checkpoint, pkg/universe should compile cleanly. Tests may still fail; addressed in Task 15.)

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/host.go pkg/universe/coordinator.go
git commit -m "refactor(universe): drop onInit/worldFactory propagation from Host"
```

### Task 15: Migrate pkg/universe tests off Config.OnInit and Config.World

**Files:**
- Modify: `pkg/universe/coordinator_test.go`
- Modify: `pkg/universe/partition_test.go`
- Modify: `pkg/universe/roles_test.go`
- Modify: `pkg/universe/auth_cookie_e2e_test.go`
- Modify: any other `*_test.go` under `pkg/universe/` that references `Config.OnInit` or `Config.World`

- [ ] **Step 1: Locate every test usage**

Run: `grep -rn 'Config{[^}]*OnInit\|Config{[^}]*World[^P]\|cfg\.OnInit\|cfg\.World\b' pkg/universe/ --include='*_test.go'`

- [ ] **Step 2: Delete `OnInit:` lines in test config literals**

For every match where `OnInit: func(w *Stage) {}` is a no-op satisfying a required field that no longer exists, delete the entire line.

Example: in `partition_test.go:154`, `roles_test.go:166` — these are no-op closures pure scaffolding. Delete the line entirely.

- [ ] **Step 3: Delete `TestConfigOnInitRunsOnceAfterConstruction` and the mutual-exclusion test**

In `coordinator_test.go`:
- Delete `TestConfigOnInitRunsOnceAfterConstruction` (around line 73-95)
- Delete the test that asserts panic when both `Config.World` and `Config.OnInit` are set (around line 28-46)

- [ ] **Step 4: Add a replacement test that proves system Init() fires once per cell**

Append to `pkg/universe/coordinator_test.go`:

```go
// onceInitTrackingSystem counts Init() invocations.
type onceInitTrackingSystem struct {
	engine.SystemBase
	calls int
}

func (s *onceInitTrackingSystem) Init() { s.calls++ }

// TestSystemInitRunsOnceAfterConstruction verifies that a system's Init()
// fires exactly once per cell during cell construction. Replaces the
// pre-C-full TestConfigOnInitRunsOnceAfterConstruction.
func TestSystemInitRunsOnceAfterConstruction(t *testing.T) {
	calls := 0
	sys := &onceInitTrackingSystem{}
	mmo := New(Config{
		CellsX: 2, CellsY: 2, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	// Use a counted factory so we can verify per-cell Init firing across the
	// 4 cells in a 2x2 grid (each cell gets a fresh system instance).
	mmo.AddSystem(engine.SystemDef{
		Name: "OnceInitTracker",
		Factory: func() engine.System {
			fresh := &onceInitTrackingSystem{}
			t.Cleanup(func() { calls += fresh.calls })
			return fresh
		},
	})
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	// 4 cells, each ran Init once on its system instance.
	expected := 4
	if calls != expected {
		t.Errorf("Init fired %d times across %d cells, expected %d", calls, expected, expected)
	}
	_ = sys // suppress unused if Cleanup doesn't fire on platforms
}
```

- [ ] **Step 5: Replace `Config.World = ...` test setup with `mmokit.AddState`**

For tests that meaningfully exercise typed game state via `Config.World`, switch to `mmokit.AddState`. (None of the listed test files appear to do this in current code; this is a defensive step in case grep missed one.)

- [ ] **Step 6: Run pkg/universe tests**

Run: `go test ./pkg/universe/ -run TestSystem -v`
Expected: PASS for `TestSystemInitRunsOnceAfterConstruction`.

Run: `go test ./pkg/universe/ -v`
Expected: PASS for the full universe test suite (allow up to 5 minutes for the longer S6/S7 integration tests; some require the Postgres docker-compose stack and may skip cleanly).

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/
git commit -m "test(universe): migrate off Config.OnInit; add System.Init lifecycle test"
```

---

## Phase E — Examples Migration

### Task 16: Migrate examples/simple to a bootstrap system

**Files:**
- Create: `examples/simple/system_field_spawner.go`
- Modify: `examples/simple/main.go`
- Modify: `examples/simple/system_sinewave.go`

- [ ] **Step 1: Create `examples/simple/system_field_spawner.go`**

```go
package main

import "github.com/zenion/mmoserver/pkg/mmokit"

// FieldSpawnerSystem seeds the demo with a horizontal row of entities at
// cell startup. Replaces the former Config.OnInit body.
type FieldSpawnerSystem struct{ mmokit.SystemBase }

func (s *FieldSpawnerSystem) Init() {
	const count = 60
	const span = 1200.0
	for i := range count {
		x := float32(i) * (span / float32(count-1))
		s.Stage().SpawnEntity(mmokit.Position{X: x, Y: 0})
	}
}
// no Update — default no-op via embedded SystemBase
```

- [ ] **Step 2: Update `examples/simple/main.go`**

Drop the `OnInit:` block from the `Config` literal:

Before:
```go
process := mmokit.New(mmokit.Config{
    WebDir:        "embed",
    StaticFS:      webFS,
    AnonymousAuth: true,
    Protocol:      mmokit.NewProtocol("simple"),
    OnInit: func(stage *mmokit.Stage) {
        const count = 60
        const span = 1200.0
        for i := range count {
            x := float32(i) * (span / float32(count-1))
            stage.SpawnEntity(mmokit.Position{X: x, Y: 0})
        }
    },
})

process.AddSystem(mmokit.NewSystem(&SineWaveSystem{}))
```

After:
```go
process := mmokit.New(mmokit.Config{
    WebDir:        "embed",
    StaticFS:      webFS,
    AnonymousAuth: true,
    Protocol:      mmokit.NewProtocol("simple"),
})

process.AddSystem(mmokit.NewSystem(&FieldSpawnerSystem{}))
process.AddSystem(mmokit.NewSystem(&SineWaveSystem{}))
```

- [ ] **Step 3: Update `examples/simple/system_sinewave.go`**

Change `mmokit.SystemBase[any]` → `mmokit.SystemBase`. No other changes.

- [ ] **Step 4: Build the example**

Run: `cd examples/simple && go build -o /tmp/simple-mmo .`
Expected: build succeeds.

- [ ] **Step 5: Smoke-run the binary briefly to confirm 60 entities spawn**

(Optional — can be skipped if the /tmp build succeeds; final task runs `just build` end-to-end.)

- [ ] **Step 6: Commit**

```bash
git add examples/simple/
git commit -m "refactor(simple): migrate Config.OnInit to FieldSpawnerSystem"
```

### Task 17: Migrate examples/4node-basic system_bots.go

**Files:**
- Modify: `examples/4node-basic/system_bots.go`

- [ ] **Step 1: Replace `mmokit.SystemBase[*mmokit.Stage]` with `mmokit.SystemBase`**

In the `BotSystem` struct definition (line ~21).

- [ ] **Step 2: Replace `s.World()` with `s.Stage()`**

Run: `grep -n 's\.World()' examples/4node-basic/system_bots.go`

For each callsite, swap `s.World()` → `s.Stage()`.

- [ ] **Step 3: Build the example**

Run: `cd examples/4node-basic && go build -o /tmp/4node-mmo .`
Expected: build succeeds.

- [ ] **Step 4: Run the example's mesh e2e test**

Run: `cd examples/4node-basic && go test -count=1 -v ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/system_bots.go
git commit -m "refactor(4node-basic): drop SystemBase[*Stage] generic; use Stage()"
```

---

## Phase F — internal/game Migration (the big one)

### Task 18: Build the migration reference lists

**Files:**
- Create: `docs/superpowers/plans/2026-05-08-stage-on-systembase-migration-lists.md`

- [ ] **Step 1: Generate Stage method list**

Run: `grep -E '^func \(b? \*Stage\) [A-Z]' pkg/universe/stage.go | sed -E 's/.*Stage\) ([A-Z][A-Za-z0-9]*).*/\1/' | sort -u`

Capture the output. These are the names that `s.World().X` callsites map to `s.Stage().X`.

- [ ] **Step 2: Generate GameWorld field list**

```bash
awk '/type GameWorld struct/,/^}$/' internal/game/gameworld.go \
  | grep -E '^\s+[A-Za-z]' \
  | awk '{print $1}' \
  | sort -u
```

Capture the output (uppercase = exported field, lowercase = unexported field). These are the names that `s.World().X` callsites map to `s.gw.X`.

- [ ] **Step 3: Write the lists to a reference file**

Create `docs/superpowers/plans/2026-05-08-stage-on-systembase-migration-lists.md` with two sections:

```markdown
# Migration Reference: s.World().X dispatch

## Stage methods (callsite -> s.Stage().X)
[paste list from Step 1]

## GameWorld fields (callsite -> s.gw.X)
[paste list from Step 2]

## Disambiguation
If a name appears in both lists, treat as field access (s.gw.X).
```

- [ ] **Step 4: Commit the reference**

```bash
git add docs/superpowers/plans/2026-05-08-stage-on-systembase-migration-lists.md
git commit -m "docs(plan): pre-built migration lists for s.World().X dispatch"
```

### Task 19: Refactor internal/game/gameworld.go (struct + factory)

**Files:**
- Modify: `internal/game/gameworld.go`

- [ ] **Step 1: Stop embedding `*mmokit.Stage`**

Find the `GameWorld` struct (line 77). Replace:
```go
type GameWorld struct {
    *mmokit.Stage
    eng *mmokit.Engine
    // ... other fields
}
```

With:
```go
type GameWorld struct {
    stage *mmokit.Stage
    eng   *mmokit.Engine
    // ... other fields (UNCHANGED)
}
```

- [ ] **Step 2: Update internal callsites within gameworld.go**

For every method on `*GameWorld` that today calls `gw.X` where X is a Stage method via embedding (e.g. `gw.Spawn(...)`, `gw.Engine()`, `gw.Cell()`, `gw.LookupNetID(...)`):
- `gw.X(...)` → `gw.stage.X(...)`

Run: `grep -nE 'func \(gw \*GameWorld\) ' internal/game/gameworld.go` to find every method, then audit each method body.

- [ ] **Step 3: Update or rewrite `NewGameWorld(stage *mmokit.Stage)` to `NewGameWorldState`**

If a `NewGameWorld(stage)` constructor exists, rename or wrap it as the AddState factory:

```go
// NewGameWorldState is the AddState[GameWorld] factory. Each cell calls this
// once during construction to instantiate per-cell game state.
func NewGameWorldState(stage *mmokit.Stage) *GameWorld {
    return &GameWorld{
        stage: stage,
        eng:   stage.Engine(),
        // ... initialize other fields as before
    }
}
```

- [ ] **Step 4: Verify gameworld.go compiles**

Run: `go vet ./internal/game/gameworld.go`
Expected: no errors. (Other game files will fail; addressed in subsequent tasks.)

- [ ] **Step 5: Commit**

```bash
git add internal/game/gameworld.go
git commit -m "refactor(game): GameWorld stops embedding Stage; carries it as private field"
```

### Task 20: Wire GameWorld via AddState; delete UnwrapGameWorld helper

The current code routes through a `WorldFactory` helper (`internal/game/factory.go`) consumed by `cmd/server/main.go` (`coordCfg.World = game.WorldFactory(...)`), and exposes an `UnwrapGameWorld(world)` cast helper used in 4 places (`cmd/server/main.go`, `internal/game/commands/helpers.go`, `internal/game/factory.go::gameWorldFromStage`, `internal/game/testutil_test.go`). After C-full all of this collapses to `mmokit.AddState(coord, NewGameWorldState)` plus `mmokit.State[GameWorld](stage)` lookups.

**Files:**
- Modify: `internal/game/factory.go`
- Modify: `internal/game/game.go` (delete `UnwrapGameWorld`)
- Modify: `cmd/server/main.go` (update World wiring + UnwrapGameWorld callers)
- Modify: `internal/game/commands/helpers.go`
- Modify: `internal/game/testutil_test.go`

- [ ] **Step 1: Replace `WorldFactory` with `NewGameWorldStateFactory` in `internal/game/factory.go`**

Replace the existing `WorldFactory` block (lines 9-32):

```go
// NewGameWorldStateFactory returns the factory passed to mmokit.AddState[GameWorld].
// The gameCfg pointer is shared across every GameWorld the coordinator creates so
// that runtime config changes made through the console apply to every cell.
func NewGameWorldStateFactory(
	gameCfg *GameConfig,
	playerDB *PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) func(base *mmokit.Stage) *GameWorld {
	return func(base *mmokit.Stage) *GameWorld {
		cell := base.Cell()
		rootCell := cell
		for rootCell.Depth > 0 {
			rootCell = rootCell.Parent()
		}
		gw := NewGameWorld(base, gameCfg, playerDB, mmokit.CellCoord{
			CellX: rootCell.X,
			CellY: rootCell.Y,
		}, base.FromSplit())
		gw.PlayerSessions = playerSessions
		return gw
	}
}
```

- [ ] **Step 2: Update `gameWorldFromStage` in `internal/game/factory.go`**

Replace (around line 140):

```go
func gameWorldFromStage(stage *mmokit.Stage) *GameWorld {
	if stage == nil {
		return nil
	}
	cell := stage.Process().CellByID(stage.CellID())
	if cell == nil {
		return nil
	}
	return UnwrapGameWorld(cell.World)
}
```

With:

```go
func gameWorldFromStage(stage *mmokit.Stage) *GameWorld {
	if stage == nil {
		return nil
	}
	return mmokit.State[GameWorld](stage)
}
```

(The cell-existence check is now unnecessary — if `stage` is non-nil, the AddState registry lookup will succeed; a torn-down cell wouldn't have a live `*Stage` reference in the first place.)

- [ ] **Step 3: Delete `UnwrapGameWorld` from `internal/game/game.go`**

Delete the function definition around line 164-170 (and its doc comment).

- [ ] **Step 4: Update `cmd/server/main.go`**

In `cmd/server/main.go`:
- Line 305: `coordCfg.World = game.WorldFactory(&gameCfg, playerDB, playerSessions)` → delete this line entirely. The world wiring moves to step 4b.
- Add immediately after `coordinator = mmokit.New(coordCfg)` (around line 308):
  ```go
  if needsGameState {
      mmokit.AddState(coordinator, game.NewGameWorldStateFactory(&gameCfg, playerDB, playerSessions))
  }
  ```
- Line 260: `gw := game.UnwrapGameWorld(node.World)` → `gw := mmokit.State[game.GameWorld](node.Stage)`
- Line 276: same replacement.

- [ ] **Step 5: Update `internal/game/commands/helpers.go`**

Locate around line 14:
```go
return game.UnwrapGameWorld(cell.World)
```

Replace with:
```go
return mmokit.State[game.GameWorld](cell.Stage)
```

(Add `"github.com/zenion/mmoserver/pkg/mmokit"` to imports if not already present.)

- [ ] **Step 6: Update `internal/game/testutil_test.go`**

Locate around line 90:
```go
return UnwrapGameWorld(node.World)
```

Replace with:
```go
return mmokit.State[GameWorld](node.Stage)
```

- [ ] **Step 7: Final straggler grep**

Run: `grep -rn 'UnwrapGameWorld\|WorldFactory\b\|cell\.World\b\|node\.World\b' internal/ cmd/ pkg/ --include='*.go'`
Expected: no output. (If anything remains, fix it before continuing.)

- [ ] **Step 8: Verify the affected packages compile**

Run: `go vet ./internal/game/... ./cmd/server/...`
Expected: no errors.

- [ ] **Step 9: Commit**

```bash
git add internal/game/factory.go internal/game/game.go internal/game/commands/helpers.go internal/game/testutil_test.go cmd/server/main.go
git commit -m "refactor(game): wire GameWorld via AddState; delete UnwrapGameWorld helper"
```

### Task 21: Migrate all internal/game system_*.go files

**Files:**
- Modify: `internal/game/system_ability.go`
- Modify: `internal/game/system_collision.go`
- Modify: `internal/game/system_docking.go`
- Modify: `internal/game/system_economy.go`
- Modify: `internal/game/system_equipment.go`
- Modify: `internal/game/system_mining.go`
- Modify: `internal/game/system_network.go`
- Modify: `internal/game/system_ship_dynamics.go`
- Modify: `internal/game/system_shieldregen.go`
- Modify: `internal/game/system_statuseffect.go`
- Modify: `internal/game/system_targetlock.go`
- Modify: `internal/game/system_wander.go`
- (and any other `system_*.go` revealed by grep below)

- [ ] **Step 1: Enumerate all targets**

Run: `grep -l 'mmokit\.SystemBase\[\*GameWorld\]' internal/game/*.go`

Confirm the file list above (or expand it).

- [ ] **Step 2: For each file, change the embed**

Replace `mmokit.SystemBase[*GameWorld]` → `mmokit.SystemBase`.

```bash
sed -i 's/mmokit\.SystemBase\[\*GameWorld\]/mmokit.SystemBase/g' internal/game/system_*.go
```

- [ ] **Step 3: For each file, add a `gw *GameWorld` field and an `Init()` method**

For each system struct, add a private `gw *GameWorld` field immediately after the embedded `mmokit.SystemBase`. Then add (or extend the existing) `Init()` method to cache the lookup once per cell:

```go
func (s *EconomySystem) Init() {
    s.gw = mmokit.State[GameWorld](s.Stage())
}
```

If a system already has an `Init()` (e.g. for query configuration), prepend the `s.gw = ...` assignment as the first statement.

This must be done manually per file (sed cannot reliably insert after the embed; field placement matters for readability, and `Init()` may need merging with existing code).

Example before:
```go
type EconomySystem struct {
    mmokit.SystemBase[*GameWorld]
    stations mmokit.Query[struct {
        Station *gamecomp.Station
        Pos     *mmokit.Position
    }]
}
```

After:
```go
type EconomySystem struct {
    mmokit.SystemBase
    gw       *GameWorld
    stations mmokit.Query[struct {
        Station *gamecomp.Station
        Pos     *mmokit.Position
    }]
}

func (s *EconomySystem) Init() {
    s.gw = mmokit.State[GameWorld](s.Stage())
}
```

- [ ] **Step 4: Rewrite all `s.World().X` callsites by category**

For each callsite, dispatch using the migration reference lists (Task 18):
- If X is a **Stage method** → `s.Stage().X`
- If X is a **GameWorld field** → `s.gw.X`
- If `gw := s.World()` is captured locally, drop the local assignment entirely and use `s.gw` directly throughout the function. (`s.gw` is exactly `*GameWorld`, so `s.gw.PlayerEntities`, `s.gw.PlayerDB`, etc. work as plain field access.)

Run a search-and-categorize pass:
```bash
grep -rn 's\.World()\.\|gw\.' internal/game/system_*.go | wc -l
```

This produces the working list. Process each file individually, dispatching by name match against the lists from Task 18. **Do NOT use free-form sed for this step** — the disambiguation rule (field-wins-over-method) requires per-callsite judgement.

Special note for the `gw.eng.Log` pattern (unexported field on GameWorld): becomes `s.gw.eng.Log` (since `eng` is unexported on GameWorld but accessible within the `internal/game` package).

- [ ] **Step 5: Verify internal/game compiles**

Run: `go vet ./internal/game/...`
Expected: no errors. If errors point to specific missed callsites, fix them with reference to Task 18's lists.

- [ ] **Step 6: Commit**

```bash
git add internal/game/system_*.go
git commit -m "refactor(game): migrate ~13 systems to mmokit.SystemBase + Init() state cache"
```

### Task 22: Migrate OnPlayerJoin / OnPlayerLeave hooks (if needed)

**Files:**
- Modify: any file under `internal/game/` registering `OnPlayerJoin` / `OnPlayerLeave` hooks that rely on Stage embedding via GameWorld.

- [ ] **Step 1: Find hook registrations**

Run: `grep -rn 'OnPlayerJoin\|OnPlayerLeave' internal/game/`

- [ ] **Step 2: Verify hooks already take `(*PlayerSession, *mmokit.Stage)`**

The 2026-04-27 spec landed this signature; current hooks should already use it. If a hook does:
```go
mmo.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
    // ... uses gw.X via captured gameworld
})
```

Update the body to use `mmokit.State[GameWorld](stage)` for typed-state access:
```go
mmo.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
    gw := mmokit.State[GameWorld](stage)
    // ... use gw.X
})
```

- [ ] **Step 3: Verify compiles**

Run: `go vet ./internal/game/...`
Expected: no errors.

- [ ] **Step 4: Commit (only if changes made)**

```bash
git add internal/game/
git commit -m "refactor(game): OnPlayerJoin/Leave hooks fetch GameWorld via mmokit.State"
```

---

## Phase G — Final Verification

### Task 23: Full build + test sweep

**Files:** none modified.

- [ ] **Step 1: Verify entire workspace compiles**

Run: `go vet ./...`
Expected: no errors anywhere.

- [ ] **Step 2: Run the full Go test suite**

Run: `go test -count=1 ./...`
Expected: PASS. (Long-running integration tests in pkg/universe may take 5+ minutes; some Postgres-tagged tests skip without `-tags=pgtest`.)

- [ ] **Step 3: Run the project's canonical build**

Run: `just build`
Expected: produces `bin/server` and the SDK without errors.

- [ ] **Step 4: Run examples/simple briefly**

Run: `cd examples/simple && go run . &` then `sleep 3 && curl -s http://localhost:8080/ > /dev/null && kill %1`
Expected: server starts cleanly; HTTP root returns 200.

- [ ] **Step 5: Run the 4node-basic example tests**

Run: `cd examples/4node-basic && go test -count=1 ./...`
Expected: PASS (`mesh_e2e_test.go` exercises full distribution behavior).

- [ ] **Step 6: Final stragglers grep**

Run: 
```bash
grep -rn 'SystemBase\[\|GameWorld interface\|BoundaryWorld\|onInitWorld\|Config\.OnInit\|Config\.World[^P]\|cfg\.OnInit\|cfg\.World\b\|WorldOf\b\|WorldOfCell\b\|UnwrapGameWorld\|WorldFactory\b\|cell\.World\b\|node\.World\b\|s\.World()\|s\.GameWorld()\|clockFromGameWorld\|NewNetworkSystemWith' \
  pkg/ internal/ examples/ cmd/ --include='*.go' 2>/dev/null
```

Expected: no output. (If anything turns up, fix it before considering the migration complete.)

- [ ] **Step 7: Update CLAUDE.md (only if migration changes documented behavior)**

Audit `CLAUDE.md` for any mentions of:
- `Config.OnInit`
- `Config.World`
- `mmokit.SystemBase[*MyWorld]`
- `mmokit.WorldOf[T]` / `mmokit.WorldOfCell[T]`
- `GameWorld` interface

Update those sections to reflect the new API (Stage(), AddState[T]/State[T] flow, no W parameter).

- [ ] **Step 8: Final commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
docs(claude): update for Stage on SystemBase API

Reflects the C-full migration: Config.OnInit/Config.World gone, mmokit.SystemBase
non-generic with Stage() accessor, typed state via AddState[T]/State[T].

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Plan Self-Review Checklist (run before handoff)

- [ ] Every spec section maps to ≥1 task.
- [ ] No "TODO" / "TBD" / "fill in" / "similar to Task N" placeholders.
- [ ] All function signatures are stable across tasks (`SetDeps(w, eng)` everywhere; `InitStage(s *Stage)` everywhere).
- [ ] All file paths are absolute (rooted at repo).
- [ ] Each task ends in a commit.
- [ ] Verification commands per task are concrete.
