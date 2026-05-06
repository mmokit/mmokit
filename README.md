# mmoserver

A server-authoritative 2D MMO server framework in Go, built on [MMOKIT](pkg/mmokit/). The server owns all game state and physics — clients are topology-agnostic renderers that send input and receive world-space entity updates.

## Architecture

- **ECS** — Entity Component System via [Ark](https://github.com/mlange-42/ark) v0.7.1
- **Server Meshing** — Automatic spatial partitioning across a configurable grid of cells with seamless entity transfer and border replication
- **Dynamic Partitioning** — Runtime quadtree split/merge of cells based on load; admin-driven cell migrate between hosts
- **Topology-Transparent Protocol** — Clients receive entities in absolute world-space coordinates with zero knowledge of cells or grid layout
- **20Hz fixed timestep** game loop with ordered system execution
- **Delta-Compressed Replication** — Per-player AoI visibility, hash-based change detection, quantized binary snapshots
- **WebSocket + UDP transport** with protobuf binary serialization
- **Persistence** — PostgreSQL with hybrid relational + JSONB schema; batched async flushes
- **State Integrity framework** — invariant checks at every commit boundary, event-sourced commit log queryable via console + HTTP, per-cell netID index with typed transition policy

### Package layout

```text
pkg/mmokit/      Single-import facade for the framework
pkg/engine/      Generic MMO engine (ECS, game loop, console, hooks)
pkg/universe/    Server meshing (Coordinator, Cell, topology, transfers, replicas, invariants, commit log, netIDIndex)
pkg/system/      Generic systems (physics, replication, spatial, click-to-move)
pkg/component/   Generic ECS components (Position, Velocity, NetworkID, Ghost, Replica, etc.)
pkg/net/         Transport interface, WebSocket + UDP implementations
pkg/spatial/     Spatial hash grid for AoI and collision queries
pkg/coords/      Infinite-world cell coordinate system
pkg/quantize/    Snapshot encoding, delta compression, quantization
pkg/metrics/     Per-cell observability with Prometheus endpoint
pkg/cmdsys/      Distributed command system with typed Args/Result, route resolvers, JSON Schema
pkg/ops/         Operation router (request/response over reliable channel)
pkg/persist/     Domain repository interfaces (Players, Market, Config); PostgreSQL implementation under pkg/persist/postgres/
pkg/logger/      Category-based debug logger (dynamic registration)
pkg/orderbook/   Generic price-time priority order book matching engine
pkg/query/       Bundle-based ECS query abstraction consumed by mmokit.Query
internal/        Game-specific logic (space game: ships, mining, combat, marketplace)
proto/           Protobuf schema (source of truth)
gen/             Generated code (Go, C#, TypeScript)
examples/        Working example games (4node-basic)
```

## Prerequisites

- Go 1.25+
- [Buf](https://buf.build/) (for protobuf codegen)
- [Bun](https://bun.sh/) (for TypeScript client builds)
- PostgreSQL 17 (via `just db-up` docker-compose)
- tmux (optional, for `just distributed` multi-process dev)

## Build & Run

```bash
just build          # compile to bin/server
just run            # build + run
just dev            # build + run server & web vite dev server
just proto          # regenerate protobuf (buf generate)
just client-sdk examples/4node-basic  # generate typed TS client SDK
just clean          # remove bin/
```

## Examples

### 4node-basic

Minimal 2x2 mesh demo. Players are circles with click-to-move. Topology-transparent client with optional debug overlays (cell boundaries, AoI radius, replica/ghost markers). Ships with State Integrity enforcement on — `InvariantPanic` and strict netID index — so any state regression fails loudly during smoke testing.

```bash
cd examples/4node-basic && just dev          # single-process all-mode dev
cd examples/4node-basic && just distributed  # 4-process tmux: coord + 2 hosts + gateway
```

## Proto Codegen

Protobuf schemas live in `proto/`. Running `just proto` (or `buf generate`) produces:

- `gen/go/enginepb/` — Go engine protocol
- `gen/go/meshpb/` — Go server-internal mesh protocol
