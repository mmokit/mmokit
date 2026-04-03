# MMOKIT

A Go toolkit for building server-authoritative 2D MMOs with built-in server meshing.

MMOKIT gives you an ECS game engine, a fixed-timestep game loop, multi-node spatial partitioning, delta-compressed replication, and persistence out of the box. You write game logic as systems and components — MMOKIT handles the infrastructure.

## Features

- **ECS Architecture** — Archetype-based entity component system via [ark](https://github.com/mlange-42/ark) for cache-friendly storage and queries
- **Server Meshing** — Automatic spatial partitioning across a configurable grid of nodes with seamless entity transfer and border replication
- **Fixed-Timestep Game Loop** — 20Hz default (configurable) with deterministic ordered system execution
- **Delta-Compressed Replication** — Per-player area-of-interest visibility, hash-based change detection, quantized binary snapshots
- **Spatial Hash Grid** — O(1) incremental updates, broad+narrow phase collision detection with layer filtering
- **Persistence** — Memory-first with async writes to pluggable storage (BoltDB included)
- **Interactive Console** — Readline-based admin CLI with tab completion, command groups, and runtime config editing
- **Observability** — Zero-alloc per-node metrics with Prometheus endpoint and tick profiling (avg/p50/p95/p99)

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

// 1. Define your components
type PlayerInput struct {
    TargetX, TargetY float32
}

// 2. Create a system
type MovementSystem struct {
    filter *ecs.Filter2[mmokit.Position, PlayerInput]
}

func NewMovementSystem(w *ecs.World) *MovementSystem {
    return &MovementSystem{
        filter: ecs.NewFilter2[mmokit.Position, PlayerInput](w),
    }
}

func (s *MovementSystem) Name() string        { return "Movement" }
func (s *MovementSystem) Update(dt float32) {
    query := s.filter.Query()
    for query.Next() {
        pos, input := query.Get()
        pos.X += (input.TargetX - pos.X) * dt
        pos.Y += (input.TargetY - pos.Y) * dt
    }
}

// 3. Define your game world
type MyWorld struct {
    mmokit.WorldBase
    Spatial  *mmokit.HashGrid
    InputMap *ecs.Map1[PlayerInput]
}

func NewMyWorld(base *mmokit.WorldBase) *MyWorld {
    return &MyWorld{
        WorldBase: *base,
        Spatial:   base.SpatialGrid(),
        InputMap:  ecs.NewMap1[PlayerInput](base.ECSWorld()),
    }
}

// 4. Wire it up
func main() {
    cm := mmokit.NewConnManager()

    coord := mmokit.NewCoordinator(
        mmokit.MeshConfig{CellsX: 2, CellsY: 2, CellSize: 8192}, // 2x2 mesh
        mmokit.EngineConfig{TickRate: 20},
        func(base *mmokit.WorldBase) (mmokit.GameWorld, []mmokit.System) {
            gw := NewMyWorld(base)
            return gw, []mmokit.System{
                NewMovementSystem(base.ECSWorld()),
                mmokit.NewPhysicsSystem(base.ECSWorld()),
            }
        },
        mmokit.WithConnManager(cm),
        mmokit.WithAoIRadius(3000),
    )

    mux := http.NewServeMux()
    mux.HandleFunc("/ws", cm.HandleWebSocket)

    go func() {
        log.Fatal(http.ListenAndServe(":8080", mux))
    }()

    coord.Start(context.Background()) // blocks until shutdown
}
```

See [examples/](#examples) for complete working games.

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

The **Coordinator** creates a grid of **Nodes**, each running its own ECS world and game loop in a separate goroutine. You provide a **NodeFactory** function that receives a pre-wired `WorldBase` and returns your game world + systems.

Entities crossing cell boundaries are automatically serialized and transferred to the destination node. Border entities within AoI range are replicated to neighboring nodes so players near edges see a seamless world.

All nodes share a single **ConnManager** that handles WebSocket upgrades and connection lifecycle. The Coordinator routes each player's events to whichever node owns them.

## Core Concepts

### ECS

MMOKIT uses [ark](https://github.com/mlange-42/ark) for entity component storage. Components are plain Go structs. Queries use generic types:

```go
// Component access (read/write a specific entity)
posMap := ecs.NewMap1[mmokit.Position](world)
pos := posMap.Get(entity)

// Queries (iterate all entities with matching components)
filter := ecs.NewFilter2[mmokit.Position, mmokit.Velocity](world)
query := filter.Query()
for query.Next() {
    pos, vel := query.Get()
    pos.X += vel.X * dt
}
// Important: call query.Close() if you break out of a query early
```

### GameWorld Interface

Embed `WorldBase` to get sensible defaults for entity transfer, replica management, and bridge wiring. Override methods as needed for game-specific behavior:

```go
type MyWorld struct {
    mmokit.WorldBase
    // your game-specific state
}
```

Key methods you may override:
- `SerializeEntity` / `SpawnFromTransfer` — custom entity transfer serialization
- `ScanBorderEntities` / `ApplyReplicas` — custom replica behavior
- `HandleCrossNodeAction` — react to actions targeting entities on this node from other nodes
- `Hooks()` — game loop lifecycle hooks (connect, disconnect, login, pre/post flush)

### Systems

Systems implement a two-method interface and run in registration order each tick:

```go
type System interface {
    Name() string
    Update(dt float32)
}
```

MMOKIT provides generic systems you can use directly:
- `PhysicsSystem` — integrates velocity into position
- `LifetimeSystem` — despawns entities when their lifetime expires
- `ReplicationSystem` — delta-compressed state sync with per-player AoI
- `BoundarySystem` — handles entity transfer across cell boundaries

### Server Meshing

The world is divided into a grid of cells. Each cell is owned by one Node. The topology is 8-connected — each node knows its neighbors in all cardinal and diagonal directions.

- **Entity Transfer**: When an entity crosses a cell boundary, it's serialized to a binary `TransferFrame`, sent to the destination node, and respawned there. A ghost entity remains on the source node briefly to prevent flicker.
- **Replica Replication**: Entities near cell borders are replicated (read-only) to neighboring nodes so players near edges see entities on adjacent cells.
- **Cross-Node Actions**: Actions targeting replica entities (e.g. combat) are forwarded to the authoritative node for execution, with results sent back.

### Networking

MMOKIT uses WebSocket transport with two logical channels:

| Channel | Purpose | Pattern |
|---------|---------|---------|
| Events (0x00) | Game state updates, input, chat | Fire-and-forget |
| Operations (0x01) | Marketplace, crafting, queries | Request/response with correlation IDs |

The connection manager handles upgrades, read/write pumps, and per-connection input buffering. Games define their own wire format — protobuf helpers are included but not required.

## Packages

| Package | Description |
|---------|-------------|
| `mmokit` | Single-import facade — re-exports all public types |
| `engine` | ECS world, game loop, console, tick queue, entity registry, player state machine, input router |
| `universe` | Coordinator, Node, NodeBridge, topology, entity transfers, replica management |
| `net` | Transport interfaces, WebSocket + UDP implementations, connection manager |
| `component` | Generic components: Position, Velocity, Rotation, Health, Shield, NetworkID, Collider, and more |
| `system` | Generic systems: physics, lifetime, delta-compressed replication, binary frame encoding |
| `spatial` | Spatial hash grid for area-of-interest queries and collision detection |
| `coords` | Infinite-world cell coordinate system with configurable cell size |
| `quantize` | Snapshot encoding, delta compression, and quantization helpers |
| `metrics` | Per-node observability: Counter, Gauge, EWMA, load scoring, Prometheus handler |
| `persist` | Storage interface + BoltDB implementation + async write queue |
| `logger` | Category-based debug logging with dynamic registration and runtime toggling |
| `ops` | Serialization-agnostic operation router for request/response RPCs |
| `orderbook` | Generic price-time priority order book matching engine |

## Examples

### Slither

A Slither.io clone built on MMOKIT. 2x2 server mesh, snake movement with ring-buffer body segments, food spawning, body collisions, leaderboard, and delta-compressed replication with quantized snapshots.

```bash
cd examples/slither && go run .
```

### 4node-basic

Minimal 2x2 mesh demo. Players are circles with click-to-move input. Custom binary networking, debug overlays showing cell boundaries, AoI radius, replica/ghost markers, and per-node stats.

```bash
cd examples/4node-basic && go run . -port 8081
```

## Requirements

- Go 1.25+
- Dependencies: [ark](https://github.com/mlange-42/ark) (ECS), [coder/websocket](https://github.com/coder/websocket), [bbolt](https://go.etcd.io/bbolt), [protobuf](https://google.golang.org/protobuf)

## Status

MMOKIT is under active development. The API is stabilizing but may still change. See the [roadmap](docs/planning/mmokit-roadmap.md) for planned features including dynamic cell partitioning, connection migration, and multi-process scaling.

## License

TBD
