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

2D space MMORPG server in Go (`github.com/zenion/mmoserver`). Server-authoritative — the Unity client (and web canvas test client) are dumb renderers. Uses a decoupled engine (`pkg/`) with ECS, WebSocket + UDP transport, protobuf serialization, and multi-cell server meshing. Game logic lives in `internal/game/` where `GameWorld` embeds `*mmokit.WorldBase`.

The `pkg/` layer is a **generic, reusable 2D game engine** with zero imports from `internal/`. It may import `gen/go/enginepb/` (engine proto) but never game-specific protos (`gen/go/gamepb/`, `gen/go/basicpb/`, etc.).

### Package Layout

**Generic engine (`pkg/` — no `internal/` imports, may import `gen/go/enginepb/`):**

- `pkg/engine/` — ECS world, game loop, interactive console (CommandGroup, Table, builtins), tick queue, entity registry, perf profiling, Configurable interface
- `pkg/metrics/` — per-cell observability: Counter, Gauge, EWMA primitives, `NodeMetrics` collector, `LoadSnapshot`, Prometheus-compatible HTTP handler (`/metrics` auto-registered by Coordinator)
- `pkg/universe/` — server meshing: `Coordinator`, `Cell`, `Host`, `Bridge`, `GameWorld` interface, topology, inter-cell messaging, metrics wiring. `HostNetwork` + `grpcBridge` carry cross-host traffic over `meshpb.MeshData` bidi streams when `Config.TestHosts` is populated; single-host colocated mode is the default and has zero gRPC overhead. Games implement `GameWorld` to plug into the meshing infrastructure
- `pkg/net/` — transport interfaces + WebSocket/UDP implementations, connection manager, byte counters (`ByteCounter` interface)
- `pkg/ops/` — serialization-agnostic operation router (request/response over reliable channel)
- `pkg/component/` — generic ECS components (Position, Velocity, Rotation, Collider, NetworkID, Health, Shield, Lifetime, Ghost, Replica, etc.)
- `pkg/system/` — generic systems (physics, lifetime, click-to-move, direction-move, spatial grid, replication with delta encoding)
- `pkg/mmokit/` — single-import facade re-exporting all `pkg/` types; system factories (`NewInputSystem`, `NewNetworkSystem`, `NewSpatialSystem`, etc.); `DefaultReplicationConfig` helper; `Protocol` for schema export
- `pkg/orderbook/` — generic price-time priority order book matching engine (returns `[]MatchEvent`, caller handles settlement)
- `pkg/spatial/` — spatial hash grid for AoI and collision queries
- `pkg/coords/` — infinite-world cell coordinate system (configurable cell size via `SetCellSize`)
- `pkg/persist/` — domain repository interfaces (`PlayerRepository`, `MarketRepository`, `ConfigRepository`) + snapshot types. Postgres implementation in `pkg/persist/postgres/`; in-memory mocks for game-domain tests in `pkg/persist/persisttest/`
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

The engine supports multi-cell server meshing via a `GameWorld` interface:

- `Coordinator` creates a configurable grid of `Cell` instances (e.g. 3x3 cells)
- Each `Cell` runs its own ECS world and game loop
- `Bridge` routes inter-cell messages (transfers, replicas, chat, spawn requests)
- Entity transfers use `[]byte` serialization — the game world marshals/unmarshals via JSON
- Border entities are replicated to neighboring cells for seamless AoI
- Games implement `universe.GameWorld` (embed `*mmokit.WorldBase` for defaults) and register via `coord.SetWorld(factory)` or `coord.OnInit(fn)` for simple games
- `GameWorld.Init()` is called after all cells are created and bridges are wired — use it for entity spawning and replicator registration. `WorldBase.FromSplit()` returns true when the world was created by a cell split (skip initial entity spawning)
- `Coordinator.Build()` creates cells and wires topology; `Coordinator.Start(ctx)` calls `Build()` if needed, then **blocks** — runs the interactive console, handles SIGINT/SIGTERM, and shuts down all cells on exit. Set `Headless: true` in Config to disable the console for tests/containers

**Multi-process mode (S6+):** a process runs a **set of roles**, specified by `--mode=` as a comma-separated list. The four atomic roles:

- `coordinator` — control plane: MeshControl gRPC server, HostRegistry, GatewayRegistry, AssignmentEngine, admin console. Holds no local cells by itself.
- `host` — in-process cells with static (pre-Build) assignment. Requires `coordinator`.
- `gateway` — terminates WebSocket + proxies client I/O via MeshData. Can stand alone or pair with `coordinator`.
- `node` — dials a remote coordinator via `--coordinator-addr`, registers via MeshControl, receives cell assignments dynamically. Cannot combine with any other role.

Combination rules: `node` is exclusive; `host` requires `coordinator`; `gateway` and `coordinator` can each stand alone or combine. Empty `--mode=` defaults to `all`.

**Common presets:**

- `--mode=all` (default, also implied when `--mode` is omitted) — alias for `coordinator,host,gateway`. Single-process dev server; the classic setup. Set `Config.TestHosts` programmatically to distribute cells across multiple in-process `Host` instances via gRPC loopback (testing-only).
- `--mode=coordinator` — pure control plane. Listens on `--control-listen` (`:9100` default). No WebSocket listener, no cells. Waits for remote nodes + gateways to register.
- `--mode=coordinator,gateway` — control plane + embedded gateway. Gateway terminates WebSockets; coordinator dispatches to nodes. No local cells.
- `--mode=coordinator,host` — control plane + in-process cells, **no** WebSocket listener. Add `--control-listen=:9100` to accept remote node joins alongside local cells; the orchestrator treats local and remote hosts uniformly.
- `--mode=node --coordinator-addr=host:9100 [--host-id=...]` — node worker. No WebSocket listener.
- `--mode=gateway --coordinator-addr=host:9100 [--gateway-id=...]` — standalone gateway. Terminates WebSockets, dials remote coordinator via `meshGatewayClient`, opens MeshData streams to nodes lazily from PeerList broadcasts.

**The WebSocket listener gate is a single check:** `coord.ServesClients()` returns true iff `RoleGateway` is in the role set. Pure-coordinator and pure-node processes don't bind a client port, so they coexist with a standalone gateway on the same host without `:8080` conflicts.

**Gateway as a role:** the gateway can run embedded (paired with `coordinator`) or standalone behind a load balancer. Standalone gateways scale horizontally — many lightweight gateway instances fronting a smaller number of CPU-bound nodes.

**Composite session key:** every wire message related to a client session carries a `{GatewayID, ConnID}` pair. ConnIDs are gateway-local monotonic counters; the gateway ID disambiguates them globally. The coordinator's `sessionRoutes` is keyed on `SessionKey{GatewayID, ConnID}`. Internal only — clients never see it.

**Login on the gateway:** `LoginHandler` runs inline on the gateway using the cached `PeerList` topology. Zero coordinator round-trip at login. The session is announced to the coordinator asynchronously via `SessionAnnounce` (control plane); the player is assigned to the target cell via `MeshFrame.PlayerAssignment` (data plane).

**Cross-host handoff:** when a player entity hands off across host boundaries, the source node sends `HostMessage.PlayerMigrated` to the coordinator. The coordinator atomically bumps the session's epoch in `sessionRoutes` and dispatches a **targeted** `CoordMessage.UpstreamSwitch` to the gateway holding that session — not a broadcast. The gateway updates its local session record; subsequent client input routes to the new authoritative host.

**`local-shortcut` (default) vs `always-proxy`:** `--gateway-mode=local-shortcut` lets the embedded gateway dispatch directly to colocated cells via `cell.Inbox`, skipping the MeshData codec for in-process sessions. `--gateway-mode=always-proxy` forces the codec path even when colocated — used by integration tests to exercise the wire format end-to-end.

**PeerList broadcast:** the coordinator broadcasts `PeerList` (host roster + full cell-to-host ownership table + gateway roster) to every registered host and every registered gateway whenever topology changes. Nodes reconcile `HostNetwork.peers`; gateways reconcile their cached topology. The broadcast fires after every rebalance, after crash reassignment, and as a one-shot targeted send immediately after `RegisterHost` or `RegisterGateway`.

**Liveness:** node heartbeat 1s / dead threshold 3s — killed node's cells reassigned within ~1s. Gateway heartbeat 1s / dead threshold 5s — dead gateway's sessions removed from `sessionRoutes`.

**Gateway crash recovery:** for S6, gateway crash = client reconnect + full re-login. Session tokens for transparent crash recovery are deferred to a follow-up phase.

**Scale-out:** by default an `all` preset process does NOT bind the MeshControl gRPC port — the control plane runs in-memory. Set `--control-listen=:9100` on any `coordinator`-bearing process (e.g. `--mode=all --control-listen=:9100` or `--mode=coordinator,host --control-listen=:9100`) to open the listener. Remote `--mode=node` processes then join the cluster, appear in `host list`, are eligible for cell assignment and migration, and participate in `PeerList` broadcasts. `host list` shows local hosts with a trailing `*` on the state column (e.g. `Live*`) and `---` in the HB-AGE column (they don't heartbeat). The rendezvous + locality placement algorithm runs uniformly over local and remote hosts — there is no "prefer local" carve-out.

**Unified cell transfer protocol (S7):** split, merge, and migrate all ride on a single `meshpb.CellTransfer` message with a `CellTransferKind` discriminator (`SPLIT`, `MERGE`, `MIGRATE`). The orchestrator on the coordinator tracks in-flight transfers, dispatches `CellTransfer` commands via MeshControl, aggregates `CellTransferReady` responses, and commits topology atomically under a single ownership lock. On each host, the executor (`cell_transfer_executor.go`) receives the command, serializes entities per kind, ships them over `MeshData` as entity + session byte blobs, and populates the destination cell before acking `Ready`. The same code path handles single-host transfers (in-process function call through the loopback bridge) and cross-host transfers (gRPC MeshData) with no branching — colocated splits are the fast-path degenerate case of the distributed protocol.

**Locality-weighted placement:** `AssignCellsAcrossHostsWithLocality` is a rendezvous variant that multiplies a candidate host's score by `1 + localityBonus` (15%) when the host already owns at least one Moore-neighborhood (8-connected) neighbor of the cell being assigned. Matches EVE's constellation-locality pattern: adjacent cells cluster on the same host when load is roughly equal, but a genuinely better-scoring host still wins under skew. Used by the split commit path so children prefer to stay on their parent's host unless load demands otherwise.

**`cell migrate <cellID> <hostID>`** (console admin command): invokes the orchestrator with `CellTransferKind=MIGRATE` to move a single cell to a specific host. Goes through the same transfer protocol as split/merge — entities and sessions hand off cleanly, clients see zero disconnect on a successful migration.

**Auto-rebalance (`PartitionConfig.AutoRebalance`, default OFF):** the per-host rebalance loop is wired but silent by default. Operators opt in via `cfg.DynamicPartitioning.AutoRebalance = true`. Defaults: `RebalanceMinDelta=0.20` (hysteresis on source−dest load difference), `RebalanceSustainTime=60s`, `RebalanceCooldown=30s`, `RebalanceMaxConcurrent=1`. When a host stays above threshold for the sustain duration and a lower-loaded host exists with sufficient delta, the orchestrator migrates one cell via the unified transfer protocol, then cools down. The split/merge monitor is orthogonal and ships on by default — auto-rebalance is specifically the cross-host migration loop.

**Graceful shutdown:** SIGINT on a `node` process sends `GracefulLeave` via MeshControl instead of dying immediately. The coordinator's `drainHost` helper migrates every cell owned by that node to surviving hosts via the unified transfer protocol, then responds `CellsDrained`. The node waits up to 30s for the ack and then exits cleanly. `drainHost` is the single source of truth: both the server-side `GracefulLeave` handler and the test path call it directly, so behavior stays identical between production and tests.

**Validation:** `pkg/universe/s6_gateway_test.go` (`TestS6HandoffAcrossNodes`) is the S6 capstone (login + cross-host handoff + disconnect). The S7 test family — `s7_split_test.go`, `s7_merge_test.go`, `s7_migrate_test.go`, `s7_graceful_shutdown_test.go`, `s7_concurrent_test.go` — covers split/merge/migrate across hosts, graceful drain, and concurrent handoff during split under the unified transfer protocol. The `examples/4node-basic` binary accepts all role combinations via `--mode=`; use `--mode=coordinator --control-listen=:9100` + `--mode=node` + `--mode=gateway` on separate processes for operator-driven 4-process setup.

Key types: `GameWorld` (interface, ~15 methods), `Bridge` (interface), `Coordinator`, `Cell`, `CellID`, `ReplicaSnapshot`, `CellMessage`. `Cell` exposes a `Base *WorldBase` field for direct infrastructure access — the bridge calls `cell.Base` for replica scanning, ghost ticking, dead reckoning, and proxy management without going through the `GameWorld` interface.

Coordinator setup pattern:

```go
coord := mmokit.NewCoordinator(mmokit.Config{
    ...,
    LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) { ... },
})
coord.SetWorld(NewMyWorld)                            // or coord.OnInit(fn) for simple games
coord.SetPlayerRouter(func(username string) string {  // determines which cell hosts each player
    return coord.CellAtPosition(spawnX, spawnY)
})
coord.SetConsole(mmokit.ConsoleOpts{...})            // optional game-specific console config
coord.OnConsoleReady(func(c *mmokit.Console) { ... }) // optional custom commands
coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
coord.Build()   // optional: create cells without blocking
coord.Start(ctx) // blocks until shutdown (calls Build() if not already called)
```

**Connection proxy:** The Coordinator acts as a connection proxy — it owns all WebSocket connections and processes logins before any cell is involved. `Config.LoginHandler` parses protocol-specific login messages and returns `(username, sessionData, error)`. After successful login, `SetPlayerRouter` determines which cell hosts the player. The coordinator tracks active sessions globally (`ActiveUserCell(username)`, `ActiveUsers()`) for duplicate detection, reconnection routing, and console command targeting. Entity transfers between cells update tracking automatically. Games never need to call `SetLoginHandler` on per-cell PlayerManagers.

**Cell identity:** `CellID{X, Y int32; Depth uint8}` identifies cells at any quadtree depth. Depth 0 is the original grid. Splitting `{X,Y,D}` produces 4 children at `{2X,2Y,D+1}`, `{2X+1,2Y,D+1}`, `{2X,2Y+1,D+1}`, `{2X+1,2Y+1,D+1}`. Cell size = `BaseCellSize / 2^Depth`. Entities always keep base-cell coordinates regardless of depth — `CellSize()` always returns `coords.CellSize`.

**Dynamic cell partitioning (`DynamicPartitioning` config):** Quadtree splitting/merging of cells at runtime based on load. **On by default** — `NewCoordinator` installs `DefaultPartitionConfig()` when the field is nil. Games that want it off pass `cfg.DynamicPartitioning = mmokit.DisabledPartitionConfig()`. Supports:
- `SplitCell(cellID, bypass)` / `MergeCell(cellID, bypass)` — programmatic or console-driven
- Automatic monitoring via `PartitionConfig` thresholds (split at 75% tick budget, merge at 20%, EWMA-smoothed, with sustain duration + cooldown)
- Console commands: `cell list/info/split/merge/cooldowns/config`
- `OnTopologyChanged` callback for broadcasting topology updates to clients via `SE_CELL_TOPOLOGY` events
- Docked player sessions are transferred during cell splits (players remain at their station)
- `WorldBase.FromSplit()` lets world factories skip initial entity spawning for split-created worlds

**Console lifecycle:** The Coordinator creates an interactive console on its own goroutine (not tied to any specific cell). Cell builtins (`cell list`, `cell load`, `log`, `perf`) are auto-wired. Games add config/entity builtins via `coord.SetConsole(ConsoleOpts{...})` and custom commands via `coord.OnConsoleReady(fn func(*Console))`. Admin commands that target players are routed to the correct cell via the coordinator's `activeUsers` tracking. When `DynamicPartitioning` is enabled, `cell` commands are auto-registered. The `debug` console command toggles the topology overlay on all connected clients (sends `SE_CELL_TOPOLOGY` events).

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

**Topology distribution is game-owned.** The engine no longer ships a `DebugTopology` flag or a built-in `BroadcastCellTopology` helper. Games that want clients to see cell boundaries / R-G replica badges / cell ownership push their own `SE_CELL_TOPOLOGY` events. Pattern (see `examples/4node-basic/world.go`):

- `Coordinator.ClusterCells() []ClusterCellInfo` returns the current cell→host view from local state (single-process) or `cellToHostMap` (multi-process; populated by `PeerList` broadcasts). Available everywhere.
- `WorldBase.ClusterCells()` delegates to the above.
- The game's player-spawn hook builds an `enginepb.CellTopologyMsg` from `ClusterCells()` and sends via `gw.Engine().ConnMgr.SendReliable(connID, frame)` — uses the game's existing engine ConnSender, so it routes correctly through `VirtualConnManager` in node mode.
- For dynamic cells: the game sets `cfg.DynamicPartitioning.OnTopologyChanged` to a closure that re-broadcasts to all connected players on split/merge.
- `IncludeMeshState` on `EngineBindingsConfig` is honored as-declared in the `EntityKindDef`. Schema export and runtime use the same value — no runtime overrides. Set `IncludeMeshState: true` in the EntityKindDef to include the per-entity LOCAL/REPLICA/GHOST byte on the wire.

### Proto Codegen

Source of truth: proto files per package. Run `buf generate` (or `just proto`) to regenerate. Example-specific protos (basicpb, slitherpb) live alongside their examples:

- `proto/enginepb/engine.proto` — generic engine protocol (envelopes, core events, base messages)
  - `gen/go/enginepb/` — Go (package `enginepb`, import as `enginepb "github.com/zenion/mmoserver/gen/go/enginepb"`)
- `proto/gamepb/game.proto` — game-specific messages (imports engine.proto)
  - `gen/go/gamepb/` — Go (package `gamepb`, import as `gamepb "github.com/zenion/mmoserver/gen/go/gamepb"`)
- `proto/meshpb/mesh.proto` — server-internal mesh data plane: `MeshData` (bidi stream of `MeshFrame` envelopes carrying border frames, handoff, and action traffic between hosts) and `MeshControl` (coordinator ↔ host control plane; stubbed for S4). Never consumed by clients.
  - `gen/go/meshpb/` — Go (package `meshpb`, import as `meshpb "github.com/zenion/mmoserver/gen/go/meshpb"`)
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
- `GameWorld.PlayerDB`: PlayerRepo — memory-first with async persistence via `PlayerFlusher` to PostgreSQL

### Persistence

Player state, marketplace orders/trades, and game config live in PostgreSQL via pgx/v5. Schema is managed by `pkg/persist/postgres/migrations/*.sql` files embedded in the binary and applied automatically at process startup by `golang-migrate`. The schema is **hybrid relational + JSONB**: hot fields (`cell_id`, `pos_x`, `pos_y`, `last_login`, `owner`, `location_id`, `item_id`) are typed columns with indexes, while sparse/evolving structures (`currencies`, `cargo`, `bank`, `equipment`) live in JSONB columns.

**Repository pattern:** `pkg/persist/repository.go` defines `PlayerRepository`, `MarketRepository`, `ConfigRepository` — typed, domain-specific interfaces. `pkg/persist/postgres.Store` implements all three via a single `pgxpool.Pool` and exposes them through `Players()`, `Market()`, `Config()` accessors. There is no generic KV abstraction — every persistence operation is typed to its domain. `cmd/server/main.go` opens one `mmokit.OpenPostgres(ctx, url)` and passes the typed handles to the game/marketplace/config wiring.

**Player flush:** `internal/game/PlayerFlusher` tracks dirty players in memory (dirty snapshots captured via `Mark`) and submits batched upserts via `pgx.Batch` on every flush. `GameWorld.postTick()` calls `PlayerDB.FlushDirty(ctx)` every 300 ticks (~15s at 20Hz) and again on shutdown. Snapshots are sorted by username before each batch to satisfy the `PlayerRepository` deadlock-prevention contract. `PlayerFlusher.FlushCell(cellID)` is a stub for S6/S7 cell-migration safety — no caller yet.

**Marketplace order IDs:** Owned by the application. The orderbook's in-memory monotonic counter allocates them; the DB stores them as a plain `BIGINT PRIMARY KEY`. At startup `Settlement.LoadAll` calls `MarketRepository.LoadMaxOrderID` and seeds the counter past the highest persisted value, so there's no per-insert `next_id` counter row and no write amplification. Marketplace writes (place/update-quantity/delete/record-trade) are **synchronous** — settlement calls the repository directly from the operation-router worker goroutines.

**Local dev:**

- `just db-up` — start PostgreSQL 17 via docker-compose
- `just db-psql` — drop into a psql shell
- `just db-reset` — wipe the volume and restart
- `just test-pg` — run the Postgres integration tests (`-tags=pgtest`)

Connection URL defaults to `postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable`; override via `POSTGRES_URL` env var or (for code callers) `mmokit.Config.PostgresURL`.

**No backward compat:** BoltDB, bbolt, and the generic KV interface are gone. Every mmoserver deployment requires a Postgres at Build time. Dev databases under `data/*.db` from the BoltDB era are obsolete; `just db-reset` wipes the docker-compose volume cleanly.

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
- `examples/4node-basic/` — Minimal 2x2 mesh demo. Players are circles, click-to-move. Uses AutoReplicator with struct tags for declarative replication. TypeScript/Canvas2D web client built with Vite, using auto-generated SDK. Debug overlays (cell boundaries, AoI radius, replica/ghost markers, node stats). Run: `cd examples/4node-basic && just dev`. Dev knob: `--gateway-mode=always-proxy` reserves future-use hook (not yet wired for colocated cells in S3). Multi-host distribution for boundary-crossing stress tests is set programmatically via `Config.TestHosts`. Dynamic partitioning is **manual by default** — use `cell split <cellID>` / `cell merge <cellID>` from the server console to drive splits and merges. Pass `--partition-demo` to install a tuned auto-split config (5s sustain, entity-weighted metric) for a live visual split demo. The interactive `bot spawn <count> [cellID]` / `bot clear` / `bot list` console command group is always registered — spawn bots, then either `cell split 0_0` manually or `--partition-demo` + `bot spawn 60` to trigger auto-split.
