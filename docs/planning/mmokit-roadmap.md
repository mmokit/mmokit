# mmokit Big-Ticket Improvements Roadmap

## Context

mmokit (`pkg/mmokit/`) is a generic 2D MMO engine toolkit. Two games are built on it: the main space MMO (`internal/`) and a slither.io clone (`examples/slither/`). Analysis of both consumers reveals significant duplicated boilerplate (~40% of game code) and scaling bottlenecks. The slither example sends 10-60KB per player per tick with no delta compression, and the fixed NxN cell grid cannot rebalance at runtime.

> **Terminology note:** The codebase uses **"cell"** (industry-standard for fixed-size spatial partitions in server meshes) throughout. The rename from the former "sector" terminology was completed in roadmap item #13.

This document identifies the highest-impact framework improvements, ordered by impact-to-effort ratio.

**Design principle:** Each feature should be designed from first principles and MMO industry best practices, not constrained by current mmokit patterns. Existing APIs can and should change to create simpler, better designs.

**Target architecture:** See [mmokit-target-architecture.md](mmokit-target-architecture.md) for the long-term architecture vision — authoritative spatial mesh, single-writer ownership, interest-driven delta snapshots, overlap-based handoff, and three-layer separation (simulation / replication / gateway). Each roadmap feature moves toward that target while keeping the system functional at every step.

---

## Feature List

### 1. Generic Visibility & Network System (P0) -- DONE

Implemented as `ReplicationSystem` in `pkg/system/`. Games register `Replicator` implementations per entity type. The system owns per-player visibility maps, hash-based diff detection, and the AoI-query-diff loop. Both main game and slither use it.

**Spec:** `docs/superpowers/specs/2026-03-24-engine-extraction-phase2-design.md`

---

### 2. Player Lifecycle Manager (P0) -- DONE

Implemented as `PlayerManager` in `pkg/engine/`. Canonical state machine with custom state registration, transition guards/actions, and callbacks. Manages connID-to-entity mapping, username tracking, duplicate rejection, disconnect grace period with reconnection. Both main game and slither use it.

**Spec:** `docs/superpowers/plans/2026-03-25-player-lifecycle-manager.md`

---

### 3. Delta Compression & Position Quantization (P1) -- DONE

Implemented as a quantize-then-delta pipeline in `pkg/quantize/` and `pkg/system/`. Both main game and slither use it.

**Architecture:** Hash-based change detection → quantized binary snapshots → per-field delta encoding against acknowledged baselines → binary wire format (`SE_DELTA_WORLD_UPDATE`). Follows Unreal Iris (centralized quantization, separate transport state) and Valve Source (acknowledged baseline) patterns.

**Key components:**

- `pkg/quantize/` — quantization functions (Pos, Angle, Vel, Norm, Rel), SnapshotWriter/Reader, DeltaEncoder (field-level bitmask + changed values), FrameEncoder/Decoder (binary wire format with viewerX/Y in header)
- `pkg/system/replication.go` — unified `EntityReplicator` interface (Hash + Snapshot + SnapshotLayout + InitialData), per-connection acknowledged baselines with AckReliable (TCP) and AckExplicit (UDP) modes
- `pkg/system/frame_writer.go` — `BinaryFrameWriter` (standard FrameWriter, replaces per-game boilerplate)
- `pkg/system/viewer_source.go` — `PlayerViewerSource` (standard ViewerSource, replaces per-game boilerplate)
- `pkg/system/position.go` — `CellRelativePos` (standard cell-relative position calculation)
- `pkg/quantize/ts/delta-decoder-core.ts` — TypeScript reference decoder library (wire parsing, dequantization, delta application, baseline store)

**Design decisions from implementation:**

- **Positions use Float32 (not quantized)** — quantized int16 positions cause ~0.7-unit shift artifacts at cell boundaries (different quantization rounding from each cell's reference frame) and camera jitter (~0.37-unit steps). Industry standard: no major engine quantizes positions relative to a moving reference point for meshed games.
- **Velocity, rotation, sizes, health/shield are quantized** — these tolerate rounding without visual artifacts.
- **Frame header carries full-precision viewerX/viewerY** — client uses this for the local player's entity position, eliminating camera jitter from quantized snapshots.
- **Angle normalization via atan2** — handles accumulated angles beyond [-π, π] from rotation systems that don't wrap.
- **Variable-length tails** use uint16 byte-length prefix (not element count) for DeltaEncoder compatibility.

**Spec:** `docs/superpowers/specs/2026-03-27-delta-compression-quantization-design.md`

---

### 4. Metrics & Observability (P1) -- DONE

Implemented as `pkg/metrics/` with per-node collection and Prometheus-compatible HTTP endpoint. Also includes a production-grade interactive console overhaul with command groups, table formatting, and built-in framework commands.

**Architecture:**

- `pkg/metrics/` — zero-alloc metric primitives (Counter, Gauge, EWMA), `NodeMetrics` per-node collector, `LoadSnapshot` types, Prometheus text HTTP handler
- `pkg/net/` — atomic byte counters on `Conn` and `UDPTransport`, `ByteCounter` interface, `ConnManager.TotalBytesSent/Recv/ConnectionCount`
- `pkg/engine/` — `Engine.Metrics` + `EntityCounter` callback wired into game loop, `RecordTick` at end of each tick
- `pkg/universe/` — automatic metrics wiring in `Coordinator`, ECS-based entity counting, `NodeLoad()`/`AllNodeLoads()` API, auto-registered `/metrics` endpoint

**Metrics collected per node:**

- Tick health: duration EWMA, effective Hz, overbudget ratio
- Entity counts: real, replica, ghost, players
- Network I/O: cumulative bytes sent/recv, connection count
- Composite load score (0.0-1.0+, EWMA-smoothed): `0.7*tickLoad + 0.3*entityLoad`

**Console overhaul (also delivered as part of this feature):**

- `CommandGroup` — subcommand support (`config set`, `entity list`, `node load`, `log on`, etc.)
- `Table` — consistent column-aligned console output utility
- `Configurable` — generic reflection-based config get/set interface (`ReflectConfig`)
- `BuiltinOpts` — opt-in framework command groups: `config` (list/get/set/save/reset), `entity` (summary/list/get/add/remove), `node` (list/load), `log` (status/on/off/toggle/only)
- `Console.Print`/`Printf` — safe output routing through readline
- Dynamic category ordering, `ExecOnGameLoop` timeout, `FmtDuration` export
- `── Game Commands ──` separator in help between framework and game-specific commands
- **Coordinator-managed lifecycle:** Console is created by default in `Coordinator.Start()`. Node builtins auto-wired from node map. Games provide game-specific builtins via `WithConsole(ConsoleOpts{})` and custom commands via `WithOnConsoleReady(fn)`. `WithHeadless()` disables console for tests/containers. `Start()` blocks — handles console, SIGINT, and clean shutdown.

**Key design decisions:**

- `pkg/metrics/` has zero dependencies on other `pkg/` packages — uses callbacks for tick stats and network stats to avoid circular imports
- Byte counting at transport level (Conn.writePump/readPump, UDPTransport.sendRaw) captures all protocol overhead
- Composite load uses EWMA (alpha=0.1) for smooth rebalancing signals — hysteresis logic deferred to Feature #7
- Console `EntityOpts` uses callbacks (Summary/List/Get/Remove) so framework commands work without importing game-specific ECS components

**Complexity:** Medium (metrics: small, console overhaul: medium).

---

### 5. Incremental Spatial Grid (P1) -- DONE

Implemented as entity-tracked incremental updates in `pkg/spatial/`. Entities register on spawn via `Register()`, update position via `Update()` (rehashes only on cell boundary crossing), and deregister on removal via `Deregister()`. Static entities are zero-cost after initial registration.

**Architecture:** Dual-slice cells (tracked + transient). Tracked entries persist across ticks; transient entries (e.g. snake body segment checkpoints) are cleared each tick via `ClearTransient()`. Swap-delete with index tracking provides O(1) deregistration. `Engine.OnEntityRemoved` hook automates grid cleanup during `FlushRemovals()`.

**Impact:** For slither (500 static food + 30 moving snakes): ~94% reduction in spatial rebuild work per tick. For the space game (1000 asteroids + 50 players): ~95% reduction.

**Complexity:** Medium.

---

### 6. Generic Input Processing Framework (P1) -- DONE

Implemented as `InputRouter` in `pkg/engine/`. Typed handler registration via `HandleProto[T]` with state bitmask filtering and two-layer guards. Main game migrated from 337-line switch-based InputSystem to ~40 lines of declarative registrations. Slither protobuf migration completed in Phase 2.

**Spec:** `docs/superpowers/specs/2026-03-26-input-router-design.md`
**Plan:** `docs/superpowers/plans/2026-03-26-input-router.md`

---

### 7. Dynamic Cell Partitioning (P2)

**Problem:** Fixed NxN grid with 1:1 cell-to-node mapping at startup. If one cell gets 10x the entities, that node bottlenecks while others idle. `CellOwner` map is set once and never updated.

**Design:** Two parts, aligned with [target architecture](mmokit-target-architecture.md#terminology):

- **1:N node-to-cell mapping:** A node owns 1+ cells and runs a single ECS world covering all of them. Cell crossings within the same node are just coordinate updates — no transfer overhead. This is the foundation for dynamic partitioning. production distributes by expected load.
- **Dynamic reassignment:** Coordinator monitors per-node entity counts and tick durations (via Feature #4 metrics). When a node exceeds threshold, Coordinator reassigns some of its cells to a new or underloaded node — entities in those cells transfer via the standard handoff protocol. Reverse merge when nodes are underloaded. Cell size never changes; only ownership moves.

**Impact:** Enables elastic scaling. A single-node game auto-scales to many nodes as population grows, without changing the spatial grid.

**Complexity:** Huge. Changes to Coordinator, BoundarySystem, topology. Cell reassignment must transfer entities cleanly. 1:N mapping is a prerequisite that can land independently.

**Dependencies:** Feature #4 (Metrics) for load signals.

---

### 8. Hierarchical Interest Management (P2) -- DONE

Implemented as per-entity-type AoI tiers, priority accumulation, and dormancy in `pkg/system/replication.go`. Stages 1 (hard relevancy filter) and 2 (priority accumulator) are complete. Stage 3 (bandwidth budget cap) deferred as a future extension.

**Architecture:** Single max-radius spatial query with post-filtering by per-type tier radius. Optional `TierProvider` interface on `EntityReplicator` — games that don't implement it get current behavior unchanged.

**Key components:**

- `pkg/system/replication.go` — `ReplicationTier` struct (Radius, UpdateDivisor, BaseWeight), `TierProvider` interface, tier caching in `NewReplicationSystem`, update loop modifications (tier cutoff, dormancy, update divisor gate, priority accumulation)
- `pkg/system/baseline.go` — `entityPriorityState` (accumulator, lastSentTick, unchangedTicks), `priorities` map on `connectionState`

**How it works:**

1. **Per-type AoI radius:** Each entity type can declare its own visibility radius via `TierProvider.ReplicationTier()`. Entities outside their type's radius are invisible. Default: global `AoIRadius`.
2. **Update divisor:** Entity types can declare an update frequency divisor (e.g., divisor=3 means send every 3rd tick). Entities on skip ticks stay visible but skip snapshot/delta — no enter/exit churn.
3. **Priority accumulator:** On skip ticks, entities accumulate priority = `baseWeight * distanceFactor * PriorityProvider multiplier`. Resets on send. Foundation for future bandwidth budgeting (sort by accumulator, drain until budget exhausted).
4. **Dormancy:** Entities unchanged for `DormancyThreshold` consecutive ticks skip all replication work (hash, snapshot, priority). Zero per-tick cost for static entities after threshold.
5. **Bug fix:** Hash-unchanged entities now correctly stay in `currentVisible` — fixes false exit events in the pre-existing code.

**Design decisions:**

- **No hysteresis** — hard cutoffs per tier. PvP 2D game where AoI exceeds camera view; soft boundaries would leak entity info to clients.
- **Priority formula:** `typeWeight * distanceFactor * timeSinceLastSent` (Halo Reach / Gaffer on Games pattern). `PriorityProvider` is an optional per-entity multiplier.
- **Single spatial query + post-filter** — one `QueryRadius` at `max(all tier radii)`, then per-entry `dist2 > tierRadius2` check. Simpler than multi-query approach with negligible overhead.

**Slither showcase:** Food entities use `{Radius: 2000, UpdateDivisor: 3, BaseWeight: 0.3}` with `DormancyThreshold: 60` (3s at 20Hz). Snake entities use defaults (full radius, every tick, weight 1.0).

**Spec:** `docs/superpowers/specs/2026-03-28-hierarchical-interest-management-design.md`

---

### 9. Declarative Replication & Client SDK Codegen (P2) -- DONE

Implemented as two layers: `AutoReplicator` for declarative server-side replication, and `cmd/sdkgen` for auto-generated TypeScript client SDKs.

**AutoReplicator** (`pkg/system/auto_replicator.go`): Composable `ComponentBinding` API replaces hand-written `EntityReplicator` implementations. Struct tags (`net:"qvel"`, `net:"auto"`, `net:"initial"`) drive reflection-based bindings at init time. Built-in bindings: `ViewerRelativePos`, `QVelocity`, `QAngle`, `QSize`, `Component[T]`, `OptionalComponent[T]`. Encoding can be explicit or inferred from Go type (`uint8`→u8, `string`→string, `bool`→bool). Hand-coded replicators still supported for complex cases (slither body segments, main game mining lasers).

**System factories** (`pkg/mmokit/`): `NewNetworkSystem(setup)`, `NewSpatialSystem()` / `NewSpatialSystemWith(hooks)`, `NewInputSystem(setup)`, `NewPhysicsSystem()`, `NewClickToMoveSystem()`, `NewDeadReckoningSystem()`, `NewLifetimeSystem()`. `DefaultReplicationConfig(eng, grid)` pre-fills 5 boilerplate fields. Examples no longer import `pkg/system` directly.

**Client SDK codegen** (`cmd/sdkgen/`): Game binary dumps protocol schema via `--dump-schema` (entity layouts from `AutoReplicator`, event codes from `Protocol` registrations). `sdkgen` reads the JSON + proto `.d.ts` files and generates a typed TypeScript client: `BasicClient` class with `sendX()` / `onX()` methods, entity interfaces, binary delta decoder, WebSocket transport. Generated code imports directly from `gen/es/` proto types — zero duplication.

**Impact:** 4node-basic server reduced from ~350 lines of custom networking to ~10 lines of declarations. Client reduced from 1050-line inline JS to 6 typed TS modules using the generated SDK.

**Complexity:** Medium-large (two codegen layers).

---

### 10. Async Entity Serialization (P3)

**Problem:** NetworkSystem is typically the most expensive system, consuming 15-25ms of the 50ms tick budget. All serialization blocks the next tick.

**Design:** End-of-tick snapshot of read-only world state for visible entities. Dedicated serialization goroutine builds frames and calls `ConnMgr.Send()` (already thread-safe). Next tick proceeds immediately. Only entities with changed hashes need state copied into snapshot.

**Impact:** Reclaims 30-50% of tick budget. Enables higher tick rates or more entities per node.

**Complexity:** Large. Safe ECS snapshotting with Ark's archetype storage is non-trivial.

**Dependencies:** Features #1 and #3 for hash-based dirty tracking and delta snapshots.

---

### 11. Lightweight Cross-Node Proxies (P3)

**Problem:** Current replica system copies full entity state across nodes every tick, even if no player observes the border. `ScanBorderEntities` in slither checks all snake body segments for border proximity -- O(snakes *segments* neighbors) per tick.

**Design:** Nodes exchange lightweight "border summaries" (position, netID, type, radius -- ~16 bytes) instead of full replicas (~100+ bytes). Receiving node adds proxy entries to spatial grid (not full ECS entities). AoI queries hitting proxies trigger on-demand detail requests. Collision proxies participate in broad-phase; narrow-phase results forwarded via CrossNodeAction.

This also introduces **soft visibility / prefetch** — entities just beyond the cell boundary are streamed at low rate before they become relevant. This warms the destination node's cache and spatial grid, enabling the [prepare-overlap-commit handoff](mmokit-target-architecture.md#entity-handoff-protocol) model where the destination runs a shadow copy before ownership flips. The current ghost-based handoff becomes overlap-based: old server stays authoritative during a short window while the new server warms up, then ownership commits at a tick boundary.

**Impact:** 60-80% reduction in inter-node bandwidth. Eliminates per-tick cost of replicating unobserved entities. Enables seamless handoff without cold-start stalls.

**Complexity:** Large. New proxy type in spatial grid, on-demand fetch protocol, 1-2 tick latency handling, overlap handoff state machine.

---

### 12. Connection Migration Protocol (P3)

**Problem:** All nodes run as goroutines in a single process, sharing one `ConnManager`. Cannot scale beyond one machine. `nodeBridge` uses Go channels, not network.

**Design:** Two sub-features aligned with the [target architecture's three-layer model](mmokit-target-architecture.md#three-layer-architecture):

- **Network NodeBridge:** TCP/gRPC implementation replacing channel-based one. The `NodeBridge` interface already abstracts this -- only the implementation changes.
- **Gateway/routing layer:** Stable session frontend that clients connect through. Gateway switches upstream sim server on entity transfer without forcing client reconnect. This is what makes the world feel seamless across processes.

Entity identity upgrades to support multi-process: NetworkID gains an authority epoch so stale packets from old owners are trivially droppable. Transfer uses the overlap handoff protocol (Feature #11).

**Impact:** Removes single-machine ceiling. Enables horizontal scaling to arbitrary cluster sizes.

**Complexity:** Huge. Network-based inter-node comms, gateway routing, connection migration, failure handling.

**Dependencies:** Feature #4 (Metrics) for load-based routing. Feature #11 (Cross-Node Proxies) for overlap handoff.

---

### 13. Rename "Sector" → "Cell" Across Codebase (P1) -- DONE

**Problem:** The codebase uses "sector" for the fixed-size spatial partition concept. "Cell" is the industry-standard term in distributed simulation and server meshing literature (SpatialOS, Improbable, academic papers). "Sector" implies a game-facing concept (EVE Online star sectors, sci-fi theming) and conflates the engine-level spatial unit with game design.

**Design:** Mechanical rename across all layers. No behavioral changes.

**Scope by layer:**

Go engine (`pkg/`):

- `coords.SectorCoord` → `coords.CellCoord`, `coords.SectorSize` → `coords.CellSize`, `SetSectorSize()` → `SetCellSize()`
- `component.SectorCoord` → `component.CellCoord`
- `mmokit.SectorCoord` alias, `mmokit.SectorSize()`, `mmokit.SetSectorSize()`
- `universe.BoundarySystem` name "SectorBoundary" → "CellBoundary"
- `universe.NodeBridge.SectorOwner()` → `CellOwner()`
- `universe.Node.Sector` → `Node.Cells` (plural, prep for 1:N)
- `universe.Coordinator.SectorOwner` → `CellOwner`
- `universe.SectorID()` → `NodeID()` or `CellID()`
- `universe.WorldBase.Sector()` → `Cell()`, `SectorCoordMap()` → `CellCoordMap()`

Go game (`internal/`):

- `game.GameWorld.Sector` field, `game.Components.SectorCoord` mapper
- `fmtSectorPos()` → `fmtCellPos()`, `fmtSectorPosRaw()` → `fmtCellPosRaw()`
- `GenerateBelts(sector)` → `GenerateBelts(cell)`
- All entity files: `.SectorCoord.Add()` calls
- All system files: `sectorDX/DY` locals, `SectorSize` refs

Proto:

- `enginepb.SE_SECTOR_CHANGE` → `SE_CELL_CHANGE`
- `enginepb.SectorChangeMsg` → `CellChangeMsg` (fields: `cell_x`, `cell_y`)
- `gamepb.PlayerSpawnedMsg`: `origin_sector_x/y` → `origin_cell_x/y`
- `gamepb.MapStationInfo`: `sector_x/y` → `cell_x/y`
- `gamepb.DebugFlagsMsg`: `show_sector_grid` → `show_cell_grid`
- `gamepb.ReplicaSnapshotPB`: `sector_sx/sy` → `cell_sx/sy`
- Regenerate all generated code (`make proto`)

Web client (`web-pixi/`):

- `SECTOR_SIZE` → `CELL_SIZE`
- `SectorGrid` → `CellGrid`, `SectorMap` → `CellMap`
- State fields: `originSectorX/Y` → `originCellX/Y`, `sectorMapOpen` → `cellMapOpen`, etc.
- CSS class: `.sector-map-open` → `.cell-map-open`
- File renames: `sector-map.ts` → `cell-map.ts`, `grid.ts` class rename

Slither example (`examples/slither/`):

- Same pattern: `sectorSize` → `cellSize`, `handleSectorChange` → `handleCellChange`, etc.

Tests:

- All test files referencing sector types/names

Docs:

- Historical specs/plans keep original terminology (they describe what was built at the time)
- Planning docs updated (this document, target architecture)

**Impact:** Consistent terminology aligned with industry standards. Prerequisite for 1:N node-to-cell mapping (Feature #7).

**Complexity:** Medium. Mechanical but wide-reaching. No logic changes.

---

## Recommended Implementation Order

```text
Phase 1 (Foundation) ✓:    #1 Visibility System -> #2 Player Manager -> #6 Input Framework
  → Simulation mesh basics, replication subsystem

Phase 2 (Performance) ✓:   #13 Cell rename ✓ -> Slither protobuf ✓ -> #3 Delta + Quantization ✓ -> #5 Incremental Grid ✓ -> #4 Metrics + Console ✓
  → Terminology alignment, serialization strategy, observability, production console

Phase 3 (Advanced) ✓:      #8 Hierarchical AoI ✓ -> 4node-basic example ✓ -> Console in Coordinator ✓ -> #9 AutoReplicator + SDK Codegen ✓ -> Movement Systems (ClickToMove, DirectionMove) ✓ -> System Factories ✓
  → Interest management pipeline, declarative replication, client codegen, consistent system APIs

Phase 3b (Remaining):      #7 Dynamic Cell Partitioning (1:N node-to-cell)
  → Elastic topology

Phase 4 (Scale-out):       #10 Async Serialization -> #11 Cross-Node Proxies -> #12 Connection Migration
  → Replication as independent layer, overlap handoff, gateway/routing layer, multi-process
```

See [target architecture](mmokit-target-architecture.md#evolutionary-path) for how each phase moves toward the distributed endgame.

**Phase 2 prerequisite:** Slither's migration from raw binary protocol to protobuf envelopes (`enginepb.ClientEvent`) is now complete. This included a new `proto/slitherpb/slither.proto` and web client protobuf migration, enabling InputRouter usage.

## Design Approach

For each feature, before implementation:

1. Research MMO industry best practices and established server architectures (e.g., Unreal Replication, SpatialOS, Photon, EVE Online's TIDI model)
2. Design from first principles -- do not assume current mmokit patterns are correct
3. Existing APIs can and should change to create simpler, better designs
4. Write a dedicated spec/plan in `docs/superpowers/` before coding

## Verification

After each feature:

- All targets build: `go vet ./...`
- All tests pass: `go test ./...`
- Slither: `cd examples/slither && make dev` — web client connects and plays
- 4node-basic: `cd examples/4node-basic && make dev` — Vite client connects, entities render, click-to-move works
- SDK codegen: `make client-sdk GAME=examples/4node-basic` — generates, `npx tsc --noEmit` passes
- For server meshing features: test with 2x2+ grid configurations
