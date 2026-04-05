# mmoserver

A server-authoritative 2D MMO server framework in Go, built on [MMOKIT](pkg/mmokit/). The server owns all game state and physics — clients are topology-agnostic renderers that send input and receive world-space entity updates.

## Architecture

- **ECS** — Entity Component System via [Ark](https://github.com/mlange-42/ark) v0.7.1
- **Server Meshing** — Automatic spatial partitioning across a configurable grid of nodes with seamless entity transfer and border replication
- **Topology-Transparent Protocol** — Clients receive entities in absolute world-space coordinates with zero knowledge of cells, nodes, or grid layout
- **20Hz fixed timestep** game loop with ordered system execution
- **Delta-Compressed Replication** — Per-player AoI visibility, hash-based change detection, quantized binary snapshots
- **WebSocket + UDP transport** with protobuf binary serialization
- **Memory-first persistence** with async writes to BBolt

### Package layout

```text
pkg/mmokit/      Single-import facade for the framework
pkg/engine/      Generic MMO engine (ECS, game loop, console, hooks)
pkg/universe/    Server meshing (Coordinator, Node, topology, entity transfers, replicas)
pkg/system/      Generic systems (physics, replication, spatial, click-to-move)
pkg/component/   Generic ECS components (Position, Velocity, NetworkID, Ghost, Replica, etc.)
pkg/net/         Transport interface, WebSocket + UDP implementations
pkg/spatial/     Spatial hash grid for AoI and collision queries
pkg/coords/      Infinite-world cell coordinate system
pkg/quantize/    Snapshot encoding, delta compression, quantization
pkg/metrics/     Per-node observability with Prometheus endpoint
pkg/ops/         Operation router (request/response over reliable channel)
pkg/persist/     Store interface, BoltStore, async writer
pkg/logger/      Category-based debug logger (dynamic registration)
pkg/orderbook/   Generic price-time priority order book matching engine
internal/        Game-specific logic (space game: ships, mining, combat, marketplace)
proto/           Protobuf schema (source of truth)
gen/             Generated code (Go, C#, TypeScript)
examples/        Working example games (slither, 4node-basic)
```

## Prerequisites

- Go 1.25+
- [Buf](https://buf.build/) (for protobuf codegen)
- [Bun](https://bun.sh/) (for TypeScript client builds)

## Build & Run

```bash
make build          # compile to bin/server
make run            # build + run
make dev            # build + run server & web vite dev server
make proto          # regenerate protobuf (buf generate)
make client-sdk GAME=examples/4node-basic  # generate typed TS client SDK
make clean          # remove bin/
```

## Examples

### 4node-basic

Minimal 2x2 mesh demo. Players are circles with click-to-move. Topology-transparent client with optional debug overlays (cell boundaries, AoI radius, replica/ghost markers).

```bash
cd examples/4node-basic && make dev
```

### Slither

Slither.io clone. 2x2 mesh, snake movement, food eating, collisions, leaderboard.

```bash
cd examples/slither && make dev
```

## Proto Codegen

Protobuf schemas live in `proto/`. Running `make proto` (or `buf generate`) produces:

- `gen/go/enginepb/` — Go engine protocol
- `gen/go/gamepb/` — Go game-specific messages
- `gen/es/enginepb/` — TypeScript engine protocol (ES modules)
- `gen/csharp/` — Unity client
