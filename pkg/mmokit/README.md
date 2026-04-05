# MMOKIT

A Go framework for building server-authoritative 2D MMOs with built-in server meshing, delta-compressed replication, and topology-transparent networking.

Write game logic as ECS systems and components. MMOKIT handles multi-node spatial partitioning, seamless entity transfer, area-of-interest culling, and bandwidth-efficient binary replication.

## Features

- **Server Meshing** — Configurable grid of nodes with automatic entity transfer, border replication, and cross-node actions
- **Topology-Transparent Protocol** — Clients receive entities in world-space coordinates with zero knowledge of cells, nodes, or server ownership
- **Delta-Compressed Replication** — Per-player AoI visibility, hash-based change detection, quantized binary snapshots with configurable struct-tag encodings
- **Dynamic Cell Partitioning** — Optional quadtree splitting/merging of cells at runtime based on load
- **ECS Architecture** — [Ark](https://github.com/mlange-42/ark) archetype-based entity component system
- **Fixed-Timestep Game Loop** — 20Hz default with deterministic ordered system execution
- **Client SDK Codegen** — Auto-generate typed TypeScript clients from Go replication bindings
- **Interactive Console** — Admin CLI with tab completion, runtime config editing, log filtering
- **Observability** — Per-node tick profiling (avg/p50/p95/p99), entity counts, Prometheus endpoint
- **Persistence** — Memory-first with async writes to pluggable storage (BoltDB included)

## Quick Start

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/mlange-42/ark/ecs"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

// 1. Define your game world
type MyWorld struct {
    mmokit.WorldBase
}

// 2. Write a system — this one oscillates all entities left/right
type OscillateSystem struct {
    mmokit.SystemBase
    filter  *ecs.Filter1[mmokit.Velocity]
    elapsed float32
    speed   float32
}

func (s *OscillateSystem) Init() {
    s.filter = ecs.NewFilter1[mmokit.Velocity](s.ECSWorld())
    s.speed = 100
}

func (s *OscillateSystem) Update(dt float32) {
    s.elapsed += dt
    if s.elapsed < 5.0 { // reverse every 5 seconds
        return
    }
    s.elapsed = 0
    s.speed = -s.speed
    query := s.filter.Query()
    for query.Next() {
        vel := query.Get()
        vel.X = s.speed
    }
}

// 3. Wire it up
func main() {
    cfg := mmokit.Config{
        CellsX:   1,
        CellsY:   1,
        CellSize: 8192,
        TickRate:  20,
        AoIRadius: 3000,
        WorldFactory: func(base *mmokit.WorldBase, coord *mmokit.Coordinator) mmokit.GameWorld {
            gw := &MyWorld{WorldBase: *base}

            // Spawn an entity that moves back and forth
            gw.SpawnEntity(mmokit.Position{X: 4096, Y: 4096},
                mmokit.WithVelocity(100, 0),
                mmokit.WithCollider(20),
            )

            return gw
        },
    }
    coord := mmokit.NewCoordinator(cfg)

    coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
    coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
    coord.AddSystem("Spatial", mmokit.NewSpatialSystem())
    coord.AddSystem("Network", mmokit.NewNetworkSystem())
 
    cm := coord.ConnManager()
    coord.Build()

    mux := http.NewServeMux()
    mux.HandleFunc("/ws", cm.HandleWebSocket)
    mux.Handle("/metrics", coord.MetricsHandler())
    go func() { log.Fatal(http.ListenAndServe(":8080", mux)) }()

    coord.Start(context.Background()) // blocks until shutdown
}
```

## Architecture

```
Coordinator
├── Node (0,0)              Node (1,0)
│   ├── Engine              ├── Engine
│   │   ├── ECS World       │   ├── ECS World
│   │   └── Game Loop       │   └── Game Loop
│   ├── GameWorld (yours)   ├── GameWorld (yours)
│   ├── Systems[]           ├── Systems[]
│   └── NodeBridge  ◄───────┴── NodeBridge
│
├── ConnManager (shared WebSocket + UDP transport)
├── Console (interactive admin CLI)
└── /metrics (Prometheus endpoint)
```

The **Coordinator** creates a grid of **Nodes**, each running its own ECS world and game loop in a separate goroutine. You provide a `WorldFactory` that receives a pre-wired `WorldBase` and returns your game world. Systems are registered once and instantiated per-node.

## Core Concepts

### Config

All coordinator settings live in a single `Config` struct:

```go
cfg := mmokit.Config{
    CellsX:    2,                  // grid width (default 1)
    CellsY:    2,                  // grid height (default 1)
    CellSize:  8192,               // world units per cell (default 8192)
    TickRate:  20,                 // Hz (default 20)
    AoIRadius: 500,                // area-of-interest radius (default 500)
    Headless:  false,              // disable interactive console
    ProxiesEnabled: false,         // lightweight proxy mode
    DebugTopology:  false,         // send mesh state to clients
    DynamicPartitioning: nil,      // quadtree splitting (nil = disabled)
    WorldFactory: func(base *mmokit.WorldBase, coord *mmokit.Coordinator) mmokit.GameWorld {
        return NewMyWorld(base, coord)
    },
}
```

### GameWorld

Embed `WorldBase` in your game world struct. It provides default implementations for entity transfer, replica management, bridge wiring, and the spatial grid.

```go
type MyWorld struct {
    mmokit.WorldBase
    InputMap *ecs.Map1[PlayerInput]
}

func NewMyWorld(base *mmokit.WorldBase, coord *mmokit.Coordinator) *MyWorld {
    return &MyWorld{
        WorldBase: *base,
        InputMap:  ecs.NewMap1[PlayerInput](base.ECSWorld()),
    }
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

### Entity Spawning

`WorldBase.SpawnEntity` creates entities with Position, Velocity, NetworkID, EntityKind, Collider, and CellCoord:

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
pm := gw.Engine().Players

pm.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
    // validate login, return error to reject
    return nil
})

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

Built-in states: `StatePending`, `StateActive`, `StateDead`, `StateTransferring`, `StateDisconnected`.

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
    return def
}
```

Register in your world constructor:

```go
gw.RegisterEntityKind(playerKindDef(gw.ECSWorld()))
```

The network system handles the rest — AoI culling, delta compression, and binary frame dispatch.

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

When entities cross cell boundaries, they're serialized and transferred to the destination node. A ghost entity remains on the source node briefly to prevent visual flicker. The transfer is invisible to clients — they see a continuous entity stream in world space.

### Border Replication

Entities near cell borders are replicated (read-only) to neighboring nodes. Replica positions are snapped to the authoritative value each tick with server-side dead-reckoning between updates.

### Cross-Node Actions

Actions targeting replica entities (e.g., combat) are forwarded to the authoritative node:

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

## Topology-Transparent Protocol

Clients receive entities in absolute world-space coordinates with zero knowledge of cells, nodes, or grid layout. `SpawnedMsg` contains only `entity_net_id`, `world_x`, `world_y`.

The `DebugTopology` coordinator flag enables optional debug info for development tools:

```go
cfg.DebugTopology = true // enables MeshState binding + CellTopologyMsg
```

When enabled, clients receive per-entity LOCAL/REPLICA/GHOST status and cell topology data for debug visualization (cell boundaries, node ownership, replica badges).

## Networking

WebSocket transport with two logical channels:

| Channel | Prefix | Purpose | Pattern |
| ------- | ------ | ------- | ------- |
| Events | 0x00 | Game state, input, chat | Fire-and-forget |
| Operations | 0x01 | Marketplace, queries | Request/response with correlation IDs |

## Console

Interactive admin CLI with tab completion, built-in commands, and game-extensible command groups:

- `perf [reset]` — tick timing, per-system breakdown, load score
- `node list` / `node load` — multi-node status
- `log on/off/toggle/only <category>` — runtime log filtering
- `config list/get/set/save/reset` — runtime config editing (when game provides `Configurable`)
- `cell list/info/split/merge` — dynamic partition management (when enabled)

## Observability

Per-node metrics with Prometheus endpoint:

```go
mux.Handle("/metrics", coord.MetricsHandler())
```

Exposes: tick duration percentiles (p50/p95/p99), effective Hz, overbudget ratio, entity counts (real/replica/ghost/player), connections, bytes sent/recv, composite load score.

## Packages

| Package | Description |
| ------- | ----------- |
| `mmokit` | Single-import facade — re-exports all public types |
| `engine` | ECS world, game loop, console, tick queue, entity registry, player state machine |
| `universe` | Coordinator, Node, NodeBridge, topology, entity transfers, replica management |
| `net` | Transport interfaces, WebSocket + UDP, connection manager |
| `component` | Generic components: Position, Velocity, Rotation, Collider, NetworkID, Health, Shield, Lifetime, Ghost, Replica |
| `system` | Generic systems: physics, lifetime, delta-compressed replication, spatial, click-to-move, direction-move |
| `spatial` | Spatial hash grid for AoI queries and collision detection |
| `coords` | Cell coordinate system with configurable cell size |
| `quantize` | Snapshot encoding, delta compression, quantization helpers |
| `metrics` | Per-node observability: Counter, Gauge, EWMA, Prometheus handler |
| `persist` | Storage interface + BoltDB + async write queue |
| `logger` | Category-based debug logging with runtime toggling |
| `ops` | Operation router for request/response RPCs |
| `orderbook` | Generic price-time priority order book matching engine |

## Examples

### 4node-basic

Minimal 2x2 mesh demo. Players are circles with click-to-move. Topology-transparent client with optional debug overlays (cell boundaries, AoI radius, replica/ghost markers). Uses `EntityKindDef` with auto-discovered replicators and generated TypeScript SDK.

```bash
cd examples/4node-basic && make dev
```

### Slither

Slither.io clone. 2x2 mesh, snake movement with ring-buffer body segments, food spawning, body collisions, leaderboard. Hand-coded replication with quantized binary snapshots.

```bash
cd examples/slither && make dev
```

## Requirements

- Go 1.25+
- [Buf](https://buf.build/) (protobuf codegen)
- [Bun](https://bun.sh/) (TypeScript client builds)
