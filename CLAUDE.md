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

2D space MMORPG server in Go (`github.com/zenion/mmoserver`). Server-authoritative — the web client is a dumb renderer. Uses a decoupled engine (`pkg/`) with ECS, WebSocket + UDP transport, a typed reflection codec for the client-facing wire stack, and multi-cell server meshing. Game logic lives in `internal/game/` where `GameWorld` embeds `*mmokit.Stage`.

The `pkg/` layer is a **generic, reusable 2D game engine** with zero imports from `internal/`. The only protobuf in the codebase is `gen/go/meshpb/` (server-internal mesh data plane); client-facing wire frames are typed reflection-codec, not proto.

### Package Layout

**Generic engine (`pkg/` — no `internal/` imports, may import `gen/go/enginepb/`):**

- `pkg/engine/` — ECS world, game loop, interactive console (`cmdsysAdapter`, Table, builtins), tick queue, entity registry, perf profiling, Configurable interface
- `pkg/metrics/` — per-cell observability: Counter, Gauge, EWMA primitives, `NodeMetrics` collector, `LoadSnapshot`, Prometheus-compatible HTTP handler (`/metrics` auto-registered by Coordinator)
- `pkg/universe/` — server meshing: `Coordinator`, `Cell`, `Host`, `Bridge`, `GameWorld` interface, topology, inter-cell messaging, metrics wiring. `HostNetwork` + `grpcBridge` carry cross-host traffic over `meshpb.MeshData` bidi streams in multi-process mode (`--mode=host --coordinator-addr=…`); single-host colocated mode is the default and has zero gRPC overhead. Games implement `GameWorld` to plug into the meshing infrastructure
- `pkg/net/` — transport interfaces + WebSocket/UDP implementations, connection manager, byte counters (`ByteCounter` interface)
- `pkg/ops/` — serialization-agnostic operation router (request/response over reliable channel)
- `pkg/component/` — generic ECS components (Position, Velocity, Rotation, Collider, NetworkID, Health, Shield, Lifetime, Ghost, Replica, etc.)
- `pkg/system/` — generic systems (physics, lifetime, click-to-move, direction-move, spatial grid, replication with delta encoding)
- `pkg/mmokit/` — single-import facade re-exporting all `pkg/` types; system factories (`NewNetworkSystem`, `NewSpatialSystem`, etc.); `OnInput` / `OnInputWith` for input handler registration; `DefaultReplicationConfig` helper; `Protocol` for schema export
- `pkg/orderbook/` — generic price-time priority order book matching engine (returns `[]MatchEvent`, caller handles settlement)
- `pkg/spatial/` — spatial hash grid for AoI and collision queries
- `pkg/coords/` — infinite-world cell coordinate system (configurable cell size via `SetCellSize`)
- `pkg/persist/` — domain repository interfaces (`PlayerRepository`, `MarketRepository`, `ConfigRepository`) + snapshot types. Postgres implementation in `pkg/persist/postgres/`; in-memory mocks for game-domain tests in `pkg/persist/persisttest/`
- `pkg/admin/` — admin/observability dashboard backend: `ClusterView` over `*Process`, `TopicBus` for live SSE updates, `/admin/api/*` HTTP routes (auth, cluster, cells, hosts, gateways, players, events, perf, commands, stream, audit, panels, logs), session-cookie auth via argon2id + `pkg/services/auth` primitives, `PanelRegistry` for game-extensible sidebar entries. The Svelte SPA source is in `web-admin/`; `bun run build` writes the bundle to `pkg/admin/static/dist/`, which the binary serves via `//go:embed`
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
- Games wire entity kinds and lifecycle hooks via `mmokit.RegisterKind[T]` and `mmokit.OnPlayerJoin`
- `Stage.FromSplit()` returns true when the stage was created by a cell split (skip initial entity spawning in `OnPlayerJoin` hooks when appropriate)
- `Coordinator.Build()` creates cells and wires topology; `Coordinator.Start([ctx])` calls `Build()` if needed, then **blocks** — runs the interactive console, handles SIGINT/SIGTERM, and shuts down all cells on exit. The ctx arg is optional (variadic): omit it for the common case (`coord.Start()`), pass one only when you need to drive shutdown externally. Set `Headless: true` in Config to disable the console for tests/containers

**Multi-process mode (S6+):** a process runs a **set of roles**, specified by `--mode=` as a comma-separated list. The three atomic roles:

- `coordinator` — control plane: MeshControl gRPC server, HostRegistry, GatewayRegistry, AssignmentEngine, admin console. Holds no local cells by itself.
- `host` — owns cells. In-process when paired with `coordinator`; **remote** when used alone with `--coordinator-addr=HOST:PORT` — the host dials the coordinator via MeshControl and receives cell assignments dynamically.
- `gateway` — terminates WebSocket + proxies client I/O via MeshData. Can stand alone or pair with `coordinator`.

Combination rules: bare `host` (no coordinator) requires `--coordinator-addr`; every other combination is accepted. Empty `--mode=` defaults to `all`.

**Control plane vs data plane.** The coordinator owns control-plane state (host roster, cell ownership, service routing, event-bus subscriptions). It is NEVER on the per-tick or per-action data path. Data flows directly between hosts/gateways/service-hosts over MeshData streams. The service event bus (`pkg/service.Bus`) enforces this principle by construction: coordinator stores subscriptions but never sees event payloads.

**Common presets:**

- `--mode=all` (default, also implied when `--mode` is omitted) — alias for `coordinator,host,gateway`. Single-process dev server; the classic setup. For multi-host stress testing, spawn separate `--mode=host --coordinator-addr=…` processes (see `just distributed-space` / `examples/4node-basic just distributed`).
- `--mode=coordinator` — pure control plane. Listens on `--control-listen` (`:9100` default). No WebSocket listener, no cells. Waits for remote hosts + gateways to register.
- `--mode=coordinator,gateway` — control plane + embedded gateway. Gateway terminates WebSockets; coordinator dispatches to remote hosts. No local cells.
- `--mode=coordinator,host` — control plane + in-process cells, **no** WebSocket listener. Add `--control-listen=:9100` to accept remote host joins alongside local cells; the orchestrator treats local and remote hosts uniformly.
- `--mode=host --coordinator-addr=host:9100 [--host-id=...]` — remote host worker. No WebSocket listener. Was `--mode=node` before the role unification; `--mode=node` now errors with a migration hint.
- `--mode=gateway --coordinator-addr=host:9100 [--gateway-id=...]` — standalone gateway. Terminates WebSockets, dials remote coordinator via `meshGatewayClient`, opens MeshData streams to hosts lazily from PeerList broadcasts.

**The WebSocket listener gate is a single check:** `coord.ServesClients()` returns true iff `RoleGateway` is in the role set. Pure-coordinator and remote-host processes don't bind a client port, so they coexist with a standalone gateway on the same host without `:8080` conflicts.

**Gateway as a role:** the gateway can run embedded (paired with `coordinator`) or standalone behind a load balancer. Standalone gateways scale horizontally — many lightweight gateway instances fronting a smaller number of CPU-bound hosts.

**Composite session key:** every wire message related to a client session carries a `{GatewayID, ConnID}` pair. ConnIDs are gateway-local monotonic counters; the gateway ID disambiguates them globally. The coordinator's `sessionRoutes` is keyed on `SessionKey{GatewayID, ConnID}`. Internal only — clients never see it.

**Login on the gateway:** `LoginHandler` runs inline on the gateway using the cached `PeerList` topology. Zero coordinator round-trip at login. The session is announced to the coordinator asynchronously via `SessionAnnounce` (control plane); the player is assigned to the target cell via `MeshFrame.PlayerAssignment` (data plane).

**Cross-host handoff:** when a player entity hands off across host boundaries, the source host sends `HostMessage.PlayerMigrated` to the coordinator. The coordinator atomically bumps the session's epoch in `sessionRoutes` and dispatches a **targeted** `CoordMessage.UpstreamSwitch` to the gateway holding that session — not a broadcast. The gateway updates its local session record; subsequent client input routes to the new authoritative host.

**`local-shortcut` (default) vs `always-proxy`:** `--gateway-mode=local-shortcut` lets the embedded gateway dispatch directly to colocated cells via `cell.Inbox`, skipping the MeshData codec for in-process sessions. `--gateway-mode=always-proxy` forces the codec path even when colocated — used by integration tests to exercise the wire format end-to-end.

**PeerList broadcast:** the coordinator broadcasts `PeerList` (host roster + full cell-to-host ownership table + gateway roster) to every registered host and every registered gateway whenever topology changes. Remote hosts reconcile `HostNetwork.peers`; gateways reconcile their cached topology. The broadcast fires after every rebalance, after crash reassignment, and as a one-shot targeted send immediately after `RegisterHost` or `RegisterGateway`.

**Liveness:** host heartbeat 1s / dead threshold 3s — killed host's cells reassigned within ~1s. Gateway heartbeat 1s / dead threshold 5s — dead gateway's sessions removed from `sessionRoutes`.

**Gateway crash recovery:** for S6, gateway crash = client reconnect + full re-login. Session tokens for transparent crash recovery are deferred to a follow-up phase.

**Scale-out:** by default an `all` preset process does NOT bind the MeshControl gRPC port — the control plane runs in-memory. Set `--control-listen=:9100` on any `coordinator`-bearing process (e.g. `--mode=all --control-listen=:9100` or `--mode=coordinator,host --control-listen=:9100`) to open the listener. Remote `--mode=host --coordinator-addr=…` processes then join the cluster, appear in `host list`, are eligible for cell assignment and migration, and participate in `PeerList` broadcasts. `host list` shows local hosts with a trailing `*` on the state column (e.g. `Live*`) and `---` in the HB-AGE column (they don't heartbeat). The rendezvous + locality placement algorithm runs uniformly over local and remote hosts — there is no "prefer local" carve-out.

**Entity handoff (hard-cut at a cluster-tick boundary):** when an entity crosses a cell boundary, authority transfer is single-shot. The source host sends one `meshpb.Handoff` message declaring `commitTick = currentClusterTick + 2` (≈100 ms at 20 Hz). At end-of-tick `commitTick-1` the source demotes `Live → Replica`; at start-of-tick `commitTick` the destination promotes `Replica → Live` (or spawns from `transfer_blob` if no replica exists yet). No warmup, no overlap, no Shadow. A 20-tick cooldown prevents boundary-hover thrash. The `handoff_driver` queues demote/promote actions until the commit tick fires, synchronized across cells via `ClusterClock`.

**Unified cell transfer protocol (S7):** split, merge, and migrate all ride on a single `meshpb.CellTransfer` message with a `CellTransferKind` discriminator (`SPLIT`, `MERGE`, `MIGRATE`). The orchestrator on the coordinator tracks in-flight transfers, dispatches `CellTransfer` commands via MeshControl, aggregates `CellTransferReady` responses, and commits topology atomically under a single ownership lock. On each host, the executor (`cell_transfer_executor.go`) receives the command, serializes entities per kind, ships them over `MeshData` as entity + session byte blobs, and populates the destination cell before acking `Ready`. The same code path handles single-host transfers (in-process function call through the loopback bridge) and cross-host transfers (gRPC MeshData) with no branching — colocated splits are the fast-path degenerate case of the distributed protocol.

**Commit-plan model (Phase B):** applySplit/Merge/MigrateCommit are no longer imperative — each is a thin dispatcher over a data-driven `CommitPlan` with named `PlanStep`s. Builders live in `commit_builders_{split,merge,migrate}.go`; `ExecuteCommitPlan` walks the steps, running invariants at entry, between steps, and at exit. Step names are stable (`apply-coord-mutation`, `rename-survivor-host`, `remap-sessions`, `release-donors`, etc.) — they appear in the commit log and are the filter axis for diagnostics. `TestBuild{Split,Merge,Migrate}Plan_StepOrdering` pins the sequence so future edits can't silently reorder.

**MERGE drain freeze:** during a MERGE, donor cells keep ticking between `executor.Execute` (serializes entities) and the commit firing. Without protection, the donor's `handoff_driver` could ship entities to the survivor via normal boundary handoff AFTER they were already included in merge populate → duplicate netIDs on the survivor. Fix: `Stage.drainingForMerge` atomic.Bool set inside the serialize `RunOnLoop` closure; `HandoffDriver.Tick` early-returns and drops pending crossings while set; cleared implicitly when the cell is torn down by `stepMergeReleaseDonors`, explicitly cleared by `executor.Abort` or on Execute-time errors.

**Locality-weighted placement:** `AssignCellsAcrossHostsWithLocality` is a rendezvous variant that multiplies a candidate host's score by `1 + localityBonus` (15%) when the host already owns at least one Moore-neighborhood (8-connected) neighbor of the cell being assigned. Matches EVE's constellation-locality pattern: adjacent cells cluster on the same host when load is roughly equal, but a genuinely better-scoring host still wins under skew. Used by the split commit path so children prefer to stay on their parent's host unless load demands otherwise.

**`cell migrate <cellID> <hostID>`** (console admin command): invokes the orchestrator with `CellTransferKind=MIGRATE` to move a single cell to a specific host. Goes through the same transfer protocol as split/merge — entities and sessions hand off cleanly, clients see zero disconnect on a successful migration.

**Auto-rebalance (`PartitionConfig.AutoRebalance`, default OFF):** the per-host rebalance loop is wired but silent by default. Operators opt in via `cfg.DynamicPartitioning.AutoRebalance = true`. Defaults: `RebalanceMinDelta=0.20` (hysteresis on source−dest load difference), `RebalanceSustainTime=60s`, `RebalanceCooldown=30s`, `RebalanceMaxConcurrent=1`. When a host stays above threshold for the sustain duration and a lower-loaded host exists with sufficient delta, the orchestrator migrates one cell via the unified transfer protocol, then cools down. The split/merge monitor is orthogonal and ships on by default — auto-rebalance is specifically the cross-host migration loop.

**Graceful shutdown:** SIGINT on a remote-host process (`--mode=host --coordinator-addr=…`) sends `GracefulLeave` via MeshControl instead of dying immediately. The coordinator's `drainHost` helper migrates every cell owned by that host to surviving hosts via the unified transfer protocol, then responds `CellsDrained`. The host waits up to 30s for the ack and then exits cleanly. `drainHost` is the single source of truth: both the server-side `GracefulLeave` handler and the test path call it directly, so behavior stays identical between production and tests.

**Validation:** `pkg/universe/s6_gateway_test.go` (`TestS6HandoffAcrossNodes`) is the S6 capstone (login + cross-host handoff + disconnect). The S7 test family — `s7_split_test.go`, `s7_merge_test.go`, `s7_migrate_test.go`, `s7_graceful_shutdown_test.go`, `s7_concurrent_test.go` — covers split/merge/migrate across hosts, graceful drain, and concurrent handoff during split under the unified transfer protocol. The `examples/4node-basic` binary accepts all role combinations via `--mode=`; from inside `examples/4node-basic/` run `just distributed` to spin up 4 processes in a tmux session (coordinator + 2 hosts + gateway), or hand-roll with `--mode=coordinator --control-listen=:9100 --admin-listen=:9101` + `--mode=host --coordinator-addr=localhost:9100 --host-id=NAME` + `--mode=gateway`. The space game has the equivalent `just distributed-space` at the repo root.

### State Integrity framework (`pkg/universe/integrity.go` + commit log + netIDIndex)

Four layers of runtime guards that catch wrong states at the point of violation rather than hours later:

**Invariants (`integrity.go`):** `type Invariant struct { Name string; Check func(*Process) error }`. Five default checks run at every commit entry and exit (and after each plan step unless suppressed): `invCoordMapsConsistent`, `invHostOwnershipMatchesCoord`, `invTopologyNeighborsOwned`, `invSessionRouteHostLive`, `invNoDuplicatePresencePerCell`. `Config.InvariantMode` is `InvariantOff` (production default), `InvariantLog` (record + continue), or `InvariantPanic` (record + panic — dev/test default). Fixtures set `InvariantPanic`; any latent state regression fails loudly during smoke testing. Mid-step suppression is available via `PlanStep.Invariants = noInvariantsSentinel` when a step legitimately transitions through an intermediate inconsistent state (see `mergeNoInvariants` on `apply-coord-mutation`).

**Commit log (`commit_log.go`):** in-memory ring (default capacity 1024, set via `Config.CommitLogCapacity`) of `CommitEvent` records — one append per plan step, one per invariant violation, one per host join/leave. Every event carries `CommitID`, `Scenario` (Split/Merge/Migrate), `StepIndex`, `Step`, `Success`, `DurationMs`, `Affected`, `HostIDs`, `Error`. Query APIs: `Recent(n)`, `ByCommitID(id)`, `ByCell(cellID)`, `Since(t)`. Append fan-outs to the logger under category `events:<kind>` so operators can tail live via `log events:*`.

**Console + HTTP query paths:**
- Admin console: `commit.log` with optional `--n=N`, `--commit=ID`, `--cell=CELLID`, `--since=DUR` (Go duration string).
- HTTP: `GET /events?n=N&commit=ID&cell=CELLID&since=DUR` returns the matching events as a JSON array.
- Endpoints are registered on the gateway HTTP mux (RoleGateway processes) AND on an optional admin HTTP listener bound by `Config.AdminListen` / `--admin-listen=:9101` — used on pure-coordinator processes (where commits actually run) because they don't bind the client HTTP port. See `examples/4node-basic just distributed` for the canonical distributed setup.

**netIDIndex (`netid_index.go`):** per-cell `map[netID] → {entity, presence}` where presence ∈ `{Live, Replica}` (the Shadow presence was removed when the hard-cut handoff landed). Every spawn path must call `Enter(netID, entity, presence)`: `SpawnFromTransferCore` (takes presence as an arg), `upsertBorderReplica` (PresenceReplica), `Stage.Spawn` (local spawn, PresenceLive). Authority transitions go through the sanctioned primitives `Demote(netID)` (Live → Replica at handoff-commit on the source) and `Promote(netID)` (Replica → Live at handoff-commit on the destination). `OnEntityRemoved` fires `Exit(netID)` during `FlushRemovals`. The transition policy is a compact 2×2 table (see `TestNetIDIndex_Transitions`): e.g. `Replica→Live = ActionReplaced` (swap entity, remove prev), `Live→Live = ActionRejected`. `Config.StrictNetIDIndex` controls enforcement — false = observational (log only), true = reject duplicates and roll back the offending spawn. 4node-basic ships with `StrictNetIDIndex: true` after commit `e4ede97` closed the handoff-race root cause.

**Log categories (all `events:*` auto-registered on Process.New):**
- `integrity` — invariant violation details
- `events:split` / `events:merge` / `events:migrate` — per-step commit events
- `events:invariant` — violation events
- `events:host` — host join/leave
- `events:session` — session route remap (reserved; wiring deferred)

Key types: `Bridge` (interface), `Coordinator`, `Cell`, `CellID`, `Stage`, `ReplicaSnapshot`, `CellMessage`. `Cell` exposes a `Stage *Stage` field for direct infrastructure access — the bridge calls `cell.Stage` for replica scanning, ghost ticking, and proxy management.

**Typed world access:** `mmokit.WorldOf[*MyWorld](sys)` type-asserts the `GameWorld()` on any `SystemBase`-embedding system; `mmokit.WorldOfCell[*MyWorld](cell)` does the same from a `*universe.Cell`. Both panic with a clear message on mismatch. Prefer over raw `.(type)` casts in system `Init()` methods.

**`Stage.SendEvent(connID, code, msg)`** builds and sends a reliable serialized event frame using the cell's engine connection manager. Use in place of manually calling `gw.Engine().ConnMgr.SendReliable`.

Coordinator setup pattern:

```go
coord := mmokit.New(mmokit.Config{
    ...,
    LoginHandler: mmokit.HandleLogin(CE_LOGIN, func(m *MyLoginMsg) (string, any, error) { ... }),
})
// MyComponents is a bundle struct: each *Comp pointer field becomes
// an auto-registered kind component. Tag with `mmokit:"local"` for
// fields that exist on the destination but aren't serialized.
mmokit.RegisterKind[MyComponents](coord, KindFoo, "Foo", bindings)
// Optional per-field/per-kind overrides (variadic):
//   mmokit.WithField[MyComp]( mmokit.WithMarshal(...), mmokit.WithBinding(...) )
//   mmokit.WithExtraBinding(b)  // kind-scoped extra (e.g. QAngle for Rotation)
coord.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
    stage.SpawnPlayer(s,
        mmokit.EntityKind{Type: KindFoo},
        mmokit.Collider{Radius: 5},
        // every other component value the kind needs — pass by value
    )
})
coord.SetPlayerRouter(func(username string) string {  // determines which cell hosts each player
    return coord.CellAtPosition(spawnX, spawnY)
})
coord.SetConsole(mmokit.ConsoleOpts{...})            // optional game-specific console config
coord.OnConsoleReady(func(c *mmokit.Console) { ... }) // optional custom commands
coord.AddSystem(mmokit.NewPhysicsSystem())
coord.Build()   // optional: create cells without blocking
coord.Start()    // blocks until shutdown (calls Build() if not already called); pass a ctx to override
```

**Connection proxy:** The Coordinator acts as a connection proxy — it owns all WebSocket connections and processes logins before any cell is involved. `Config.LoginHandler` parses protocol-specific login messages and returns `(username, sessionData, error)`. After successful login, `SetPlayerRouter` determines which cell hosts the player. The coordinator tracks active sessions globally (`ActiveUserCell(username)`, `ActiveUsers()`) for duplicate detection, reconnection routing, and console command targeting. Entity transfers between cells update tracking automatically. Games never need to call `SetLoginHandler` on per-cell PlayerManagers.

**Cell identity:** `CellID{X, Y int32; Depth uint8}` identifies cells at any quadtree depth. Depth 0 is the original grid. Splitting `{X,Y,D}` produces 4 children at `{2X,2Y,D+1}`, `{2X+1,2Y,D+1}`, `{2X,2Y+1,D+1}`, `{2X+1,2Y+1,D+1}`. Cell size = `BaseCellSize / 2^Depth`. Entities always keep base-cell coordinates regardless of depth — `CellSize()` always returns `coords.CellSize`.

**Dynamic cell partitioning (`DynamicPartitioning` config):** Quadtree splitting/merging of cells at runtime based on load. **Off by default** — `Config.DynamicPartitioning` is nil unless the game explicitly assigns `mmokit.DefaultPartitionConfig()` (or a custom `*PartitionConfig`). Supports:
- `SplitCell(cellID, bypass)` / `MergeCell(cellID, bypass)` — programmatic or console-driven
- Automatic monitoring via `PartitionConfig` thresholds (split at 75% tick budget, merge at 20%, EWMA-smoothed, with sustain duration + cooldown)
- Console commands: `cell list/info/split/merge/cooldowns/config`
- `OnTopologyChanged` callback for broadcasting topology updates to clients via `SE_CELL_TOPOLOGY` events
- Docked player sessions are transferred during cell splits (players remain at their station)
- `Stage.FromSplit()` lets spawn hooks skip initial entity spawning for split-created stages

**Console lifecycle:** The Coordinator creates an interactive console on its own goroutine (not tied to any specific cell). Cell builtins (`cell list`, `cell load`, `log`, `perf`) are auto-wired. Games add config/entity builtins via `coord.SetConsole(ConsoleOpts{...})` and custom commands via `coord.OnConsoleReady(fn func(*Console))`. Admin commands that target players are routed to the correct cell via the coordinator's `activeUsers` tracking. When `DynamicPartitioning` is enabled, `cell` commands are auto-registered. The `debug` console command toggles the topology overlay on all connected clients (sends `SE_CELL_TOPOLOGY` events).

**Admin dashboard** (auto-enabled when `--admin-listen` is set; disable with `--admin-enabled=false`): mounts the engine-shipped admin SPA + JSON/SSE API on the AdminListen mux at `/admin/*`. Operators live in the `admin_operators` Postgres table managed via `persist.AdminOperatorRepository`; `admin.NewServer` seeds a default `admin`/`admin` operator with `*.*` grants on first run when the table is empty (the credentials are logged with a "CHANGE IN PRODUCTION" banner). Operator management is exposed via the `admin.operator.*` cmdsys verbs (`create`, `delete`, `password`, `list`) — runnable from the server console (passwords prompted via TTY, no echo) and from the admin UI's `/users` page. `delete` enforces a last-operator guard and a self-delete guard (HTTP-only; the console caller never matches an operator username). `mmokit.New` auto-wires `DefaultAdminServerFactory` when admin is enabled and no custom factory is set; `Build()` requires `--postgres-url` when admin is enabled and panics otherwise. Live updates flow over SSE topics (`cells`, `hosts`, `events`, `topology`, `alerts`); commands route through the existing `cmdsys.Dispatcher` with `Caller.Source = SourceAdminHTTP`. Games register custom sidebar panels via `mmokit.RegisterAdminPanel(coord, AdminPanelDef{...})`. All cluster reads go through the `ClusterView` interface; `LocalClusterView` is the v1 in-process impl, with a future `RemoteClusterView` enabling a `--mode=admin` standalone role. See [docs/superpowers/specs/2026-05-10-admin-dashboard-design.md](docs/superpowers/specs/2026-05-10-admin-dashboard-design.md).

**Admin feature convention.** Every operator-facing admin action MUST be implemented as a cmdsys verb first. The console gets the verb for free (registered against `Process.CmdRegistry()`), the admin UI calls it via `POST /admin/api/commands/<verb>`, and an audit-log entry is recorded automatically. Don't reach for ad-hoc HTTP routes or per-page handlers — they bypass RBAC, the audit log, and the dual-surface (console + UI) requirement. The four `admin.operator.*` verbs are the worked example: backed by `persist.AdminOperatorRepository`, registered in [pkg/admin/operator_commands.go](pkg/admin/operator_commands.go), exposed as the `/users` route in the SPA, and reachable from the server console with TTY-prompted passwords via `cmd:"secret"` field tags.

The SPA lives in `web-admin/` (Svelte 5 + Vite + Bun + Tailwind v4 + `@lucide/svelte`). `bun run build` outputs directly into `pkg/admin/static/dist/` so the binary's `//go:embed` picks it up. Local dev: `just admin-dev` runs Vite on `:5173` proxying API calls to a backend at `:9101`. CI/release: `just admin-build` regenerates the bundle; wired into the top-level `just build`. The Cluster page is canvas-rendered (`CellMap.svelte`) with quadtree-aware nesting (cell IDs `X_Y` at depth 0, `X_Y:1..4` for splits → NW/NE/SW/SE quadrants); click a cell → `CellDrawer` with split/merge/migrate actions. Live updates flow through one multiplexed SSE connection at `/admin/api/stream`. Stores using Svelte 5 runes live in `web-admin/src/lib/stores.svelte.ts` (the `.svelte.ts` extension is required for `$state` outside `.svelte` files).

The Hosts, Gateways, and Players routes (`/hosts`, `/gateways`, `/players`) consume `Process.HostListEntries` / `GatewayListEntries` / `ActivePlayerSnapshots` — typed accessors that aggregate registry + metrics state. Live updates flow on the `hosts` / `gateways` / `players` SSE topics (one shared 1Hz `rosterPublisher` ticker). Player operations (tp / tpto / kick) POST to `/admin/api/commands/<verb>`, dispatched through the existing cmdsys with the operator's grants. Offline player listing is a placeholder until `PlayerRepository` exposes a search API. Hosts/Gateways tables use a shared `DataTable.svelte` component (sortable, headless); the Players table is hand-rolled because each row needs interactive op buttons.

Performance data lives inside the Cluster page rather than a dedicated route: a 60-sample SPA-side ring buffer (`metricsHistoryStore`) is fed by the `cells` SSE topic, and clicking a cell opens `CellDrawer` with per-cell sparklines (load, tick p99 µs, real entities, bytes/sec) plus a `BarChart` system-time drilldown. The drawer calls `/admin/api/perf/<cellId>`, which routes through the `perf.snapshot` cmdsys verb (`RouteAllHosts`) via `Process.PerfSnapshotForCell` so it works in distributed mode without per-host wiring. The `/performance` URL is kept as an alias for `/cluster` in [app.svelte](web-admin/src/app.svelte). The Events route (`/events`) tails `/admin/api/events` + the `events` SSE topic with client-side filters (scenario / kind / cell-or-host substring) and a pause toggle. Sparklines are zero-dep canvas (`Sparkline.svelte`).

The Command palette (`⌘K`, `CommandPalette.svelte`) is a VS-Code-style entity **finder** — search and navigation only, NOT command invocation. It reads `cellsStore` / `hostsStore` / `gatewaysStore` / `playersStore` directly (no API call), fuzzy-matches the typed query, and on Enter writes a `pendingNav` signal + navigates to the appropriate route — `/cluster` for cells, `/nodes` for hosts/gateways, `/players` for players. Routes consume `pendingNav` in a `$effect` to open the right detail surface. Commands are invoked from per-page UI surfaces (e.g. PlayerDrawer buttons, PanelHost toolbars, the Users page actions), never from the palette.

The Audit, Logs, and Settings routes (`/audit`, `/logs`, `/settings`) round out the sidebar. Audit reads the in-memory `AuditLog` ring via `/admin/api/audit?n=N`; Logs lists categories grouped by prefix and toggles them through new `/admin/api/logs/categories` endpoints (GET list, POST per-category enable). Settings is read-only — operator info from `sessionStore`; active-session table awaits a `SessionStore.List` method (Phase 2). Per-row drawers (`NodeDrawer.svelte`, `PlayerDrawer.svelte`) replicate the Cluster page's `CellDrawer` pattern: clicking a row opens detail + actions; the ⌘K palette opens drawers directly when the picked entity is in the live store. PlayerDrawer's "load full info" button POSTs the `player.info` cmdsys verb so game-specific fields are accessible without a custom panel.

Games register custom admin panels with `mmokit.RegisterAdminPanel(coord, AdminPanelDef{...})` and push live data via `mmokit.PublishAdminTopic(coord, topic, payload)`. The SPA's `PanelHost.svelte` is the single renderer for every game-registered panel — it subscribes to the declared topic, auto-derives a `DataTable` from row-array payloads, and renders one toolbar button per declared cmdsys verb. Buttons with non-empty `argsSchema` open a generic `ArgsModal.svelte` that builds typed inputs from `pkg/cmdsys/schema.go::FieldSchema`. `FieldSchema` carries a `Secret bool` flag (set by `cmd:"secret"`); the console TTY-prompts for those fields (no echo) and `ArgsModal` renders them as `<input type="password">`. Zero-arg verbs POST directly. The 4node-basic Bots panel (`/panel/bots`, registered in `examples/4node-basic/main.go`) is the worked example — it publishes a per-cell bot count via `admin_bots_publisher.go` and exposes `bot.spawn` / `bot.clear` / `bot.list` from the toolbar. Game-side code never imports `pkg/admin` directly; the `mmokit` facade owns the per-`*Process` bus (`adminBusMap`) so panels and publishers exist alongside the rest of the game wiring in Go.

The Logs page (`/logs`) renders a live cluster-wide log tail. Each host's `*logger.Logger` carries a `Hook` (added by `mmokit`) that batches entries every ~100ms or 64 entries (whichever first) and ships them as `meshpb.HostMessage.LogBatch` over the existing MeshControl bidi stream. The coordinator's controlServer demuxes, stamps the sender host_id, and feeds `admin.LogRing` + `TopicBus.Publish("logs", entry)`. The SPA's `LogTail.svelte` hydrates from `/admin/api/logs/recent?n=200` and subscribes to the `logs` SSE topic. Cross-host category toggles flow through a `log.set` cmdsys verb (`Route: RouteAllHosts`) so checking a category in the sidebar affects every host. The forwarder drops on backpressure rather than blocking the game loop — visible as gaps in the tail.

### Game Loop (20Hz fixed timestep in `pkg/engine/loop.go`)

Each tick runs these phases in order:

1. `ClearTickState` hook (game)
2. Process connect/disconnect events
3. Drain admin commands from console (`RunOnLoop` queue)
4. Process pending sessions (login state machine)
5. **Drain wire input → dispatch handlers** (engine-owned; bindings registered via `mmokit.OnInput` / `mmokit.OnInputWith`)
6. Run all systems in registration order
7. `PreFlush` hook (game) — pre-removal notifications
8. `FlushRemovals` (engine)
9. `PostFlush` hook (game) — post-removal spawns / state changes
10. `PostTick` hook (game) — periodic saves, etc.

Phases 1, 7, 9, 10 are game-side extension points. The engine itself has no concept of "death notifications", "loot crate spawning", or "respawn"; those are space-game implementations of the `PreFlush` / `PostFlush` / `PostTick` hooks.

Input dispatch (phase 5) is no longer a `System` the game adds — it is a framework-owned phase, fed by `mmokit.OnInput` / `mmokit.OnInputWith` registrations on the `Process`. See [docs/superpowers/specs/2026-04-28-player-input-api-design.md](docs/superpowers/specs/2026-04-28-player-input-api-design.md).

### Systems (executed in order, defined in `internal/game/factory.go`)

Docking → TargetLock → ShipControl → Mining → Economy → Equipment → Ability → StatusEffect → Wander → Physics → Lifetime → Spatial → Collision → ShieldRegen → Network

Each system implements `System.Update(dt float32)`. Every factory returns an `engine.SystemDef` (carrying name + factory closure); pass it to `Process.AddSystem`. The built-in factories embed canonical short names ("Spatial", "Physics", etc.):

```go
coord.AddSystem(mmokit.NewClickToMoveSystem())
coord.AddSystem(mmokit.NewPhysicsSystem())
coord.AddSystem(mmokit.NewSpatialSystem())                          // or NewSpatialSystemWith(hooks)
coord.AddSystem(mmokit.NewNetworkSystemWith(setupNetwork))          // or NewNetworkSystem() for defaults
```

Game-specific systems use `mmokit.NewSystem(&T{})` — the pointer is used only for type info, fresh zero-values are made per cell. Names auto-derive from the struct type with a trailing `"System"` suffix stripped (`*BotSystem` → `"Bot"`):

```go
coord.AddSystem(mmokit.NewSystem(&MySystem{}))                      // name: "My"
coord.AddSystem(mmokit.NewSystem(&MySystem{}).Named("AILogic"))     // override (rare)
```

For systems with constructor arguments (closures, channels, prebuilt deps), write a typed factory that returns a `mmokit.SystemDef` directly — the same shape as the built-in `New*` helpers. Don't pass a hand-constructed instance to `NewSystem`; its fields are ignored.

### ECS (Ark v0.7.1)

Ark is the storage engine. `pkg/system/`, `pkg/universe/`, `pkg/engine/` import it directly (perf-critical framework code). **`internal/game/` must NOT import `github.com/mlange-42/ark/ecs`** — game code always goes through mmokit's wrappers. The two exceptions are `internal/game/var_tail_bindings.go` and `internal/game/entity_kinds.go`, which implement framework-binding glue and need `*ecs.Map1` / `*ecs.World`. Everything else uses the wrappers described below.

The `just lint-no-ark` recipe enforces the invariant — it fails the build if any non-exempted file in `internal/game/` reimports ark.

- `Map1[A]` through `Map12[...]` for entity creation and component access (framework code only)
- Use `HasAll()` not `Has()` to check components
- `world.Alive(entity)` before accessing removed entities
- Never spawn/remove entities during query iteration — use `Commands` (below) for deferred mutations

### Deferred ECS mutations (Commands)

Every structural ECS mutation from game code goes through the per-stage `Commands` buffer. Inside a system's `Update`, queue ops via `s.Commands()`; the engine flushes after each system's Update (via `engine.Hooks.AfterSystem`), so ops queued in System N are visible to System N+1 in the same tick. Outside systems (hooks, input handlers, command verbs), reach the buffer via `stage.Commands()`.

API surface (in `pkg/mmokit`):

- `s.Commands().Despawn(handle)` — queue entity destruction. The entity is removed at the next flush.
- `s.Commands().Defer(func(){...})` — escape hatch for multi-step game-action logic that doesn't fit a single ECS primitive. Use for things like `SpawnLootCrate`, `startDockingFor`, `executeRespawnFor`.
- `mmokit.AddComponent(s.Commands(), e, val)` — queue component add/overwrite (T inferred from val).
- `mmokit.RemoveComponent[T](s.Commands(), e)` — queue component removal (T explicit).

**`mmokit.Set[T]` vs `Commands.AddComponent`:** `Set` is IMMEDIATE and can only be called from non-query contexts (post-flush handlers, command verbs, hooks). Inside a `System.Update` loop, always use `Commands` — `Set` would panic against ark's locked-world rule. The deferred form's closure does the `world.Alive` check at flush time, so AddComponent-after-Despawn within the same batch is safe.

The `Defer` closure runs at the next system boundary. Multi-step game-action helpers (e.g. `gw.SpawnLootCrate(x, y, items)`) are invoked from inside a Defer:

```go
gw.stage.Commands().Defer(func() { gw.SpawnLootCrate(x, y, items) })
```

The `TickQueue` / `mmokit.Enqueue` / `mmokit.Drain` / `mmokit.Peek` pattern that was used for `Pending*`-typed game-action queues is GONE. All those types were migrated to typed `Commands` ops (`AddComponent`/`RemoveComponent`/`Despawn`) or to `Defer(closure)`.

### Query wrappers

For one-shot queries (existence checks, find-one lookups, ad-hoc iterations), use the mmokit wrappers — all auto-close:

- `mmokit.Any[T](stage) bool` — existence check.
- `mmokit.FindOne[T](stage) (Entity, bool)` — first matching entity.
- `mmokit.ForEach1/2/3[T](stage, fn)` — iterate every entity with T (and T2/T3).

The pre-existing `mmokit.Query[Bundle]` sticky-query pattern is unchanged — it's the right abstraction for systems iterating every tick over a fixed component set. `ForEachN` is the right abstraction for one-shot lookups in helpers / spawn functions / input handlers.

### EntityHandle

Game code uses `mmokit.Entity` (the rich wrapper with `NetID()`, `Stage()`, `Send()`, etc.) for ECS operations — `Stage.Spawn` returns it directly. For raw entity handles (e.g. `engine.PlayerSession.Entity` fields), use the `mmokit.EntityHandle` type alias instead of importing `ecs.Entity` directly, or call `e.Handle()` on an `mmokit.Entity`. `EntityHandle` is just a rename for `ecs.Entity` that lets game code avoid an ark import.

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
    for e, b := range s.entities {
        b.Pos.X += b.Vel.X * dt
        if b.Params != nil { /* optional component present */ }
    }
}
```

`Query[T]` is itself a rangefunc (`iter.Seq2[ecs.Entity, *T]`) — range over the value directly, no `.All()` needed. `break` is safe. Bundle rules: exported fields must be `*ComponentType`; use `ecs:"optional"` for optional components (nil when absent). Raw `ecs.FilterN` is still available as an escape hatch for max performance.

Note: `pkg/system/` files cannot import `pkg/mmokit` (circular dependency). Use `pkg/query` directly: `query.Query[T]`, `query.Without[T]()`, `query.IncludeAll()`.

### Entity Files

Each entity type has its own file (`internal/game/entity_*.go`) containing only spawn functions (e.g., `SpawnPlayer`, `SpawnAsteroid`) and a typed `XxxBundle` struct whose pointer fields enumerate its components (with `mmokit:"local"` for fields that aren't serialized over the wire). Entity kinds are registered in `internal/game/entity_kinds.go` via `RegisterEntityKinds` calling `mmokit.RegisterKind[XxxBundle]` per kind. Spawn functions call `gw.stage.Spawn(components...)` — every component value passed at the call site, no auto-attach, no post-spawn `Set` walls.

**Spawning entities.** `stage.Spawn(components ...any) Entity` is the canonical entity-creation API. Pass every component by value: `mmokit.Position{X: x, Y: y}`, `mmokit.EntityKind{Type: KindFoo}`, `mmokit.Collider{Width: w, Height: h, Radius: r, Layer: L, Shape: S}`, plus the game-specific components the bundle declares. Position is required (panics if missing); EntityKind is optional (kindless spawns are legal); a duplicate component type is a programmer error (panics). Velocity defaults to zero when not supplied. Spawn returns the rich `mmokit.Entity` wrapper, not the raw handle. Under `InvariantPanic` mode (dev/test default per `Config.InvariantMode`), kinded spawns missing any non-`mmokit:"local"` Bundle component panic at spawn time — forcing silent zero-fill bugs to surface immediately. For player spawning, `stage.SpawnPlayer(session, components...)` injects Position (from `session.SpawnLocation`) and PlayerConn (from `session.ConnID`) and otherwise behaves identically to `Spawn`.

Current entity types: ship, asteroid, lootcrate, npc, station.

`EntityRegistry` (`pkg/engine/registry.go`) maps entity names to definitions for admin commands.

### Networking & Replication

- WebSocket via `github.com/coder/websocket`, protobuf binary frames
- Channel byte prefix: `0x00` = events, `0x01` = operations
- `ReplicationSystem` (`pkg/system/`) handles per-player AoI visibility, hash-based diff detection, delta encoding, and frame dispatch
- `AutoReplicator` builds entity replicators from `EntityKindDef` registrations — the network system auto-discovers replicators from registered entity kinds via `BuildReplicators()`, no hand-coded nethandlers needed
- Components added to entity kinds via `mmokit.RegisterKind[Bundle]` are serialized using `net:"..."` struct tags on the component definitions; `mmokit:"local"` on a bundle field marks it as local-only — added on transfer receive but not serialized over the wire.
- `DefaultReplicationConfig(eng, grid)` pre-fills boilerplate; games set `AoIRadius`, callbacks
- Entity state is quantized for bandwidth: `qvel` (int16), `qangle` (uint16), `qnorm` (uint8), `f32` (float32)
- Struct tag encodings: `net:"qvel"` (explicit), `net:"auto"` (inferred from Go type), `net:"initial"` (sent once on visibility enter)
- `ClusterClock` (`pkg/universe/cluster_clock.go`; exposed via `mmokit.ClusterClock`) maintains a host-local offset against the coordinator, seeded at join by the MeshControl handshake and refreshed by periodic `CoordTimeSync` broadcasts (default 10 s cadence). Every replication sample is stamped at emit time with `ClusterClock.Now()`; the stamp travels opaquely through border-replica caches so downstream clients observe one coherent timeline across hosts.
- Replication wire format carries per-entity `producedAtMs` (8 bytes immediately after `entityType` in each `FullEntry` / `DeltaEntry`). The frame header is 20 bytes (down from 28) — frame-level `serverTimeMs` is removed; the client derives frame time as `max(producedAtMs)` across entries. Border frames carry the same per-entity stamp; `upsertBorderReplica` caches it on `Replica.ProducedAtMs` so the shipped sample carries its origin timestamp through to the AoI-visible viewer.

**Client rendering:** server-authoritative. Clients receive 20Hz authoritative samples and render at 60fps by interpolating between samples on a per-entity ring keyed by `producedAtMs` (the producer-side `ClusterClock` stamp). Render-lag of `RENDER_DELAY` ms keeps the bracketing pair available; extrapolation past the newest sample is capped at `MAX_EXTRAPOLATE_MS`. There is no client-side prediction — clicks send to the server and the player waits for server confirmation before moving. The interpolation primitives live in the generated SDK at `sdk/_core/interpolation-core.ts` (copied from `pkg/quantize/ts/interpolation-core.ts`); per-game code provides a thin glue layer with an `entityRotation` callback.

**Topology-transparent protocol:** Clients receive entities in absolute world-space coordinates with zero knowledge of cells, nodes, or grid layout. `SpawnedMsg` contains only `entity_net_id`, `world_x`, `world_y` — no grid metadata. Server mesh topology is a server-internal concern.

**Topology distribution is game-owned.** Games that want clients to see cell boundaries / R-G replica badges / cell ownership push their own `SE_CELL_TOPOLOGY` events. The demo wires kinds + lifecycle in `examples/4node-basic/main.go`:

- `Coordinator.ClusterCells() []ClusterCellInfo` returns the current cell→host view from local state (single-process) or `cellToHostMap` (multi-process; populated by `PeerList` broadcasts). Available everywhere.
- `Stage.ClusterCells()` delegates to the above; `Stage.Topology()` is the same call via the `topologyView` interface.
- `mmokit.SendCellTopology(gw, stage, connID)` builds an `enginepb.CellTopologyMsg` from the stage's current topology view and sends via `gw.SendEvent` — call from player-spawn hooks.
- For dynamic cells: the game sets `cfg.DynamicPartitioning.OnTopologyChanged` to a closure that re-broadcasts to all connected players on split/merge.
- `IncludeMeshState` on `EngineBindingsConfig` is honored as-declared in the `EntityKindDef`. Schema export and runtime use the same value — no runtime overrides. Set `IncludeMeshState: true` in the EntityKindDef to include the per-entity LOCAL/REPLICA/GHOST byte on the wire.

### Proto Codegen

Source of truth: proto files per package. Run `buf generate` (or `just proto`) to regenerate. Example-specific protos (basicpb) live alongside their examples:

- `proto/enginepb/engine.proto` — generic engine protocol (envelopes, core events, base messages)
  - `gen/go/enginepb/` — Go (package `enginepb`, import as `enginepb "github.com/zenion/mmoserver/gen/go/enginepb"`)
- `proto/meshpb/mesh.proto` — server-internal mesh data plane: `MeshData` (bidi stream of `MeshFrame` envelopes carrying border frames, single-shot `Handoff` messages, and action traffic between hosts) and `MeshControl` (coordinator ↔ host control plane; carries the `CoordTimeSync` ClusterClock broadcast). Never consumed by clients.
  - `gen/go/meshpb/` — Go (package `meshpb`, import as `meshpb "github.com/zenion/mmoserver/gen/go/meshpb"`)

Engine event codes use `enginepb.ClientEventCode_CE_*` / `enginepb.ServerEventCode_SE_*` (values 0-15). Game-specific event codes are declared as native Go consts in `internal/game/` (values start at 100+ to avoid colliding with engine codes); the web client receives them via the sdkgen-emitted TS SDK.

### Distributed Command System (`pkg/cmdsys/`)

All admin/operator commands go through a single `cmdsys.Dispatcher`. Commands declare typed Go struct Args/Results, a capability tag for RBAC, and a `RouteKind` that determines where the handler runs (Local, PlayerOwner, AllHosts, etc.).

Console, future CLI, future dashboard, and future in-game chat are thin adapters over `Dispatcher.Invoke(ctx, caller, verb, args)`. The console adapter runs commands on the REPL goroutine — handlers that need ECS access call `engine.RunOnLoop(ctx, fn)` internally. This keeps the game loop free for cross-goroutine work like cell splits.

Key types: `cmdsys.Command`, `cmdsys.Caller`, `cmdsys.Grant`, `cmdsys.Registry`, `cmdsys.Dispatcher`, `cmdsys.RouteKind`. Commands are registered at startup via `Registry.Register(cmd)`. Adding a command is one file: typed Args/Result structs + a handler function.

`cmdsys.OnLoop[R](ctx, runner, fn)` is the ergonomic way for command handlers to access ECS and return a typed result — wraps `engine.RunOnLoop` and eliminates the capture-and-assign boilerplate. Use: `return cmdsys.OnLoop(ctx, cell.Engine, func() (MyResult, error) { ... })`.

`engine.RunOnLoop(ctx, fn)` is the lower-level primitive for any goroutine that needs ECS access. It detects on-loop reentrance (goroutine-ID check) and runs fn inline when the caller is already the loop goroutine. Off-loop callers post to a bounded queue drained each tick with an 8ms budget. Replaces the old `PendingAdminCmds` channel.

`GET /commands` and `GET /commands/{verb}` expose JSON Schema for every registered command — same mux as `/metrics` and `/events`. Result schemas allow arbitrary struct nesting (via `cmdsys.SchemaOfResult`); Arg schemas remain flat (1-level max) since the CLI parser consumes them as flat inputs.

### Thread Safety

The ECS world is **not thread-safe**. All ECS reads/writes must happen on the game loop goroutine. Handlers that need ECS access use `engine.RunOnLoop(ctx, fn)` to schedule closures that run on the game tick. Admin commands capture `*GameWorld` in closures. Use `Console.Print()`/`Printf()` for output (routes through readline's safe writer).

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

`web-pixi/` — TypeScript/PixiJS game client built with Vite. Run via `just dev` during development. Imports typed wire-format classes (events, operations, entity types) from the generated SDK at `web-pixi/sdk/`. Interpolates between 20Hz server ticks for smooth rendering.

### Debug Logging

All new server-side game logic must include category-based debug logging via `gw.Log.Log(game.CatXxx, ...)`. Game-specific log categories are defined in `internal/game/logcat.go` (e.g. `CatCombat`, `CatMining`, `CatEconomy`). The logger itself (`pkg/logger/`) is generic with dynamic category registration — no game-specific constants. Log significant state changes: item transfers, bank operations, sells, loot pickups, combat events, etc. Include player identity and relevant quantities in log messages (e.g. `"bank deposit: player=%s item=%d qty=%.1f"`).

### Usernames

Usernames are forced lowercase everywhere. Duplicate usernames are rejected at login with a `LoginRejectedMsg`.

### Client SDK Codegen

`cmd/sdkgen/` auto-generates typed TypeScript client SDKs from protocol schema. Go is the single source of truth; the engine assembles the schema entirely from runtime registries — entity replicators from each cell's `EntityKindDefs`, server events from `mmokit.RegisterEvent[T]`, client inputs from `mmokit.HandleClient[T]`, broadcast types from `mmokit.HandleAll[T]`, and typed operations from `mmokit.RegisterOp[Req, Res]`. `just build` regenerates the SDK automatically — no manual step.

To regenerate just the SDK without a full build:

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Games configure SDK output only by setting `Config.Name` — it becomes both the JSON `game` field and the prefix on the generated TS client class (`SimpleClient`, `BasicClient`, `SpaceClient`). `mmokit.New` synthesizes the schema dumper internally; there is no user-facing `Protocol` object to construct or wire up.

```go
process := mmokit.New(mmokit.Config{
    Name: "mygame",
    // ...
})

// Anywhere at startup — server events:
mmokit.RegisterEvent[MyEventMsg]()                                    // server → client typed event
mmokit.HandleClient[MyInputMsg](process, func(p, msg) { ... })        // client → server typed input
mmokit.RegisterOp[MyOpReq, MyOpRes](mmokit.RouteGatewayLocal, handler) // request/response
```

The engine-default typed events (`Pong`, `DebugInfo`, `WorldDelta`, `PlayerEntityAssigned`, `CellChange`, `ServerConfig`) are auto-registered by `mmokit.New` — every game gets them for free. The engine-default `Ping` client-input is also pre-wired with a Pong-replying handler.

Engine intercepts the `--dump-schema` flag in `Process.Start` after `Build()` returns, calls `Protocol.AssembleFromProcess(*Process)` to harvest router/op/entity-kind metadata from the populated runtime registries, writes the JSON to stdout, and exits. Games never declare or handle `--dump-schema` themselves. The codegen produces a typed client class, entity interfaces, binary delta decoder, and WebSocket transport — all self-contained in the SDK output directory.

### Examples

- `examples/4node-basic/` — Minimal 2x2 mesh demo. Players are circles, click-to-move. Uses AutoReplicator with struct tags for declarative replication. TypeScript/Canvas2D web client built with Vite, using auto-generated SDK. Debug overlays (cell boundaries, AoI radius, replica/ghost markers, node stats). Run: `cd examples/4node-basic && just dev`. Dev config has `InvariantMode: InvariantPanic` + `StrictNetIDIndex: true` — every smoke run exercises the full State Integrity enforcement. Spawn position is registered via `process.OnResolveSpawn(func(s *mmokit.PlayerSession) mmokit.Location { return mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85} })` in `main.go`; the game-side `OnEnter` hook calls `gw.SpawnAtLocation(s.SpawnLocation, ...)` so the entity lands where the resolver chose. When no resolver is registered, the engine defaults to the center of cell (0,0). Dev knob: `--gateway-mode=always-proxy` reserves future-use hook (not yet wired for colocated cells in S3). Multi-host distribution for boundary-crossing stress tests is driven by `just distributed` (from inside `examples/4node-basic/`), which spins up 4 separate processes in a tmux session (coordinator + 2 hosts + gateway, with `--admin-listen=:9101` on coord for `/events`). Dynamic partitioning is **manual-only** in this example — use `cell split <cellID>` / `cell merge <cellID>` from the server console to drive splits and merges. The interactive `bot spawn <count> <cellID>` / `bot clear` / `bot list` console command group is always registered — note `bot.spawn` is `RouteSpecificCell` so the cellID is **required** (e.g. `bot spawn 30 cell_0_0` or `0_0` — both forms accepted). Spawn bots, then `cell split 0_0` manually to exercise split behavior.
