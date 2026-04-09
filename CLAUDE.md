# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

DO NOT BUILD BINARIES IN THE ROOT. ONLY BUILD INTO bin or dist directories.
Never use `go build ./...` to verify compilation — it drops binaries in the package directory. Use `go vet ./...` or `just build` instead.

```bash
just build          # compile to bin/server
just run            # build + run
just dev            # build + run server & web-pixi vite dev server
just proto          # regenerate protobuf (buf generate)
just client-sdk examples/4node-basic  # generate typed TS client SDK
just clean          # remove bin/
```

The web test client is served at `http://localhost:8080` automatically.

## Architecture

2D space MMORPG server in Go (`github.com/zenion/mmoserver`). Server-authoritative — the Unity client (and web canvas test client) are dumb renderers. Uses a decoupled engine (`pkg/`) with ECS, WebSocket + UDP transport, protobuf serialization, and multi-node server meshing. Game logic lives in `internal/game/` where `GameWorld` embeds `*mmokit.WorldBase`.

The `pkg/` layer is a **generic, reusable 2D game engine** with zero imports from `internal/`. It may import `gen/go/enginepb/` (engine proto) but never game-specific protos (`gen/go/gamepb/`, `gen/go/basicpb/`, etc.).

### Package Layout

**Generic engine (`pkg/` — no `internal/` imports, may import `gen/go/enginepb/`):**

- `pkg/engine/` — ECS world, game loop, interactive console (CommandGroup, Table, builtins), tick queue, entity registry, perf profiling, Configurable interface
- `pkg/metrics/` — per-node observability: Counter, Gauge, EWMA primitives, `NodeMetrics` collector, `LoadSnapshot`, Prometheus-compatible HTTP handler (`/metrics` auto-registered by Coordinator)
- `pkg/universe/` — server meshing: `Coordinator`, `Node`, `NodeBridge`, `GameWorld` interface, topology, inter-node messaging, metrics wiring. Games implement `GameWorld` to plug into the meshing infrastructure
- `pkg/net/` — transport interfaces + WebSocket/UDP implementations, connection manager, byte counters (`ByteCounter` interface)
- `pkg/ops/` — serialization-agnostic operation router (request/response over reliable channel)
- `pkg/component/` — generic ECS components (Position, Velocity, Rotation, Collider, NetworkID, Health, Shield, Lifetime, Ghost, Replica, etc.)
- `pkg/system/` — generic systems (physics, lifetime, click-to-move, direction-move, spatial grid, replication with delta encoding)
- `pkg/mmokit/` — single-import facade re-exporting all `pkg/` types; system factories (`NewInputSystem`, `NewNetworkSystem`, `NewSpatialSystem`, etc.); `DefaultReplicationConfig` helper; `Protocol` for schema export
- `pkg/orderbook/` — generic price-time priority order book matching engine (returns `[]MatchEvent`, caller handles settlement)
- `pkg/spatial/` — spatial hash grid for AoI and collision queries
- `pkg/coords/` — infinite-world cell coordinate system (configurable cell size via `SetCellSize`)
- `pkg/persist/` — Store interface + BoltStore + AsyncWriter
- `pkg/logger/` — category-based debug logging with dynamic registration

**Game-specific (`internal/`):**

- `internal/game/` — all game-specific code in one package: GameWorld, entity kind registration (`entity_kinds.go`), entity files (`entity_*.go` with spawn functions), ECS systems (`system_*.go`), input handlers, `factory.go`, lifecycle, commands, config, player DB, log categories, transfer codec
- `internal/component/` — game-specific ECS components (ShipControl, MiningLaser, Inventory, Equipment, AbilitySet, StatusEffects, etc.)
- `internal/marketplace/` — game-specific marketplace settlement (wraps `pkg/orderbook`, applies Flux currency, bank ops, trade notifications)
- `internal/bot/` — headless bot client for load testing

### Component Imports

Generic components live in `pkg/component`, game-specific in `internal/component`. Files using both need aliased imports:

```go
import (
    comp "github.com/zenion/mmoserver/pkg/component"
    gamecomp "github.com/zenion/mmoserver/internal/component"
)
```

### Server Meshing (`pkg/universe/`)

The engine supports multi-node server meshing via a `GameWorld` interface:

- `Coordinator` creates a configurable grid of `Node` instances (e.g. 3x3 cells)
- Each `Node` runs its own ECS world and game loop
- `NodeBridge` routes inter-node messages (transfers, replicas, chat, spawn requests)
- Entity transfers use `[]byte` serialization — the game world marshals/unmarshals via JSON
- Border entities are replicated to neighboring nodes for seamless AoI
- Games implement `universe.GameWorld` (embed `*mmokit.WorldBase` for defaults) and register via `coord.SetWorld(factory)` or `coord.OnInit(fn)` for simple games
- `GameWorld.Init()` is called after all nodes are created and bridges are wired — use it for entity spawning and replicator registration. `WorldBase.FromSplit()` returns true when the world was created by a cell split (skip initial entity spawning)
- `Coordinator.Build()` creates nodes and wires topology; `Coordinator.Start(ctx)` calls `Build()` if needed, then **blocks** — runs the interactive console, handles SIGINT/SIGTERM, and shuts down all nodes on exit. Set `Headless: true` in Config to disable the console for tests/containers

Key types: `GameWorld` (interface, ~15 methods), `NodeBridge` (interface), `Coordinator`, `Node`, `CellID`, `ReplicaSnapshot`, `NodeMessage`. `Node` exposes a `Base *WorldBase` field for direct infrastructure access — the bridge calls `node.Base` for replica scanning, ghost ticking, dead reckoning, and proxy management without going through the `GameWorld` interface.

Coordinator setup pattern:

```go
coord := mmokit.NewCoordinator(mmokit.Config{
    ...,
    LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) { ... },
})
coord.SetWorld(NewMyWorld)                            // or coord.OnInit(fn) for simple games
coord.SetPlayerRouter(func(username string) string {  // determines which node hosts each player
    return coord.NodeAtPosition(spawnX, spawnY)
})
coord.SetConsole(mmokit.ConsoleOpts{...})            // optional game-specific console config
coord.OnConsoleReady(func(c *mmokit.Console) { ... }) // optional custom commands
coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
coord.Build()   // optional: create nodes without blocking
coord.Start(ctx) // blocks until shutdown (calls Build() if not already called)
```

**Connection proxy:** The Coordinator acts as a connection proxy — it owns all WebSocket connections and processes logins before any node is involved. `Config.LoginHandler` parses protocol-specific login messages and returns `(username, sessionData, error)`. After successful login, `SetPlayerRouter` determines which node hosts the player. The coordinator tracks active sessions globally (`ActiveUserNode(username)`, `ActiveUsers()`) for duplicate detection, reconnection routing, and console command targeting. Entity transfers between nodes update tracking automatically. Games never need to call `SetLoginHandler` on per-node PlayerManagers.

**Cell identity:** `CellID{X, Y int32; Depth uint8}` identifies cells at any quadtree depth. Depth 0 is the original grid. Splitting `{X,Y,D}` produces 4 children at `{2X,2Y,D+1}`, `{2X+1,2Y,D+1}`, `{2X,2Y+1,D+1}`, `{2X+1,2Y+1,D+1}`. Cell size = `BaseCellSize / 2^Depth`. Entities always keep base-cell coordinates regardless of depth — `CellSize()` always returns `coords.CellSize`.

**Dynamic cell partitioning (`DynamicPartitioning` config):** Opt-in quadtree splitting/merging of cells at runtime based on load. Disabled by default (nil config = zero overhead). Enable with `DynamicPartitioning: mmokit.DefaultPartitionConfig()` or the `--dynamic-cells` CLI flag. Supports:
- `SplitCell(cellID, bypass)` / `MergeCell(cellID, bypass)` — programmatic or console-driven
- Automatic monitoring via `PartitionConfig` thresholds (split at 75% tick budget, merge at 20%, EWMA-smoothed, with sustain duration + cooldown)
- Console commands: `cell list/info/split/merge/cooldowns/config`
- `OnTopologyChanged` callback for broadcasting topology updates to clients via `SE_CELL_TOPOLOGY` events
- Docked player sessions are transferred during cell splits (players remain at their station)
- `WorldBase.FromSplit()` lets world factories skip initial entity spawning for split-created worlds

**Console lifecycle:** The Coordinator creates an interactive console on its own goroutine (not tied to any specific node). Node builtins (`node list`, `node load`, `log`, `perf`) are auto-wired. Games add config/entity builtins via `coord.SetConsole(ConsoleOpts{...})` and custom commands via `coord.OnConsoleReady(fn func(*Console))`. Admin commands that target players are routed to the correct node via the coordinator's `activeUsers` tracking. When `DynamicPartitioning` is enabled, `cell` commands are auto-registered. The `debug` console command toggles the topology overlay on all connected clients (sends `SE_CELL_TOPOLOGY` events).

### Game Loop (20Hz fixed timestep in `pkg/engine/loop.go`)

Each tick runs in this order:

1. Process connect/disconnect events
2. Drain admin commands from console
3. Process pending sessions
4. Run all systems (in registration order)
5. Send death notifications
6. Flush entity removals
7. Spawn loot crates from deaths
8. Process respawn requests

### Systems (executed in order, defined in `internal/game/factory.go`)

Input → Docking → TargetLock → ShipControl → Mining → Economy → Equipment → Ability → StatusEffect → Wander → Physics → DeadReckoning → Lifetime → Spatial → Collision → ShieldRegen → Network

Each system implements `System.Update(dt float32)`. Generic systems use factory functions from `mmokit`:

```go
coord.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
coord.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
coord.AddSystem("DeadReckoning", mmokit.NewDeadReckoningSystem())
coord.AddSystem("Spatial", mmokit.NewSpatialSystem())           // or NewSpatialSystemWith(hooks)
coord.AddSystem("Network", mmokit.NewNetworkSystem(setupNetwork)) // or custom struct with DefaultReplicationConfig
```

Game-specific systems use inline factories: `func() mmokit.System { return &MySystem{} }`

### ECS (Ark v0.7.1)

- `Map1[A]` through `Map12[...]` for entity creation and component access
- Use `HasAll()` not `Has()` to check components
- `world.Alive(entity)` before accessing removed entities
- Never spawn/remove entities during query iteration — collect in a slice, process after

### Query[T] (mmokit)

`mmokit.Query[T]` provides ergonomic ECS iteration over component bundle structs. Prefer this for new systems over raw `ecs.FilterN`.

```go
type MySystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct {
        Pos    *comp.Position
        Vel    *comp.Velocity
        Params *comp.MoveParams `ecs:"optional"` // nil when absent
    }]
}

func (s *MySystem) Init() {
    s.entities.Init(s)                          // default: excludes Ghost + Replica
    // s.entities.Init(s, mmokit.IncludeAll())  // no exclusions
    // s.entities.Init(s, mmokit.Without[X]())  // add extra exclusions
}

func (s *MySystem) Update(dt float32) {
    for e, b := range s.entities.All() {
        b.Pos.X += b.Vel.X * dt
        if b.Params != nil { /* optional component present */ }
    }
}
```

Bundle rules: exported fields must be `*ComponentType`. Use `ecs:"optional"` for optional components (nil when absent). `All()` returns `iter.Seq2[ecs.Entity, *T]` — `break` is safe. Also provides `Each()`, `Count()`, `Any()`. Raw `ecs.FilterN` is still available as an escape hatch for max performance.

Note: `pkg/system/` files cannot import `pkg/mmokit` (circular dependency). Use `pkg/query` directly: `query.Query[T]`, `query.Without[T]()`, `query.IncludeAll()`.

### Entity Files

Each entity type has its own file (`internal/game/entity_*.go`) containing only spawn functions (e.g., `SpawnPlayer`, `SpawnAsteroid`). Entity kinds are registered in `internal/game/entity_kinds.go` via `initEntityKinds()` using `mmokit.KindComponent()` and `mmokit.KindComponentLocalOnly()`. Spawn functions use `gw.SpawnEntity()` with `mmokit.WithComponents()` to auto-add all registered kind components.

Current entity types: ship, asteroid, lootcrate, npc, station.

`EntityRegistry` (`pkg/engine/registry.go`) maps entity names to definitions for admin commands.

### Networking & Replication

- WebSocket via `github.com/coder/websocket`, protobuf binary frames
- Channel byte prefix: `0x00` = events, `0x01` = operations
- `ReplicationSystem` (`pkg/system/`) handles per-player AoI visibility, hash-based diff detection, delta encoding, and frame dispatch
- `AutoReplicator` builds entity replicators from `EntityKindDef` registrations — the network system auto-discovers replicators from registered entity kinds via `BuildReplicators()`, no hand-coded nethandlers needed
- Components added to entity kinds via `KindComponent()` are serialized using `net:"..."` struct tags; `KindComponentLocalOnly()` registers components that are added after transfer but not serialized
- `DefaultReplicationConfig(eng, grid)` pre-fills boilerplate; games set `AoIRadius`, callbacks
- Entity state is quantized for bandwidth: `qvel` (int16), `qangle` (uint16), `qnorm` (uint8), `f32` (float32)
- Struct tag encodings: `net:"qvel"` (explicit), `net:"auto"` (inferred from Go type), `net:"initial"` (sent once on visibility enter)

**Topology-transparent protocol:** Clients receive entities in absolute world-space coordinates with zero knowledge of cells, nodes, or grid layout. `SpawnedMsg` contains only `entity_net_id`, `world_x`, `world_y` — no grid metadata. Server mesh topology is a server-internal concern.

**`DebugTopology` config flag:** Single coordinator flag (`DebugTopology: true`) that gates all debug topology info sent to clients:
- `MeshState` binding (per-entity LOCAL/REPLICA/GHOST status + owner node index)
- `CellTopologyMsg` (cell boundaries, depths, node IDs)
- When false (default): clients get a clean, topology-agnostic protocol
- When true (e.g., 4node-basic): clients can render cell boundaries, R/G badges, node ownership
- `IncludeMeshState` on `EngineBindingsConfig` is auto-driven by coordinator's `DebugTopology` at runtime; the EntityKindDef value is used for schema export (nil coordinator)

### Proto Codegen

Source of truth: proto files per package. Run `buf generate` (or `just proto`) to regenerate. Example-specific protos (basicpb, slitherpb) live alongside their examples:

- `proto/enginepb/engine.proto` — generic engine protocol (envelopes, core events, base messages)
  - `gen/go/enginepb/` — Go (package `enginepb`, import as `enginepb "github.com/zenion/mmoserver/gen/go/enginepb"`)
- `proto/gamepb/game.proto` — game-specific messages (imports engine.proto)
  - `gen/go/gamepb/` — Go (package `gamepb`, import as `gamepb "github.com/zenion/mmoserver/gen/go/gamepb"`)
- `gen/csharp/` — Unity client (Engine.cs + Game.cs)
- `gen/es/enginepb/` + `gen/es/gamepb/` — Web client (ES modules via `@bufbuild/protobuf`)

Engine event codes use `enginepb.ClientEventCode_CE_*` / `enginepb.ServerEventCode_SE_*` (values 0-15).
Game event codes use `gamepb.GameClientEventCode_GCE_*` / `gamepb.GameServerEventCode_GSE_*` (values start at 100+ to avoid colliding with engine codes).

### Thread Safety

The ECS world is **not thread-safe**. All ECS reads/writes must happen on the game loop goroutine. The console uses `engine.ExecOnGameLoop()` (5s timeout) to schedule closures that run on the game tick. Admin commands capture `*GameWorld` in closures. Use `Console.Print()`/`Printf()` for output (routes through readline's safe writer).

### Key Mappings

- `GameWorld.PlayerEntities`: connID → ECS entity
- `GameWorld.ConnToUsername`: connID → username
- `GameWorld.NetIDToEntity`: netID → entity (rebuilt each tick by SpatialSystem)
- `GameWorld.PlayerDB`: PlayerRepo — memory-first with async persistence to BoltDB

### Persistence

Memory-first with async writes: `PlayerRepo` (in-memory map) is authoritative. `MarkDirty()` flags changed players; `FlushDirty()` runs every 300 ticks (~15s) via `AsyncWriter` to BoltDB (`data/gameserver.db`).

### Config

All tunable game parameters are in `internal/game/config.go`. The `GameConfig` struct supports reflection-based get/set for runtime tweaking via the server console (`config get/set/list/save/reset`). The generic `Configurable` interface and `ReflectConfig` adapter live in `pkg/engine/configurable.go` — games wire them via `RegisterBuiltins(BuiltinOpts{Config: NewReflectConfig(&cfg)})`. Values copied into components at spawn time (e.g. `ShieldRegenRate`) only affect newly spawned entities.

### Marketplace / Order Book

`pkg/orderbook/` is a generic price-time priority matching engine. It returns `[]MatchEvent` from order placement — the caller decides how to settle trades. `internal/marketplace/settlement.go` wraps it with game-specific Flux currency, bank operations, tax, and trade notifications.

### Web Client

`web-pixi/` — TypeScript/PixiJS game client built with Vite. Run via `just dev` during development. Uses protobuf for server communication. Interpolates between 20Hz server ticks for smooth rendering. Imports from `@gen/engine_pb.js` (engine types) and `@gen/game_pb.js` (game types).

### Debug Logging

All new server-side game logic must include category-based debug logging via `gw.Log.Log(game.CatXxx, ...)`. Game-specific log categories are defined in `internal/game/logcat.go` (e.g. `CatCombat`, `CatMining`, `CatEconomy`). The logger itself (`pkg/logger/`) is generic with dynamic category registration — no game-specific constants. Log significant state changes: item transfers, bank operations, sells, loot pickups, combat events, etc. Include player identity and relevant quantities in log messages (e.g. `"bank deposit: player=%s item=%d qty=%.1f"`).

### Usernames

Usernames are forced lowercase everywhere. Duplicate usernames are rejected at login with a `LoginRejectedMsg`.

### Client SDK Codegen

`cmd/sdkgen/` auto-generates typed TypeScript client SDKs from protocol schema. Go code is the single source of truth — no duplication. Pipeline:

```bash
go run ./examples/4node-basic --dump-schema | go run ./cmd/sdkgen --out examples/4node-basic/web/sdk
```

The `--dump-schema` flag outputs JSON describing client events, server events, and entity replication layouts (extracted from `AutoReplicator` bindings and `Protocol` registrations). The codegen produces a typed client class, entity interfaces, binary delta decoder, and WebSocket transport — all importing directly from `gen/es/` proto types.

### Examples

- `examples/slither/` — Slither.io clone. 2x2 grid, snake movement, food eating, collisions, leaderboard. Uses ReplicationSystem with binary delta encoding and hand-coded replicators. TypeScript/Pixi.js web client built with Vite. Run: `cd examples/slither && just dev`
- `examples/4node-basic/` — Minimal 2x2 mesh demo. Players are circles, click-to-move. Uses AutoReplicator with struct tags for declarative replication. TypeScript/Canvas2D web client built with Vite, using auto-generated SDK. Debug overlays (cell boundaries, AoI radius, replica/ghost markers, node stats). Run: `cd examples/4node-basic && just dev`
