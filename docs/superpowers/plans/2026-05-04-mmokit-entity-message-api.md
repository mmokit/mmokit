# mmokit Entity & Message-Passing API Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the new mmokit API surface — `Entity`, `Get/Has/Set`, `Spawn`, `Nearby`, `Send/Handle`, `OnTickEach`, plus cross-cell routing — alongside the existing API, fully unit-tested. After this plan, game code *can* use the new API; nothing forces it to yet.

**Architecture:** All new types live in `pkg/mmokit/`. Entity is a value handle holding `(netID, *Stage, cached ecs.Entity)` resolved lazily. Send/Handle uses a per-Stage dispatcher keyed by Go reflect type-name. Cross-cell `Send` to a replica reuses the existing `Bridge.SendAction` channel with a new `ActionType` opcode that wraps the typed-message bytes; the receiving cell decodes and dispatches via the same handler registry. No existing behavior changes — the new opcode is added as a branch in `HandleCrossCellAction` *before* the legacy switch.

**Tech Stack:** Go 1.22+ generics, `github.com/mlange-42/ark/ecs` v0.7.1, existing `pkg/universe` mesh, existing `pkg/spatial` grid, existing `ReflectMarshal`/`ReflectUnmarshal` in `pkg/universe/reflect_marshal.go`.

**Spec:** [docs/superpowers/specs/2026-05-03-entity-message-passing-design.md](../specs/2026-05-03-entity-message-passing-design.md)

---

## File Structure

**New files (`pkg/mmokit/`):**
- `entity.go` — `Entity` value type + methods
- `entity_test.go`
- `components.go` — `Get[T]` / `Has[T]` / `Set[T]`
- `components_test.go`
- `spawn.go` — `Spawn` / `Despawn`
- `spawn_test.go`
- `spatial.go` — `Nearby` / `NearbyWith[T]` iterators
- `spatial_test.go`
- `messaging.go` — `Send` / `Handle[M]` + global dispatcher type + name-keyed registry
- `messaging_test.go`
- `messaging_cross_cell_test.go` — multi-cell integration tests
- `tick.go` — `OnWorldTick` / `OnTick[T]` / `OnTickEach[Bundle]`
- `tick_test.go`
- `raw.go` — `RawWorld(w)` escape hatch
- `doc.go` — package overview, mental model

**Modified files (`pkg/universe/`):**
- `cross_cell_action.go` — add `ActionTypedMessage` opcode constant
- `stage.go` — add `Dispatcher` field + `RouteTypedMessage` method
- `cell.go` (wherever `HandleCrossCellAction` is wired into the cell loop) — branch on the new opcode before delegating to the GameWorld switch

**No files deleted, no existing functions modified beyond additions.**

---

## Naming conventions used throughout

- `*pkguniverse.Process` is exposed publicly as `*mmokit.World` (alias)
- `*pkguniverse.Stage` stays internal-shaped; `mmokit.Entity` carries a `*Stage` ref under the hood, but the public type is `mmokit.Entity` only
- The dispatcher's wire key is the message type's `reflect.TypeOf(...).Name()` for clarity (FNV-hash optimization deferred)

---

## Phase 1: Entity primitive

### Task 1.1: Define the `Entity` value type with NetID + zero-value semantics

**Files:**
- Create: `pkg/mmokit/entity.go`
- Test: `pkg/mmokit/entity_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/mmokit/entity_test.go
package mmokit_test

import (
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
)

func TestEntity_ZeroValueIsNotAlive(t *testing.T) {
    var e mmokit.Entity
    if e.Alive() {
        t.Fatal("zero-value Entity should not be Alive")
    }
    if e.NetID() != 0 {
        t.Fatalf("zero-value NetID = %d, want 0", e.NetID())
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mmokit/ -run TestEntity_ZeroValueIsNotAlive -v`
Expected: FAIL — `Entity` undefined.

- [ ] **Step 3: Create entity.go with minimal Entity**

```go
// pkg/mmokit/entity.go
package mmokit

import (
    "github.com/mlange-42/ark/ecs"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// Entity is the game-facing handle. Value type, cheap to pass.
// Wraps a NetID + lazily-resolved local ECS handle + stage ref.
// Methods are safe on zero/dead entities — they return zero values, never panic.
type Entity struct {
    netID  uint32
    cached ecs.Entity     // resolved on first method call; zero means unresolved
    stage  *pkguniverse.Stage
}

// NetID returns the entity's stable cross-cell network ID.
// Returns 0 for zero-value Entity.
func (e Entity) NetID() uint32 { return e.netID }

// Alive reports whether the entity exists and is alive on its bound stage.
// Returns false for zero-value or cross-stage-stale Entity.
func (e Entity) Alive() bool {
    if e.netID == 0 || e.stage == nil {
        return false
    }
    h := e.resolveHandle()
    return h != (ecs.Entity{}) && e.stage.ECSWorld().Alive(h)
}

// resolveHandle returns the cached ECS handle, re-resolving from the stage's
// NetID index if the cache is stale or unset. Returns ecs.Entity{} if the
// entity is not currently known to the stage.
func (e Entity) resolveHandle() ecs.Entity {
    // Implementation in Task 1.3 once stage NetID lookup is wired.
    return e.cached
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/mmokit/ -run TestEntity_ZeroValueIsNotAlive -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/entity.go pkg/mmokit/entity_test.go
git commit -m "feat(mmokit): introduce Entity value type with zero-value semantics"
```

---

### Task 1.2: Add `EntityByNetID` constructor + lazy handle resolution

**Files:**
- Modify: `pkg/mmokit/entity.go`
- Test: `pkg/mmokit/entity_test.go`

- [ ] **Step 1: Write the failing test**

```go
// Append to pkg/mmokit/entity_test.go
func TestEntityByNetID_ResolvesAlive(t *testing.T) {
    stage, _ := newTestStage(t)            // helper from Task 1.4 below
    e := spawnTestEntity(t, stage, 42)     // creates an entity with NetworkID=42
    h := mmokit.EntityByNetID(stage, 42)
    if !h.Alive() {
        t.Fatal("EntityByNetID(42).Alive() = false, want true")
    }
    if h.NetID() != 42 {
        t.Fatalf("NetID = %d, want 42", h.NetID())
    }
    _ = e
}

func TestEntityByNetID_UnknownReturnsDead(t *testing.T) {
    stage, _ := newTestStage(t)
    h := mmokit.EntityByNetID(stage, 999)
    if h.Alive() {
        t.Fatal("unknown netID should not be Alive")
    }
}
```

- [ ] **Step 2: Run test (will fail — EntityByNetID and helpers undefined)**

Run: `go test ./pkg/mmokit/ -run TestEntityByNetID -v`
Expected: FAIL.

- [ ] **Step 3: Implement `EntityByNetID` and `resolveHandle`**

Add to `pkg/mmokit/entity.go`:

```go
// EntityByNetID constructs an Entity bound to the given stage, resolving the
// local ECS handle on first method call. Use when you have a NetID and need
// to interact with the corresponding entity on this stage.
func EntityByNetID(stage *pkguniverse.Stage, netID uint32) Entity {
    return Entity{netID: netID, stage: stage}
}

// EntityFromECS wraps an ecs.Entity into an Entity by reading its NetworkID
// component. Used internally by the framework (e.g. when handing entities to
// system callbacks). Returns zero-value Entity if the handle is not alive or
// has no NetworkID.
func EntityFromECS(stage *pkguniverse.Stage, h ecs.Entity) Entity {
    if !stage.ECSWorld().Alive(h) {
        return Entity{}
    }
    netIDMap := stage.NetworkIDMap()  // accessor added in stage.go (Task 1.3)
    if !netIDMap.HasAll(h) {
        return Entity{}
    }
    return Entity{netID: netIDMap.Get(h).ID, cached: h, stage: stage}
}

// (replaces stub from Task 1.1)
func (e Entity) resolveHandle() ecs.Entity {
    if e.cached != (ecs.Entity{}) && e.stage.ECSWorld().Alive(e.cached) {
        return e.cached
    }
    if e.stage == nil {
        return ecs.Entity{}
    }
    return e.stage.LookupNetID(e.netID) // accessor in Task 1.3
}
```

- [ ] **Step 4: Run test (will fail — Stage methods missing)**

Run: `go test ./pkg/mmokit/ -run TestEntityByNetID -v`
Expected: FAIL — `Stage.LookupNetID undefined` etc.

- [ ] **Step 5: Commit (will pass after Task 1.3)**

Skip commit until after 1.3 — the changes link.

---

### Task 1.3: Expose Stage NetID lookup + NetworkID Map1

**Files:**
- Modify: `pkg/universe/stage.go`

- [ ] **Step 1: Locate existing NetID infrastructure**

Search for: `replicaNetIDs`, `netIDIdx`, or similar existing NetID-keyed maps on Stage.

Run: `grep -n "netID\|NetID" pkg/universe/stage.go | head -30`

Confirm Stage has a `netIDIdx` or equivalent field for live + replica lookups. (If it does not, also create one — but as of the bug fixes today, `Stage.netIDIdx` exists per `pkg/universe/netid_index.go`.)

- [ ] **Step 2: Add `LookupNetID`, `NetworkIDMap`, `NetIDIndex` accessors to Stage**

Append to `pkg/universe/stage.go`:

```go
// LookupNetID returns the local ECS handle for the given netID, or
// ecs.Entity{} if the netID is not currently known to this stage.
// Used by mmokit.Entity.resolveHandle().
func (s *Stage) LookupNetID(netID uint32) ecs.Entity {
    if s.netIDIdx == nil {
        return ecs.Entity{}
    }
    h, _, ok := s.netIDIdx.Lookup(netID)
    if !ok {
        return ecs.Entity{}
    }
    return h
}

// NetworkIDMap exposes the typed Map1[NetworkID] for read access.
// Used by mmokit.EntityFromECS to read a NetworkID off an existing handle.
func (s *Stage) NetworkIDMap() *ecs.Map1[component.NetworkID] {
    return s.netIDMap
}

// NetIDIndex returns the underlying NetIDIndex for read access (Live vs
// Replica presence checks). Used by mmokit.Entity.Local().
func (s *Stage) NetIDIndex() *NetIDIndex {
    return s.netIDIdx
}
```

If `s.netIDMap` doesn't exist on Stage, add the field (`netIDMap *ecs.Map1[component.NetworkID]`) and initialize it once in `NewStage` as `s.netIDMap = ecs.NewMap1[component.NetworkID](world)`. Verify with `grep -n "netIDMap\|NewMap1\[component.NetworkID\]" pkg/universe/stage.go` before adding.

- [ ] **Step 3: Run mmokit tests**

Run: `go test ./pkg/mmokit/ -run TestEntityByNetID -v`
Expected: still FAIL — test helpers not yet defined.

- [ ] **Step 4: Commit Stage accessors**

```bash
git add pkg/universe/stage.go
git commit -m "feat(universe): expose Stage.LookupNetID and NetworkIDMap for mmokit Entity"
```

---

### Task 1.4: Test helpers (`newTestStage`, `spawnTestEntity`)

**Files:**
- Create: `pkg/mmokit/testutil_test.go`

- [ ] **Step 1: Write the helper**

```go
// pkg/mmokit/testutil_test.go
package mmokit_test

import (
    "testing"

    "github.com/mlange-42/ark/ecs"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
    "github.com/zenion/mmokit/pkg/component"
    "github.com/zenion/mmokit/pkg/coords"
    "github.com/zenion/mmokit/pkg/engine"
    "github.com/zenion/mmokit/pkg/logger"
    "github.com/zenion/mmokit/pkg/net"
    "github.com/zenion/mmokit/pkg/spatial"
)

// newTestStage spins up a single-cell Stage with the minimum wiring needed
// for mmokit tests: ECS, spatial grid, NetID index. No game world, no bridge.
func newTestStage(t *testing.T) (*pkguniverse.Stage, *engine.Engine) {
    t.Helper()
    log := logger.New()
    cm := net.NewConnManager()
    eng := engine.New(engine.Config{TickRate: 20}, cm, log)
    stage := pkguniverse.NewStage(eng, pkguniverse.CellID{}, 1000, nil)
    stage.SetSpatialGrid(spatial.NewHashGrid(coords.CellSize / 10))
    return stage, eng
}

// spawnTestEntity creates a bare entity on the stage with NetworkID=netID
// and Position=(0,0). Returns the ECS handle. Registers the netID in
// stage's index so LookupNetID succeeds.
func spawnTestEntity(t *testing.T, stage *pkguniverse.Stage, netID uint32) ecs.Entity {
    t.Helper()
    w := stage.ECSWorld()
    mapper := ecs.NewMap2[component.Position, component.NetworkID](w)
    h := mapper.NewEntity(
        &component.Position{X: 0, Y: 0},
        &component.NetworkID{ID: netID},
    )
    stage.RegisterLiveNetID(netID, h) // accessor added below
    return h
}
```

- [ ] **Step 2: Add `RegisterLiveNetID` to Stage**

In `pkg/universe/stage.go`:

```go
// RegisterLiveNetID adds (netID, handle) to the stage's local NetID index
// as a Live presence. Used by tests; production code reaches the index via
// the entity-spawn paths.
func (s *Stage) RegisterLiveNetID(netID uint32, h ecs.Entity) {
    if s.netIDIdx == nil {
        return
    }
    s.netIDIdx.Enter(netID, h, PresenceLive)
}
```

- [ ] **Step 3: Run all Entity tests**

Run: `go test ./pkg/mmokit/ -run TestEntity -v`
Expected: PASS for both `TestEntity_ZeroValueIsNotAlive` and `TestEntityByNetID_*`.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/entity.go pkg/mmokit/entity_test.go pkg/mmokit/testutil_test.go pkg/universe/stage.go
git commit -m "feat(mmokit): EntityByNetID + Alive resolution via Stage NetID index"
```

---

### Task 1.5: Add `Pos`, `Local`, `String` methods on Entity

**Files:**
- Modify: `pkg/mmokit/entity.go`
- Test: `pkg/mmokit/entity_test.go`

- [ ] **Step 1: Write tests**

```go
func TestEntity_PosReturnsPosition(t *testing.T) {
    stage, _ := newTestStage(t)
    h := spawnTestEntity(t, stage, 42)
    // override position
    posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
    posMap.Get(h).X = 50
    posMap.Get(h).Y = -25
    e := mmokit.EntityByNetID(stage, 42)
    x, y := e.Pos()
    if x != 50 || y != -25 {
        t.Fatalf("Pos = (%v, %v), want (50, -25)", x, y)
    }
}

func TestEntity_PosOnDeadIsZero(t *testing.T) {
    stage, _ := newTestStage(t)
    e := mmokit.EntityByNetID(stage, 999)
    x, y := e.Pos()
    if x != 0 || y != 0 {
        t.Fatalf("dead Pos = (%v, %v), want (0, 0)", x, y)
    }
}

func TestEntity_LocalReportsAuthority(t *testing.T) {
    stage, _ := newTestStage(t)
    spawnTestEntity(t, stage, 42)
    e := mmokit.EntityByNetID(stage, 42)
    if !e.Local() {
        t.Fatal("local Live entity should report Local()==true")
    }
}
```

- [ ] **Step 2: Run (fails — undefined)**

Run: `go test ./pkg/mmokit/ -run TestEntity_Pos -v` and `-run TestEntity_Local -v`
Expected: FAIL.

- [ ] **Step 3: Implement methods**

Add to `pkg/mmokit/entity.go`:

```go
// Pos returns the entity's local-cell-relative position. Returns (0, 0) for
// dead entities or entities lacking a Position component.
func (e Entity) Pos() (x, y float32) {
    h := e.resolveHandle()
    if h == (ecs.Entity{}) || e.stage == nil {
        return 0, 0
    }
    posMap := ecs.NewMap1[component.Position](e.stage.ECSWorld())
    if !posMap.HasAll(h) {
        return 0, 0
    }
    p := posMap.Get(h)
    return p.X, p.Y
}

// Local reports whether the authoritative copy of this entity lives on the
// stage this Entity is bound to. Mostly for diagnostics — game code rarely
// needs to know.
func (e Entity) Local() bool {
    if e.stage == nil || e.netID == 0 {
        return false
    }
    _, presence, ok := e.stage.NetIDIndex().Lookup(e.netID)
    return ok && presence == pkguniverse.PresenceLive
}

// String returns a debug representation: "Entity(netID=42)".
func (e Entity) String() string {
    return fmt.Sprintf("Entity(netID=%d)", e.netID)
}
```

(Add `NetIDIndex()` accessor on Stage if it doesn't exist — returns `*NetIDIndex`.)

- [ ] **Step 4: Run all Entity tests**

Run: `go test ./pkg/mmokit/ -run TestEntity -v`
Expected: ALL PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/entity.go pkg/mmokit/entity_test.go pkg/universe/stage.go
git commit -m "feat(mmokit): Entity.Pos / Local / String methods"
```

---

## Phase 2: Component access

### Task 2.1: `Get[T]`, `Has[T]`, `Set[T]` generic functions

**Files:**
- Create: `pkg/mmokit/components.go`
- Test: `pkg/mmokit/components_test.go`

- [ ] **Step 1: Write tests**

```go
// pkg/mmokit/components_test.go
package mmokit_test

import (
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
    "github.com/zenion/mmokit/pkg/component"
)

func TestGet_ReturnsComponent(t *testing.T) {
    stage, _ := newTestStage(t)
    spawnTestEntity(t, stage, 42)
    e := mmokit.EntityByNetID(stage, 42)
    pos := mmokit.Get[component.Position](e)
    if pos == nil {
        t.Fatal("Get[Position] returned nil for entity with Position")
    }
}

func TestGet_NilForMissingComponent(t *testing.T) {
    stage, _ := newTestStage(t)
    spawnTestEntity(t, stage, 42)
    e := mmokit.EntityByNetID(stage, 42)
    type Custom struct{ X int }
    c := mmokit.Get[Custom](e)
    if c != nil {
        t.Fatal("Get[Custom] should return nil — entity has no Custom component")
    }
}

func TestGet_NilForDeadEntity(t *testing.T) {
    stage, _ := newTestStage(t)
    e := mmokit.EntityByNetID(stage, 999)
    if mmokit.Get[component.Position](e) != nil {
        t.Fatal("Get on dead entity should return nil")
    }
}

func TestHas(t *testing.T) {
    stage, _ := newTestStage(t)
    spawnTestEntity(t, stage, 42)
    e := mmokit.EntityByNetID(stage, 42)
    if !mmokit.Has[component.Position](e) {
        t.Fatal("Has[Position] should be true")
    }
    type Foo struct{}
    if mmokit.Has[Foo](e) {
        t.Fatal("Has[Foo] should be false")
    }
}

func TestSet_AddsAndOverwrites(t *testing.T) {
    stage, _ := newTestStage(t)
    spawnTestEntity(t, stage, 42)
    e := mmokit.EntityByNetID(stage, 42)
    type HP struct{ Current int }
    mmokit.Set[HP](e, HP{Current: 100})
    h := mmokit.Get[HP](e)
    if h == nil || h.Current != 100 {
        t.Fatalf("after Set(HP=100), Get returned %+v", h)
    }
    mmokit.Set[HP](e, HP{Current: 50})
    if mmokit.Get[HP](e).Current != 50 {
        t.Fatal("Set should overwrite existing component")
    }
}
```

- [ ] **Step 2: Run (fails)**

Run: `go test ./pkg/mmokit/ -run "TestGet|TestHas|TestSet" -v`
Expected: FAIL.

- [ ] **Step 3: Implement components.go**

```go
// pkg/mmokit/components.go
package mmokit

import (
    "github.com/mlange-42/ark/ecs"
)

// Get returns a pointer to the entity's component of type T, or nil if the
// entity is dead or does not have the component.
func Get[T any](e Entity) *T {
    h := e.resolveHandle()
    if h == (ecs.Entity{}) || e.stage == nil {
        return nil
    }
    m := ecs.NewMap1[T](e.stage.ECSWorld())
    if !m.HasAll(h) {
        return nil
    }
    return m.Get(h)
}

// Has reports whether the entity has a component of type T.
func Has[T any](e Entity) bool {
    h := e.resolveHandle()
    if h == (ecs.Entity{}) || e.stage == nil {
        return false
    }
    m := ecs.NewMap1[T](e.stage.ECSWorld())
    return m.HasAll(h)
}

// Set installs the component on the entity, adding it if absent or
// overwriting if present. No-op on dead entities.
func Set[T any](e Entity, v T) {
    h := e.resolveHandle()
    if h == (ecs.Entity{}) || e.stage == nil {
        return
    }
    m := ecs.NewMap1[T](e.stage.ECSWorld())
    if m.HasAll(h) {
        *m.Get(h) = v
        return
    }
    m.Add(h, &v)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/mmokit/ -run "TestGet|TestHas|TestSet" -v`
Expected: ALL PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/components.go pkg/mmokit/components_test.go
git commit -m "feat(mmokit): generic Get[T] / Has[T] / Set[T] component access"
```

---

## Phase 3: Spawn / Despawn

### Task 3.1: `Spawn` with kind + position + variadic component overrides

**Files:**
- Create: `pkg/mmokit/spawn.go`
- Test: `pkg/mmokit/spawn_test.go`

- [ ] **Step 1: Write tests**

```go
// pkg/mmokit/spawn_test.go
package mmokit_test

import (
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
    "github.com/zenion/mmokit/pkg/component"
)

type testKindHealth struct{ Current, Max int }

func TestSpawn_ReturnsLiveEntity(t *testing.T) {
    stage, _ := newTestStage(t)
    registerTestKind(t, stage)  // helper below
    e := mmokit.Spawn(stage, testKindID, mmokit.Pos{X: 10, Y: 20})
    if !e.Alive() {
        t.Fatal("Spawn returned dead entity")
    }
    if e.NetID() == 0 {
        t.Fatal("Spawn returned entity with zero NetID")
    }
}

func TestSpawn_AppliesPosition(t *testing.T) {
    stage, _ := newTestStage(t)
    registerTestKind(t, stage)
    e := mmokit.Spawn(stage, testKindID, mmokit.Pos{X: 10, Y: 20})
    x, y := e.Pos()
    if x != 10 || y != 20 {
        t.Fatalf("Pos = (%v, %v), want (10, 20)", x, y)
    }
}

func TestSpawn_AppliesComponentOverrides(t *testing.T) {
    stage, _ := newTestStage(t)
    registerTestKind(t, stage)
    e := mmokit.Spawn(stage, testKindID, mmokit.Pos{}, testKindHealth{Current: 50, Max: 100})
    h := mmokit.Get[testKindHealth](e)
    if h == nil || h.Current != 50 {
        t.Fatalf("Get[testKindHealth] = %+v, want Current=50", h)
    }
}
```

- [ ] **Step 2: Test helper for kind registration**

Add to `pkg/mmokit/testutil_test.go`:

```go
const testKindID mmokit.KindID = 99

func registerTestKind(t *testing.T, stage *pkguniverse.Stage) {
    t.Helper()
    // Register the testKindHealth component on this stage's ECS world
    // so Spawn can attach it. Uses existing kind registration plumbing.
    def := pkguniverse.EntityKindDef{Kind: uint8(testKindID), Name: "TestKind"}
    w := stage.ECSWorld()
    pkguniverse.KindComponentByID(&def, w,
        ecs.ComponentID[testKindHealth](w), reflect.TypeFor[testKindHealth](),
        false)
    stage.RegisterEntityKind(def)
}
```

- [ ] **Step 3: Run (fails — Spawn / KindID / Pos undefined)**

Run: `go test ./pkg/mmokit/ -run TestSpawn -v`
Expected: FAIL.

- [ ] **Step 4: Implement spawn.go**

```go
// pkg/mmokit/spawn.go
package mmokit

import (
    "reflect"
    "unsafe"

    "github.com/mlange-42/ark/ecs"
    "github.com/zenion/mmokit/pkg/component"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// KindID identifies an entity kind registered with the stage.
type KindID uint8

// Pos is the spawn position in cell-local coordinates.
type Pos struct{ X, Y float32 }

// Spawn creates a new entity of the given kind on the stage, applying any
// supplied component overrides. Returns the resulting Entity.
//
// The kind's default component set is auto-attached (zero-valued); for each
// override in components, that component's data is copied onto the entity.
// Component override types must match those declared in the kind's
// EntityKindDef — passing an unknown type panics with a clear message.
func Spawn(stage *pkguniverse.Stage, kind KindID, pos Pos, components ...any) Entity {
    h := stage.SpawnEntity(
        component.Position{X: pos.X, Y: pos.Y},
        pkguniverse.WithEntityKind(uint8(kind)),
        pkguniverse.WithComponents(),
    )

    for _, c := range components {
        v := reflect.ValueOf(c)
        if v.Kind() == reflect.Ptr {
            v = v.Elem()
        }
        t := v.Type()
        id := ecs.TypeID(stage.ECSWorld(), t)
        u := stage.ECSWorld().Unsafe()
        if !u.Has(h, id) {
            panic("mmokit.Spawn: component " + t.Name() + " not registered on kind")
        }
        ptr := u.Get(h, id)
        // Copy v's value into the existing component slot.
        reflect.NewAt(t, unsafe.Pointer(ptr)).Elem().Set(v)
    }

    return EntityFromECS(stage, h)
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/mmokit/ -run TestSpawn -v`
Expected: ALL PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/spawn.go pkg/mmokit/spawn_test.go pkg/mmokit/testutil_test.go
git commit -m "feat(mmokit): Spawn(stage, kind, pos, components...) returns typed Entity"
```

---

### Task 3.2: `Despawn` removes the entity

**Files:**
- Modify: `pkg/mmokit/spawn.go`
- Test: `pkg/mmokit/spawn_test.go`

- [ ] **Step 1: Write the test**

```go
func TestDespawn_RemovesEntity(t *testing.T) {
    stage, _ := newTestStage(t)
    registerTestKind(t, stage)
    e := mmokit.Spawn(stage, testKindID, mmokit.Pos{})
    if !e.Alive() { t.Fatal("precondition") }
    mmokit.Despawn(e)
    // Stage may defer removal to flush; do it explicitly here.
    stage.FlushRemovals()
    if e.Alive() {
        t.Fatal("Despawn should make entity not Alive after FlushRemovals")
    }
}
```

- [ ] **Step 2: Run (fails — Despawn undefined)**

Run: `go test ./pkg/mmokit/ -run TestDespawn -v`
Expected: FAIL.

- [ ] **Step 3: Implement Despawn**

Add to `pkg/mmokit/spawn.go`:

```go
// Despawn marks the entity for removal. The actual ECS removal happens at
// the next flush in the simulation tick. Safe on dead/zero entities (no-op).
func Despawn(e Entity) {
    h := e.resolveHandle()
    if h == (ecs.Entity{}) || e.stage == nil {
        return
    }
    e.stage.MarkForRemoval(h)
}
```

(If `Stage.MarkForRemoval` doesn't exist, find the existing equivalent — likely a method or a slice append.)

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/mmokit/ -run TestDespawn -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/spawn.go pkg/mmokit/spawn_test.go
git commit -m "feat(mmokit): Despawn(e) marks entity for removal"
```

---

## Phase 4: Spatial query

### Task 4.1: `Nearby` and `NearbyWith[T]` iterators

**Files:**
- Create: `pkg/mmokit/spatial.go`
- Test: `pkg/mmokit/spatial_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/mmokit/spatial_test.go
package mmokit_test

import (
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
    "github.com/zenion/mmokit/pkg/component"
)

func TestNearby_ReturnsEntitiesInRadius(t *testing.T) {
    stage, _ := newTestStage(t)
    registerTestKind(t, stage)
    a := mmokit.Spawn(stage, testKindID, mmokit.Pos{X: 0, Y: 0})
    b := mmokit.Spawn(stage, testKindID, mmokit.Pos{X: 5, Y: 0})
    c := mmokit.Spawn(stage, testKindID, mmokit.Pos{X: 100, Y: 100})
    // Spatial system populates the grid each tick — call directly here.
    refreshSpatialGrid(t, stage)

    found := map[uint32]bool{}
    for e := range mmokit.Nearby(stage, 0, 0, 10) {
        found[e.NetID()] = true
    }
    if !found[a.NetID()] || !found[b.NetID()] {
        t.Fatal("Nearby should include both within-radius entities")
    }
    if found[c.NetID()] {
        t.Fatal("Nearby should exclude out-of-radius entity")
    }
}
```

(Add `refreshSpatialGrid` helper that iterates the stage's entities once and registers each in the grid — same logic as `SpatialSystem.Update`, abbreviated.)

- [ ] **Step 2: Run (fails)**

Run: `go test ./pkg/mmokit/ -run TestNearby -v`
Expected: FAIL.

- [ ] **Step 3: Implement spatial.go**

```go
// pkg/mmokit/spatial.go
package mmokit

import (
    "iter"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// Nearby yields every entity within radius r of (x, y) on the stage's
// spatial grid. Includes both Live and Replica entities — game code does
// not need to distinguish.
func Nearby(stage *pkguniverse.Stage, x, y, r float32) iter.Seq[Entity] {
    return func(yield func(Entity) bool) {
        grid := stage.SpatialGrid()
        if grid == nil {
            return
        }
        for _, entry := range grid.QueryRadius(x, y, r, nil) {
            e := EntityFromECS(stage, entry.Entity)
            if !yield(e) {
                return
            }
        }
    }
}

// NearbyWith yields nearby entities that have component T.
func NearbyWith[T any](stage *pkguniverse.Stage, x, y, r float32) iter.Seq[Entity] {
    return func(yield func(Entity) bool) {
        for e := range Nearby(stage, x, y, r) {
            if Has[T](e) {
                if !yield(e) {
                    return
                }
            }
        }
    }
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/mmokit/ -run TestNearby -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/spatial.go pkg/mmokit/spatial_test.go pkg/mmokit/testutil_test.go
git commit -m "feat(mmokit): Nearby + NearbyWith[T] iterators over spatial grid"
```

---

## Phase 5: Local Send / Handle dispatch

### Task 5.1: Define the per-Stage `Dispatcher` and global registration

**Files:**
- Create: `pkg/mmokit/messaging.go`
- Modify: `pkg/universe/stage.go`

- [ ] **Step 1: Add Dispatcher field to Stage**

Append to `pkg/universe/stage.go`:

```go
// Dispatcher dispatches typed messages to registered handlers, keyed by
// reflect type-name. Initialized lazily on first registration. Public for
// access from mmokit messaging code; not intended for direct game use.
func (s *Stage) Dispatcher() *MessageDispatcher {
    if s.dispatcher == nil {
        s.dispatcher = newMessageDispatcher(s)
    }
    return s.dispatcher
}
```

(Add `dispatcher *MessageDispatcher` field to Stage struct.)

- [ ] **Step 2: Define MessageDispatcher type**

Create `pkg/universe/message_dispatcher.go`. The dispatcher cannot import `pkg/mmokit` (would cycle), so it accepts an entity-construction callback set by mmokit when handlers register:

```go
package universe

import (
    "reflect"
    "sync"
)

// EntityCtor builds the typed mmokit.Entity passed to handlers. Provided by
// the mmokit layer at handler-registration time so this package avoids an
// import cycle. Returns `any` here; the receiving handler reflects it as
// the concrete mmokit.Entity type.
type EntityCtor func(stage *Stage, netID uint32) any

// MessageDispatcher routes typed messages to registered handlers. One
// dispatcher per Stage. Handlers are keyed by message type name (Go
// reflect.Type.Name()) for cross-cell wire compatibility.
type MessageDispatcher struct {
    stage    *Stage
    mu       sync.RWMutex
    handlers map[string]reflect.Value // type name → handler reflect.Value
    types    map[string]reflect.Type  // type name → message type for decode
    ctor     EntityCtor               // set once by SetEntityCtor
}

func newMessageDispatcher(s *Stage) *MessageDispatcher {
    return &MessageDispatcher{
        stage:    s,
        handlers: make(map[string]reflect.Value),
        types:    make(map[string]reflect.Type),
    }
}

// SetEntityCtor installs the callback the dispatcher uses to build typed
// Entity values for handler invocations. Idempotent — second and later
// calls with the same ctor are no-ops; mismatched ctors panic.
func (d *MessageDispatcher) SetEntityCtor(c EntityCtor) {
    d.mu.Lock()
    defer d.mu.Unlock()
    if d.ctor != nil {
        // Compare by function pointer; cheap sanity check.
        if reflect.ValueOf(d.ctor).Pointer() != reflect.ValueOf(c).Pointer() {
            panic("mmokit: conflicting EntityCtor on dispatcher")
        }
        return
    }
    d.ctor = c
}

// Register installs fn as the handler for messages of the named type. fn
// must have signature func(target Entity, msg *M). Panics if a handler is
// already registered for that type name (one handler per type).
func (d *MessageDispatcher) Register(typeName string, msgType reflect.Type, fn reflect.Value) {
    d.mu.Lock()
    defer d.mu.Unlock()
    if _, exists := d.handlers[typeName]; exists {
        panic("mmokit: handler already registered for " + typeName)
    }
    d.handlers[typeName] = fn
    d.types[typeName] = msgType
}

// Invoke synchronously calls the handler for msg, with target bound to the
// given netID on this dispatcher's stage. No-op if no handler is registered
// or no entity ctor is set. msgPtr must be a pointer to a value of the
// registered type.
func (d *MessageDispatcher) Invoke(targetNetID uint32, msgPtr any) {
    typeName := reflect.TypeOf(msgPtr).Elem().Name()
    d.mu.RLock()
    fn, ok := d.handlers[typeName]
    ctor := d.ctor
    d.mu.RUnlock()
    if !ok || ctor == nil {
        return
    }
    target := ctor(d.stage, targetNetID)
    fn.Call([]reflect.Value{
        reflect.ValueOf(target),
        reflect.ValueOf(msgPtr),
    })
}

// MessageType returns the registered Go type for a type name, or nil if
// unregistered. Used by the cross-cell decoder to allocate a fresh value.
func (d *MessageDispatcher) MessageType(typeName string) reflect.Type {
    d.mu.RLock()
    defer d.mu.RUnlock()
    return d.types[typeName]
}
```

- [ ] **Step 3: Commit dispatcher skeleton**

```bash
git add pkg/universe/stage.go pkg/universe/message_dispatcher.go
git commit -m "feat(universe): per-Stage MessageDispatcher with EntityCtor callback"
```

---

### Task 5.2: `Handle[M]` registration in mmokit

**Files:**
- Create: `pkg/mmokit/messaging.go`
- Test: `pkg/mmokit/messaging_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/mmokit/messaging_test.go
package mmokit_test

import (
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
)

type pingMsg struct{ Note string }

func TestHandle_RegistersHandlerInvokedBySend(t *testing.T) {
    stage, _ := newTestStage(t)
    registerTestKind(t, stage)
    e := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

    var got string
    mmokit.Handle[pingMsg](stage, func(target mmokit.Entity, msg *pingMsg) {
        if target.NetID() != e.NetID() {
            t.Fatalf("target.NetID = %d, want %d", target.NetID(), e.NetID())
        }
        got = msg.Note
    })

    e.Send(pingMsg{Note: "hello"})

    if got != "hello" {
        t.Fatalf("handler did not run; got=%q", got)
    }
}
```

- [ ] **Step 2: Run (fails)**

Run: `go test ./pkg/mmokit/ -run TestHandle -v`
Expected: FAIL.

- [ ] **Step 3: Implement Handle and Send**

Create `pkg/mmokit/messaging.go`:

```go
// pkg/mmokit/messaging.go
package mmokit

import (
    "reflect"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// Handle registers fn as the global handler for messages of type M on the
// given stage. fn is called whenever an Entity on this stage receives a Send
// of an M, regardless of whether the Send originated locally or cross-cell.
// One handler per message type per stage; calling Handle twice for the same
// M panics.
func Handle[M any](stage *pkguniverse.Stage, fn func(target Entity, msg *M)) {
    d := stage.Dispatcher()
    d.SetEntityCtor(entityCtorAdapter)
    var zero M
    msgType := reflect.TypeOf(zero)
    d.Register(msgType.Name(), msgType, reflect.ValueOf(fn))
}

// entityCtorAdapter is what the universe-layer dispatcher calls to construct
// the typed Entity argument for the handler. Lives here to avoid import
// cycles (universe.MessageDispatcher returns `any` which is a mmokit.Entity).
func entityCtorAdapter(stage *pkguniverse.Stage, netID uint32) any {
    return EntityByNetID(stage, netID)
}

// Send delivers msg to the entity. If the entity is local on its stage, the
// registered handler runs synchronously before Send returns. If the entity
// is a replica (lives elsewhere), Send is fire-and-forget — the handler runs
// on the authoritative stage when the wire message arrives.
//
// (Cross-cell routing is added in Phase 6; this version handles local only.)
func (e Entity) Send(msg any) {
    if e.stage == nil || e.netID == 0 {
        return
    }
    // Reflect-package the msg into a pointer for handler invocation.
    v := reflect.ValueOf(msg)
    if v.Kind() != reflect.Ptr {
        // Box into a mutable address so the handler can fill in result fields.
        ptr := reflect.New(v.Type())
        ptr.Elem().Set(v)
        e.stage.Dispatcher().Invoke(e.netID, ptr.Interface())
        return
    }
    e.stage.Dispatcher().Invoke(e.netID, msg)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/mmokit/ -run TestHandle -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/messaging.go pkg/mmokit/messaging_test.go
git commit -m "feat(mmokit): Handle[M] + Entity.Send (local synchronous dispatch)"
```

---

### Task 5.3: Test handler can mutate the message via pointer receiver

**Files:**
- Modify: `pkg/mmokit/messaging_test.go`

- [ ] **Step 1: Write the test**

```go
type damageMsg struct {
    Amount float32
    Dealt  float32 // result, mutated by handler
}

func TestSend_HandlerMutatesMessage(t *testing.T) {
    stage, _ := newTestStage(t)
    registerTestKind(t, stage)
    e := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

    mmokit.Handle[damageMsg](stage, func(target mmokit.Entity, msg *damageMsg) {
        msg.Dealt = msg.Amount  // simulate "all damage applied"
    })

    msg := &damageMsg{Amount: 25}
    e.Send(msg)

    if msg.Dealt != 25 {
        t.Fatalf("after Send, msg.Dealt = %v, want 25 (handler should mutate via *damageMsg)", msg.Dealt)
    }
}
```

- [ ] **Step 2: Run (should pass — Send already supports pointer)**

Run: `go test ./pkg/mmokit/ -run TestSend_HandlerMutates -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/messaging_test.go
git commit -m "test(mmokit): handler can mutate message via pointer Send"
```

---

## Phase 6: Cross-cell Send routing

### Task 6.1: Wire codec for typed messages

**Files:**
- Create: `pkg/universe/typed_message_codec.go`
- Test: `pkg/universe/typed_message_codec_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/universe/typed_message_codec_test.go
package universe_test

import (
    "reflect"
    "testing"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

type damageWire struct {
    Amount float32
    Dealt  float32
}

func TestEncodeDecodeTypedMessage(t *testing.T) {
    src := damageWire{Amount: 25, Dealt: 0}
    bytes := pkguniverse.EncodeTypedMessage("damageWire", &src)

    typeName, payload := pkguniverse.SplitTypedMessage(bytes)
    if typeName != "damageWire" {
        t.Fatalf("type name = %q, want damageWire", typeName)
    }
    out := damageWire{}
    pkguniverse.DecodeTypedMessage(payload, &out)
    if !reflect.DeepEqual(src, out) {
        t.Fatalf("roundtrip:\n got %+v\nwant %+v", out, src)
    }
}
```

- [ ] **Step 2: Run (fails)**

Run: `go test ./pkg/universe/ -run TestEncodeDecodeTypedMessage -v`
Expected: FAIL.

- [ ] **Step 3: Implement codec**

```go
// pkg/universe/typed_message_codec.go
package universe

import "encoding/binary"

// Wire format for a typed cross-cell message:
//   [u16 typeNameLen][typeNameLen bytes type name][reflect-marshaled struct bytes]
//
// The type name is the Go reflect.Type.Name() of the message struct. Names
// must match between sending and receiving processes — typically the same
// build, but cross-version is fine as long as the type isn't renamed.

// EncodeTypedMessage builds the wire frame: type name length + name + body.
// body is produced by ReflectMarshal on the message pointer.
func EncodeTypedMessage(typeName string, msgPtr any) []byte {
    body := ReflectMarshal(msgPtr)
    nameBytes := []byte(typeName)
    out := make([]byte, 2+len(nameBytes)+len(body))
    binary.LittleEndian.PutUint16(out[0:2], uint16(len(nameBytes)))
    copy(out[2:], nameBytes)
    copy(out[2+len(nameBytes):], body)
    return out
}

// SplitTypedMessage decodes the wire frame's type name and returns the
// remaining payload bytes for ReflectUnmarshal. Returns ("", nil) on a
// malformed frame.
func SplitTypedMessage(data []byte) (typeName string, payload []byte) {
    if len(data) < 2 {
        return "", nil
    }
    n := int(binary.LittleEndian.Uint16(data[0:2]))
    if 2+n > len(data) {
        return "", nil
    }
    return string(data[2 : 2+n]), data[2+n:]
}

// DecodeTypedMessage unmarshals payload bytes into ptr (pointer to struct).
// Wraps ReflectUnmarshal for symmetry with EncodeTypedMessage at call sites.
func DecodeTypedMessage(payload []byte, ptr any) {
    ReflectUnmarshal(payload, ptr)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/universe/ -run TestEncodeDecodeTypedMessage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/typed_message_codec.go pkg/universe/typed_message_codec_test.go
git commit -m "feat(universe): wire codec for typed cross-cell messages (name + ReflectMarshal body)"
```

---

### Task 6.2: New `ActionTypedMessage` opcode + Bridge plumbing

**Files:**
- Modify: `pkg/universe/cross_cell_action.go` (or wherever `ActionType` constants live)
- Modify: `pkg/universe/stage.go`

- [ ] **Step 1: Locate the opcode constants**

Run: `grep -n "ActionType\|ActionDamage\|MeshActionType" pkg/universe/*.go internal/game/*.go`

Find the canonical `ActionType` definition.

- [ ] **Step 2: Add `ActionTypedMessage` constant**

Append wherever the engine-internal action types live (NOT in `internal/game/action_codec.go` — that's game-defined). If there's no engine-side ActionType yet, define one in `pkg/universe/`:

```go
// pkg/universe/typed_message_action.go
package universe

// EngineActionType identifies engine-internal cross-cell action shapes.
// Distinct from game-defined ActionTypes (which start at 1, this starts at 100).
const ActionTypedMessage ActionType = 100
```

(Use a value high enough to not collide with game-defined types. If `ActionType` is currently game-only and lives in `internal/game/action_codec.go`, leave that as-is and use a separate dispatch path — see Step 4.)

- [ ] **Step 3: Add Stage.RouteTypedMessage**

In `pkg/universe/stage.go`:

```go
// RouteTypedMessage delivers a typed message to the entity with targetNetID.
// If the entity is local Live on this stage, the registered handler runs
// synchronously. If the entity is a Replica, the message is wire-encoded and
// shipped to the replica's source cell via the bridge; the handler runs there
// when the frame arrives.
//
// Returns true if the message was dispatched (locally or remotely), false
// if the entity is not known to this stage.
func (s *Stage) RouteTypedMessage(targetNetID uint32, msg any) bool {
    h, presence, ok := s.netIDIdx.Lookup(targetNetID)
    if !ok {
        return false
    }
    if presence == PresenceLive {
        s.Dispatcher().Invoke(targetNetID, msg)
        return true
    }
    // Replica — route to source cell.
    if !s.replicaMap.HasAll(h) {
        return false
    }
    rep := s.replicaMap.Get(h)
    typeName := reflect.TypeOf(msg).Elem().Name()
    payload := EncodeTypedMessage(typeName, msg)
    s.bridge.SendAction(MeshCellID(rep.SourceCellID), &CrossCellAction{
        Type:         ActionTypedMessage,
        TargetNetID:  rep.SourceNetID,
        SourceCellID: string(s.cellID),
        Payload:      payload,
    })
    return true
}
```

- [ ] **Step 4: Wire into the receiving cell's action dispatch**

Find where `CrossCellAction` is dispatched on the receiving cell. Today this is `GameWorld.HandleCrossCellAction`. We must intercept `ActionTypedMessage` BEFORE the game's switch, because it's engine-level routing.

Option: add a Stage-level `HandleEngineAction` that runs first; falls through to game's handler if the action isn't engine-recognized.

```go
// pkg/universe/stage.go (or cell.go where actions are received)

// HandleEngineAction processes engine-level cross-cell actions (currently
// just ActionTypedMessage). Returns true if the action was consumed; the
// caller falls back to game-defined handling if false.
func (s *Stage) HandleEngineAction(action *CrossCellAction) bool {
    if action.Type != ActionTypedMessage {
        return false
    }
    typeName, payload := SplitTypedMessage(action.Payload)
    msgType := s.Dispatcher().MessageType(typeName)
    if msgType == nil {
        // No registered handler — drop. Logged by caller for visibility.
        return true
    }
    msgPtr := reflect.New(msgType)
    DecodeTypedMessage(payload, msgPtr.Interface())
    s.Dispatcher().Invoke(action.TargetNetID, msgPtr.Interface())
    return true
}
```

Then modify the cell's action-receive path to call `HandleEngineAction` first:

Find the current action-dispatch code (`Cell.handleAction` / `CellMessage MsgAction` handling) and add at the top:

```go
if cell.Stage.HandleEngineAction(action) {
    return  // engine consumed it
}
// existing game-handler dispatch follows
gw.HandleCrossCellAction(action)
```

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/typed_message_action.go pkg/universe/stage.go pkg/universe/cell.go
git commit -m "feat(universe): RouteTypedMessage + ActionTypedMessage opcode + receive path"
```

---

### Task 6.3: Update `Entity.Send` to use `RouteTypedMessage`

**Files:**
- Modify: `pkg/mmokit/messaging.go`

- [ ] **Step 1: Replace local-only Send body**

```go
func (e Entity) Send(msg any) {
    if e.stage == nil || e.netID == 0 {
        return
    }
    // Box into a pointer so handlers can mutate result fields.
    var msgPtr any
    if v := reflect.ValueOf(msg); v.Kind() == reflect.Ptr {
        msgPtr = msg
    } else {
        ptr := reflect.New(reflect.TypeOf(msg))
        ptr.Elem().Set(reflect.ValueOf(msg))
        msgPtr = ptr.Interface()
    }
    e.stage.RouteTypedMessage(e.netID, msgPtr)
}
```

- [ ] **Step 2: Run all messaging tests**

Run: `go test ./pkg/mmokit/ -run "TestSend|TestHandle" -v`
Expected: ALL PASS (existing local-only tests still pass; cross-cell tests added in 6.4).

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/messaging.go
git commit -m "feat(mmokit): Entity.Send routes via Stage.RouteTypedMessage (replica-aware)"
```

---

### Task 6.4: Cross-cell integration test

**Files:**
- Create: `pkg/mmokit/messaging_cross_cell_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/mmokit/messaging_cross_cell_test.go
package mmokit_test

import (
    "testing"
    "time"

    "github.com/zenion/mmokit/pkg/mmokit"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

type crossDamage struct {
    Amount float32
    Dealt  float32
}

// Two-cell coordinator using a loopback bridge. Spawn an entity on cell A,
// create a replica of it on cell B (via border-replication test helper),
// then call Send from cell B targeting the replica's NetID. Verify the
// handler runs on cell A (the authoritative cell).
func TestCrossCellSend_RoutesToAuthoritativeCell(t *testing.T) {
    coord, cellA, cellB := newTwoCellLoopbackCoord(t)
    defer coord.Stop()

    registerTestKindOn(t, cellA.Stage)
    registerTestKindOn(t, cellB.Stage)

    // Spawn live on cell A.
    a := mmokit.Spawn(cellA.Stage, testKindID, mmokit.Pos{X: 0, Y: 0})

    // Push a border replica of a onto cell B.
    pushBorderReplicaTo(t, cellA.Stage, cellB.Stage, a.NetID())

    // Register handler on BOTH cells (Handle is per-stage, so register on each).
    var aDealt, bDealt float32
    mmokit.Handle[crossDamage](cellA.Stage, func(target mmokit.Entity, msg *crossDamage) {
        msg.Dealt = msg.Amount
        aDealt = msg.Amount
    })
    mmokit.Handle[crossDamage](cellB.Stage, func(target mmokit.Entity, msg *crossDamage) {
        bDealt = msg.Amount
        msg.Dealt = -1 // sentinel: this cell should NOT run the handler
    })

    // Lookup the replica from cell B's perspective.
    eOnB := mmokit.EntityByNetID(cellB.Stage, a.NetID())
    if !eOnB.Alive() {
        t.Fatal("replica should resolve as Alive on cell B")
    }

    // Send from cell B — should route cross-cell to cell A.
    eOnB.Send(&crossDamage{Amount: 25})

    // Drain the loopback bridge so the action arrives on cell A.
    drainBridge(t, coord, 100*time.Millisecond)

    if aDealt != 25 {
        t.Fatalf("cell A handler did not run: aDealt=%v, want 25", aDealt)
    }
    if bDealt != 0 {
        t.Fatalf("cell B handler ran (bDealt=%v, want 0) — Send should have been routed to A only", bDealt)
    }
}
```

(`newTwoCellLoopbackCoord`, `pushBorderReplicaTo`, `drainBridge` are test helpers — base them on existing patterns in `pkg/universe/border_replication_apply_test.go` and `pkg/universe/loopback_bridge.go`.)

- [ ] **Step 2: Implement helpers in testutil**

Add the three helpers to `pkg/mmokit/testutil_test.go`. `newTwoCellLoopbackCoord` constructs a `pkguniverse.Coordinator` with a 2-cell config + loopback bridge. `pushBorderReplicaTo` calls `cellA.Stage.scanEntityComponents(...)` + `cellB.Stage.upsertBorderReplica(...)` directly. `drainBridge` polls the loopback bridge's inbox and processes messages until either drained or timeout.

- [ ] **Step 3: Run the cross-cell test**

Run: `go test ./pkg/mmokit/ -run TestCrossCellSend -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/messaging_cross_cell_test.go pkg/mmokit/testutil_test.go
git commit -m "test(mmokit): cross-cell Send routes to authoritative cell via bridge"
```

---

## Phase 7: Tick callbacks

### Task 7.1: `OnWorldTick` — once-per-tick global callback

**Files:**
- Create: `pkg/mmokit/tick.go`
- Test: `pkg/mmokit/tick_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/mmokit/tick_test.go
package mmokit_test

import (
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
)

func TestOnWorldTick_FiresOncePerTick(t *testing.T) {
    stage, eng := newTestStage(t)
    var ticks int
    var lastDt float32
    mmokit.OnWorldTick(stage, func(dt float32) {
        ticks++
        lastDt = dt
    })
    runTicks(t, eng, stage, 3)  // helper: drives 3 ticks
    if ticks != 3 {
        t.Fatalf("ticks = %d, want 3", ticks)
    }
    if lastDt <= 0 {
        t.Fatalf("dt should be positive, got %v", lastDt)
    }
}
```

- [ ] **Step 2: Implement OnWorldTick + runTicks helper**

```go
// pkg/mmokit/tick.go
package mmokit

import pkguniverse "github.com/zenion/mmokit/pkg/universe"

// OnWorldTick registers fn to fire once per simulation tick on the stage.
// Fires before per-entity callbacks (OnTick / OnTickEach). Use for
// stage-wide bookkeeping that doesn't iterate entities.
func OnWorldTick(stage *pkguniverse.Stage, fn func(dt float32)) {
    stage.RegisterTickCallback(fn)
}
```

Add to `pkg/universe/stage.go`:

```go
// RegisterTickCallback adds fn to the per-tick callback list. Called by the
// engine's game-loop adapter once per tick (before per-entity callbacks).
func (s *Stage) RegisterTickCallback(fn func(dt float32)) {
    s.tickCallbacks = append(s.tickCallbacks, fn)
}

// TickCallbacks returns the registered list (called by the loop driver).
func (s *Stage) TickCallbacks() []func(dt float32) {
    return s.tickCallbacks
}
```

Add `tickCallbacks []func(dt float32)` to Stage.

Find the engine's tick driver (likely `engine.GameLoop.Tick`) and ensure it calls each registered callback once per tick. Add the call site in the game-loop adapter — likely right before or after `engine.Systems` run.

- [ ] **Step 3: Add `runTicks` helper**

In `pkg/mmokit/testutil_test.go`:

```go
func runTicks(t *testing.T, eng *engine.Engine, stage *pkguniverse.Stage, n int) {
    t.Helper()
    for i := 0; i < n; i++ {
        const dt = float32(1.0 / 20.0)
        for _, fn := range stage.TickCallbacks() {
            fn(dt)
        }
    }
}
```

- [ ] **Step 4: Run test**

Run: `go test ./pkg/mmokit/ -run TestOnWorldTick -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/tick.go pkg/mmokit/tick_test.go pkg/mmokit/testutil_test.go pkg/universe/stage.go
git commit -m "feat(mmokit): OnWorldTick — per-tick stage-wide callback"
```

---

### Task 7.2: `OnTick[T]` — per-entity tick for entities with component T

**Files:**
- Modify: `pkg/mmokit/tick.go`
- Test: `pkg/mmokit/tick_test.go`

- [ ] **Step 1: Write the test**

```go
type tickComp struct{ N int }

func TestOnTick_FiresPerEntityWithComponent(t *testing.T) {
    stage, eng := newTestStage(t)
    registerTestKind(t, stage)
    a := mmokit.Spawn(stage, testKindID, mmokit.Pos{})
    b := mmokit.Spawn(stage, testKindID, mmokit.Pos{})
    mmokit.Set[tickComp](a, tickComp{N: 0})
    mmokit.Set[tickComp](b, tickComp{N: 0})

    mmokit.OnTick[tickComp](stage, func(e mmokit.Entity, dt float32) {
        c := mmokit.Get[tickComp](e)
        c.N++
    })
    runTicks(t, eng, stage, 5)

    if mmokit.Get[tickComp](a).N != 5 {
        t.Fatalf("a.N = %d, want 5", mmokit.Get[tickComp](a).N)
    }
    if mmokit.Get[tickComp](b).N != 5 {
        t.Fatalf("b.N = %d, want 5", mmokit.Get[tickComp](b).N)
    }
}
```

- [ ] **Step 2: Implement**

Append to `pkg/mmokit/tick.go`. We delegate to `pkg/query.NewQuery` rather than touching ark directly — `pkg/query` already wraps ark v0.7.1 idioms for us, and using it here keeps mmokit's dependency on ark minimal.

```go
import (
    "github.com/zenion/mmokit/pkg/query"
)

// OnTick registers fn to fire once per tick for every entity with component
// T on the stage. fn receives an Entity bound to the stage and the tick dt.
func OnTick[T any](stage *pkguniverse.Stage, fn func(e Entity, dt float32)) {
    type bundleT struct{ _ *T }
    w := stage.ECSWorld()
    q := query.NewQuery[bundleT](stageQueryAdapter{world: w})
    OnWorldTick(stage, func(dt float32) {
        for h := range q.Iter {
            fn(EntityFromECS(stage, h), dt)
            _ = h
        }
    })
}
```

If the `_ *T` zero-field-name trick in `bundleT` doesn't compile (Go's anonymous struct treatment of `_`), promote `T` to a named exported field; the field name is not used, only the type list:

```go
type bundleT struct{ X *T }
```

`stageQueryAdapter` is added in Task 7.3 — when implementing this task, also add the adapter even though the bundle is single-component:

```go
type stageQueryAdapter struct{ world *ecs.World }
func (a stageQueryAdapter) ECSWorld() *ecs.World { return a.world }
```

- [ ] **Step 3: Run test**

Run: `go test ./pkg/mmokit/ -run TestOnTick -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/tick.go pkg/mmokit/tick_test.go
git commit -m "feat(mmokit): OnTick[T] per-entity tick callback"
```

---

### Task 7.3: `OnTickEach[Bundle]` — bundle-typed per-entity tick

**Files:**
- Modify: `pkg/mmokit/tick.go`
- Test: `pkg/mmokit/tick_test.go`

- [ ] **Step 1: Write the test**

```go
type tickAccum struct{ Acc float32 }
type tickRate struct{ Rate float32 }

func TestOnTickEach_BundleAccess(t *testing.T) {
    stage, eng := newTestStage(t)
    registerTestKind(t, stage)
    e := mmokit.Spawn(stage, testKindID, mmokit.Pos{})
    mmokit.Set[tickAccum](e, tickAccum{Acc: 0})
    mmokit.Set[tickRate](e, tickRate{Rate: 2})

    type bundle struct {
        A *tickAccum
        R *tickRate
    }
    mmokit.OnTickEach[bundle](stage, func(e mmokit.Entity, b *bundle, dt float32) {
        b.A.Acc += b.R.Rate * dt
    })
    runTicks(t, eng, stage, 4)  // 4 ticks of dt=1/20

    expected := float32(2 * 4 * (1.0 / 20.0))
    got := mmokit.Get[tickAccum](e).Acc
    if got != expected {
        t.Fatalf("Acc = %v, want %v", got, expected)
    }
}
```

- [ ] **Step 2: Implement**

```go
import "github.com/zenion/mmokit/pkg/query"

// OnTickEach registers fn to fire once per tick for every entity that
// matches the bundle B. B must be a struct whose exported fields are all
// pointers to component types (matching pkg/query.Query[B] semantics).
func OnTickEach[B any](stage *pkguniverse.Stage, fn func(e Entity, b *B, dt float32)) {
    w := stage.ECSWorld()
    q := query.NewQuery[B](stageQueryAdapter{stage: stage, world: w})
    OnWorldTick(stage, func(dt float32) {
        for h, b := range q.Iter {
            fn(EntityFromECS(stage, h), b, dt)
        }
    })
}

// stageQueryAdapter satisfies query.NewQuery's interface (interface{ECSWorld() *ecs.World}).
type stageQueryAdapter struct {
    stage *pkguniverse.Stage
    world *ecs.World
}

func (a stageQueryAdapter) ECSWorld() *ecs.World { return a.world }
```

- [ ] **Step 3: Run test**

Run: `go test ./pkg/mmokit/ -run TestOnTickEach -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/tick.go pkg/mmokit/tick_test.go
git commit -m "feat(mmokit): OnTickEach[Bundle] bundle-typed per-entity tick"
```

---

## Phase 8: Escape hatch + docs

### Task 8.1: `RawWorld(stage)` named escape hatch

**Files:**
- Create: `pkg/mmokit/raw.go`
- Test: `pkg/mmokit/raw_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/mmokit/raw_test.go
package mmokit_test

import (
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
)

func TestRawWorld_ReturnsECSWorld(t *testing.T) {
    stage, _ := newTestStage(t)
    w := mmokit.RawWorld(stage)
    if w == nil {
        t.Fatal("RawWorld returned nil")
    }
    if w != stage.ECSWorld() {
        t.Fatal("RawWorld should return stage.ECSWorld()")
    }
}
```

- [ ] **Step 2: Implement**

```go
// pkg/mmokit/raw.go
//
// Escape hatch: direct ECS access. Use ONLY when OnTickEach / Get / Has /
// Set cannot express the iteration or perf-critical pattern. Naming this
// RawWorld (vs ECSWorld or World) makes escape-hatch usage trivially
// grep-able in code review.
package mmokit

import (
    "github.com/mlange-42/ark/ecs"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

func RawWorld(stage *pkguniverse.Stage) *ecs.World {
    return stage.ECSWorld()
}
```

- [ ] **Step 3: Run test**

Run: `go test ./pkg/mmokit/ -run TestRawWorld -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/raw.go pkg/mmokit/raw_test.go
git commit -m "feat(mmokit): RawWorld(stage) escape hatch"
```

---

### Task 8.2: Package documentation in `doc.go`

**Files:**
- Create: `pkg/mmokit/doc.go`

- [ ] **Step 1: Write doc.go**

```go
// Package mmokit is the game-facing facade for the mmokit MMO framework.
//
// The framework reduces game development to five primitives:
//
//   1. Entity   — a value handle that's transparent across cell boundaries.
//   2. Get/Set  — generic component access on an entity.
//   3. Spawn    — create entities of a registered kind at a position.
//   4. Nearby   — iterate entities within a radius on the spatial grid.
//   5. Send     — deliver a typed message to an entity, regardless of which
//                 cell currently owns it. Handlers register via Handle[M].
//
// Per-tick game logic uses OnTickEach[Bundle] (or OnTick[T] / OnWorldTick)
// rather than custom system structs with Query fields. Direct ECS access
// (RawWorld) is available as a labeled escape hatch.
//
// See docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
// for the full design rationale.
//
// Example: deal damage that works regardless of cell boundaries.
//
//	type Damage struct {
//	    Amount float32
//	    Source mmokit.Entity
//	    Dealt  float32  // result, mutated by handler
//	}
//
//	mmokit.Handle[Damage](stage, func(target mmokit.Entity, msg *Damage) {
//	    h := mmokit.Get[Health](target); if h == nil { return }
//	    h.Current -= msg.Amount
//	    msg.Dealt = msg.Amount
//	})
//
//	// Anywhere in game code:
//	target.Send(Damage{Amount: 25, Source: caster})
package mmokit
```

- [ ] **Step 2: Verify it builds**

Run: `go vet ./pkg/mmokit/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/doc.go
git commit -m "docs(mmokit): package overview describing the new five primitives"
```

---

## Phase 9: Integration test

### Task 9.1: End-to-end damage flow (single-cell + cross-cell)

**Files:**
- Create: `pkg/mmokit/integration_damage_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/mmokit/integration_damage_test.go
//
// End-to-end demonstration of the foundation API: Damage as its own
// message type, applied via Send, mutating Health, broadcasting via the
// cross-cell mechanism. No game-side cross-cell awareness anywhere.
package mmokit_test

import (
    "testing"
    "time"
    "github.com/zenion/mmokit/pkg/mmokit"
)

type intHealth struct{ Current, Max float32 }
type intDamage struct {
    Amount float32
    Dealt  float32
}

const intKind mmokit.KindID = 200

func TestIntegration_Damage_SameCell(t *testing.T) {
    stage, _ := newTestStage(t)
    registerKindWithComponents(t, stage, intKind, "IntKind",
        intHealth{}, intDamage{})

    // Register the damage handler.
    mmokit.Handle[intDamage](stage, func(target mmokit.Entity, msg *intDamage) {
        h := mmokit.Get[intHealth](target); if h == nil { return }
        h.Current -= msg.Amount
        msg.Dealt = msg.Amount
    })

    target := mmokit.Spawn(stage, intKind, mmokit.Pos{})
    mmokit.Set[intHealth](target, intHealth{Current: 100, Max: 100})

    target.Send(&intDamage{Amount: 25})

    if got := mmokit.Get[intHealth](target).Current; got != 75 {
        t.Fatalf("Health.Current = %v, want 75", got)
    }
}

func TestIntegration_Damage_CrossCell(t *testing.T) {
    coord, cellA, cellB := newTwoCellLoopbackCoord(t)
    defer coord.Stop()
    registerKindWithComponents(t, cellA.Stage, intKind, "IntKind",
        intHealth{}, intDamage{})
    registerKindWithComponents(t, cellB.Stage, intKind, "IntKind",
        intHealth{}, intDamage{})

    // Authoritative on cell A. Replica gets pushed to cell B.
    mmokit.Handle[intDamage](cellA.Stage, func(target mmokit.Entity, msg *intDamage) {
        h := mmokit.Get[intHealth](target); if h == nil { return }
        h.Current -= msg.Amount
        msg.Dealt = msg.Amount
    })

    target := mmokit.Spawn(cellA.Stage, intKind, mmokit.Pos{})
    mmokit.Set[intHealth](target, intHealth{Current: 100, Max: 100})

    pushBorderReplicaTo(t, cellA.Stage, cellB.Stage, target.NetID())

    // Caller on cell B has the replica; Send routes to cell A.
    eOnB := mmokit.EntityByNetID(cellB.Stage, target.NetID())
    eOnB.Send(&intDamage{Amount: 25})

    drainBridge(t, coord, 100*time.Millisecond)

    if got := mmokit.Get[intHealth](target).Current; got != 75 {
        t.Fatalf("after cross-cell Send: Health.Current = %v, want 75", got)
    }
}
```

- [ ] **Step 2: Run integration tests**

Run: `go test ./pkg/mmokit/ -run TestIntegration_Damage -v`
Expected: BOTH PASS.

- [ ] **Step 3: Run the full mmokit + universe test suites**

Run: `go test ./pkg/mmokit/... ./pkg/universe/...`
Expected: ALL PASS.

- [ ] **Step 4: Run go vet on the whole module**

Run: `go vet ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/integration_damage_test.go
git commit -m "test(mmokit): end-to-end damage flow (same-cell + cross-cell)"
```

---

## Closeout

After all tasks are complete:

- [ ] Run the full test suite one more time end-to-end:

```bash
go test ./pkg/... ./internal/...
```

Expected: ALL PASS, including all pre-existing tests (the foundation adds new surface, doesn't change existing behavior).

- [ ] Verify `go vet ./...` is clean.

- [ ] Verify `just build` succeeds.

- [ ] Update the spec's §10 migration plan to mark Step 1+2 as landed:

In `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md`, prefix the migration step entries with `[done]` for the foundation work.

```bash
git add docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
git commit -m "docs(spec): mark foundation API + cross-cell routing as landed"
```

- [ ] Push the branch and open a PR (or merge to main per the solo-developer convention).

---

## Out-of-scope / not in this plan

- Migrating any existing game code (`internal/game/`) to the new API. The old `gw.eng.ECS.Alive`, `gw.C.X.Get`, `Bridge.SendAction` direct callers, `HandleCrossCellAction` switch, `action_codec.go`, `SideEffectRegistry`, `MarshalDamageAction`/etc. all remain.
- AoI auto-anchored client broadcast for visuals — Plan C.
- `ServerOnly` marker enforcement — Plan D (folded with C).
- Damage→Death→KillCredit composition example as actual game code — Plan E.
- Migrating remaining verbs (mining, status effects, target lock, etc.) — Plan F.
- Mechanical replacement of old API across systems — Plan G.
- Deleting old API surfaces — Plan H.
- Migrating client input handling to use Send — Plan I.

Each subsequent plan is independently revertible and the codebase remains green between plans.
