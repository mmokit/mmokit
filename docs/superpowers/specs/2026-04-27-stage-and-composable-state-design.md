# Stage and Composable State — Design

**Date:** 2026-04-27
**Status:** Draft, awaiting user review

## Motivation

The 4node-basic example's `world.go` reads as raw infrastructure scaffolding rather than game code. The same shape appears anywhere a game touches per-cell state: command_bots.go, system_bots.go, future game subsystems. Concrete pain points in the current design:

1. **Component maps are declared twice** — once as `*ecs.Map1[T]` fields on the game's `World` struct, again inside `Init()` via `ecs.NewMap1[T](w)` for `KindComponent` registration.
2. **Entity-kind blocks are imperative recipe code** — `EntityKindDef{...}` + 3-4 `KindComponent(&def, ...)` calls + `RegisterEntityKind(def)`. Two kinds that share most components have no way to express that.
3. **Lifecycle setup is buried** — wiring spawn/despawn requires reaching through `gw.Engine().Players.OnState(StateActive, StateCallbacks{...})` inside `World.Init()`. Three layers of navigation for the most important game hook.
4. **`OnEnter` re-implements the engine's job** — every game writes `Spawn → attach PlayerConn → set name → SendSpawnedMsg`. Three of those four steps are universal.
5. **`OnExit` is pure boilerplate** — the "if alive && !ghost mark for removal, zero `s.Entity`" pattern is identical for every game.
6. **`mmokit.WithComponents()` (no args) is mysterious** — comment says "auto-adds PlayerName, DebugInfo, MoveTarget" but if `WithEntityKind(KindPlayer)` already knows the components, the second call is vestigial.
7. **The "World" abstraction leaks ECS** — "World" is the ark `ecs.World` storage type. The developer's mental model is *a slice of authoritative simulation that owns space, runs systems, and hosts players*. The ECS happens to live inside it.
8. **Per-cell custom state forces one big struct** — if a game has `MarketState` + `AIState` + `EventQueue`, all three get crammed into `type World struct`. They're forced to know about each other; they can't ship as independent packages.

## Goals

- The 4node-basic demo should look like the easiest possible thing a developer can write.
- New APIs land on the existing `*Process` handle (or as free generics), **not as new fields on `mmokit.Config`**.
- One declaration of a typed component bundle should cover three uses: kind registration, query iteration, and spawn-time initialization.
- Per-cell custom state must be **composable across independently-shipped game subsystems** (marketplace, combat, AI) without forcing them through a single struct.
- Rename the developer-facing per-cell type from `World`/`WorldBase` to `Stage` to evoke the actual abstraction (an authoritative simulation surface where actors enter, act, and exit).
- No backward compat shims. Direct cutover.

## Non-goals

- Renaming the internal `pkg/universe/Cell` (topology/ownership unit). Cell stays as the meshing concept; a Cell *holds* a Stage.
- Hiding ark's `ecs.World` from advanced users. The escape hatch (`stage.ECS()`) remains for components that need direct ark access.
- Rewriting the meshing layer or the cmdsys layer. They consume Stage but don't need to change.
- Backwards compatibility with the existing `Config.World` factory pattern. We rip the old API out; downstream examples migrate.

## Design

### 1. The bundle struct: one declaration, three uses

Component bundles are typed structs of `*ComponentType` fields, mirroring the existing `mmokit.Query[T]` shape. Each bundle serves three purposes:

```go
type PlayerComponents struct {
    Name       *PlayerName
    Debug      *DebugInfo
    MoveTarget *mmokit.MoveTarget
}

type BotComponents struct {
    Name       *PlayerName
    Debug      *DebugInfo
    MoveTarget *mmokit.MoveTarget
    Behavior   *BotBehavior
}
```

**Use 1 — Kind registration:**

```go
mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings)
mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot", botBindings)
```

`RegisterKind[T]` reflects on `T`'s fields, registers each as a `KindComponent` on the kind. Per-cell realization (creating `ecs.Map1[FieldType]` against each cell's ark world) happens automatically when the engine instantiates each Stage. Game code never calls `KindComponent` directly.

**Use 2 — Query iteration** (already exists; no change):

```go
type BotSystem struct {
    mmokit.SystemBase[*mmokit.Stage]
    bots mmokit.Query[BotComponents]
}
```

**Use 3 — Spawn-time initialization:**

```go
stage.SpawnPlayer(s,
    mmokit.WithEntityKind(KindPlayer),
    mmokit.WithCollider(PlayerRadius),
    mmokit.Init(func(c *PlayerComponents) { c.Name.Name = s.Username }),
)
```

`mmokit.Init(fn)` is a `SpawnOption`. After the entity is created and components are attached, the engine builds `c *T` (where `T` is inferred from the function argument's type) by reflecting on the bundle struct, populating each field with the corresponding component pointer on the just-spawned entity. Type inference from the function arg means no `[PlayerComponents]` ceremony at the call site.

### 2. `Stage` replaces `WorldBase` as the per-cell handle

```go
package mmokit

// Stage is the authoritative simulation surface for one cell. It hosts
// entities, runs systems, and processes player lifecycle. A Cell (topology)
// holds a Stage (simulation).
type Stage struct {
    // unexported fields: ecs world, spatial grid, player manager, etc.
}

// Public methods — all current WorldBase methods, renamed where they refer to
// "world" or "ECS". Examples:
func (s *Stage) Spawn(pos Position, opts ...SpawnOption) ecs.Entity     // was SpawnEntity
func (s *Stage) SpawnAtLocation(loc Location, opts ...SpawnOption) ecs.Entity
func (s *Stage) SpawnPlayer(session *PlayerSession, opts ...SpawnOption) ecs.Entity
func (s *Stage) MarkForRemoval(e ecs.Entity)
func (s *Stage) SpatialGrid() *HashGrid
func (s *Stage) Cell() universe.CellID  // the cell this stage runs on
func (s *Stage) ECS() *ecs.World         // escape hatch for direct ark access
func (s *Stage) SendEvent(connID uint32, code uint32, msg proto.Message) error
// ... etc
```

`Stage` is a concrete struct, not an interface. The previous `GameWorld` interface is gone — there's nothing for games to implement. The runtime instantiates `*Stage` per cell directly.

`mmokit.WorldBase` and `mmokit.GameWorld` are deleted. All references in the codebase get updated.

`mmokit.WorldOf[T]` / `WorldOfCell[T]` are deleted. Replaced by `mmokit.State[T](stage)` (see §4).

### 3. `SpawnPlayer` collapses the universal spawn ritual

```go
func (s *Stage) SpawnPlayer(session *PlayerSession, opts ...SpawnOption) ecs.Entity
```

Wraps `SpawnAtLocation(session.SpawnLocation, ...)` plus four universal steps:

1. Attach `PlayerConn{ConnID: session.ConnID}`.
2. Assign `session.Entity = e`.
3. Send `SpawnedMsg(session.ConnID, e)` via the engine's connection manager.
4. Apply any `mmokit.Init(...)` options (game-supplied component initialization).

Returns the entity for any further per-game setup (rare — most games will only use `Init` callbacks).

### 4. Composable per-stage state via `AddState[T]` / `State[T]`

Per-cell custom state is a registry of independently-declared typed plugins, instantiated per stage by the runtime.

```go
package mmokit

// AddState registers a factory that produces a typed state value for each Stage
// the runtime creates. Called at process setup time, before Start.
func AddState[T any](mmo *Process, factory func(*Stage) *T)

// State retrieves the typed state previously registered via AddState.
// Panics if T was never registered (programmer error, not runtime concern).
func State[T any](stage *Stage) *T
```

Subsystems package up their state, kinds, systems, and lifecycle hooks behind a single `Install` function:

```go
// internal/marketplace/install.go
package marketplace

func Install(mmo *mmokit.Process) {
    mmokit.AddState(mmo, func(*mmokit.Stage) *State {
        return &State{Orders: orderbook.New()}
    })

    mmokit.RegisterKind[OrderListing](mmo, KindOrder, "Order", orderBindings)

    mmokit.OnPlayerJoin(mmo, func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
        market := mmokit.State[State](stage)
        market.Orders.RestoreFor(s.Username)
    })

    mmo.AddSystem(mmokit.NewSystem(&SettlementSystem{}))
}

// main.go
marketplace.Install(mmo)
combat.Install(mmo)
```

Subsystems compose without touching each other. Order of `Install` calls doesn't matter (kinds, systems, and state factories are registered, not instantiated, until `Start`).

### 5. Lifecycle hooks attach to the Process, not the Stage factory

```go
package mmokit

func OnPlayerJoin(mmo *Process, fn func(*PlayerSession, *Stage))
func OnPlayerLeave(mmo *Process, fn func(*PlayerSession, *Stage))
```

Internally these route to `PlayerManager.OnState(StateActive, ...)` per stage. Multiple registrations are allowed and run in order (lets independent subsystems each install their own hooks).

**Default `OnPlayerLeave` is built in** — the runtime always runs the alive-and-not-ghost → `MarkForRemoval` → zero-`s.Entity` cleanup. Game-supplied `OnPlayerLeave` callbacks run *after* the default cleanup.

### 6. `Config.World` is replaced by `Config.OnStageReady` (rare path)

For the 99% case, the runtime instantiates `*mmokit.Stage` per cell. No game-side factory needed. `Config.World` is deleted.

For exotic cases that need to attach raw state to a stage at construction time without going through `AddState[T]` (very rare — likely only for built-in mmokit packages), an optional `Config.OnStageReady func(*Stage)` hook fires once per stage right after construction. Most games will never use it.

### 7. Drop the redundant `WithComponents()` SpawnOption

`WithEntityKind(K)` already knows the kind's components. After this redesign it auto-attaches them. The vestigial `WithComponents()` (no-args) call is removed.

## Resulting 4node-basic structure

### `examples/4node-basic/world.go`

**Deleted.** No `World` struct, no `NewWorld` factory, no `Init` method. The runtime uses `*mmokit.Stage` directly.

### `examples/4node-basic/components.go`

```go
package main

type PlayerName struct {
    Name string `net:"initial"`
}

type DebugInfo struct {
    AoIRadius float32 `net:"f32"`
}

type BotBehavior struct {
    TicksUntilRetarget uint16
    Mode               uint8
}

// Bundle structs — used for kind registration, query iteration, and spawn init.
type PlayerComponents struct {
    Name       *PlayerName
    Debug      *DebugInfo
    MoveTarget *mmokit.MoveTarget
}

type BotComponents struct {
    Name       *PlayerName
    Debug      *DebugInfo
    MoveTarget *mmokit.MoveTarget
    Behavior   *BotBehavior
}
```

### `examples/4node-basic/main.go` (relevant additions)

```go
mmo := mmokit.New(mmokit.Config{
    InvariantMode:    mmokit.InvariantPanic,
    StrictNetIDIndex: true,
    CellsX: CellsX, CellsY: CellsY, CellSize: CellSize,
    TickRate: TickRate, AoIRadius: AoIRadius,
    StaticFS: webDist, StaticFSPrefix: "web/dist",
    DefaultSpawn: mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
    ExtraMigrations: []mmokit.ExtraMigrationSource{{FS: migrations.FS}},
    LoginHandler: mmokit.HandleLogin(
        uint32(basicpb.ClientEventCode_BCE_LOGIN),
        func(m *basicpb.LoginMsg) (string, any, error) {
            name, err := mmokit.ValidateUsername(m.Name, 20)
            return name, nil, err
        },
    ),
    OnConsoleReady: func(p *mmokit.Process, console *mmokit.Console) {
        registerBotCommands(p, console.Registry())
    },
    Protocol: mmokit.NewProtocol("basic").ClientEvents(func(e *mmokit.ClientEvents) {
        mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
    }),
})

playerBindings := mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true}
mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings)
mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot", playerBindings)

mmokit.OnPlayerJoin(mmo, func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
    stage.SpawnPlayer(s,
        mmokit.WithEntityKind(KindPlayer),
        mmokit.WithCollider(PlayerRadius),
        mmokit.Init(func(c *PlayerComponents) { c.Name.Name = s.Username }),
    )
})

mmo.AddSystem(mmokit.NewInputSystem(...))
// ... rest of systems
mmo.Start()
```

### `examples/4node-basic/system_bots.go`

Unchanged — already uses `mmokit.Query[struct{...}]` with the bundle pattern. The struct literal can optionally be hoisted to `BotComponents` from `components.go` for consistency.

### `examples/4node-basic/command_bots.go`

```go
func spawnBotsOnLoop(stage *mmokit.Stage, count int) int {
    cellSize := mmokit.CellSize()
    minX, minY, maxX, maxY := stage.Cell().WorldBounds(cellSize)
    // ... bounds math ...

    rng := rand.New(rand.NewSource(int64(time.Now().UnixNano() % 1_000_000)))
    for i := range count {
        x, y := /* random within cell */
        tx, ty := /* random target */
        stage.Spawn(
            mmokit.Position{X: x - minX, Y: y - minY},
            mmokit.WithCollider(PlayerRadius),
            mmokit.WithEntityKind(KindBot),
            mmokit.Init(func(c *BotComponents) {
                c.Name.Name = fmt.Sprintf("bot_%s_%06d", stage.Cell(), i)
                mmokit.SetMoveTarget(c.MoveTarget, tx, ty)
                c.Behavior.TicksUntilRetarget = uint16(rng.Intn(100))
            }),
        )
    }
    return count
}
```

Three map lookups → one bundle callback. Same pattern as player spawn.

## Implementation surface

New files in `pkg/mmokit/`:

- `kindreg.go` (~120 lines) — `RegisterKind[T]`, bundle reflection cache, per-cell realization hook called during stage construction.
- `lifecycle.go` (~80 lines) — `OnPlayerJoin`/`OnPlayerLeave`, default-OnLeave body, multi-callback ordering.
- `state.go` (~60 lines) — `AddState[T]` / `State[T]`, per-stage typed state map, factory invocation during stage construction.
- `spawn_init.go` (~50 lines) — `mmokit.Init(fn)` SpawnOption, bundle population from a just-spawned entity.

Renames / deletions:

- `pkg/mmokit/world.go` → `pkg/mmokit/stage.go`. `WorldBase` struct → `Stage` struct. All methods renamed where appropriate (e.g. `ECSWorld()` → `ECS()`).
- `pkg/universe/GameWorld` interface deleted. `Cell.GameWorld` field becomes `Cell.Stage *mmokit.Stage`. All ~15 interface methods inline directly on `Stage`.
- `Config.World` field deleted. `Config.OnStageReady` added (rarely used).
- `mmokit.WorldOf[T]` / `WorldOfCell[T]` deleted. Replaced by `mmokit.State[T](stage)`.
- `mmokit.WithComponents()` (no-args) deleted. `WithEntityKind` auto-attaches kind components.

Migrations:

- 4node-basic: delete `world.go`, update `main.go`, `command_bots.go`, `system_bots.go`. The whole demo becomes ~3 source files plus components and a system.
- Any other examples currently in the repo follow the same migration pattern.
- Internal references to `WorldBase` / `GameWorld` in `pkg/universe`, `pkg/system`, `pkg/engine` get updated mechanically.

No backward-compat shims, no deprecation aliases. Direct cutover per project rule.

## Tradeoffs

**Composable state plugins are less Go-idiomatic than embedding.** Embedding is the standard Go way to extend a type. The `AddState[T]` / `State[T]` registry pattern is more like dependency injection — it's strictly more composable across packages, but readers familiar with idiomatic Go may find it less natural than `type World struct { *WorldBase; Market *MarketState }`. The composability win outweighs the idiom cost: the embedding pattern doesn't scale across independently-shipped subsystems.

**Reflection at the boundary is unavoidable.** `RegisterKind[T]`, `Init(fn)`, and `Query[T]` all reflect on bundle struct fields. This is the same pattern `mmokit.Query[T]` already uses, accepted as established. Reflection happens once per kind/init at startup or first-spawn — not in hot paths.

**Stage methods replicate the GameWorld interface surface.** Stage will have ~15 public methods that today live on the `GameWorld` interface (Spawn, SendEvent, MarkForRemoval, etc.). This is mechanical duplication during the rename and shouldn't introduce new design pressure.

## Open questions

None. The naming question (Stage), the abstraction shape (composable typed state), and the spec-side decisions (no Config additions, no backward compat) are all resolved.
