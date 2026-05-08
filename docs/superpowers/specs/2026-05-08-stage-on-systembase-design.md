# Stage on SystemBase — Design

**Date:** 2026-05-08
**Status:** Draft, awaiting user review

## Motivation

A game system is the natural unit for managing entity lifecycles — it ticks every frame, it owns simulation behavior, and it's where spawning, despawning, and querying belong. Today, mmokit forces game authors to reach `*Stage` (the per-cell entity API) through a typed-world detour: either declare a custom struct with `*Stage` embedded and parameterize every system on it (`mmokit.SystemBase[*MyWorld]`), or use the `Config.OnInit` escape hatch which fires once at cell construction outside any system.

Both paths are needless ceremony for what is fundamentally "this system needs to spawn entities." Concrete pain:

1. **The "World" abstraction has outlived its purpose.** The 2026-04-27 stage-and-composable-state design landed `Stage`, `AddState[T]`, `RegisterKind[T]`, `OnPlayerJoin/Leave`, but stopped short of removing `Config.World` and the `W` type parameter on `SystemBase`. The result: two patterns coexist (typed monolithic world vs. composable state plugins) with no clear winner.
2. **`Config.OnInit` is a Config-level escape hatch for what should be system-owned.** Bootstrap entity setup belongs in a system's `Init()`, not in a hook on the configuration struct.
3. **The `*onInitWorld` wrapper exists solely to give `OnInit` a place to fire** — an unexported type that wraps `Stage` purely for lifecycle plumbing. Pure scaffolding.
4. **`SystemBase[*MyWorld]` couples every system to a single God-world type.** Multiple game subsystems (combat, market, AI) get crammed into one struct because that's the only typed handle a system can hold.
5. **`pkg/system` internals don't use the `W` parameter at all** — every generic system embeds `engine.SystemBase[any]`. The W generic is vestigial inside the engine.

After this design, every game system trivially gets `*Stage` access, typed game state plugs in declaratively, and three layers of indirection (Config.OnInit, Config.World, the W parameter) collapse into one direct path.

## Goals

- A game system embeds `mmokit.SystemBase` (no generic) and gets `s.Stage()` access for free.
- Typed per-cell game state attaches to a system declaratively via `mmokit.StateRef[T]` field discovery — same shape as the existing `mmokit.Query[T]` pattern.
- Bootstrap (one-shot per-cell setup) is a system's `Init()`. No Config-level lifecycle hook.
- `Config.OnInit`, `Config.World`, the `*onInitWorld` wrapper, the `GameWorld`/`BoundaryWorld` interfaces, the `W` type parameter on both `engine.SystemBase` and `mmokit.SystemBase`, and the `WorldOf[T]`/`WorldOfCell[T]` helpers are all deleted in lockstep.
- Default no-op `Update(dt)` so systems that only need `Init()` can omit `Update` entirely.
- No backward compatibility shims. Direct cutover.

## Non-goals

- Renaming `Stage`, `Cell`, or any meshing concept. Those are settled.
- Decomposing `internal/game`'s monolithic `GameWorld` into multiple state plugins. The C-full migration moves it to a single `AddState[GameWorld]` registration; finer decomposition is a separate refactor.
- Adding new lifecycle hooks (`OnStageReady`, etc.). Everything one-shot becomes a system's `Init()`.
- Changing `mmokit.AddState[T]` / `mmokit.State[T]` semantics — they stay as-is.
- Touching `pkg/system` internal systems beyond the trivial signature change (`engine.SystemBase[any]` → `engine.SystemBase`).

## Design

### 1. Public API surface

After the change, three patterns cover every game system.

**(i) Pure system — simulation only:**
```go
type SineWaveSystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct{ Pos *mmokit.Position }]
}

func (s *SineWaveSystem) Update(dt float32) {
    for _, b := range s.entities {
        b.Pos.Y = float32(math.Sin(/*...*/))
    }
}
```

**(ii) Bootstrap system — replaces `Config.OnInit`:**
```go
type FieldSpawnerSystem struct{ mmokit.SystemBase }

func (s *FieldSpawnerSystem) Init() {
    for i := range 60 {
        s.Stage().SpawnEntity(mmokit.Position{X: float32(i) * 20, Y: 0})
    }
}
// no Update — default no-op via embedded SystemBase
```

**(iii) System with typed game state — replaces `SystemBase[*MyWorld]`:**
```go
type CombatSystem struct {
    mmokit.SystemBase
    Game    mmokit.StateRef[GameWorld]
    targets mmokit.Query[struct{ Health *Health }]
}

func (s *CombatSystem) Update(dt float32) {
    for e, b := range s.targets {
        if b.Health.HP <= 0 {
            delete(s.Game.PlayerEntities, /*...*/)
            s.Stage().MarkForRemoval(e)
        }
    }
}
```

**Top-level `main.go`:**
```go
mmo := mmokit.New(mmokit.Config{
    /* no OnInit, no World */
})

mmokit.AddState(mmo, func(*mmokit.Stage) *GameWorld { return NewGameWorld() })

mmo.AddSystem(mmokit.NewSystem(&FieldSpawnerSystem{}))
mmo.AddSystem(mmokit.NewSystem(&CombatSystem{}))
mmo.Start()
```

Three things to notice:
- `mmokit.SystemBase` is no longer parameterized (no `[W]`, no `[*Stage]`, no `[any]`).
- `s.Stage()` is always available, regardless of what the system needs.
- Typed state plugs in via `mmokit.StateRef[T]` field — declarative, type-safe, autocomplete-friendly. No tags, no `Init()` boilerplate.

### 2. Type-level changes

**`pkg/engine/system.go` — SystemBase loses its generic and its world plumbing:**
```go
type SystemBase struct {
    ecsWorld *ecs.World
    eng      *Engine
    queries  []queryBuildable
}

func (b *SystemBase) ECSWorld() *ecs.World { return b.ecsWorld }
func (b *SystemBase) Engine() *Engine     { return b.eng }
func (b *SystemBase) Init()                { /* default no-op */ }
func (b *SystemBase) Update(dt float32)    { /* default no-op — new */ }
func (b *SystemBase) BindQueries(outer any) { /* unchanged */ }
func (b *SystemBase) BuildQueries()         { /* unchanged */ }

func (b *SystemBase) SetDeps(w *ecs.World, eng *Engine) {
    b.ecsWorld = w
    b.eng = eng
}
```

Removed: `World()`, `GameWorld()`, the `world W` field, the `gw any` parameter on `SetDeps`, the `gw.(W)` assertion + panic, all the W-specific tests.

Added: a default no-op `Update(dt float32)` so Update becomes optional via embedding (mirrors the existing default `Init()`).

**`pkg/mmokit/system.go` — becomes a real wrapper struct (was a type alias):**
```go
type SystemBase struct {
    engine.SystemBase
    stage     *universe.Stage
    stateRefs []stateBuildable // populated in BindStateFields
}

func (b *SystemBase) Stage() *universe.Stage { return b.stage }

// InitStage is called by the universe framework after SetDeps.
// Game code should never call this directly.
func (b *SystemBase) InitStage(s *universe.Stage) { b.stage = s }

// BindStateFields discovers StateRef[T] fields on the outer system struct
// via reflection and populates each by looking up its T against the AddState
// registry on the stage. Called by the framework after InitStage.
func (b *SystemBase) BindStateFields(outer any) {
    v := reflect.ValueOf(outer).Elem()
    for i := 0; i < v.NumField(); i++ {
        fp := unsafe.Pointer(v.Field(i).UnsafeAddr())
        field := reflect.NewAt(v.Type().Field(i).Type, fp).Interface()
        if sb, ok := field.(stateBuildable); ok {
            sb.buildFromStage(b.stage)
            b.stateRefs = append(b.stateRefs, sb)
        }
    }
}
```

**`pkg/mmokit/state.go` — `StateRef[T]` field marker:**
```go
type StateRef[T any] struct {
    *T
}

// stateBuildable is satisfied only by *StateRef[T] in pkg/mmokit (unexported
// method). Same locked-down pattern as queryBuildable.
type stateBuildable interface {
    buildFromStage(s *universe.Stage)
}

func (r *StateRef[T]) buildFromStage(s *universe.Stage) {
    r.T = State[T](s) // panics if T was never registered via AddState
}
```

`mmokit.State[T](stage)` (the existing free-function lookup) stays, for callers without a `SystemBase` (lifecycle hooks, command handlers).

**`pkg/universe/stage.go` — Stage loses its world field/accessors:**
- Field removed: `world any`
- Methods removed: `GameWorld() any`, `SetGameWorld(any)`

**Interfaces deleted:**
- `universe.GameWorld` (the per-game contract)
- `universe.BoundaryWorld` (vestigial after W-removal — Stage is the only implementor and the interface only existed to fill the type parameter)
- `mmokit.WorldOf[T]` and `mmokit.WorldOfCell[T]` helpers (replaced by `mmokit.State[T]`)

### 3. Universe wiring

The heart of the change is `pkg/universe/coordinator.go` around lines 2150–2245.

**Before:**
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

for _, def := range c.systemDefs {
    sys := def.Factory()
    if di, ok := sys.(depsInjectable); ok {
        di.SetDeps(eng.ECS, eng, world) // 3-arg, with gw=world
    }
    if qb, ok := sys.(queryBinder); ok {
        qb.BindQueries(sys)
    }
}
// later: world.Init() — where OnInit fired
```

**After:**
```go
// no world wrapping at all; state factories run as today

for _, def := range c.systemDefs {
    sys := def.Factory()
    if di, ok := sys.(depsInjectable); ok {
        di.SetDeps(eng.ECS, eng) // 2-arg, no gw
    }
    if si, ok := sys.(stageInjectable); ok {
        si.InitStage(base) // mmokit.SystemBase only
    }
    if sf, ok := sys.(stateFieldBinder); ok {
        sf.BindStateFields(sys) // mmokit.SystemBase only
    }
    if qb, ok := sys.(queryBinder); ok {
        qb.BindQueries(sys)
    }
}
// no world.Init() — Config.OnInit is gone, system.Init() takes over
```

**Wiring interfaces (defined in pkg/universe):**
```go
type depsInjectable interface {
    SetDeps(w *ecs.World, eng *engine.Engine)
}
type stageInjectable interface {
    InitStage(s *Stage)
}
type stateFieldBinder interface {
    BindStateFields(outer any)
}
```

`engine.SystemBase` satisfies the first. `mmokit.SystemBase` satisfies all three. Universe checks each interface independently — engine-layer systems quietly skip the stage and state-binding steps.

**Wiring order matters:** `SetDeps` → `InitStage` → `BindStateFields` → `BindQueries`. State binding must run after `InitStage` (it reads `b.stage`) and before `BindQueries` (so `Init()` can read both state and queries).

**`Cell` struct (`pkg/universe/cell.go`)** loses its `World GameWorld` field. All `cell.World.X` callers become `cell.Stage.X` (Stage already has every method `GameWorld` exposed).

**Config plumbing:** `coordinator.go` and `host.go` lose the `onInit func(*Stage)` field, the `worldFactory func(*Stage) GameWorld` field, the `*onInitWorld` wrapper struct + its `Init()` method, the mutual-exclusion panic at line 1554, and the `BoundaryWorld` type-check at line 2256 (Stage always satisfies it now).

**`BoundarySystem` rewire:** today it does `engine.SystemBase[BoundaryWorld]`. After: `engine.SystemBase` (no generic) plus a private `stage *Stage` field set directly during construction by the universe-internal wiring (BoundarySystem is a universe-internal system — direct field assignment is the right pattern, not the public InitStage interface). All `s.World()` calls inside `BoundarySystem.Update` become `s.stage`.

### 4. Default no-op `Update(dt)`

`engine.SystemBase` gets a default `Update(dt float32) {}`. Embedded method promotion means any system embedding `mmokit.SystemBase` (which embeds `engine.SystemBase`) inherits the no-op. Outer types that define their own `Update(dt float32)` override via Go's method resolution.

This makes bootstrap systems concise:
```go
type FieldSpawnerSystem struct{ mmokit.SystemBase }
func (s *FieldSpawnerSystem) Init() { /* spawn entities */ }
// no Update needed
```

`Init()` already had a default no-op (current behavior); this adds the symmetric default for `Update`.

### 5. Deletion inventory

Comprehensive list of what gets ripped out:

**`pkg/engine/`**
- `system.go`: `[W any]` generic, `world W` field, `World()`, `GameWorld()`, `gw any` param on `SetDeps`, `gw.(W)` assertion + panic
- `system_test.go`: `TestSystemBase_TypedWorld`, `TestSystemBase_TypeMismatch_Panics`, `TestSystemBase_NilGameWorld_OK_ForAny`, `TestSystemBase_NilGameWorld_OK_ForPointer`. Adapt `TestSystemBase_AutoBindsQueries` and `TestSystemBase_DefaultExclusions` to the new signature.

**`pkg/mmokit/`**
- The `type SystemBase[W any] = engine.SystemBase[W]` alias (replaced by the wrapper struct in §2)
- `WorldOf[T]` and `WorldOfCell[T]` helpers and their tests

**`pkg/universe/`**
- `world.go`: `GameWorld` interface, `BoundaryWorld` interface
- `boundary_system.go`: `engine.SystemBase[BoundaryWorld]` → `engine.SystemBase` + private `stage *Stage` field; `s.World()` → `s.stage`
- `stage.go`: `world any` field, `GameWorld()` method, `SetGameWorld()` method
- `cell.go`: `Cell.World GameWorld` field
- `coordinator.go`: `Config.OnInit` field, `Config.World` field, mutual-exclusion panic at line 1554, `c.onInit` and `c.worldFactory` fields on `Process`, world-creation block (lines 2151–2162), `*onInitWorld` wrapper struct + `Init()` method, `world.Init()` call site, `BoundaryWorld` type-check at line 2256
- `host.go`: `onInit` field, `worldFactory` field, both propagation lines (`h.onInit = c.onInit` etc.)
- `coordinator_test.go`: `TestConfigOnInitRunsOnceAfterConstruction` and the mutual-exclusion panic test (replaced by `TestSystemInitRunsOnceAfterConstruction`)
- `partition_test.go`, `roles_test.go`, `auth_cookie_e2e_test.go` and other tests using `Config.OnInit`/`Config.World`: migrate to a one-shot bootstrap system or delete the no-op closure if scaffolding only.

**Net diff:** ~250 lines of typed-world plumbing, wrappers, mutual-exclusion checks, and the full `GameWorld`/`BoundaryWorld` interface contracts → replaced by three new methods (`mmokit.SystemBase.Stage`, `InitStage`, `BindStateFields`) and one new marker type (`mmokit.StateRef[T]`). Plus the default `Update` no-op.

### 6. Migration impact

#### `examples/simple` (small)
- Move the `Config.OnInit` body into a new `system_field_spawner.go`:
  ```go
  type FieldSpawnerSystem struct{ mmokit.SystemBase }
  func (s *FieldSpawnerSystem) Init() { /* spawn 60 entities via s.Stage().SpawnEntity */ }
  ```
- `main.go`: drop `Config.OnInit`; `process.AddSystem(mmokit.NewSystem(&FieldSpawnerSystem{}))` before `SineWaveSystem`.
- `system_sinewave.go`: `mmokit.SystemBase[any]` → `mmokit.SystemBase`. No other change.

#### `examples/4node-basic` (small)
- One file, `system_bots.go`: `mmokit.SystemBase[*mmokit.Stage]` → `mmokit.SystemBase`. Replace `s.World()` (returned `*Stage`) with `s.Stage()`. Done.

#### `internal/game` (the real work)

**`gameworld.go`:**
- `GameWorld` stops embedding `*mmokit.Stage`. Adds an unexported `stage *mmokit.Stage` field set by the `AddState` factory. Internal `GameWorld` methods that today call `gw.Spawn(...)` via embedding become `gw.stage.Spawn(...)`.
- `factory.go` swaps `Config.World = func(stage) GameWorld { return NewGameWorld(stage) }` for:
  ```go
  mmokit.AddState(mmo, func(stage *mmokit.Stage) *GameWorld {
      return NewGameWorld(stage)
  })
  ```

**~13 game systems (`system_*.go`):**
- Type change: `mmokit.SystemBase[*GameWorld]` → `mmokit.SystemBase` everywhere.
- Add `Game mmokit.StateRef[GameWorld]` field on each system that needs game state. The framework auto-populates via `BindStateFields`; no `Init()` boilerplate.
- All call sites:
  - `s.World().X` where X is a **Stage method** (e.g. `Spawn`, `MarkForRemoval`, `SendEvent`) → `s.Stage().X`
  - `s.World().X` where X is a **GameWorld field** (e.g. `PlayerEntities`, `PlayerDB`, `Config`) → `s.Game.X` (field promotion via embedded `*GameWorld`)

This is mechanical. The implementation plan drives the rewrite with two pre-built lists — every Stage method name, every GameWorld field name — and dispatches each callsite by name match. No free-form sed.

**`OnPlayerJoin`/`OnPlayerLeave` hooks** already take `(*PlayerSession, *Stage)` — they switch from `gw.X` to `mmokit.State[GameWorld](stage).X` (or grab once at the top of the hook). Stays as the explicit free-function path for non-system callers.

**Volume estimate:** ~13 system files × 5–30 callsites each = a few hundred mechanical edits. Plus `gameworld.go` constructor reshape. No behavioral change.

#### `pkg/system/` internals
- Every `engine.SystemBase[any]` → `engine.SystemBase`. Trivial rename across ~5 files (physics.go, click_to_move.go, direction_move.go, lifetime.go, spatial_system.go).

#### `pkg/universe/` tests
- `coordinator_test.go`, `partition_test.go`, `roles_test.go`, `auth_cookie_e2e_test.go`, others: typical pattern is `Config.OnInit: func(*Stage){}` to satisfy required-field — delete the line. Tests that meaningfully exercise OnInit firing migrate to a one-shot bootstrap system.

## Implementation surface

New code in `pkg/mmokit/`:
- `system.go` (~40 lines): wrapper struct over `engine.SystemBase`, with `Stage()`, `InitStage()`, and `BindStateFields()`.
- `state.go` additions (~25 lines): `StateRef[T]` marker type, `stateBuildable` interface, `buildFromStage` method.

New tests:
- `pkg/mmokit/state_test.go`: `StateRef[T]` discovery + population, panic-when-T-not-registered.
- `pkg/engine/system_test.go`: `TestSystemBase_DefaultUpdateIsNoop`.
- `pkg/universe/coordinator_test.go`: `TestSystemInitRunsOnceAfterConstruction` (replaces the deleted OnInit test).

Modified code:
- `pkg/engine/system.go`: collapse the W generic; add default `Update`.
- `pkg/universe/coordinator.go`: rewire system construction loop (~30-line block replacement).
- `pkg/universe/boundary_system.go`: drop the W parameter; add private stage field.
- `pkg/universe/stage.go`, `cell.go`, `host.go`, `world.go`: deletions.
- All examples and `internal/game`: per §6 migration.

## Tradeoffs

**Compile-time → startup-time check shift.** `SystemBase[*GameWorld]` gives a compiler error if you typo the world type. `mmokit.StateRef[GameWorld]` gives a panic at process start (during `BindStateFields`) if `GameWorld` was never registered with `AddState`. Both are programmer errors, not runtime conditions, but the failure surface moves from compile to first-construction.

**No phased migration.** Callers update in lockstep. Solo-dev / no-backward-compat repo, so the cost is acceptable and matches the project's existing posture.

**Reflection at the boundary.** `BindStateFields` walks struct fields via reflection — same shape as the existing `BindQueries` mechanism, accepted as established. Reflection runs once per system per cell construction; not in hot paths.

**Tag-driven field discovery considered and rejected.** A struct tag (`mmokit:"state"`) was an earlier design candidate. Rejected: the tag is always the same string on every state field (pure noise), it gives the IDE no type information for autocomplete, and a typo is silent until runtime. Marker-type discovery via `StateRef[T]` is type-driven, autocomplete-friendly, and mirrors the established `Query[T]` pattern.

## Risks

1. **`internal/game` mechanical churn (largest risk).** Hundreds of `s.World().X` callsites across 13 systems split into two categories — Stage methods vs GameWorld fields. *Mitigation:* the implementation plan drives this with explicit pre-built lists, dispatching each callsite by name match. No free-form sed.

2. **`BoundarySystem` rewire.** Universe-internal, today does `engine.SystemBase[BoundaryWorld]`. *Mitigation:* spec pins the new shape (private `stage *Stage` field set during construction at `coordinator.go:2256`). Mechanical.

3. **`SerializeEntity` / `SpawnFromTransfer` callsites.** Today these are typed methods on the `GameWorld` interface, called by the mesh transfer machinery. After: direct methods on `*Stage`. *Mitigation:* audit during implementation; any `world.SerializeEntity(...)` becomes `stage.SerializeEntity(...)`.

4. **Tests that validated OnInit firing become tests for nothing.** `TestConfigOnInitRunsOnceAfterConstruction` validated the deleted path. *Mitigation:* replace with `TestSystemInitRunsOnceAfterConstruction` proving the new contract (`System.Init()` fires exactly once per cell).

## Open questions

None blocking.

- Should `engine.SystemBase` keep `Engine()` as a separate accessor when `Stage().Engine()` does the same job? — Yes, keep both. `pkg/system` files use `s.Engine()` directly without going through Stage; not all systems have a Stage.
- Naming: `InitStage(s *Stage)` for the framework-private injection method (alternatives: `SetStage`, `BindStage`). Going with `InitStage` since it mirrors the existing `Init()` lifecycle vocabulary.
