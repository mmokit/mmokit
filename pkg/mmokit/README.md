# MMOKIT

A Go framework for building server-authoritative 2D MMOs with built-in server meshing, delta-compressed replication, and topology-transparent networking.

Write game logic as ECS systems and components. MMOKIT handles multi-node spatial partitioning, seamless entity transfer, area-of-interest culling, and bandwidth-efficient binary replication.

## Features

- **Server Meshing** — Configurable grid of cells with automatic entity transfer, border replication, and cross-cell actions
- **Topology-Transparent Protocol** — Clients receive entities in world-space coordinates with zero knowledge of cells, nodes, or server ownership
- **Delta-Compressed Replication** — Per-player AoI visibility, hash-based change detection, quantized binary snapshots with configurable struct-tag encodings
- **Dynamic Cell Partitioning** — Optional quadtree splitting/merging of cells at runtime based on load
- **ECS Architecture** — [Ark](https://github.com/mlange-42/ark) archetype-based entity component system
- **Fixed-Timestep Game Loop** — 20Hz default with deterministic ordered system execution
- **Client SDK Codegen** — Auto-generate typed TypeScript clients from Go replication bindings
- **Interactive Console** — Admin CLI with tab completion, runtime config editing, log filtering
- **Observability** — Per-node tick profiling (avg/p50/p95/p99), entity counts, Prometheus endpoint
- **Persistence** — PostgreSQL with hybrid relational + JSONB schema; batched async flushes via `PlayerFlusher`
- **State Integrity** — Invariants + commit log + per-cell netID index guard commit paths against silent inconsistencies

## Quick Start

```go
package main

import (
    "context"

    "github.com/zenion/mmoserver/pkg/mmokit"
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
    coord.OnInit(func(w *mmokit.Stage) {
        w.SpawnEntity(mmokit.Position{X: 4096, Y: 4096}, mmokit.WithCollider(20))
    })
    coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
    coord.Start(context.Background())
}
```

## Architecture

```text
+----------------------------------------------------------------+
|                        Coordinator                             |
|                                                                |
|  +-----------------------+   +------------------------+        |
|  |     Cell (0,0)        |   |     Cell (1,0)         |        |
|  |  +----------------+   |   |  +----------------+    |        |
|  |  |     Engine     |   |   |  |     Engine     |    |        |
|  |  |  +----------+  |   |   |  |  +----------+  |    |        |
|  |  |  |ECS World |  |   |   |  |  |ECS World |  |    |        |
|  |  |  +----------+  |   |   |  |  +----------+  |    |        |
|  |  |  Game Loop     |   |   |  |  Game Loop     |    |        |
|  |  |  (goroutine)   |   |   |  |  (goroutine)   |    |        |
|  |  +----------------+   |   |  +----------------+    |        |
|  |                       |   |                        |        |
|  |  GameWorld (yours)    |   |  GameWorld (yours)     |        |
|  |  Systems[]            |   |  Systems[]             |        |
|  +----------+------------+   +----------+-------------+        |
|             |   Bridge <-----------------+                     |
|             |   (transfers, replicas, actions)                 |
|             |                                                  |
|  +----------+------------------------------------------------+ |
|  |                      ConnManager                          | |
|  |          WebSocket + UDP  |  /ws  |  /metrics             | |
|  +-----------------------------------------------------------+ |
|                                                                |
|  Console (interactive admin CLI)                               |
+----------------------------------------------------------------+
                              |
              +---------------+---------------+
              v               v               v
          Client A        Client B        Client C
       (world-space     (world-space     (world-space
        entities)        entities)        entities)
```

The **Coordinator** creates a grid of **Cells**, each running its own ECS world and game loop in a separate goroutine. You register systems with `AddSystem` and set a world factory via `SetWorld` (custom struct) or `OnInit` (simple init callback). Systems are instantiated per-cell; the world factory is called once per cell with a pre-wired `*Stage`.

Clients connect to the shared **ConnManager** and receive entities in absolute world-space coordinates — they have zero knowledge of which cell owns which entity.

## Core Concepts

### Config

`Config` holds coordinator settings plus the login handler. World setup, console configuration, and routing callbacks are registered via methods on `*Coordinator`:

```go
coord := mmokit.NewCoordinator(mmokit.Config{
    CellsX:    2,                  // grid width (default 1)
    CellsY:    2,                  // grid height (default 1)
    CellSize:  8192,               // world units per cell (default 8192)
    TickRate:  20,                 // Hz (default 20)
    AoIRadius: 500,                // area-of-interest radius (default 500)
    Headless:  false,              // disable interactive console
    DynamicPartitioning: nil,      // quadtree splitting (nil = disabled)
    LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
        // Parse login messages, return (username, sessionData, nil) or ErrLoginPending
        return "", nil, mmokit.ErrLoginPending
    },
})
coord.SetWorld(NewMyWorld)         // factory called once per cell with *Stage
coord.SetPlayerRouter(func(username string) string {
    return coord.CellAtPosition(spawnX, spawnY) // determines which cell hosts each player
})
coord.SetConsole(mmokit.ConsoleOpts{...})       // optional: config/entity console commands
coord.OnConsoleReady(func(c *mmokit.Console) { ... }) // optional: register custom commands
```

### GameWorld

Embed `*Stage` in your game world struct. It provides default implementations for entity transfer, replica management, bridge wiring, and the spatial grid. `Init()` is called by the Coordinator after all cells are created and bridges are wired — use it for entity spawning and replicator registration.

```go
type MyWorld struct {
    *mmokit.Stage
    InputMap *ecs.Map1[PlayerInput]
}

func NewMyWorld(base *mmokit.Stage) *MyWorld {
    return &MyWorld{
        Stage: base,
        InputMap:  ecs.NewMap1[PlayerInput](base.ECSWorld()),
    }
}

func (gw *MyWorld) Init() {
    // Called after bridges are wired — safe to spawn entities, register handlers
    gw.SpawnEntity(mmokit.Position{X: 4096, Y: 4096}, mmokit.WithCollider(25))
}
```

### Systems

Systems run in registration order each tick. MMOKIT provides generic systems you can use directly:

```go
coord.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
coord.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
coord.AddSystem("DeadReckoning", mmokit.NewDeadReckoningSystem())
coord.AddSystem("Lifetime", mmokit.NewLifetimeSystem())
coord.AddSystem("Spatial", mmokit.NewSpatialSystem())
coord.AddSystem("Network", mmokit.NewNetworkSystem())          // or NewNetworkSystemWith(setup)
```

Game-specific systems use inline factories:

```go
coord.AddSystem("Combat", func() mmokit.System { return &CombatSystem{} })
```

### Query[T]

`Query[T]` provides ergonomic ECS iteration over component bundle structs. It replaces raw Ark `ecs.FilterN` + manual `query.Next()`/`query.Get()` loops with a single generic type and Go range iterators.

```go
type MovementSystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct {
        Pos    *mmokit.Position
        Vel    *mmokit.Velocity
        Params *mmokit.MoveParams `ecs:"optional"` // nil when entity has no custom params
    }]
}

func (s *MovementSystem) Init() {
    s.entities.Init(s) // default: excludes Ghost + Replica entities
}

func (s *MovementSystem) Update(dt float32) {
    for entity, b := range s.entities.All() {
        b.Pos.X += b.Vel.X * dt
        b.Pos.Y += b.Vel.Y * dt
    }
}
```

**Bundle rules:** Exported fields must be `*ComponentType`. Fields tagged `ecs:"optional"` are set to nil when the entity lacks that component.

**Options:**

```go
s.q.Init(s)                                     // excludes Ghost + Replica (default)
s.q.Init(s, mmokit.IncludeAll())                // no exclusions
s.q.Init(s, mmokit.Without[comp.Dormant]())     // default exclusions + Dormant
s.q.Init(s, mmokit.IncludeAll(),
    mmokit.Without[comp.Ghost]())                // only Ghost excluded
```

**Methods:** `All()` (range iterator), `Each(fn)` (callback), `Count()`, `Any()`.

### Entity Spawning

`Stage.SpawnEntity` creates entities with Position, Velocity, NetworkID, EntityKind, Collider, and CellCoord:

```go
entity := gw.SpawnEntity(mmokit.Position{X: 100, Y: 200},
    mmokit.WithCollider(25),
    mmokit.WithEntityKind(KindPlayer),
    mmokit.WithVelocity(0, 0),
    mmokit.WithComponents(), // auto-add all components registered on this entity kind
)
```

### Input Handling

Register typed protobuf handlers filtered by player state:

```go
coord.AddSystem("Input", mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *MyWorld) {
    mmokit.Handle(router, mypb.ClientEventCode_MOVE,
        mmokit.States(mmokit.StateActive),
        func(ctx *mmokit.InputContext, msg *mypb.MoveMsg) {
            // ctx.Entity, ctx.ConnID, ctx.Session available
            pos := gw.PositionMap().Get(ctx.Entity)
            pos.X = msg.TargetX
            pos.Y = msg.TargetY
        })
}))
```

### Player State Machine

Players transition through states with enter/exit callbacks:

```go
// Login handling is configured at the coordinator level via Config.LoginHandler.
// Per-node PlayerManagers only handle state transitions and callbacks:
pm := gw.Engine().Players

pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
    OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
        s.Entity = gw.spawnPlayer(s.ConnID, s.Username)
        gw.SendSpawnedMsg(s.ConnID, s.Entity)
    },
    OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
        gw.MarkForRemoval(s.Entity)
    },
})
```

Built-in states: `StatePending`, `StateActive`, `StateTransferring`, `StateDisconnected`. Games can register custom states (e.g. "dead", "docked") via `RegisterState()`.

## Replication

### Entity Kind Definitions

Define entity types with replication bindings. The network system auto-discovers these and builds replicators:

```go
func playerKindDef(w *ecs.World) mmokit.EntityKindDef {
    def := mmokit.EntityKindDef{
        Kind: KindPlayer,
        Name: "Player",
        EngineBindings: &mmokit.EngineBindingsConfig{
            VelQuantScale:  2000, // int16 = vel * 2000 (max ~16 units/s, precision 0.0005)
            SizeQuantScale: 500,  // int16 = radius * 500
        },
    }
    mmokit.KindComponent(&def, ecs.NewMap1[PlayerName](w))
    mmokit.KindComponent(&def, ecs.NewMap1[Health](w))
    // KindComponentLocalOnly registers components added after transfer but not serialized:
    mmokit.KindComponentLocalOnly(&def, ecs.NewMap1[PlayerConn](w))
    return def
}
```

Register in your world constructor:

```go
gw.RegisterEntityKind(playerKindDef(gw.ECSWorld()))
```

The network system auto-discovers replicators from all registered entity kinds via `BuildReplicators()` and inherits `AoIRadius` from `Stage`. No hand-coded replicators are needed.

### Struct Tag Replication

For custom components, struct tags control wire encoding:

```go
type ShipState struct {
    ThrottleX float32 `net:"qvel"`              // quantized velocity encoding (int16)
    ThrottleY float32 `net:"qvel"`
    Boost     float32 `net:"qnorm"`             // normalized [0,1] → uint8
    ShieldHP  float32 `net:"f32"`               // full float32
    SkinID    uint8   `net:"initial,u8"`        // sent once on visibility enter
    Name      string  `net:"initial,string"`    // sent once, variable length
}
```

Encodings: `qvel` (int16 velocity), `qangle` (uint16 rotation), `qnorm` (uint8 normalized), `qsize` (uint16 radius), `f32` (float32), `u8`/`u16`/`u32` (unsigned int), `i16` (signed int16), `bool`, `string`, `auto` (inferred from Go type). Add `initial` prefix for data sent only on first visibility.

### Engine Bindings

Every entity gets standard bindings automatically via `EngineBindings`:

- **Position** — absolute world-space coordinates (2x float32)
- **Velocity** — quantized (2x int16 with configurable scale)
- **Size** — quantized collider radius (int16)

### Client SDK Codegen

Generate a typed TypeScript client from your replication schema:

```bash
go run ./your-game --dump-schema | go run ./cmd/sdkgen --out web/sdk --proto-es gen/es
```

The `--dump-schema` flag exports entity layouts from your `EntityKindDef` registrations. The codegen produces a typed client class, entity interfaces, binary delta decoder, and WebSocket transport.

## Server Meshing

### Entity Transfer

When entities cross cell boundaries, they're serialized and transferred to the destination cell. A ghost entity remains on the source cell briefly to prevent visual flicker. The transfer is invisible to clients — they see a continuous entity stream in world space.

### Border Replication

Entities near cell borders are replicated (read-only) to neighboring cells. Replica positions are snapped to the authoritative value each tick with server-side dead-reckoning between updates.

### Cross-Cell Actions

Actions targeting replica entities (e.g., combat) are forwarded to the authoritative cell:

```go
func (gw *MyWorld) HandleCrossNodeAction(action *mmokit.CrossNodeAction) *mmokit.ActionResult {
    // Execute action on authoritative entity
    return &mmokit.ActionResult{Success: true, Payload: resultData}
}
```

### Dynamic Cell Partitioning

Optional quadtree splitting/merging based on load:

```go
cfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
// Defaults: split at 75% tick budget, merge at 20%, EWMA-smoothed
```

Console commands: `cell list`, `cell info <id>`, `cell split <id>`, `cell merge <id>`, `cell autosplit on/off`.

Use the `debug` console command to toggle the topology overlay on all connected clients. `Stage.FromSplit()` returns true when the world was created by a cell split — use it to skip initial entity spawning in the world factory. Docked player sessions are automatically transferred during cell splits.

## Topology-Transparent Protocol

Clients receive entities in absolute world-space coordinates with zero knowledge of cells, hosts, or grid layout. `SpawnedMsg` contains only `entity_net_id`, `world_x`, `world_y`.

Topology distribution is game-owned. The engine exposes `Coordinator.ClusterCells() []ClusterCellInfo` (and `Stage.ClusterCells()`) returning the current cell→host view from local state or the cached PeerList topology. Games build their own `enginepb.CellTopologyMsg` and push it via `gw.Engine().ConnMgr.SendReliable(connID, frame)` from a player-spawn hook — see `examples/4node-basic/world.go` for the pattern.

For per-entity LOCAL/REPLICA/GHOST status, set `IncludeMeshState: true` in your `EntityKindDef.EngineBindings` config. Schema export and runtime both honor the same value (no runtime override), so the wire format stays consistent.

## Networking

WebSocket transport with two logical channels:

| Channel | Prefix | Purpose | Pattern |
| ------- | ------ | ------- | ------- |
| Events | 0x00 | Game state, input, chat | Fire-and-forget |
| Operations | 0x01 | Marketplace, queries | Request/response with correlation IDs |

## Console

Interactive admin CLI with tab completion, built-in commands, and game-extensible command groups. All commands are routed through `pkg/cmdsys` — they work from any process in a distributed deployment and automatically fan out or dispatch via MeshControl.

- `perf [reset]` — tick timing, per-system breakdown, load score
- `cell list/info/split/merge/migrate/cooldowns/config` — multi-cell status + dynamic partition management
- `host list` — host roster (live / leaving / dead)
- `log on/off/toggle/only <category>` — runtime log filtering; live-tail State Integrity events via `log events:*`
- `config list/get/set/save/reset` — runtime config editing (when game provides `Configurable`)
- `commit.log [--n=N|--commit=ID|--cell=CELLID|--since=DUR]` — query the in-memory commit event ring

## Observability

Per-cell metrics with Prometheus endpoint plus State Integrity observability:

```go
mux.Handle("/metrics", coord.MetricsHandler())
```

Auto-registered HTTP endpoints on the gateway mux (any RoleGateway process) and on the admin HTTP mux (`Config.AdminListen` / `--admin-listen=:9101` on pure-coordinator processes):

- `GET /metrics` — Prometheus-compatible scrape target. Tick duration percentiles (p50/p95/p99), effective Hz, overbudget ratio, entity counts (real/replica/ghost/player), connections, bytes sent/recv, composite load score.
- `GET /commands` and `GET /commands/{verb}` — JSON Schema catalog of every registered command.
- `GET /events?n=N&commit=ID&cell=CELLID&since=DUR` — JSON stream of the commit-log ring (begin/end markers, per-step success/duration, invariant violations, host join/leave).

## State Integrity

Runtime guards that catch wrong states at the point of violation. Opt-in via `Config`:

```go
mmokit.Config{
    InvariantMode:    mmokit.InvariantPanic, // or InvariantLog / InvariantOff
    StrictNetIDIndex: true,
    CommitLogCapacity: 1024,
    AdminListen:       ":9101",
}
```

- **Invariants** run at every commit entry, between every plan step, and at commit exit. Panic mode fails fast in dev/tests; Log mode records a `CommitEvent` in production and keeps going.
- **CommitPlan model** — split/merge/migrate are data-driven step lists inspectable via `commit.log --commit=N`.
- **netIDIndex** — per-cell `{netID → entity, presence}` table with a documented transition policy. Six spawn paths wire through it; `OnEntityRemoved` clears entries on removal. Strict mode rejects violations at spawn time; observational mode logs and continues.

## Packages

| Package | Description |
| ------- | ----------- |
| `mmokit` | Single-import facade — re-exports all public types |
| `query` | Bundle-based ECS query (`Query[T]`, `Without`, `IncludeAll`) — imported by pkg/system and pkg/universe directly |
| `engine` | ECS world, game loop, console, tick queue, entity registry, player state machine |
| `universe` | Coordinator, Cell, Bridge, topology, entity transfers, replica management |
| `net` | Transport interfaces, WebSocket + UDP, connection manager |
| `component` | Generic components: Position, Velocity, Rotation, Collider, NetworkID, Lifetime, Ghost, Replica, PlayerConn, MoveTarget |
| `system` | Generic systems: physics, lifetime, delta-compressed replication, spatial, click-to-move, direction-move |
| `spatial` | Spatial hash grid for AoI queries and collision detection |
| `coords` | Cell coordinate system with configurable cell size |
| `quantize` | Snapshot encoding, delta compression, quantization helpers |
| `metrics` | Per-node observability: Counter, Gauge, EWMA, Prometheus handler |
| `cmdsys` | Distributed command system — typed Args/Result, route resolvers, JSON Schema export |
| `persist` | Domain repository interfaces (Players, Market, Config); PostgreSQL-backed via `persist/postgres` |
| `logger` | Category-based debug logging with runtime toggling |
| `ops` | Operation router for request/response RPCs |
| `orderbook` | Generic price-time priority order book matching engine |

## Examples

### 4node-basic

Minimal 2x2 mesh demo. Players are circles with click-to-move. Topology-transparent client with optional debug overlays (cell boundaries, AoI radius, replica/ghost markers). Uses `EntityKindDef` with auto-discovered replicators and generated TypeScript SDK.

```bash
cd examples/4node-basic && just dev
```

### Slither

Slither.io clone. 2x2 mesh, snake movement with ring-buffer body segments, food spawning, body collisions, leaderboard. Hand-coded replication with quantized binary snapshots.

```bash
cd examples/slither && just dev
```

## Requirements

- Go 1.25+
- [Buf](https://buf.build/) (protobuf codegen)
- [Bun](https://bun.sh/) (TypeScript client builds)
