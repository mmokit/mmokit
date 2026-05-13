# Entity Spawn API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `Stage.SpawnEntity(pos, opts...)` and its `WithX` option helpers with a single variadic-component method `Stage.Spawn(components ...any) Entity` that takes every component value at the call site and returns the rich `mmokit.Entity` wrapper.

**Architecture:** A new `Stage.Spawn` method walks the variadic `any` slice three times — once to find `Position` (required), once to find `EntityKind` (optional), once to dispatch each arg through a `sync.Map`-cached reflect.Type → attach-handler. The handler closure captures the typed `ecs.Map1[T]` and writes the value. Component handlers are built lazily on cache miss. After attach, the entity is registered in the netID index + spatial grid, and (under InvariantPanic) the kind's Bundle fields are checked against the attached set. Old API kept across Phases 1–3, deleted in Phase 4. Spec: [docs/superpowers/specs/2026-05-13-entity-spawn-api-design.md](docs/superpowers/specs/2026-05-13-entity-spawn-api-design.md).

**Tech Stack:** Go 1.22+ generics, ark/ecs v0.7.1, `reflect`, `sync.Map`, existing mmokit/universe wiring.

---

## File Structure

**New files:**
- `pkg/universe/spawn.go` — new `Stage.Spawn` method, component-attach handler cache, invariant check
- `pkg/universe/spawn_test.go` — unit tests for `Stage.Spawn` (position-required, duplicate-component, missing-required, return-type, all-attached)

**Modified files (in order):**
- `pkg/universe/stage.go` — `Stage.SpawnPlayer` signature change (Phase 1); deletion of `SpawnEntity` + `WithX` helpers (Phase 4); rewrite of `SpawnAtLocation` (Phase 4)
- `pkg/mmokit/mmokit.go` — re-export update (Phase 1, Phase 4 cleanup)
- `pkg/mmokit/spawn.go` — delete free function `mmokit.Spawn` (Phase 3)
- `pkg/mmokit/spawn_init.go` — delete `mmokit.Init` (Phase 3)
- `internal/game/entity_lootcrate.go` — migrate `SpawnLootCrate` (Phase 2)
- `internal/game/entity_station.go` — migrate `SpawnStation` (Phase 2)
- `internal/game/entity_asteroid.go` — migrate `spawnAsteroidWithItem` (Phase 2)
- `internal/game/entity_npc.go` — migrate `SpawnNPC` (Phase 2)
- `internal/game/entity_poi.go` — migrate `SpawnPOI` (Phase 2)
- `internal/game/entity_ship.go` — migrate `gw.SpawnPlayer` (Phase 2)
- `pkg/universe/builtins_entity.go` — migrate the entity-spawn console verb (Phase 3)
- `pkg/universe/stage_test.go` — update `Stage.SpawnPlayer` test (Phase 1)
- `examples/4node-basic/main.go`, `examples/4node-basic/command_bots.go`, `examples/simple/system_sinewave.go` — migrate example callers (Phase 3)
- Eleven `pkg/mmokit/*_test.go` files that use `mmokit.Spawn` (Phase 3)
- `internal/game/commands/npc_spawn.go`, `internal/game/system_targetlock_multi_test.go` — adjust return-type-changed consumers (Phase 2)
- `CLAUDE.md` — Phase 5 docs

**Why this structure:** `Stage.Spawn` is new behavior that deserves its own file. The handler cache and reflection plumbing keep `stage.go` from growing further (it's already 1700+ lines). Tests for the new API also live in `spawn_test.go` so they move together. All other changes are call-site migrations in their existing files — no new files needed for those.

---

## Phase 1 — Land the new API

### Task 1: Add Stage.Spawn skeleton + component handler cache

**Files:**
- Create: `pkg/universe/spawn.go`
- Create: `pkg/universe/spawn_test.go`

- [ ] **Step 1: Write the failing test (basic spawn returns Entity with components attached)**

Create `pkg/universe/spawn_test.go`:

```go
package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

func newTestStage(t *testing.T) *Stage {
	t.Helper()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), logger.New())
	return NewStage(eng, CellID{X: 0, Y: 0}, 100, nil)
}

// TestSpawn_AttachesEveryComponent verifies that Stage.Spawn attaches each
// variadic component value to the entity and returns a usable Entity wrapper.
func TestSpawn_AttachesEveryComponent(t *testing.T) {
	stage := newTestStage(t)

	type marker struct{ Tag uint8 }
	_ = ecs.NewMap1[marker](stage.ECSWorld()) // pre-register

	e := stage.Spawn(
		component.Position{X: 100, Y: 200},
		component.Collider{Radius: 5, Layer: 1},
		marker{Tag: 42},
	)

	if e.NetID() == 0 {
		t.Fatal("expected non-zero netID on spawned entity")
	}
	if !e.Alive() {
		t.Fatal("expected spawned entity to be Alive")
	}
	posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
	if !posMap.HasAll(e.Handle()) {
		t.Fatal("expected Position attached")
	}
	if p := posMap.Get(e.Handle()); p.X != 100 || p.Y != 200 {
		t.Errorf("expected Position{100,200}, got {%v,%v}", p.X, p.Y)
	}
	colMap := ecs.NewMap1[component.Collider](stage.ECSWorld())
	if !colMap.HasAll(e.Handle()) {
		t.Fatal("expected Collider attached")
	}
	if c := colMap.Get(e.Handle()); c.Radius != 5 || c.Layer != 1 {
		t.Errorf("Collider not copied verbatim: %+v", c)
	}
	mkMap := ecs.NewMap1[marker](stage.ECSWorld())
	if !mkMap.HasAll(e.Handle()) || mkMap.Get(e.Handle()).Tag != 42 {
		t.Errorf("marker component missing or wrong: %+v", mkMap.Get(e.Handle()))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd . && go test -run TestSpawn_AttachesEveryComponent ./pkg/universe/ -v`
Expected: FAIL with `stage.Spawn undefined` (compile error).

- [ ] **Step 3: Write minimal implementation**

Create `pkg/universe/spawn.go`:

```go
package universe

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// componentAttachHandlers caches per-reflect.Type closures that attach a
// component value to an entity through ark's typed maps. Populated lazily
// on cache miss; once warm, every spawn hits the indirect-call fast path.
var componentAttachHandlers sync.Map // map[reflect.Type]attachFn

// attachFn copies the component value v onto entity in stage's ECS world.
type attachFn func(stage *Stage, entity ecs.Entity, v any)

// Spawn creates an entity carrying the given components. The framework
// walks the variadic args, dispatches each by Go type, and attaches it
// to the new entity. Components must be passed by VALUE (not pointer).
//
// Position must be present — Spawn panics if not. Every entity
// participates in spatial indexing, which requires a known position.
//
// The same component type passed twice is a programmer error; Spawn
// panics. Order of args has no semantic effect.
//
// Returns the rich Entity wrapper, not the raw ecs.Entity handle.
func (b *Stage) Spawn(components ...any) Entity {
	var (
		pos     component.Position
		hasPos  bool
		kind    component.EntityKind
		hasKind bool
	)
	seen := make(map[reflect.Type]struct{}, len(components))
	for _, c := range components {
		t := reflect.TypeOf(c)
		if _, dup := seen[t]; dup {
			panic(fmt.Sprintf("universe.Stage.Spawn: component %s passed twice", t.String()))
		}
		seen[t] = struct{}{}
		switch v := c.(type) {
		case component.Position:
			pos = v
			hasPos = true
		case component.EntityKind:
			kind = v
			hasKind = true
		}
	}
	if !hasPos {
		panic("universe.Stage.Spawn: Position component is required")
	}

	w := b.ECSWorld()
	entity := w.NewEntity()
	nid := b.eng.NextNetID()

	for _, c := range components {
		t := reflect.TypeOf(c)
		fn := loadOrBuildAttachFn(t)
		fn(b, entity, c)
	}

	// NetworkID is always assigned by the framework; not a user-supplied
	// component on Spawn. Add it after user components so it can't collide.
	netIDMap := ecs.NewMap1[component.NetworkID](w)
	netIDMap.Add(entity, &component.NetworkID{ID: nid})
	cellMap := ecs.NewMap1[component.CellCoord](w)
	cellMap.Add(entity, &component.CellCoord{CellX: b.rootCell().X, CellY: b.rootCell().Y})

	if hasKind && b.spatialGrid != nil {
		colMap := ecs.NewMap1[component.Collider](w)
		var radius float32
		if colMap.HasAll(entity) {
			radius = colMap.Get(entity).Radius
		}
		b.spatialGrid.Register(spatial.Entry{
			Entity: entity,
			X:      pos.X,
			Y:      pos.Y,
			Radius: radius,
		})
	}
	_ = kind // intentionally unused in this task; invariant check lands in Task 2

	if b.netIDIdx != nil && nid != 0 {
		b.netIDIdx.Enter(nid, entity, PresenceLive)
	}

	b.eng.Log.Log(CatMeshCell, "[%s] spawned entity netID=%d at (%.0f,%.0f)", b.cellID, nid, pos.X, pos.Y)
	return EntityFromECS(b, entity)
}

// loadOrBuildAttachFn returns the cached attach handler for t, building it on cache miss.
func loadOrBuildAttachFn(t reflect.Type) attachFn {
	if v, ok := componentAttachHandlers.Load(t); ok {
		return v.(attachFn)
	}
	fn := buildAttachFn(t)
	actual, _ := componentAttachHandlers.LoadOrStore(t, fn)
	return actual.(attachFn)
}

// buildAttachFn reflects on the component type once and returns a closure
// that uses world.Unsafe() to attach the value. We can't materialize
// ecs.Map1[T] generically here, so the closure routes through Unsafe.
func buildAttachFn(t reflect.Type) attachFn {
	if t.Kind() == reflect.Pointer {
		panic(fmt.Sprintf("universe.Stage.Spawn: component %s must be passed by value, not pointer", t.String()))
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("universe.Stage.Spawn: component %s must be a struct, got %v", t.String(), t.Kind()))
	}
	return func(stage *Stage, e ecs.Entity, v any) {
		w := stage.ECSWorld()
		u := w.Unsafe()
		id := ecs.TypeID(w, t)
		if !u.Has(e, id) {
			u.Add(e, id)
		}
		ptr := u.Get(e, id)
		reflect.NewAt(t, unsafe.Pointer(ptr)).Elem().Set(reflect.ValueOf(v))
	}
}
```

**Entity currently lives in `pkg/mmokit/entity.go`.** It must move down to `pkg/universe/entity.go` so `Stage.Spawn` (which lives in universe) can return it without an import cycle. Verified-safe move:

- `pkg/mmokit/entity.go` defines: `Entity` struct + `EntityByNetID`, `EntityFromECS` constructors + methods `NetID`, `Alive`, `resolveHandle`, `Pos`, `Local`, `String`, `Stage`, `Handle` + an `init()` wire-codec registration. All of these use only `pkguniverse.Stage`, `ecs.Entity`, and `component.*` — universe-level types. They move cleanly.
- `pkg/mmokit/messaging.go:100` defines `Entity.Send(msg any)`. Body uses `e.stage.RouteTypedMessage(...)` which already lives in universe. Move this method to `pkg/universe/entity.go` alongside the others.

**After moving, in `pkg/mmokit/entity.go`** (now near-empty), replace with type-alias re-exports:

```go
package mmokit

import "github.com/zenion/mmoserver/pkg/universe"

// Entity is the rich game-facing handle. Value type, cheap to pass.
// Methods are safe on zero/dead entities — they return zero values, never panic.
type Entity = universe.Entity

// EntityByNetID constructs an Entity bound to the given stage, resolving the
// local ECS handle on first method call.
var EntityByNetID = universe.EntityByNetID

// EntityFromECS wraps an ecs.Entity into an Entity by reading its NetworkID
// component. Returns zero-value Entity if the handle is not alive or has no NetworkID.
var EntityFromECS = universe.EntityFromECS
```

Delete the old `pkg/mmokit/messaging.go:100` `func (e Entity) Send(msg any)` — it now lives in universe.

`Stage.Spawn`'s return uses `EntityFromECS(b, entity)` (the now-universe version).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd . && go test -run TestSpawn_AttachesEveryComponent ./pkg/universe/ -v && go vet ./...`
Expected: PASS; no vet errors.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/spawn.go pkg/universe/spawn_test.go pkg/universe/entity.go pkg/mmokit/entity.go
git commit -m "feat(universe): add Stage.Spawn(components ...any) method"
```

---

### Task 2: Position-required + duplicate-component panic tests

**Files:**
- Modify: `pkg/universe/spawn_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/universe/spawn_test.go`:

```go
// TestSpawn_PanicsWithoutPosition verifies that omitting Position is
// a programmer error caught at call time.
func TestSpawn_PanicsWithoutPosition(t *testing.T) {
	stage := newTestStage(t)
	type marker struct{}
	_ = ecs.NewMap1[marker](stage.ECSWorld())

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when Position is missing")
		}
		msg, _ := r.(string)
		if msg == "" {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
	}()

	stage.Spawn(marker{})
}

// TestSpawn_PanicsOnDuplicateComponentType verifies that passing the same
// component type twice is a programmer error caught at call time.
func TestSpawn_PanicsOnDuplicateComponentType(t *testing.T) {
	stage := newTestStage(t)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when same component type passed twice")
		}
	}()

	stage.Spawn(
		component.Position{X: 1, Y: 2},
		component.Position{X: 3, Y: 4},
	)
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `cd . && go test -run 'TestSpawn_PanicsWithoutPosition|TestSpawn_PanicsOnDuplicateComponentType' ./pkg/universe/ -v`
Expected: PASS (the panic paths were already coded in Task 1's implementation).

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/spawn_test.go
git commit -m "test(universe): cover Spawn panic paths (missing Position, duplicate component)"
```

---

### Task 3: Invariant check for kinded spawns

**Files:**
- Modify: `pkg/universe/spawn.go`
- Modify: `pkg/universe/spawn_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/spawn_test.go`:

```go
// TestSpawn_PanicsOnMissingRequiredKindComponent verifies that under
// InvariantPanic mode, kinded spawns missing a required Bundle component
// fail loudly at spawn time.
func TestSpawn_PanicsOnMissingRequiredKindComponent(t *testing.T) {
	stage := newTestStage(t)
	if stage.coord == nil {
		stage.coord = &Process{invariantMode: InvariantPanic}
	} else {
		stage.coord.invariantMode = InvariantPanic
	}

	type health struct{ HP int32 }
	_ = ecs.NewMap1[health](stage.ECSWorld())

	// Register a kind that requires `health`, but spawn without it.
	w := stage.ECSWorld()
	def := EntityKindDef{Kind: 99, Name: "TestKind"}
	id := ecs.TypeID(w, reflect.TypeOf(health{}))
	KindComponentByID(&def, w, id, reflect.TypeOf(health{}), false)
	stage.RegisterEntityKind(def)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when kinded spawn omits required component")
		}
	}()

	stage.Spawn(
		component.Position{},
		component.EntityKind{Type: 99},
		// missing health
	)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd . && go test -run TestSpawn_PanicsOnMissingRequiredKindComponent ./pkg/universe/ -v`
Expected: FAIL — the spawn currently succeeds because the invariant check is not yet wired.

- [ ] **Step 3: Wire the invariant check**

Edit `pkg/universe/spawn.go`, replace the `_ = kind // intentionally unused...` line with:

```go
	if hasKind && b.coord != nil && b.coord.invariantMode == InvariantPanic {
		if def, ok := b.entityKinds[kind.Type]; ok {
			missing := findMissingRequiredComponents(b.ECSWorld(), entity, def, seen)
			if len(missing) > 0 {
				panic(fmt.Sprintf("universe.Stage.Spawn: kind %q (%d) missing required components: %v",
					def.Name, def.Kind, missing))
			}
		}
	}
```

And add at the bottom of `spawn.go`:

```go
// findMissingRequiredComponents returns the names of Bundle fields registered
// on the kind that are not present in `seen` (the set of component types
// passed to Spawn). Skips fields tagged `mmokit:"local"` — those are
// transfer-local and not user-required.
func findMissingRequiredComponents(
	w *ecs.World, entity ecs.Entity, def *EntityKindDef, seen map[reflect.Type]struct{},
) []string {
	var missing []string
	u := w.Unsafe()
	for _, c := range def.requiredFieldTypes() {
		if _, ok := seen[c]; ok {
			continue
		}
		id := ecs.TypeID(w, c)
		if u.Has(entity, id) {
			continue
		}
		missing = append(missing, c.Name())
	}
	return missing
}
```

This requires `EntityKindDef.requiredFieldTypes() []reflect.Type` — add it to `pkg/universe/entity_kind.go`:

```go
// requiredFieldTypes returns the reflect.Types of every non-local Bundle
// field this kind declares — the set Spawn's debug invariant uses to
// verify that callers attached every required component.
func (def *EntityKindDef) requiredFieldTypes() []reflect.Type {
	return def.requiredTypes // populated by KindComponentByID below
}
```

And update `KindComponentByID` (`pkg/universe/entity_kind.go`) to capture `t reflect.Type` into a new `requiredTypes []reflect.Type` field on `EntityKindDef` (only when `localOnly == false`).

`EntityKindDef` struct (`pkg/universe/entity_kind.go`):
```go
type EntityKindDef struct {
	Kind            uint8
	Name            string
	components      []kindComponent
	requiredTypes   []reflect.Type // populated by KindComponentByID for non-local fields
	NetworkBindings []system.ComponentBinding
}
```

`KindComponentByID`, append after building `kc`:
```go
	def.components = append(def.components, kc)
	if !localOnly {
		def.requiredTypes = append(def.requiredTypes, t)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd . && go test -run TestSpawn_PanicsOnMissingRequiredKindComponent ./pkg/universe/ -v && go vet ./...`
Expected: PASS; no vet errors.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/spawn.go pkg/universe/spawn_test.go pkg/universe/entity_kind.go
git commit -m "feat(universe): Spawn panics on missing required kind component under InvariantPanic"
```

---

### Task 4: Replace Stage.SpawnPlayer signature

**Why now:** `Stage.SpawnPlayer(session, components ...any) Entity` collides on method name with the existing `Stage.SpawnPlayer(session, opts ...SpawnOption) ecs.Entity`. Go has no method overloading. The cleanest path is to replace in place and update its two callers in the same commit. Doing this in Phase 1 keeps the new API consistent (both `Spawn` and `SpawnPlayer` use the new arg shape from day one).

**Files:**
- Modify: `pkg/universe/stage.go` (the `SpawnPlayer` method, ~line 1653)
- Modify: `pkg/universe/stage_test.go` (line 157)
- Modify: `examples/4node-basic/main.go` (line 79)

- [ ] **Step 1: Update SpawnPlayer in pkg/universe/stage.go**

Replace the existing `Stage.SpawnPlayer` (line 1653) with:

```go
// SpawnPlayer is the canonical player-spawn helper. It performs four universal
// steps that every game's OnEnter handler needs:
//
//  1. Resolve session.SpawnLocation to cell-local coordinates and inject as Position.
//  2. Call Spawn(components...) with the resolved Position + caller-supplied components.
//  3. Attach component.PlayerConn{ConnID: session.ConnID} (caller does not pass it).
//  4. Assign session.Entity = e.Handle().
//  5. Call SendPlayerEntityAssigned(session.ConnID, e.Handle()) to notify the client.
//
// When session.ConnID is 0, SendPlayerEntityAssigned is a safe no-op.
//
// Caller-supplied components must NOT include Position (set from session.SpawnLocation)
// or PlayerConn (set from session.ConnID). Spawn panics on duplicate-type.
func (b *Stage) SpawnPlayer(session *engine.PlayerSession, components ...any) Entity {
	rootCell := b.rootCell()
	cellSize := coords.CellSize
	minX := float32(rootCell.X) * cellSize
	minY := float32(rootCell.Y) * cellSize

	pos := component.Position{
		X: session.SpawnLocation.X - minX,
		Y: session.SpawnLocation.Y - minY,
	}

	args := make([]any, 0, len(components)+2)
	args = append(args, pos, component.PlayerConn{ConnID: session.ConnID})
	args = append(args, components...)

	e := b.Spawn(args...)
	session.Entity = e.Handle()
	b.SendPlayerEntityAssigned(session.ConnID, e.Handle())
	return e
}
```

- [ ] **Step 2: Update pkg/universe/stage_test.go (line 157)**

Replace:
```go
	e := base.SpawnPlayer(session, WithEntityKind(7))

	if e == (ecs.Entity{}) {
		t.Fatal("expected non-zero entity")
	}
	if session.Entity != e {
		t.Errorf("expected session.Entity to be set to %v, got %v", e, session.Entity)
	}
```

With:
```go
	e := base.SpawnPlayer(session, component.EntityKind{Type: 7})

	if e.NetID() == 0 {
		t.Fatal("expected non-zero entity")
	}
	if session.Entity != e.Handle() {
		t.Errorf("expected session.Entity to be set to %v, got %v", e.Handle(), session.Entity)
	}
```

Update the rest of the test body to use `e.Handle()` wherever `e` was previously typed as `ecs.Entity`.

- [ ] **Step 3: Update examples/4node-basic/main.go (line 79)**

Replace:
```go
		stage.SpawnPlayer(session,
			mmokit.WithCollider(PlayerRadius),
			mmokit.WithEntityKind(KindPlayer),
			mmokit.Init(func(c *PlayerComponents) {
				c.Name.Name = session.Username
			}),
		)
```

With:
```go
		stage.SpawnPlayer(session,
			mmokit.EntityKind{Type: KindPlayer},
			mmokit.Collider{Radius: PlayerRadius},
			Name{Name: session.Username},
			// Add any other PlayerComponents fields (PlayerHealth, etc.) here as
			// concrete values — Phase 1 deletes the mmokit.Init bundle pattern.
		)
```

Replace `Name{Name: ...}` with whatever the actual component type is in the example's PlayerComponents bundle. **Before writing**, read `examples/4node-basic/main.go` and find the `PlayerComponents` struct definition to enumerate every field — pass them all by value to keep behavior identical.

- [ ] **Step 4: Compile + test**

Run: `cd . && go build ./... && go test ./pkg/universe/ -v -run SpawnPlayer && go test ./examples/4node-basic/...`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/stage.go pkg/universe/stage_test.go examples/4node-basic/main.go
git commit -m "refactor(universe): Stage.SpawnPlayer takes variadic components, returns Entity"
```

---

### Task 5: Mmokit re-exports for new API + tests pass

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

The new `Stage.Spawn` is a method, so it's automatically usable as `stage.Spawn(...)` through the mmokit `Stage = universe.Stage` alias. No new top-level re-exports needed. However, the existing `WithCollider`/`WithEntityKind`/etc. re-exports still live; we leave them in place until Phase 4.

Game code will now pass component values like `mmokit.Position{...}`, `mmokit.Collider{...}`, `mmokit.EntityKind{Type: ...}`. Confirm these types are already mmokit-exported:

- [ ] **Step 1: Verify needed types are re-exported**

Run: `cd . && grep -E "^\s*(Position|Collider|EntityKind|Rotation|Velocity)\s*=" pkg/mmokit/mmokit.go pkg/mmokit/components.go`
Expected: each appears as a type alias to its component package.

If `EntityKind` is missing as a re-exported type alias, add it to `pkg/mmokit/components.go`:

```go
// EntityKind is the framework component that tags an entity's type.
// Pass by value to Stage.Spawn(...).
type EntityKind = component.EntityKind
```

- [ ] **Step 2: Run full test suite as a smoke check**

Run: `cd . && just build && go test ./pkg/... ./internal/...`
Expected: all green (Phase 1 is additive — no existing behavior changes).

- [ ] **Step 3: Commit if anything was added**

```bash
git add pkg/mmokit/components.go
git commit -m "feat(mmokit): re-export EntityKind for Stage.Spawn callers"
```

(Skip the commit if nothing changed.)

---

## Phase 2 — Migrate game-side spawn helpers

**Migration order (simplest → most complex):**
LootCrate → Station → Asteroid → NPC → POI → Player ship

Each task is a behavior-preserving refactor. After each task, both `go test ./internal/game/...` and `just build` must stay green. The helper's return type may change from `mmokit.EntityHandle` (or void) to `mmokit.Entity` (or void) — propagate any necessary call-site updates in the same commit.

### Task 6: Migrate SpawnLootCrate

**Files:**
- Modify: `internal/game/entity_lootcrate.go`

- [ ] **Step 1: Rewrite SpawnLootCrate**

Replace lines 20–35:

```go
// SpawnLootCrate creates a loot crate entity with the given cargo.
func (gw *GameWorld) SpawnLootCrate(x, y float32, items map[uint32]int32) {
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindLootCrate},
		mmokit.Collider{Radius: gw.Config.LootCrateRadius},
		gamecomp.Inventory{Items: items, MaxMass: math.MaxFloat32},
		mmokit.Lifetime{Remaining: gw.Config.LootCrateLifetime},
		gamecomp.LootCrate{},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", e.NetID(), x, y)
}
```

- [ ] **Step 2: Run tests**

Run: `cd . && go test ./internal/game/... -run LootCrate -v && go vet ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/game/entity_lootcrate.go
git commit -m "refactor(game): migrate SpawnLootCrate to Stage.Spawn"
```

---

### Task 7: Migrate SpawnStation

**Files:**
- Modify: `internal/game/entity_station.go`

- [ ] **Step 1: Rewrite SpawnStation**

Replace lines 28–37:

```go
// SpawnStation creates the trade station entity in the station cell.
func (gw *GameWorld) SpawnStation() {
	e := gw.stage.Spawn(
		mmokit.Position{X: StationLocalX, Y: StationLocalY},
		mmokit.EntityKind{Type: gamecomp.KindStation},
		mmokit.Collider{Radius: gw.Config.StationRadius},
		gamecomp.Station{},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "station spawned: netID=%d pos=(%.1f,%.1f)", e.NetID(), StationLocalX, StationLocalY)
}
```

- [ ] **Step 2: Run tests**

Run: `cd . && go test ./internal/game/... && go vet ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/game/entity_station.go
git commit -m "refactor(game): migrate SpawnStation to Stage.Spawn"
```

---

### Task 8: Migrate spawnAsteroidWithItem

**Files:**
- Modify: `internal/game/entity_asteroid.go`

- [ ] **Step 1: Rewrite spawnAsteroidWithItem**

Replace lines 64–88:

```go
func (gw *GameWorld) spawnAsteroidWithItem(x, y float32, itemID uint32) {
	radius := gw.Config.AsteroidMinRadius + rand.Float32()*(gw.Config.AsteroidMaxRadius-gw.Config.AsteroidMinRadius)

	layer := gamecomp.LayerTerrain
	if def := item.Get(itemID); def != nil && def.Gaseous {
		layer = 0
	}

	gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindAsteroid},
		mmokit.Collider{Radius: radius, Layer: layer},
		mmokit.Rotation{Angle: rand.Float32() * 2 * math.Pi},
		gamecomp.Minable{ItemID: itemID, Remaining: radius * 5},
	)
}
```

- [ ] **Step 2: Run tests**

Run: `cd . && go test ./internal/game/... && go vet ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/game/entity_asteroid.go
git commit -m "refactor(game): migrate spawnAsteroidWithItem to Stage.Spawn"
```

---

### Task 9: Migrate SpawnNPC

**Return type changes** from `mmokit.EntityHandle` to `mmokit.Entity`. Update consumers in the same commit:

- `internal/game/commands/npc_spawn.go:67` — `handle := gw.SpawnNPC(...)` then `e := mmokit.EntityFromECS(cell.Stage, handle)` → just `e := gw.SpawnNPC(...)`.
- `internal/game/system_targetlock_multi_test.go:60-61, 90, 97, 130` — `a := gw.SpawnNPC(...)` followed by `TargetEntity: a` (where TargetEntity is `ecs.Entity`) → `TargetEntity: a.Handle()`. Same for `netIDOfECS(gw, a)` → `netIDOfECS(gw, a.Handle())`.
- `internal/game/system_npc_ai_test.go:72, 90, 117, 138` — `npc := gw.SpawnNPC(...)` followed by `mmokit.EntityFromECS(gw.stage, npc)` → just use `npc` directly (it's already `mmokit.Entity`).
- `internal/game/entity_poi.go:64` — `gw.SpawnNPC(...)` called for side effect, return value unused: no change needed.

**Files:**
- Modify: `internal/game/entity_npc.go`
- Modify: `internal/game/commands/npc_spawn.go` (1 call site)
- Modify: `internal/game/system_targetlock_multi_test.go` (4 call sites)
- Modify: `internal/game/system_npc_ai_test.go` (4 call sites)

- [ ] **Step 1: Rewrite SpawnNPC**

Replace `internal/game/entity_npc.go` lines 28–78:

```go
// SpawnNPC creates an NPC ship of the given archetype, anchored at the
// given local position with anchor link to poiNetID. Pass poiNetID=0
// for console-spawned test NPCs (they leash to their spawn position
// when no POI exists).
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32) mmokit.Entity {
	d := archetypeDefaults(gw.Config, archetype)

	components := []any{
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindNPC},
		mmokit.Collider{
			Width:  gw.Config.NpcWidth,
			Height: gw.Config.NpcHeight,
			Layer:  gamecomp.LayerPlayer,
			Shape:  mmokit.ShapeRect,
			Radius: boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight),
		},
		mmokit.Rotation{},
		gamecomp.Health{Current: d.HP, Max: d.HP},
		gamecomp.Shield{
			Current:    d.Shield,
			Max:        d.Shield,
			RegenRate:  gw.Config.NpcShieldRegenRate,
			RegenDelay: gw.Config.NpcShieldRegenDelay,
		},
		gamecomp.StatusEffects{},
		gamecomp.TargetLock{
			MaxSlots: gw.Config.LockMaxSlotsNPC,
			Range:    d.AggroRadius,
		},
		gamecomp.NPCAI{
			Archetype:      archetype,
			State:          AIStateIdle,
			MaxSpeed:       d.MaxSpeed,
			TurnRate:       d.TurnRate,
			PreferredRange: d.PreferredRange,
			WeaponRange:    d.WeaponRange,
			AggroRadius:    d.AggroRadius,
			MotionPolicy:   d.MotionPolicy,
			DamagePerShot:  d.DamagePerShot,
			FireRate:       d.FireRate,
		},
	}
	if poiNetID != 0 {
		components = append(components, gamecomp.POIAnchor{POINetID: poiNetID})
	}
	e := gw.stage.Spawn(components...)
	gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d archetype=%d pos=(%.0f,%.0f) anchor=%d",
		e.NetID(), archetype, x, y, poiNetID)
	return e
}
```

- [ ] **Step 2: Update call sites**

`internal/game/commands/npc_spawn.go` line 67, replace:
```go
				handle := gw.SpawnNPC(localX, localY, archetype, 0)
				e := mmokit.EntityFromECS(cell.Stage, handle)
				return NPCSpawnResult{
					NetID:     e.NetID(),
```
With:
```go
				e := gw.SpawnNPC(localX, localY, archetype, 0)
				return NPCSpawnResult{
					NetID:     e.NetID(),
```

`internal/game/system_targetlock_multi_test.go` — for each of the 4 calls like `a := gw.SpawnNPC(...)`, change subsequent `TargetEntity: a` to `TargetEntity: a.Handle()` and `netIDOfECS(gw, a)` to `netIDOfECS(gw, a.Handle())`. (Read the file once and apply consistently.)

`internal/game/system_npc_ai_test.go` — for each of the 4 calls like `npc := gw.SpawnNPC(...)`, find subsequent `mmokit.EntityFromECS(gw.stage, npc)` calls and replace with `npc` directly.

- [ ] **Step 3: Compile + test**

Run: `cd . && go vet ./... && go test ./internal/game/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/game/entity_npc.go internal/game/commands/npc_spawn.go internal/game/system_targetlock_multi_test.go internal/game/system_npc_ai_test.go
git commit -m "refactor(game): migrate SpawnNPC to Stage.Spawn (returns mmokit.Entity)"
```

---

### Task 10: Migrate SpawnPOI

**Files:**
- Modify: `internal/game/entity_poi.go`

`SpawnPOI`'s return type stays `uint32` (the netID). Internal call site at line 23 (`gw.SpawnPOI(d.X, d.Y, d.Type, d.RosterIdx)`) does not consume the return value; tests consume it via `poiNetID := gw.SpawnPOI(...)`. No external call-site changes needed.

- [ ] **Step 1: Rewrite SpawnPOI**

Replace `internal/game/entity_poi.go` lines 29–51:

```go
// SpawnPOI creates a POI entity at the given local position and spawns
// its roster of NPCs anchored to it. Returns the POI's network ID.
func (gw *GameWorld) SpawnPOI(x, y float32, poiType uint8, rosterIdx uint16) uint32 {
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindPOI},
		gamecomp.POI{
			Type:         poiType,
			Status:       gamecomp.POIStatusActive,
			AnchorRadius: gw.Config.POIAnchorRadius,
			LeashRadius:  gw.Config.POILeashRadius,
			RosterDefIdx: rosterIdx,
		},
	)

	poiNetID := e.NetID()
	gw.spawnPOIRoster(x, y, poiNetID, rosterIdx)
	gw.poiRosters[poiNetID] = gw.collectRosterNetIDs(poiNetID)

	gw.eng.Log.Log(CatPOI, "poi: spawned netID=%d type=%d pos=(%.0f,%.0f) roster=%s",
		poiNetID, poiType, x, y, rosterForIdx(rosterIdx).Name)
	return poiNetID
}
```

- [ ] **Step 2: Run tests**

Run: `cd . && go test ./internal/game/... -run POI -v && go vet ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/game/entity_poi.go
git commit -m "refactor(game): migrate SpawnPOI to Stage.Spawn"
```

---

### Task 11: Migrate gw.SpawnPlayer (game-side ship spawn)

**Return type:** stays void (the ship-spawn helper has no return). Internal logic split:
- Fresh spawn: replace the `gw.stage.SpawnEntity(...)` + post-spawn `Set` wall with one `gw.stage.Spawn(components...)` call.
- Reconnect path (`reconnectPlayer`): unchanged — it doesn't go through SpawnEntity.

**Files:**
- Modify: `internal/game/entity_ship.go`

- [ ] **Step 1: Rewrite the spawn path in gw.SpawnPlayer**

In `internal/game/entity_ship.go`, replace lines 95–145 (from `br := boundingRadius(...)` through the `gw.eng.Log.Log(CatPlayerSpawn, "player spawned: ...")` call) with:

```go
	br := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)

	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindShip},
		mmokit.Collider{
			Width:  gw.Config.ShipWidth,
			Height: gw.Config.ShipHeight,
			Layer:  gamecomp.LayerPlayer,
			Shape:  mmokit.ShapeRect,
			Radius: br,
		},
		mmokit.Rotation{},
		mmokit.PlayerConn{ConnID: connID},
		gamecomp.PilotName{Name: username},
		gamecomp.ShipControl{
			Thrust:    gw.Config.ShipThrust,
			TurnRate:  gw.Config.ShipTurnRate,
			TurnAccel: gw.Config.ShipTurnAccel,
			MaxSpeed:  gw.Config.MaxSpeed,
		},
		gamecomp.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth},
		gamecomp.Shield{
			Current:    gw.Config.ShipShield,
			Max:        gw.Config.ShipShield,
			RegenRate:  gw.Config.ShieldRegenRate,
			RegenDelay: gw.Config.ShieldRegenDelay,
		},
		gamecomp.Inventory{Items: savedCargo, MaxMass: gw.Config.MaxCargo},
		gamecomp.TargetLock{
			MaxSlots: gw.Config.LockMaxSlotsPlayer,
			Range:    gw.Config.LockOnRange,
		},
		gamecomp.Equipment{
			Weapon1:  equip.Weapon1,
			Weapon2:  equip.Weapon2,
			Shield:   equip.Shield,
			Thruster: equip.Thruster,
		},
		gamecomp.AbilitySet{},
		gamecomp.StatusEffects{},
		mmokit.MoveTarget{},
		gamecomp.LockedBy{},
		gamecomp.ActiveMining{},
		gamecomp.PlayerInput{},
		gamecomp.MiningLaser{},
	)

	// Apply equipment passive stats (shield max/regen, thrust/speed).
	gw.ApplyEquipmentStats(e)

	handle := e.Handle()
	s.Entity = handle
	netID := e.NetID()
	sec := mmokit.Get[mmokit.CellCoord](e)
	gw.eng.Log.Log(CatPlayerSpawn, "player spawned: conn=%d netID=%d pos=(%.0f,%.0f) equip=[w1=%d w2=%d sh=%d th=%d]",
		connID, netID, x, y, equip.Weapon1, equip.Weapon2, equip.Shield, equip.Thruster)
```

The rest of the function (PlayerSpawned event send, MapData, currency updates) reads from `mmokit.Get[gamecomp.Equipment](entity)`/etc. — confirm `entity` (the old wrapper variable name) is replaced by `e` everywhere in the remainder of the function.

**Confirm `ShipBundle`** (in `entity_ship.go`) lists every component you pass in the call above. Any Bundle field missing from the Spawn args will fail the InvariantPanic check immediately during `just dev` smoke testing — that's the design goal.

**Verify** `gw.ApplyEquipmentStats` accepts `mmokit.Entity` not `ecs.Entity`. Read its signature; if it takes `mmokit.Entity` already, no change. If it takes `ecs.Entity`, pass `e.Handle()`.

- [ ] **Step 2: Compile + test**

Run: `cd . && go vet ./... && go test ./internal/game/... && just build`
Expected: PASS, binary builds.

- [ ] **Step 3: Smoke test in dev mode**

Run: `cd . && timeout 5 go run ./cmd/server/ -dev 2>&1 | tail -30`
Watch for: clean startup, no panic. (You won't be able to log in via WebSocket from this script — it's a "does the cell spin up and accept the OnPlayerJoin hook" check. Player ship spawn only fires on actual client connect.)

If startup is clean and tests pass, the design is satisfied. Real player-spawn validation requires a manual `just dev` + browser session, which the user does themselves.

- [ ] **Step 4: Commit**

```bash
git add internal/game/entity_ship.go
git commit -m "refactor(game): migrate gw.SpawnPlayer ship-spawn to Stage.Spawn"
```

---

## Phase 3 — Framework-internal callers + free helpers

### Task 12: Migrate examples/4node-basic/command_bots.go

**File:** `examples/4node-basic/command_bots.go`

- [ ] **Step 1: Rewrite the bot-spawn call**

Replace lines 243–254 (`stage.SpawnEntity(...) ... mmokit.Init(func(c *BotComponents) {...})`):

```go
		stage.Spawn(
			mmokit.Position{X: x - minX, Y: y - minY},
			mmokit.Collider{Radius: PlayerRadius},
			mmokit.EntityKind{Type: KindBot},
			Name{Name: botName},
			MoveTargetFromXY(tx, ty),
			BotBehavior{TicksUntilRetarget: retarget},
		)
```

`Name`, `BotBehavior` are field types in the `BotComponents` bundle defined elsewhere in `examples/4node-basic/`. **Before writing**, read `examples/4node-basic/main.go` or wherever `BotComponents` is declared to confirm the exact field types. Use `mmokit.MoveTarget{}` and set its target via field-level construction (or write a `MoveTargetFromXY` helper if MoveTarget's setter is the cleanest path; mirror what main.go does).

- [ ] **Step 2: Compile + test**

Run: `cd . && go vet ./... && go test ./examples/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/command_bots.go
git commit -m "refactor(examples): migrate 4node-basic bot spawn to Stage.Spawn"
```

---

### Task 13: Migrate examples/simple/system_sinewave.go

**File:** `examples/simple/system_sinewave.go`

- [ ] **Step 1: Rewrite the spawn call (line 36)**

Replace:
```go
		s.Stage().SpawnEntity(mmokit.Position{X: x, Y: 0})
```
With:
```go
		s.Stage().Spawn(mmokit.Position{X: x, Y: 0})
```

This is a kindless spawn — no Position-required panic, no kind invariant.

- [ ] **Step 2: Compile + test**

Run: `cd . && go vet ./... && go test ./examples/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add examples/simple/system_sinewave.go
git commit -m "refactor(examples): migrate simple/system_sinewave to Stage.Spawn"
```

---

### Task 14: Migrate pkg/universe/builtins_entity.go entity-spawn verb

**File:** `pkg/universe/builtins_entity.go` (~line 330)

- [ ] **Step 1: Rewrite the call**

Replace:
```go
				destCell.Stage.SpawnEntity(
					component.Position{X: localX, Y: localY},
					WithEntityKind(kindID),
				)
```
With:
```go
				destCell.Stage.Spawn(
					component.Position{X: localX, Y: localY},
					component.EntityKind{Type: kindID},
				)
```

- [ ] **Step 2: Compile + test**

Run: `cd . && go vet ./... && go test ./pkg/universe/... -run Entity`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/builtins_entity.go
git commit -m "refactor(universe): migrate entity-spawn console verb to Stage.Spawn"
```

---

### Task 15: Delete pkg/mmokit/spawn.go (the free Spawn function) and migrate its 11 test consumers

**Files:**
- Delete: `pkg/mmokit/spawn.go` (the `mmokit.Spawn(stage, kind, pos, components...)` free function — keep `Despawn`)
- Modify: 11 test files in `pkg/mmokit/` that call `mmokit.Spawn(...)`

Test files affected (from `grep -l "mmokit\.Spawn(\|^.*Spawn(stage" pkg/mmokit/*_test.go`):
- `pkg/mmokit/tick_all_test.go` (2 calls)
- `pkg/mmokit/integration_damage_test.go` (2 calls)
- `pkg/mmokit/messaging_test.go` (2 calls)
- `pkg/mmokit/client_input_test.go` (3 calls)
- `pkg/mmokit/messaging_all_test.go` (2 calls)
- `pkg/mmokit/messaging_cross_cell_test.go` (1 call)
- `pkg/mmokit/tick_test.go` (3 calls)
- `pkg/mmokit/spawn_test.go` (4 calls)

- [ ] **Step 1: Inventory each call site**

Run: `cd . && grep -n "mmokit\.Spawn(" pkg/mmokit/*_test.go`
Expected: ~20 hits. Read each to understand the test fixture's expectations.

- [ ] **Step 2: For each `mmokit.Spawn(stage, testKindID, mmokit.Pos{})` migrate to `stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: uint8(testKindID)})`**

A representative replacement, in `pkg/mmokit/tick_test.go`:

```go
// Before:
a := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

// After:
a := stage.Spawn(mmokit.Position{}, mmokit.EntityKind{Type: uint8(testKindID)})
```

For variants like `mmokit.Spawn(stage, testKindID, mmokit.Pos{}, testKindHealth{Current: 50, Max: 100})`:
```go
a := stage.Spawn(
	mmokit.Position{},
	mmokit.EntityKind{Type: uint8(testKindID)},
	testKindHealth{Current: 50, Max: 100},
)
```

Confirm `testKindID` is `uint8` in the test fixture (likely declared as `const testKindID mmokit.KindID = 1`); cast as needed when constructing `EntityKind{Type: ...}`.

Note: `pkg/mmokit/spawn_test.go` was the test file for the old free function — it can be **deleted entirely** since its scenarios are now duplicated by `pkg/universe/spawn_test.go`. **Verify this before deleting** by reading both files and confirming no unique scenarios are lost; if there are unique cases, keep `spawn_test.go` but rewrite its calls to use `stage.Spawn(...)`.

- [ ] **Step 3: Delete the free function `mmokit.Spawn` and `mmokit.Pos`**

Open `pkg/mmokit/spawn.go`, delete the `Spawn` function body and the `Pos`/`KindID` types if they're only used by it. Confirm by `grep -rn "mmokit\.Pos\|mmokit\.KindID" --include='*.go' .` after deletion — should return zero hits.

Keep `func Despawn(e Entity)` — that helper is still used.

- [ ] **Step 4: Compile + test**

Run: `cd . && go vet ./... && go test ./pkg/mmokit/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/
git commit -m "refactor(mmokit): delete free Spawn helper; migrate tests to Stage.Spawn"
```

---

### Task 16: Delete pkg/mmokit/spawn_init.go (the Init helper)

**Files:**
- Delete: `pkg/mmokit/spawn_init.go`
- Check: no remaining callers of `mmokit.Init`

After Task 4 migrated `examples/4node-basic/main.go` and Task 12 migrated `command_bots.go`, the only references to `mmokit.Init` should be the file itself.

- [ ] **Step 1: Verify no callers remain**

Run: `cd . && grep -rn "mmokit\.Init(" --include='*.go' .`
Expected: only the doc comment in `pkg/mmokit/spawn_init.go` itself.

If there are unexpected hits, migrate them inline using `stage.Spawn(...)`.

- [ ] **Step 2: Delete the file**

```bash
rm pkg/mmokit/spawn_init.go
```

- [ ] **Step 3: Compile + test**

Run: `cd . && go vet ./... && go test ./pkg/... ./examples/... ./internal/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/spawn_init.go
git commit -m "refactor(mmokit): delete mmokit.Init bundle helper (superseded by Stage.Spawn)"
```

---

## Phase 4 — Delete the old API

### Task 17: Delete Stage.SpawnEntity + WithX options

**Files:**
- Modify: `pkg/universe/stage.go`
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Verify zero remaining callers of SpawnEntity**

Run:
```bash
cd .
grep -rn "\.SpawnEntity(" --include='*.go' .
```
Expected: zero hits.

If there are remaining hits, migrate them before continuing.

- [ ] **Step 2: Delete the old SpawnEntity method**

In `pkg/universe/stage.go`, delete the `SpawnEntity` method (currently lines 1503–1582). Delete the `spawnOpts` struct, the `WithVelocity`, `WithCollider`, `WithEntityKind`, `WithRotation`, `WithFacing`, `WithoutSpatial`, `WithPostInit`, `WithComponents` functions (lines ~62–143). Delete the `SpawnOption` type (line 63).

**Keep:** the `spawner *ecs.Map6[...]` field (line 211) and its initialization (line 330) — it is still used by `Stage.SpawnFromTransfer` at line 867. The Map6 batches the six core-component attach into a single archetype move for the transfer-receive hot path, so it stays.

`EnsureEntityKindComponents` is also no longer called from `SpawnEntity`, but it IS called from `Stage.SpawnFromTransfer` (line 905) and `upsertBorderReplica` (line 1315). Keep it — those callers are still live.

- [ ] **Step 3: Delete mmokit re-exports**

In `pkg/mmokit/mmokit.go`, delete:
- The `SpawnOption = universe.SpawnOption` type alias (line 393).
- The `WithVelocity`, `WithCollider`, `WithEntityKind`, `WithRotation`, `WithFacing`, `WithComponents`, `WithoutSpatial` variable bindings (lines 960–981).

- [ ] **Step 4: Compile + test**

Run: `cd . && go vet ./... && go test ./... && just build`
Expected: clean compile, all tests pass.

- [ ] **Step 5: Verify success criteria**

Run:
```bash
cd .
grep -rn "WithComponents\|WithEntityKind\|WithCollider\|WithRotation" --include='*.go' .
```
Expected: zero hits.

Run:
```bash
grep -rn "stage\.SpawnEntity\|Stage\.SpawnEntity" --include='*.go' .
```
Expected: zero hits.

Run:
```bash
grep -rn "mmokit\.EntityFromECS.*Spawn" --include='*.go' internal/game/
```
Expected: zero hits.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/stage.go pkg/mmokit/mmokit.go
git commit -m "refactor(universe): delete SpawnEntity and WithX option helpers"
```

---

### Task 18: Rewrite Stage.SpawnAtLocation

`SpawnAtLocation` currently wraps `SpawnEntity` and adds cell-bounds validation. With `SpawnEntity` deleted, rewrite it to wrap `Spawn` and take variadic components.

**Files:**
- Modify: `pkg/universe/stage.go`

- [ ] **Step 1: Find all SpawnAtLocation callers**

Run: `cd . && grep -rn "SpawnAtLocation(" --include='*.go' .`

If zero non-test callers remain (likely — its only caller `SpawnPlayer` was rewritten in Task 4), **delete** `SpawnAtLocation` entirely (lines 1598–1638 of original). If there are callers, rewrite the signature:

```go
// SpawnAtLocation spawns an entity at the given world-space Location.
//
// The Location must fall within this cell's world bounds; out-of-bounds
// behavior is the same as the previous version (log under CatInvariant,
// optional commit-log violation, panic under InvariantPanic, clamp under
// InvariantOff/InvariantLog).
//
// Caller-supplied components must NOT include Position — it is injected
// from loc. Spawn panics on duplicate-type.
func (b *Stage) SpawnAtLocation(loc coords.Location, components ...any) Entity {
	rootCell := b.rootCell()
	cellSize := coords.CellSize
	minX := float32(rootCell.X) * cellSize
	minY := float32(rootCell.Y) * cellSize
	maxX := minX + cellSize
	maxY := minY + cellSize

	if loc.X < minX || loc.X >= maxX || loc.Y < minY || loc.Y >= maxY {
		// ... existing out-of-bounds logging + panic logic ...
		// (copy verbatim from current implementation)
	}

	pos := component.Position{X: loc.X - minX, Y: loc.Y - minY}
	args := append([]any{pos}, components...)
	return b.Spawn(args...)
}
```

(Copy the out-of-bounds handling block from the current implementation verbatim — do not omit the InvariantPanic + clamp logic.)

- [ ] **Step 2: Update callers if any**

If `grep` from Step 1 returned production callers, migrate each one.

- [ ] **Step 3: Compile + test**

Run: `cd . && go vet ./... && go test ./... && just build`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/stage.go
git commit -m "refactor(universe): SpawnAtLocation takes variadic components"
```

---

## Phase 5 — Documentation

### Task 19: Update CLAUDE.md

**File:** `CLAUDE.md`

- [ ] **Step 1: Find the spawn-related section**

Run: `cd . && grep -n "SpawnEntity\|WithComponents\|WithCollider\|WithEntityKind\|RegisterKind\|stage.SpawnPlayer" CLAUDE.md`

Note the line numbers of any mention.

- [ ] **Step 2: Rewrite the spawn-section snippet**

Locate the example block (likely under "Server Meshing" or a similar architecture section). Replace any code blocks like:

```go
stage.SpawnPlayer(s, mmokit.WithEntityKind(KindFoo), ...)
```

with:

```go
stage.SpawnPlayer(s, mmokit.EntityKind{Type: KindFoo}, mmokit.Collider{Radius: 5}, ...)
```

Add a short paragraph documenting the new API near the spawn section:

> **Spawning entities.** `stage.Spawn(components ...any) Entity` is the canonical entity-creation API. Pass every component by value at the call site — Position is required (panics if missing), EntityKind is optional (kindless spawns are legal), and a duplicate type is a programmer error (panics). Spawn returns the rich `mmokit.Entity` wrapper, not the raw handle. Under dev/test (`InvariantPanic` mode), kinded spawns that omit any non-`mmokit:"local"` Bundle component panic at spawn time — forcing silent zero-fill bugs to surface immediately. There is no `WithX` option helper, no `WithComponents()` magic marker, no `EntityFromECS` post-spawn wrap.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(CLAUDE): document Stage.Spawn variadic-component API"
```

---

## Self-Review (pinned for the controller)

Before declaring the plan complete, the executor (subagent-driven-development or executing-plans) should run the spec's three success-criterion greps as a final smoke check:

```bash
cd .
grep -rn "WithComponents\|WithEntityKind\|WithCollider\|WithRotation" --include='*.go' . | grep -v "docs/" | grep -v "_test.go.bak"
grep -rn "\.SpawnEntity(" --include='*.go' .
grep -rn "mmokit\.EntityFromECS.*Spawn" --include='*.go' internal/game/
```

Expected: zero hits in each. If any hits remain, the migration is incomplete — fix before final commit.

Also run the dev smoke once:

```bash
just build && timeout 5 go run ./cmd/server/ -dev 2>&1 | tail -30
```

Expected: clean startup, no panic. Manual WebSocket smoke (login + ship spawn) is the user's responsibility — flag it in the final commit summary.
