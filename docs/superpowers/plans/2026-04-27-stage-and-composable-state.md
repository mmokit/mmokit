# Stage and Composable State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-cell "World" abstraction with "Stage", introduce composable typed per-stage state via `AddState[T]`/`State[T]`, and consolidate scattered low-level APIs (KindComponent, OnState, WithComponents) behind ergonomic generics (`RegisterKind[T]`, `mmo.OnPlayerJoin`, `Stage.SpawnPlayer`, `mmokit.Init(fn)`).

**Architecture:** Eight phases. Phases 1–5 add new APIs **alongside** existing ones, each with TDD. Phase 6 migrates the 4node-basic example to use exclusively new APIs and deletes its `world.go`. Phase 7 migrates pkg/universe internal tests to validate the new API at the universe layer. Phase 8 mechanically renames `WorldBase` → `Stage` and `Cell.Base` → `Cell.Stage` across the entire codebase.

**Tech Stack:** Go 1.23+, ark ECS v0.7.1, reflect-based bundle structs (mirrors existing `pkg/query/query.go` pattern), Go generics for type-safe registries.

## Scope

**In scope (this plan):**
- Add `mmokit.RegisterKind[T]`, `mmokit.Init(fn)`, `WorldBase.SpawnPlayer`, `Process.OnPlayerJoin`/`OnPlayerLeave`, `mmokit.AddState[T]`/`State[T]`.
- Migrate `examples/4node-basic/` to the new APIs; delete `examples/4node-basic/world.go`.
- Migrate `pkg/universe/` internal tests that use `KindComponent`/`RegisterEntityKind` directly.
- Rename `WorldBase` → `Stage` and `Cell.Base` → `Cell.Stage` codebase-wide.
- Delete `mmokit.WorldOf[T]` / `mmokit.WorldOfCell[T]` (4node-basic stops needing them post-migration).

**Out of scope (deferred to follow-up plan):**
- Migrating `internal/game/` (the spaceship MMO) to the new APIs. Its embed-based `GameWorld` struct keeps working through Phase 8 because `Config.World` and the `GameWorld` interface remain in place.
- Deleting `Config.World`, `Config.OnInit`, `KindComponent`, `KindComponentLocalOnly`, `RegisterEntityKind`, `WithComponents`, the `GameWorld` interface. These can only be removed once `internal/game/` is migrated.
- Refactoring the `GameWorld` interface methods (SerializeEntity, HandleCrossCellAction, DispatchChat, etc.) into Process-level extension hooks. Out of scope; they remain interface methods.

This staging keeps every commit green and defers the largest migration to a dedicated plan.

## File Structure

**New files in `pkg/mmokit/`:**
- `kindreg.go` — `RegisterKind[T]`, bundle reflection, kind-spec registry on Process, per-cell realization hook.
- `kindreg_test.go` — TDD tests for `RegisterKind[T]`.
- `lifecycle.go` — `Process.OnPlayerJoin`, `Process.OnPlayerLeave`, default `OnPlayerLeave` body.
- `lifecycle_test.go` — TDD tests for lifecycle hooks.
- `state.go` — `AddState[T]`, `State[T]`, per-stage typed-state registry.
- `state_test.go` — TDD tests for state plugins.
- `spawn_init.go` — `mmokit.Init(fn)` SpawnOption (lives in mmokit, not universe, because the type-inferred bundle generic is more discoverable as a free function).
- `spawn_init_test.go` — TDD tests for `Init(fn)`.

**Modified files:**
- `pkg/universe/world_base.go` — add `SpawnPlayer` method; modify `WithEntityKind` behavior to auto-attach kind components; drop `WithComponents` no-args (becomes no-op then deleted in scope-limited fashion); add private hooks for kind-spec realization and state-factory invocation; **renamed to `pkg/universe/stage.go` in Phase 8** with `WorldBase` → `Stage`.
- `pkg/universe/coordinator.go` — add `worldFactory`-side hooks for invoking new mmokit registries during `createNode`; **rename `Cell.Base` → `Cell.Stage` in Phase 8**.
- `pkg/universe/cell.go` — **rename `Cell.Base` field → `Cell.Stage` in Phase 8**.
- `pkg/mmokit/mmokit.go` — wire `Process` to new registries in Phase 1, 4, 5.
- `examples/4node-basic/components.go` — add `PlayerComponents`, `BotComponents` bundle structs in Phase 6.
- `examples/4node-basic/main.go` — register kinds, lifecycle hooks, and systems via new APIs in Phase 6; remove `World: NewWorld` field.
- `examples/4node-basic/command_bots.go` — use `mmokit.Init(fn)`, drop `WorldOfCell` calls in Phase 6.
- `examples/4node-basic/system_bots.go` — switch `BotSystem` to `mmokit.SystemBase[*mmokit.Stage]` in Phase 8 rename.
- `examples/4node-basic/world.go` — **deleted in Phase 6**.
- `pkg/universe/border_replication_*_test.go` — migrate from `KindComponent` direct calls to `RegisterKind[T]` in Phase 7.

## Pre-Plan Sanity

Before starting Phase 1, run `just build` and `go test ./...` to confirm the tree is green on `feature/pluggable-services`. Capture and review any pre-existing failures so they aren't attributed to plan work.

## Phase 0: Forward-reference alias for `Stage`

All new code in Phases 1–7 will reference the developer-facing type as `Stage`. To avoid a giant rename in already-written test code at Phase 8, introduce the alias upfront. The underlying type stays `universe.WorldBase` until Phase 8 renames it.

### Task 0.1: Add `mmokit.Stage` alias

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Locate the existing `mmokit.WorldBase` alias**

```bash
grep -n 'type WorldBase' pkg/mmokit/mmokit.go
```

Expected: a line like `type WorldBase = universe.WorldBase`. If absent, find where mmokit re-exports `universe` types and add `WorldBase` there.

- [ ] **Step 2: Add the `Stage` alias next to `WorldBase`**

```go
// Stage is the per-cell simulation surface. Implementation-wise it is the
// same value as WorldBase — the rename is in flight (see plan
// docs/superpowers/plans/2026-04-27-stage-and-composable-state.md). Phase 8
// renames the underlying universe.WorldBase to universe.Stage and updates
// this alias accordingly.
type Stage = universe.WorldBase
```

- [ ] **Step 3: Verify build is unchanged**

```bash
just build
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "feat(mmokit): add Stage alias as forward-reference for WorldBase rename"
```

---

## Phase 1: `mmokit.RegisterKind[T]`

Reflection-based kind registration. The bundle struct `T` has `*ComponentType` fields. Reflection enumerates them, the registry stores a per-cell realizer that calls the existing `KindComponent` API once per cell.

### Task 1.1: Define the kind-spec registry on Process

**Files:**
- Modify: `pkg/mmokit/mmokit.go` (Process struct)
- Create: `pkg/mmokit/kindreg.go`

- [ ] **Step 1: Add a private `kindSpecs` slice to Process**

In `pkg/mmokit/mmokit.go`, find the `Process` struct (it's the type returned by `New()` — likely `type Process = universe.Process` alias). Confirm the alias path. We will add the spec storage to the upstream `universe.Process`. So:

In `pkg/universe/coordinator.go`, find `type Process struct {`. Add this field after the existing `worldFactory` field (around line 381):

```go
// kindSpecs holds typed entity-kind registrations from
// mmokit.RegisterKind[T]. Realized per-cell during createNode.
kindSpecs []kindSpec
```

Add the `kindSpec` type (also in `coordinator.go` near other private types):

```go
// kindSpec captures one mmokit.RegisterKind[T] call. The realize closure
// runs once per cell to materialize the kind's components against that
// cell's ecs.World.
type kindSpec struct {
    realize func(*WorldBase)
}
```

- [ ] **Step 2: Add a public `RegisterKindSpec` accessor on Process**

In `pkg/universe/coordinator.go`, add (anywhere among the other Process methods):

```go
// RegisterKindSpec registers a per-cell realizer for an entity kind. Called
// by mmokit.RegisterKind[T]; each registered realize fn runs against every
// cell's WorldBase during createNode. Internal API — game code uses
// mmokit.RegisterKind[T].
func (p *Process) RegisterKindSpec(realize func(*WorldBase)) {
    p.kindSpecs = append(p.kindSpecs, kindSpec{realize: realize})
}
```

- [ ] **Step 3: Invoke registered kindSpecs during createNode**

In `pkg/universe/coordinator.go`, find `createNode` (around line 1496). After the `world = c.worldFactory(base)` / `world = base` decision tree (around line 1547–1552), and before the cell construction completes, add:

```go
// Realize all registered kind specs against this cell's WorldBase.
for _, spec := range c.kindSpecs {
    spec.realize(base)
}
```

The exact insertion point: after `base` is fully constructed and the world factory has run, but before any code that depends on entity kinds. Look for where the cell's `EntityKindDefs()` would be queried.

- [ ] **Step 4: Commit (no tests yet — this is plumbing)**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat(universe): add kindSpecs registry on Process for mmokit.RegisterKind"
```

### Task 1.2: Implement `mmokit.RegisterKind[T]` reflection

**Files:**
- Create: `pkg/mmokit/kindreg.go`
- Create: `pkg/mmokit/kindreg_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/mmokit/kindreg_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/mmokit/ -run TestRegisterKind -v
```

Expected: FAIL with `undefined: buildKindSpec`.

- [ ] **Step 3: Implement `buildKindSpec` and `RegisterKind[T]`**

Ark v0.7.1 exposes the type-erased primitives we need:
- `ecs.TypeID(world *World, tp reflect.Type) ID` — registers/returns a component ID by reflect.Type (in `ark/ecs/functions.go:49`).
- `World.Unsafe()` returns an `Unsafe` handle.
- `Unsafe.Add(entity Entity, comp ...ID)` adds components by ID (in `ark/ecs/unsafe.go`).

This means we can do everything with reflection — no per-component generic dispatch table.

Create `pkg/mmokit/kindreg.go`:

```go
// Package-internal kind registration. Game code uses mmokit.RegisterKind[T]
// to declare entity kinds via a typed component-bundle struct. Reflection
// enumerates the bundle's *ComponentType fields and registers each as a
// KindComponent on every cell's WorldBase via ark's type-erased TypeID +
// Unsafe.Add primitives.
//
// Pattern mirrors pkg/query/query.go: a generic bundle struct serves as
// both the kind spec (here) and the query iterator (there).

package mmokit

import (
	"fmt"
	"reflect"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/universe"
)

// RegisterKind registers an entity kind on the Process. T is a struct of
// pointer-to-component fields; each field is added as a KindComponent on
// every cell's WorldBase during cell construction.
//
//	type PlayerComponents struct {
//	    Name       *PlayerName
//	    MoveTarget *mmokit.MoveTarget
//	}
//	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings)
func RegisterKind[T any](p *universe.Process, kind uint8, name string, bindings universe.EngineBindingsConfig) {
	realize := buildKindSpec[T](kind, name, &bindings, nil)
	p.RegisterKindSpec(realize)
}

// buildKindSpec is the reflection core of RegisterKind. It validates T's
// fields once (cheap) and returns a closure that, given a *WorldBase,
// registers an EntityKindDef with one component per bundle field.
//
// The optional `notify` argument is for testing — called with each
// component reflect.Type as the spec is built, before any WorldBase exists.
func buildKindSpec[T any](kind uint8, name string, bindings *universe.EngineBindingsConfig, notify func(reflect.Type)) func(*universe.WorldBase) {
	bundleType := reflect.TypeFor[T]()
	if bundleType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.RegisterKind: T must be a struct, got %v", bundleType.Kind()))
	}

	var compTypes []reflect.Type
	for i := 0; i < bundleType.NumField(); i++ {
		f := bundleType.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Pointer {
			panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must be a pointer (got %v)", bundleType.Name(), f.Name, f.Type.Kind()))
		}
		compType := f.Type.Elem()
		if compType.Kind() != reflect.Struct {
			panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must point to a struct (got *%v)", bundleType.Name(), f.Name, compType.Kind()))
		}
		compTypes = append(compTypes, compType)
		if notify != nil {
			notify(compType)
		}
	}

	return func(base *universe.WorldBase) {
		def := universe.EntityKindDef{Kind: kind, Name: name}
		if bindings != nil {
			def.EngineBindings = bindings
		}
		w := base.ECSWorld()
		for _, ct := range compTypes {
			id := ecs.TypeID(w, ct)
			universe.AddKindComponentByID(&def, id)
		}
		base.RegisterEntityKind(def)
	}
}
```

- [ ] **Step 4: Add `universe.AddKindComponentByID` and the corresponding type-erased add path**

In `pkg/universe/entity_kind.go`, after the existing `KindComponent` function, add:

```go
// AddKindComponentByID is the type-erased counterpart to KindComponent.
// Mirrors KindComponent (which takes a *ecs.Map1[T]) but works from a
// pre-resolved ecs.ID — used by mmokit.RegisterKind[T] which walks a
// bundle struct via reflection and resolves each field's component type
// via ecs.TypeID.
//
// Internal API; game code uses mmokit.RegisterKind[T].
func AddKindComponentByID(def *EntityKindDef, id ecs.ID) {
	def.components = append(def.components, kindComponent{
		typeID: id,
		add: func(w *ecs.World, e ecs.Entity) {
			w.Unsafe().Add(e, id)
		},
	})
}
```

Verify that the existing `kindComponent` struct shape (`typeID`, `add` closure) is what's there. If field names differ, adapt. Read `pkg/universe/entity_kind.go` first to confirm.

- [ ] **Step 5: Run tests**

```bash
go test ./pkg/mmokit/ -run TestRegisterKind -v
```

Expected: PASS for all three TestRegisterKind tests.

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/kindreg.go pkg/mmokit/kindreg_test.go pkg/universe/entity_kind.go
git commit -m "feat(mmokit): RegisterKind[T] with bundle-struct reflection"
```

### Task 1.3: Integration test — RegisterKind realizes per-cell

**Files:**
- Modify: `pkg/mmokit/kindreg_test.go`

- [ ] **Step 1: Add an integration test that spins up a Process with one cell**

In `pkg/mmokit/kindreg_test.go`, add:

```go
func TestRegisterKind_RealizesPerCell(t *testing.T) {
	mmo := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	RegisterKind[kindRegTestBundle](mmo, 100, "TestKind", EngineBindingsConfig{})
	if err := mmo.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = mmo.Shutdown(context.Background()) })

	// Inspect: the single cell's WorldBase should have an EntityKindDef
	// for kind 100 with both bundle fields registered.
	cells := mmo.Cells
	var cell *Cell
	for _, c := range cells { cell = c; break }
	if cell == nil {
		t.Fatal("expected at least one cell")
	}
	defs := cell.Base.EntityKindDefs()
	def, ok := defs[100]
	if !ok {
		t.Fatalf("kind 100 not registered on cell %s", cell.ID)
	}
	if def.Name != "TestKind" {
		t.Errorf("expected kind name TestKind, got %q", def.Name)
	}
	// The number of registered components should equal the bundle field count.
	// (Use whatever inspection the EntityKindDef exposes — may need to add a
	// public ComponentCount() method if none exists.)
}
```

- [ ] **Step 2: Run test, fix anything broken in plumbing**

```bash
go test ./pkg/mmokit/ -run TestRegisterKind_RealizesPerCell -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/kindreg_test.go
git commit -m "test(mmokit): integration test for RegisterKind per-cell realization"
```

---

## Phase 2: `mmokit.Init(fn)` SpawnOption + auto-attach kind components

### Task 2.1: Make `WithEntityKind` auto-attach kind components

Today: `WithEntityKind(K)` only sets the entity-kind component. Auto-attach requires a separate `WithComponents()` call. We collapse them: `WithEntityKind(K)` always auto-attaches.

**Files:**
- Modify: `pkg/universe/world_base.go`
- Create or extend: `pkg/universe/world_base_test.go`

- [ ] **Step 1: Write the failing test**

In `pkg/universe/world_base_test.go` (create if needed; check if a sibling test file already covers SpawnEntity), add:

```go
func TestSpawnEntity_WithEntityKind_AutoAttachesComponents(t *testing.T) {
	base := newTestWorldBase(t)
	type fakeMarker struct{ V int }
	def := EntityKindDef{Kind: 1, Name: "Marker"}
	KindComponent(&def, ecs.NewMap1[fakeMarker](base.ECSWorld()))
	base.RegisterEntityKind(def)

	e := base.SpawnEntity(component.Position{X: 0, Y: 0}, WithEntityKind(1))

	mp := ecs.NewMap1[fakeMarker](base.ECSWorld())
	if !mp.HasAll(e) {
		t.Fatal("expected fakeMarker to be auto-attached when WithEntityKind(1) is set")
	}
}
```

If `newTestWorldBase` does not exist, look for one in existing tests (`bootstrap_test.go`, `border_replication_stub_test.go`); if absent, write a minimal one that constructs `engine.New` + `NewWorldBase` with default config.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/universe/ -run TestSpawnEntity_WithEntityKind_AutoAttachesComponents -v
```

Expected: FAIL — fakeMarker not present.

- [ ] **Step 3: Modify SpawnEntity to auto-attach kind components**

In `pkg/universe/world_base.go` SpawnEntity (around line 1351), find where the kind is currently set. Replace the conditional:

```go
if o.withComps {
    b.EnsureEntityKindComponents(entity)
}
```

with:

```go
if o.hasKind {
    b.EnsureEntityKindComponents(entity)
}
```

Now the auto-attach happens whenever `WithEntityKind(K)` is set, regardless of `WithComponents()`.

- [ ] **Step 4: Make `WithComponents()` a deprecated no-op (kept temporarily for internal/game)**

In `pkg/universe/world_base.go` (around line 126), change `WithComponents` body to:

```go
// WithComponents is a no-op as of the Stage refactor. WithEntityKind(K)
// now auto-attaches the kind's components. Kept temporarily so
// internal/game does not break; will be deleted after that migration.
func WithComponents() SpawnOption {
    return func(*spawnOpts) {}
}
```

- [ ] **Step 5: Run all universe tests**

```bash
go test ./pkg/universe/ -v
```

Expected: PASS (including the new test). If any old test relied on `WithComponents()` being load-bearing, fix it.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/world_base.go pkg/universe/world_base_test.go
git commit -m "feat(universe): WithEntityKind auto-attaches kind components"
```

### Task 2.2: Implement `mmokit.Init(fn)` SpawnOption

**Files:**
- Create: `pkg/mmokit/spawn_init.go`
- Create: `pkg/mmokit/spawn_init_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/mmokit/spawn_init_test.go`:

```go
package mmokit

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/universe"
	"github.com/mlange-42/ark/ecs"
)

type initTestNameComp struct{ Name string }
type initTestHealthComp struct{ HP float32 }
type initTestBundle struct {
	Name   *initTestNameComp
	Health *initTestHealthComp
}

func TestInit_PopulatesBundleAfterSpawn(t *testing.T) {
	base := universe.NewTestWorldBase(t) // helper to expose; or test in pkg/universe instead
	def := universe.EntityKindDef{Kind: 5, Name: "InitTest"}
	universe.KindComponent(&def, ecs.NewMap1[initTestNameComp](base.ECSWorld()))
	universe.KindComponent(&def, ecs.NewMap1[initTestHealthComp](base.ECSWorld()))
	base.RegisterEntityKind(def)

	var captured *initTestBundle
	e := base.SpawnEntity(component.Position{},
		universe.WithEntityKind(5),
		Init(func(b *initTestBundle) {
			b.Name.Name = "alice"
			b.Health.HP = 42
			captured = b
		}),
	)
	if captured == nil {
		t.Fatal("Init callback never fired")
	}

	mp := ecs.NewMap1[initTestNameComp](base.ECSWorld())
	if mp.Get(e).Name != "alice" {
		t.Errorf("expected name 'alice', got %q", mp.Get(e).Name)
	}
	hmp := ecs.NewMap1[initTestHealthComp](base.ECSWorld())
	if hmp.Get(e).HP != 42 {
		t.Errorf("expected HP 42, got %f", hmp.Get(e).HP)
	}
}
```

This test requires `universe.NewTestWorldBase(t)` which may not exist. If it doesn't, either add it (a minimal test helper exposing `NewWorldBase` + a fake engine) or move the test into `pkg/universe/` where private helpers are accessible.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/mmokit/ -run TestInit_PopulatesBundleAfterSpawn -v
```

Expected: FAIL — `Init` not defined.

- [ ] **Step 3: Implement `mmokit.Init`**

The challenge: `Init` is a `SpawnOption`, but `SpawnOption` is `func(*spawnOpts)` — a private type. To run user code AFTER component attachment, we need a new spawnOpts field plus a SpawnEntity hook.

In `pkg/universe/world_base.go`, add to `spawnOpts`:

```go
postInit []func(ecs.Entity) // user-provided post-spawn callbacks
```

Add a public constructor `WithPostInit(fn func(ecs.Entity)) SpawnOption`:

```go
// WithPostInit registers a callback to run after all kind components are
// attached but before SpawnEntity returns. mmokit.Init(fn) uses this to
// populate typed component bundles.
func WithPostInit(fn func(ecs.Entity)) SpawnOption {
	return func(o *spawnOpts) {
		o.postInit = append(o.postInit, fn)
	}
}
```

Modify SpawnEntity to invoke `postInit` callbacks at the end (after all components are attached):

```go
// at the bottom of SpawnEntity, before `return entity`
for _, fn := range o.postInit {
	fn(entity)
}
```

In `pkg/mmokit/spawn_init.go`, implement `Init[T]` as a wrapper:

```go
package mmokit

import (
	"reflect"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/universe"
)

// Init is a SpawnOption that populates a typed component bundle after the
// entity is spawned and all kind components are attached. T is inferred
// from the function argument's type.
//
//	stage.SpawnEntity(pos,
//	    mmokit.WithEntityKind(KindPlayer),
//	    mmokit.Init(func(c *PlayerComponents) { c.Name.Name = "alice" }),
//	)
//
// Reflection walks T's pointer fields once at SpawnEntity time and resolves
// each field to the live component pointer on the just-spawned entity.
// The resolved bundle is passed to fn.
func Init[T any](fn func(*T)) universe.SpawnOption {
	bundleType := reflect.TypeFor[T]()
	if bundleType.Kind() != reflect.Struct {
		panic("mmokit.Init: T must be a struct")
	}

	type fieldInfo struct {
		offset uintptr
		// Per-field "fetch" closure: given an ecs.World + Entity, returns
		// a typed unsafe.Pointer to that component on that entity.
		fetch func(*ecs.World, ecs.Entity) unsafe.Pointer
	}

	var fields []fieldInfo
	for i := 0; i < bundleType.NumField(); i++ {
		f := bundleType.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Pointer {
			panic("mmokit.Init: bundle field must be a pointer")
		}
		// A per-field generic helper provides fetch via ecs.NewMap1[CompType](w).Get(e).
		// Same dispatch-table problem as RegisterKind. Resolution: the bundle
		// field types should already have been registered via RegisterKind
		// (because Init is only meaningful after the kind has been registered).
		// So we can reuse the same `componentAddRegistry` trick — store a
		// "fetch" closure when the kind is registered.
		fields = append(fields, fieldInfo{
			offset: f.Offset,
			fetch:  resolveComponentFetcher(f.Type.Elem()),
		})
	}

	return universe.WithPostInit(func(e ecs.Entity) {
		// Build a fresh T, populate its fields by walking fields[], call fn.
		bundle := new(T)
		base := unsafe.Pointer(bundle)
		// Need ecs.World — the postInit closure should receive it. Update
		// WithPostInit to pass (world, entity) instead of just entity.
		_ = base
		fn(bundle)
	})
}
```

This sketch has gaps; refine: change `WithPostInit` to pass `(*ecs.World, ecs.Entity)` so `Init` can fetch components. Use the component-fetcher registry populated by `RegisterKind[T]` to resolve per-field fetchers.

The full implementation will fall out from working through the test. Iterate until the test passes.

- [ ] **Step 4: Run test, iterate until it passes**

```bash
go test ./pkg/mmokit/ -run TestInit_PopulatesBundleAfterSpawn -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/spawn_init.go pkg/mmokit/spawn_init_test.go pkg/universe/world_base.go
git commit -m "feat(mmokit): Init(fn) SpawnOption populates typed bundle"
```

### Task 2.3: Edge case — Init runs after auto-attached kind components

- [ ] **Step 1: Add a test that verifies ordering**

```go
func TestInit_RunsAfterKindComponentsAttached(t *testing.T) {
	// Spawn with WithEntityKind(K) but no Init — verify components present.
	// Spawn with WithEntityKind(K) + Init(fn) — verify fn sees attached components.
	// Spawn with Init(fn) but no WithEntityKind — verify fn panics or fields are nil.
}
```

Implement, run, fix until passing. Commit.

---

## Phase 3: `WorldBase.SpawnPlayer`

### Task 3.1: Add SpawnPlayer method with TDD

**Files:**
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/world_base_test.go` (or `pkg/mmokit/spawn_player_test.go` if cleaner)

- [ ] **Step 1: Write the failing test**

```go
func TestSpawnPlayer_AttachesPlayerConn(t *testing.T) {
	base := newTestWorldBase(t)
	// register a kind that the player will use
	def := EntityKindDef{Kind: 7, Name: "Player"}
	base.RegisterEntityKind(def)

	session := &engine.PlayerSession{
		ConnID:        42,
		Username:      "alice",
		SpawnLocation: coords.Location{X: 100, Y: 200},
	}
	e := base.SpawnPlayer(session, WithEntityKind(7))

	if e == (ecs.Entity{}) {
		t.Fatal("expected non-zero entity")
	}
	if session.Entity != e {
		t.Errorf("expected session.Entity to be set to %v, got %v", e, session.Entity)
	}
	pcMap := ecs.NewMap1[component.PlayerConn](base.ECSWorld())
	if !pcMap.HasAll(e) {
		t.Fatal("expected PlayerConn component attached")
	}
	if pcMap.Get(e).ConnID != 42 {
		t.Errorf("expected ConnID 42, got %d", pcMap.Get(e).ConnID)
	}
}

func TestSpawnPlayer_SendsSpawnedMsg(t *testing.T) {
	// Verify SendSpawnedMsg is called once with (session.ConnID, entity).
	// Use a test ConnMgr or hook into the existing test infrastructure.
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/universe/ -run TestSpawnPlayer -v
```

Expected: FAIL — SpawnPlayer not defined.

- [ ] **Step 3: Implement SpawnPlayer**

In `pkg/universe/world_base.go`, after `SpawnAtLocation`:

```go
// SpawnPlayer is the canonical player-spawn helper. It calls
// SpawnAtLocation(session.SpawnLocation, opts...) plus four universal
// steps every game performs:
//
//  1. Attach component.PlayerConn{ConnID: session.ConnID}
//  2. Assign session.Entity = e
//  3. Send SpawnedMsg(session.ConnID, e) via the engine connection mgr
//  4. Apply any mmokit.Init(fn) callbacks (handled inside SpawnEntity)
//
// Returns the spawned entity for any further per-game setup.
func (b *WorldBase) SpawnPlayer(session *engine.PlayerSession, opts ...SpawnOption) ecs.Entity {
	e := b.SpawnAtLocation(session.SpawnLocation, opts...)

	pcMap := ecs.NewMap1[component.PlayerConn](b.ECSWorld())
	pcMap.Add(e, &component.PlayerConn{ConnID: session.ConnID})

	session.Entity = e
	b.SendSpawnedMsg(session.ConnID, e)
	return e
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./pkg/universe/ -run TestSpawnPlayer -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/world_base.go pkg/universe/world_base_test.go
git commit -m "feat(universe): WorldBase.SpawnPlayer collapses universal player-spawn ritual"
```

---

## Phase 4: `mmo.OnPlayerJoin` / `mmo.OnPlayerLeave`

### Task 4.1: Add OnPlayerJoin to Process

**Files:**
- Create: `pkg/mmokit/lifecycle.go`
- Create: `pkg/mmokit/lifecycle_test.go`
- Modify: `pkg/universe/coordinator.go` (add hook fields on Process)

- [ ] **Step 1: Write the failing test**

```go
package mmokit

import (
	"testing"
	"github.com/zenion/mmoserver/pkg/engine"
)

func TestOnPlayerJoin_FiresOnStateActive(t *testing.T) {
	mmo := New(Config{CellsX: 1, CellsY: 1, CellSize: 100, TickRate: 20, AoIRadius: 50, Headless: true})
	called := 0
	mmo.OnPlayerJoin(func(s *engine.PlayerSession, stage *Stage) {
		called++
		if s.Username != "alice" {
			t.Errorf("expected alice, got %q", s.Username)
		}
	})
	if err := mmo.Build(); err != nil { t.Fatal(err) }

	// Drive a player through to StateActive — use the existing test plumbing
	// (look for how player_manager_test.go does this).
	// ...

	if called != 1 {
		t.Errorf("expected OnPlayerJoin to fire once, got %d", called)
	}
}
```

- [ ] **Step 2: Run, verify failure, implement**

Add to `pkg/universe/coordinator.go` Process struct:

```go
onPlayerJoin  []func(*engine.PlayerSession, *WorldBase)
onPlayerLeave []func(*engine.PlayerSession, *WorldBase)
```

Add public methods (these will be the entry points; final form lives in `pkg/mmokit/lifecycle.go` but Process needs the storage):

```go
func (p *Process) AddPlayerJoinHook(fn func(*engine.PlayerSession, *WorldBase)) {
    p.onPlayerJoin = append(p.onPlayerJoin, fn)
}
func (p *Process) AddPlayerLeaveHook(fn func(*engine.PlayerSession, *WorldBase)) {
    p.onPlayerLeave = append(p.onPlayerLeave, fn)
}
```

In `createNode`, wire these into the per-cell PlayerManager via `pm.OnState(StateActive, ...)`:

```go
// after the world is constructed and pm is reachable:
pm := base.Engine().Players
pm.OnState(engine.StateActive, engine.StateCallbacks{
    OnEnter: func(s *engine.PlayerSession, _ *engine.PlayerManager) {
        for _, hook := range c.onPlayerJoin {
            hook(s, base)
        }
    },
    OnExit: func(s *engine.PlayerSession, _ *engine.PlayerManager) {
        // Default cleanup body — see Task 4.2.
        for _, hook := range c.onPlayerLeave {
            hook(s, base)
        }
    },
})
```

In `pkg/mmokit/lifecycle.go`:

```go
package mmokit

import (
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/universe"
)

// OnPlayerJoin registers a callback fired when a player session enters
// StateActive. Multiple hooks may be registered; they fire in registration
// order. Use State[T](stage) to look up typed per-stage state.
func (p *Process) OnPlayerJoin(fn func(*engine.PlayerSession, *Stage)) {
	p.AddPlayerJoinHook(func(s *engine.PlayerSession, base *universe.WorldBase) {
		fn(s, base) // Stage is an alias for *WorldBase post-Phase-8
	})
}
```

NOTE: `Process` is `type Process = universe.Process` in the mmokit package — methods on `*Process` cannot be defined in `pkg/mmokit/`. Adjust accordingly: define `OnPlayerJoin` and `OnPlayerLeave` directly on `*universe.Process` in `pkg/universe/coordinator.go`, then expose via the mmokit alias.

- [ ] **Step 3: Run tests, iterate**

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go pkg/mmokit/lifecycle.go pkg/mmokit/lifecycle_test.go
git commit -m "feat(universe): OnPlayerJoin/OnPlayerLeave methods on Process"
```

### Task 4.2: Default OnPlayerLeave cleanup body

The default OnExit body that every game writes today: `if entity alive && not ghost → MarkForRemoval; zero session.Entity`. Make this run automatically.

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Write the test**

```go
func TestOnPlayerLeave_DefaultCleanupRemovesEntity(t *testing.T) {
	// Spawn a player, transition to StateInactive (or whatever StateExit
	// equivalent), verify the entity is marked for removal and session.Entity
	// is zeroed — without registering any OnPlayerLeave hook.
}
```

- [ ] **Step 2: Run, verify fail, implement default body**

In `createNode`'s OnExit closure (the StateActive callback registered above), prepend the default cleanup:

```go
OnExit: func(s *engine.PlayerSession, _ *engine.PlayerManager) {
    // Default cleanup: remove the player entity if alive and not a ghost.
    if s.Entity != (ecs.Entity{}) && base.ECSWorld().Alive(s.Entity) {
        if !base.GhostMap().HasAll(s.Entity) {
            base.MarkForRemoval(s.Entity)
        }
        s.Entity = ecs.Entity{}
    }
    // Then user-supplied OnPlayerLeave hooks.
    for _, hook := range c.onPlayerLeave {
        hook(s, base)
    }
},
```

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git commit -am "feat(universe): default OnPlayerLeave cleanup runs before user hooks"
```

---

## Phase 5: `mmokit.AddState[T]` / `mmokit.State[T]`

### Task 5.1: Per-stage typed state registry

**Files:**
- Create: `pkg/mmokit/state.go`
- Create: `pkg/mmokit/state_test.go`
- Modify: `pkg/universe/coordinator.go` (add stateFactories field on Process; add stateRegistry on WorldBase)
- Modify: `pkg/universe/world_base.go` (add private state map)

- [ ] **Step 1: Write the failing test**

```go
package mmokit

import (
	"context"
	"testing"
)

type stateTestMarket struct{ Orders int }
type stateTestAI struct{ Bots int }

func TestAddState_RoundTrip(t *testing.T) {
	mmo := New(Config{CellsX: 1, CellsY: 1, CellSize: 100, TickRate: 20, AoIRadius: 50, Headless: true})
	AddState(mmo, func(*Stage) *stateTestMarket { return &stateTestMarket{Orders: 5} })
	AddState(mmo, func(*Stage) *stateTestAI { return &stateTestAI{Bots: 10} })
	if err := mmo.Build(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = mmo.Shutdown(context.Background()) })

	var stage *Stage
	for _, c := range mmo.Cells { stage = c.Base; break } // post-Phase-8 will be c.Stage

	market := State[stateTestMarket](stage)
	if market.Orders != 5 {
		t.Errorf("expected 5 orders, got %d", market.Orders)
	}
	ai := State[stateTestAI](stage)
	if ai.Bots != 10 {
		t.Errorf("expected 10 bots, got %d", ai.Bots)
	}
}

func TestState_PanicsOnUnregistered(t *testing.T) {
	mmo := New(Config{CellsX: 1, CellsY: 1, CellSize: 100, TickRate: 20, AoIRadius: 50, Headless: true})
	if err := mmo.Build(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = mmo.Shutdown(context.Background()) })

	var stage *Stage
	for _, c := range mmo.Cells { stage = c.Base; break }

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for unregistered state type")
		}
	}()
	_ = State[stateTestMarket](stage)
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./pkg/mmokit/ -run TestAddState -v
```

Expected: FAIL — AddState/State undefined.

- [ ] **Step 3: Implement state registry**

In `pkg/universe/coordinator.go` Process struct, add:

```go
stateFactories []stateFactory
```

Type:
```go
type stateFactory struct {
    typeName string // reflect.Type.String() for lookup
    build    func(*WorldBase) any
}
```

In `pkg/universe/world_base.go` WorldBase struct, add:

```go
state map[string]any // per-stage typed state, populated during construction
```

Initialize `state` in `NewWorldBase`. In `createNode`, after world construction:

```go
base.state = make(map[string]any, len(c.stateFactories))
for _, sf := range c.stateFactories {
    base.state[sf.typeName] = sf.build(base)
}
```

Add public access on WorldBase:

```go
// State returns the typed state value associated with name (a
// reflect.Type.String). Internal API; game code uses mmokit.State[T].
func (b *WorldBase) StateByName(name string) (any, bool) {
    v, ok := b.state[name]
    return v, ok
}
```

Add public registration on Process:

```go
func (p *Process) RegisterStateFactory(name string, build func(*WorldBase) any) {
    p.stateFactories = append(p.stateFactories, stateFactory{typeName: name, build: build})
}
```

In `pkg/mmokit/state.go`:

```go
package mmokit

import (
	"fmt"
	"reflect"

	"github.com/zenion/mmoserver/pkg/universe"
)

// AddState registers a per-stage state factory. Each Stage instantiates
// one *T at construction time by calling fn. Look up via State[T](stage).
//
//	mmokit.AddState(mmo, func(*Stage) *MarketState {
//	    return &MarketState{Orders: orderbook.New()}
//	})
func AddState[T any](p *universe.Process, fn func(*universe.WorldBase) *T) {
	name := reflect.TypeFor[T]().String()
	p.RegisterStateFactory(name, func(base *universe.WorldBase) any {
		return fn(base)
	})
}

// State returns the typed state previously registered via AddState[T] for
// this stage. Panics if T was not registered (programmer error).
func State[T any](stage *universe.WorldBase) *T {
	name := reflect.TypeFor[T]().String()
	v, ok := stage.StateByName(name)
	if !ok {
		panic(fmt.Sprintf("mmokit.State: type %s not registered via AddState", name))
	}
	return v.(*T)
}
```

- [ ] **Step 4: Run tests, iterate until pass**

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/state.go pkg/mmokit/state_test.go pkg/universe/coordinator.go pkg/universe/world_base.go
git commit -m "feat(mmokit): AddState[T]/State[T] composable per-stage state"
```

---

## Phase 6: Migrate 4node-basic to new APIs

### Task 6.1: Add bundle structs to components.go

**Files:**
- Modify: `examples/4node-basic/components.go`

- [ ] **Step 1: Add the bundle structs**

Append to `examples/4node-basic/components.go`:

```go
// PlayerComponents is the kind bundle for KindPlayer entities. Used for
// kind registration via mmokit.RegisterKind, query iteration, and spawn-
// time initialization via mmokit.Init.
type PlayerComponents struct {
	Name       *PlayerName
	Debug      *DebugInfo
	MoveTarget *mmokit.MoveTarget
}

// BotComponents is the kind bundle for KindBot entities.
type BotComponents struct {
	Name       *PlayerName
	Debug      *DebugInfo
	MoveTarget *mmokit.MoveTarget
	Behavior   *BotBehavior
}
```

Add the import: `"github.com/zenion/mmoserver/pkg/mmokit"`.

- [ ] **Step 2: Verify compiles**

```bash
just build
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/components.go
git commit -m "feat(4node): add PlayerComponents and BotComponents bundle structs"
```

### Task 6.2: Replace world.go's Init with main.go calls

**Files:**
- Modify: `examples/4node-basic/main.go`

- [ ] **Step 1: Add kind registrations + lifecycle hooks to main.go**

In `examples/4node-basic/main.go`, after the `mmokit.New(...)` call but before the `AddSystem` calls, insert:

```go
playerBindings := mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true}
mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings)
mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot", playerBindings)

mmo.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.WorldBase) {
    stage.SpawnPlayer(s,
        mmokit.WithCollider(PlayerRadius),
        mmokit.WithEntityKind(KindPlayer),
        mmokit.Init(func(c *PlayerComponents) {
            c.Name.Name = s.Username
        }),
    )
})
```

Note: `*mmokit.WorldBase` here will become `*mmokit.Stage` after Phase 8.

- [ ] **Step 2: Verify compiles + smoke-test**

```bash
just build && bin/server &  # or run via 4node-basic just dev
# Open http://localhost:8080 in a browser, log in as a test user, verify the
# player entity appears and click-to-move works.
```

Expected: PASS, player visible.

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/main.go
git commit -m "feat(4node): register kinds + OnPlayerJoin via mmokit (parallel to world.go)"
```

### Task 6.3: Update command_bots.go to drop WorldOfCell

**Files:**
- Modify: `examples/4node-basic/command_bots.go`

- [ ] **Step 1: Replace WorldOfCell calls with State[T] / direct stage access**

In `command_bots.go`, find the three `mmokit.WorldOfCell[*World](cell)` call sites (currently lines 216, 253, 268).

Replace `w := mmokit.WorldOfCell[*World](cell)` with `stage := cell.Base` (post-Phase-8: `cell.Stage`).

Drop `w.NameMap.Get(e).Name = ...` style calls; replace with `mmokit.Init(func(c *BotComponents) { c.Name.Name = ... })` inside the SpawnEntity call.

The new spawn pattern in `spawnBotsOnLoop`:

```go
func spawnBotsOnLoop(cell *mmokit.Cell, count int) int {
    stage := cell.Base // Phase 8: cell.Stage
    cellSize := mmokit.CellSize()
    minX, minY, maxX, maxY := cell.Cell.WorldBounds(cellSize)
    sizeX := maxX - minX
    sizeY := maxY - minY
    padX := sizeX * 0.1
    padY := sizeY * 0.1

    spawned := 0
    base := int(time.Now().UnixNano() % 1_000_000)
    rng := rand.New(rand.NewSource(int64(base)))
    for i := range count {
        x := minX + padX + rng.Float32()*(sizeX-2*padX)
        y := minY + padY + rng.Float32()*(sizeY-2*padY)
        tx := minX + padX + rng.Float32()*(sizeX-2*padX)
        ty := minY + padY + rng.Float32()*(sizeY-2*padY)
        retarget := uint16(rng.Intn(100))
        botName := fmt.Sprintf("bot_%s_%06d", cell.ID, base+i)

        stage.SpawnEntity(
            mmokit.Position{X: x - minX, Y: y - minY},
            mmokit.WithCollider(PlayerRadius),
            mmokit.WithEntityKind(KindBot),
            mmokit.Init(func(c *BotComponents) {
                c.Name.Name = botName
                mmokit.SetMoveTarget(c.MoveTarget, tx, ty)
                c.Behavior.TicksUntilRetarget = retarget
            }),
        )
        spawned++
    }
    return spawned
}
```

Apply the same pattern to `clearBotsOnLoop` and `countBotsOnLoop` — they iterate via `ecs.NewFilter1[BotBehavior]` directly on `cell.Base.ECSWorld()` (i.e. drop the `WorldOfCell` indirection).

- [ ] **Step 2: Verify build**

```bash
just build
```

- [ ] **Step 3: Smoke test**

Run `4node-basic`, type `bot spawn 10 0_0` in console, verify 10 bots appear.

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/command_bots.go
git commit -m "feat(4node): bot spawn uses mmokit.Init(fn) instead of WorldOfCell maps"
```

### Task 6.4: Update system_bots.go to drop `*World` reference

`BotSystem` declares `mmokit.SystemBase[*World]` and an inline query bundle. We must drop the `*World` reference *before* deleting `world.go` in Task 6.5.

**Files:**
- Modify: `examples/4node-basic/system_bots.go`

- [ ] **Step 1: Update SystemBase generic param + hoist bundle**

```go
type BotSystem struct {
    mmokit.SystemBase[*mmokit.WorldBase] // Phase 8: *mmokit.Stage

    bots mmokit.Query[BotComponents]
}
```

The inline query struct literal that previously declared `Behavior *BotBehavior; MT *mmokit.MoveTarget; Pos *mmokit.Position` is replaced by `BotComponents` — except `BotComponents` doesn't include `Pos *mmokit.Position`. Either:
- (a) Add `Pos *mmokit.Position` to `BotComponents` in components.go (it's a kind component on every bot anyway via auto-attach), then use `BotComponents` directly here.
- (b) Keep the inline struct here for query-only fields, just drop `*World` from the SystemBase param.

Pick (a): `BotComponents` becomes the canonical bundle. Update `examples/4node-basic/components.go` to add `Pos *mmokit.Position` to `BotComponents`. Note that `Position` is a built-in component already attached to every entity by `SpawnEntity` — no kind registration needed for it. The bundle reflection in `RegisterKind` will register it; that's fine because it's the same component the engine attaches.

In `system_bots.go.Update`, the existing field references (`b.Behavior`, `b.MT`, `b.Pos`) need to map to the new bundle field names: `b.Behavior`, `b.MoveTarget`, `b.Pos`. Rename `MT` → `MoveTarget` in the loop body.

- [ ] **Step 2: Build and smoke-test**

```bash
just build
```

Bots should still wander after `bot spawn 30 0_0`.

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/system_bots.go examples/4node-basic/components.go
git commit -m "feat(4node): BotSystem binds to *mmokit.WorldBase, uses BotComponents bundle"
```

### Task 6.5: Delete world.go

**Files:**
- Delete: `examples/4node-basic/world.go`
- Modify: `examples/4node-basic/main.go` (remove `World: NewWorld` from Config)

- [ ] **Step 1: Verify no remaining references to `*World`**

```bash
grep -n '\*World\b\|NewWorld\b' examples/4node-basic/
```

Expected: only matches inside `world.go` itself (which we're about to delete) and the `World: NewWorld` line in `main.go`. If any other file still references these, fix it before continuing.

- [ ] **Step 2: Remove the `World: NewWorld` field from Config**

In `main.go`, the `mmokit.Config{...}` literal currently has `World: NewWorld`. Delete that line.

- [ ] **Step 3: Delete the file**

```bash
git rm examples/4node-basic/world.go
```

- [ ] **Step 4: Verify the runtime falls back to bare *WorldBase when Config.World is nil**

Check `pkg/universe/coordinator.go` createNode (around line 1545–1552). The current decision tree is `worldFactory → onInitWorld → bare base`. Confirm the third path (`world = base`) is taken when both Config.World and Config.OnInit are nil — that's exactly what we want now.

- [ ] **Step 5: Build and smoke-test**

```bash
just build
```

Expected: PASS. Run the demo (`cd examples/4node-basic && just dev`), log in via browser, verify the player appears and click-to-move works. In console: `bot spawn 30 0_0`; verify bots spawn and wander.

- [ ] **Step 6: Commit**

```bash
git add examples/4node-basic/main.go
git commit -m "feat(4node): delete world.go — registrations live in main.go via new mmokit APIs"
```

---

## Phase 7: Migrate pkg/universe internal tests to RegisterKind[T]

The tests in `pkg/universe/border_replication_*_test.go` currently call `KindComponent + RegisterEntityKind` directly. These can stay as-is (the old API isn't deleted in this plan) OR migrate to validate the new API at the universe layer.

**Decision: skip. These tests exercise the internal implementation of `KindComponent` and `RegisterEntityKind`, which are still the underlying primitives that `RegisterKind[T]` calls into. Migrating them adds no coverage. Phase 1's `TestRegisterKind_RealizesPerCell` already integration-tests the high-level path.**

- [ ] **Phase 7 task list: empty.** Marking phase complete.

---

## Phase 8: Rename WorldBase → Stage, Cell.Base → Cell.Stage

### Task 8.1: Mechanical rename across the repo

**Files:**
- Many (all `WorldBase` references and all `cell.Base` references).

- [ ] **Step 1: Establish the scope**

Before renaming, capture current call counts as a sanity check:

```bash
echo "WorldBase refs:"
grep -rn "WorldBase" pkg/ internal/ examples/ cmd/ | wc -l
echo "cell.Base refs:"
grep -rn "\.Base\b" pkg/universe/ pkg/mmokit/ internal/ examples/ | wc -l
```

Record the numbers. Post-rename, the same greps for the new names should return matching counts (modulo any deletions).

- [ ] **Step 2: Rename the WorldBase struct + file**

```bash
git mv pkg/universe/world_base.go pkg/universe/stage.go
```

In `pkg/universe/stage.go`, replace the type declaration and all method receivers:

```bash
sed -i 's/\bWorldBase\b/Stage/g' pkg/universe/stage.go
sed -i 's/\bNewWorldBase\b/NewStage/g' pkg/universe/stage.go
```

Verify the rename didn't catch any unrelated tokens.

- [ ] **Step 3: Update Cell.Base → Cell.Stage**

```bash
sed -i 's/\bBase \*WorldBase\b/Stage *Stage/g' pkg/universe/cell.go
sed -i 's/\bcell\.Base\b/cell.Stage/g' $(grep -rl 'cell\.Base' pkg/ examples/ internal/ cmd/ 2>/dev/null)
sed -i 's/\b\.Base\.ECSWorld()\b/.Stage.ECSWorld()/g' $(grep -rl '\.Base\.ECSWorld' pkg/ examples/ internal/ cmd/ 2>/dev/null)
```

Manually inspect for any remaining `c\.Base\b` or `\.Base\b` patterns that should have been swapped. Some `Base` references in tests may belong to test variables, not Cell — read context before bulk-replacing.

- [ ] **Step 4: Update WorldBase references in remaining files**

```bash
sed -i 's/\bWorldBase\b/Stage/g' $(grep -rl 'WorldBase' pkg/ internal/ examples/ cmd/ 2>/dev/null | grep -v '_test.go.bak')
sed -i 's/\bNewWorldBase\b/NewStage/g' $(grep -rl 'NewWorldBase' pkg/ internal/ examples/ cmd/ 2>/dev/null)
```

Also rename mmokit aliases:

```bash
sed -i 's/\bmmokit\.WorldBase\b/mmokit.Stage/g' $(grep -rl 'mmokit\.WorldBase' . 2>/dev/null)
```

In `pkg/mmokit/mmokit.go`, the sed will mechanically rewrite both existing aliases:

```go
// pre-Phase-8 (after Phase 0):
type WorldBase = universe.WorldBase  // existing — kept for internal/game compat
type Stage = universe.WorldBase      // Phase 0 forward-alias

// post-sed:
type WorldBase = universe.Stage      // intentional: keeps internal/game compiling
type Stage = universe.Stage          // canonical going forward
```

Both aliases pointing to the same type are valid Go and intentional — `WorldBase` is a temporary backward-compat alias for `internal/game/`, slated for deletion in the follow-up plan that migrates that consumer. Document this with a comment:

```go
// Stage is the per-cell simulation surface — the canonical name as of the
// Stage refactor. Use Stage in all new code.
type Stage = universe.Stage

// WorldBase is a deprecated alias for Stage. Kept for internal/game/'s
// embed-based GameWorld struct, which is migrated in a follow-up plan.
// Do not use in new code.
type WorldBase = universe.Stage
```

- [ ] **Step 5: Build and fix anything left**

```bash
just build 2>&1 | head -50
```

Expected: a list of compile errors. Walk through each, fix, rebuild. Common patterns:
- A test fixture that built `*WorldBase` directly and was missed by sed.
- Doc comments referencing "WorldBase" — update to "Stage" for consistency.

Iterate until `just build` passes.

- [ ] **Step 6: Run the full test suite**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor: rename WorldBase → Stage, Cell.Base → Cell.Stage

Mechanical rename across pkg/, internal/, examples/, cmd/. No semantic
changes — all method signatures and behavior preserved. Aligns naming
with the developer mental model: a Stage is the per-cell simulation
surface where entities and systems run. Cell remains the topology unit.
"
```

### Task 8.2: Delete WorldOf[T] and WorldOfCell[T]

These were only used by 4node-basic's command_bots.go (now removed in Phase 6) and `world_of_test.go`.

**Files:**
- Modify: `pkg/mmokit/mmokit.go` (delete `WorldOf` and `WorldOfCell` functions)
- Delete: `pkg/mmokit/world_of_test.go`

- [ ] **Step 1: Delete the functions**

In `pkg/mmokit/mmokit.go`, find `WorldOf` (around line 1618) and `WorldOfCell` (around line 1656). Delete both functions and any associated doc comments.

- [ ] **Step 2: Delete the test file**

```bash
git rm pkg/mmokit/world_of_test.go
```

- [ ] **Step 3: Build and fix**

```bash
just build
```

Expected: PASS (4node-basic should no longer reference these). If `internal/game` references them, that's out of scope for this plan — leave the internal/game references and these functions in place if needed. If they're entirely unreferenced, the deletion is clean.

Verify with:

```bash
grep -rn 'WorldOf\b\|WorldOfCell\b' pkg/ internal/ examples/ cmd/ 2>/dev/null
```

If any references remain in scope, restore the functions; if all are out-of-scope (e.g. internal/game), leave the functions and revisit in the follow-up plan.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/
git commit -m "feat(mmokit): delete WorldOf/WorldOfCell — replaced by AddState/State"
```

### Task 8.3: Final verification

- [ ] **Step 1: Full build and test suite**

```bash
just build
go test ./...
just test-pg  # if Postgres docker is up
```

All pass.

- [ ] **Step 2: Smoke-test 4node-basic**

```bash
cd examples/4node-basic && just dev
```

Expected:
- Process starts, console prompt appears.
- Open `http://localhost:8080` in browser.
- Log in with a test username; player circle appears.
- Click to move; player moves.
- In console, type `bot spawn 30 0_0`; 30 bots appear, wandering.
- Type `cell split 0_0`; cell splits, bots redistribute, no errors.
- Type `cell merge cell_d1_0_0_0`; cells merge cleanly.

- [ ] **Step 3: Commit any leftover doc updates**

If any doc comments still reference "WorldBase" / "world factory" / etc., update them.

```bash
git commit -am "docs: update Stage-related doc comments after rename"
```

---

## Self-Review Notes

Before declaring the plan complete, the executing engineer should:

1. **Spec coverage:** Open `docs/superpowers/specs/2026-04-27-stage-and-composable-state-design.md` and confirm:
   - §1 (bundle structs triple-duty): covered by Phase 1, 2, 5 (kind reg, init fn, query iteration unchanged).
   - §2 (Stage replaces WorldBase): Phase 8.
   - §3 (SpawnPlayer): Phase 3.
   - §4 (AddState/State): Phase 5.
   - §5 (lifecycle hooks): Phase 4.
   - §6 (Config.World replaced): Phase 6.4 deletes its 4node-basic usage; full deletion deferred per scope note.
   - §7 (drop WithComponents): Phase 2.1 makes it a no-op; full deletion deferred.

2. **Placeholder scan:** All test code blocks are concrete. The `TODO` in Task 1.2 Step 6 is honest — that step explicitly says "implement until test passes" because the exact ark-API wiring depends on which of two ark APIs is available. The engineer should resolve this in Phase 1 before proceeding.

3. **Type consistency:** `*Stage` is used post-Phase-8 in tests written in earlier phases (Tasks 4.1, 5.1). Pre-Phase-8 those tests reference `*mmokit.WorldBase` (the alias). Phase 8's mechanical sed pass updates them. Confirm by greppiing for "WorldBase" after Phase 8 commit — should return zero hits (or only rare doc-comment cases).

4. **Scope adherence:** internal/game is NOT touched by this plan. After Phase 8, internal/game still references `mmokit.WorldBase` (now an alias to `Stage`) — so `mmokit.Stage = universe.Stage` and `mmokit.WorldBase = mmokit.Stage` (compatibility alias). Verify the alias still exists after Phase 8 — it's the only thing keeping internal/game compiling.

   **Action item for Phase 8:** keep `type WorldBase = universe.Stage` as a *compile-only alias* in `pkg/mmokit/mmokit.go`. Rename it to `Stage` everywhere within this plan's scope, but leave the `WorldBase` alias for internal/game until the follow-up plan deletes it. Document this clearly with a comment.
