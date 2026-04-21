# mmokit Big-Ticket Improvements Roadmap

**Original:** 2026-03-27 · **Revised:** 2026-04-21 to reflect work shipped through State Integrity.

## Context

mmokit (`pkg/mmokit/`) is a generic 2D MMO engine toolkit. Two games are built on it: the main space MMO (`internal/`) and a slither.io clone (`examples/slither/`). The original roadmap (items #1–#13 below) captured the boilerplate-reduction + performance agenda from the Phase-2 era; everything marked DONE in that block is on `main` today.

Since then the framework has grown meaningfully beyond the original scope — see [Post-Phase-3b Major Work](#post-phase-3b-major-work) for the S4–S7, Time & Transparency, and State Integrity plans. That section is the authoritative record of what shipped after the original roadmap, and [What's Next](#whats-next-prioritized) is the current prioritized queue.

> **Terminology note:** The codebase uses **"cell"** (industry-standard for fixed-size spatial partitions in server meshes) throughout. The rename from the former "sector" terminology was completed in roadmap item #13.

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

### 7. Dynamic Cell Partitioning (P2) — DONE

**Implemented** as quadtree-based cell splitting and merging at runtime. See [design spec](../superpowers/specs/2026-04-03-dynamic-cell-partitioning-design.md).

**Key components:**

- `pkg/universe/cell_id.go` — `CellID{X, Y, Depth}` with parent/child math, spatial adjacency
- `pkg/universe/partition.go` — `PartitionConfig`, `SplitCell()`, `MergeCell()`, cooldowns, `OnTopologyChanged`
- `pkg/universe/partition_monitor.go` — EWMA-smoothed load monitoring, auto split/merge triggers
- `pkg/universe/partition_console.go` — `cell list/info/split/merge/cooldowns/config` commands
- `pkg/universe/net_id_alloc.go` — Runtime net ID range allocator with recycling

**How it works:**

- `CellID` extends the old `CellCoord` with a `Depth` field for quadtree levels
- Entities always keep base-cell coordinates (`coords.CellSize`) regardless of depth — no position remapping on split
- `SplitCell`: serializes entities on old node's game loop, creates 4 new nodes under write lock, transfers entities via standard protocol, shuts down old node
- `MergeCell`: renames survivor to parent cell, shuts down 3 siblings synchronously
- `Coordinator.mu` RWMutex protects `Nodes`, `NodeOwner`, `Topology` for thread-safe split/merge
- Topology updates are incremental (`UpdateAfterSplit`/`UpdateAfterMerge`), not full recompute
- Auto-monitor: EWMA smoothing, asymmetric thresholds (75% split / 20% merge), sustained duration, cooldowns
- `SE_CELL_TOPOLOGY` server event broadcasts full cell map to clients after topology changes

**Opt-in API:** `Config.DynamicPartitioning = mmokit.DefaultPartitionConfig()`. Nil = disabled, zero overhead.

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

### 10. Async Entity Serialization (P3) — OPEN, deferred to Phase 6

**Problem:** NetworkSystem is typically the most expensive system, consuming 15-25ms of the 50ms tick budget. All serialization blocks the next tick.

**Design:** End-of-tick snapshot of read-only world state for visible entities. Dedicated serialization goroutine builds frames and calls `ConnMgr.Send()` (already thread-safe). Next tick proceeds immediately. Only entities with changed hashes need state copied into snapshot.

**Impact:** Reclaims 30-50% of tick budget. Enables higher tick rates or more entities per node.

**Complexity:** Large. Safe ECS snapshotting with Ark's archetype storage is non-trivial.

**Dependencies:** Features #1 and #3 for hash-based dirty tracking and delta snapshots. Now that tiered-push replication, hierarchical AoI, and auto-replicator bindings are shipped, the snapshot-build step has a clean handoff boundary.

---

### 11. Lightweight Cross-Node Proxies (SHIPPED, with deferred follow-ups)

**Shipped on `main` via merge of `feature/tiered-push-replication` (2026-04-12):** The tiered-push border replication protocol replaces the legacy `ScanBorderProxies` / `ScanBorderEntities` path. Key deliverables:

- New `pkg/replication/` shared primitives package (`Viewer` interface, `BaselineStore`, `Frame`/`FrameEntry` wire format, `Dispatcher.Walk`, tier helpers).
- `pkg/system/replication.go` refactored as the first consumer.
- `component.NetworkID` gained an `Epoch uint32` field threaded through `pkg/quantize` wire format and the TypeScript decoder.
- `pkg/universe/border_replication.go` `BorderDispatcher` + `pkg/universe/border_viewer.go` `NodeViewer` wired into production `PostSystems`.
- New `MsgBorderFrame` envelope replacing `MsgReplica`/`MsgProxySummary`/`MsgDetailRequest`/`MsgDetailResponse`.
- `WorldBase.ApplyBorderFrame` receiver with epoch-based stale-packet detection.
- Registry-driven per-component border frame tail (`[u16 count][repeated: u16 id, u16 len, N bytes]`) carrying full game state from the sender's `ReplicationRegistry` — ships correct Health/Shield/Inventory/etc. across cell boundaries. Legacy 18-byte frames decode as zero-component frames for free via the natural zero-count subset.
- `WorldBase.upsertBorderReplica` calls `EnsureEntityKindComponents` so every replica has the full kind-registered component set the local `AutoReplicator` bindings expect; ensures `reflectBinding.HasAll` never panics in `ReplicationSystem.Update`.
- `NodeViewer` default tier radius set to `coords.CellSize * 2` so corner-of-cell entities reach both cardinal and diagonal neighbors (fix for asymmetric visibility).
- ~1800 lines of legacy border replication code deleted (`replication_scan.go`, `ReplicaFrame`/`ProxySummary` types, `ProxiesEnabled` config, `RequestDetail`/`SendDetailResponse` NodeBridge methods, all supporting `WorldBase` proxy/replica scan methods).
- Inter-node bandwidth metrics (`RecordBorderFrameSent`/`Recv`, `InterNodeSnapshot`).
- Unit tests for `ApplyBorderFrame` (create, update, stale-epoch drop, truncated buf, multi-entity, auto-fill, registry round-trip, legacy back-compat, unknown-ID skip, cross-frame updates) + perf benchmarks + a corner-entity regression test covering `NodeViewer` radius.

**Deferred to #12 follow-up (built as unwired infrastructure):**

- **Co-simulation handoff state machine** — `pkg/universe/handoff.go` (Unseen → Border → Promoted → Handoff lifecycle with `MinWarmupTicks=5` + `CrossingCooldownTicks=20`), baseline handover helpers, `MsgHandoffPrepare`/`MsgHandoffCommit`/`MsgForwardInput` message types all ship fully built and unit-tested but not wired into `BorderDispatcher`. The existing `MsgTransfer`+`Ghost`+`ArrivalConfirm` protocol continues to handle entity ownership transfer.
- **`Coordinator.UpdatePlayerRoute`** atomic routing-table update for player handoff.
- **Delta compression of border frames** — current path sends the full registry-driven component tail every tick. Delta encoding against acknowledged baselines will reuse the per-`NodeViewer` `BaselineStore` (already allocated but unread). Expected 60–80% bandwidth reduction for mostly-static entities.
- **Soft visibility / prefetch + prepare-co-simulate-commit handoff model** — see the [target architecture](mmokit-target-architecture.md#entity-handoff-protocol). The handoff state machine is built; wiring it into production is the remaining work.
- **`pkg/universe/loopback_bridge.go`** (Phase 6 from the plan) — built as a test-only 2-node integration harness with latency/loss injection, used only by its own unit tests so far. #12's integration tests can drive it.

**Known regression:** Slither multi-node snake visual fidelity — the legacy path had a slither-specific `ScanBorderEntities` override that walked body segments to mark long snakes as near-border. The new `BorderDispatcher.entityNearNeighborEdge` only tests the head's Position. Fix requires adding a game-facing candidate-provider hook; tracked as slither-only cleanup for the next pass. Space game unaffected.

**References:** [spec](../superpowers/specs/2026-04-11-tiered-push-replication-design.md), [plan](../superpowers/plans/2026-04-11-tiered-push-replication.md).

**Complexity of remaining work:** Medium. All the hard design and groundwork is on `main`. #12 picks up wiring + delta encoding + soft visibility.

---

### 12. Connection Migration Protocol — SHIPPED across S4–S7 + T&T

**Original problem:** All nodes run as goroutines in a single process, sharing one `ConnManager`. Cannot scale beyond one machine. `nodeBridge` uses Go channels, not network.

Done across a sequence of subsequent plans that are tracked below in [Post-Phase-3b Major Work](#post-phase-3b-major-work):

- **Network NodeBridge** — S4 (Coordinator Control Plane) added the MeshControl gRPC service and `HostNetwork` carrying cross-host traffic over `meshpb.MeshData` bidi streams. Single host-to-host path: loopback (same process) or gRPC.
- **Gateway / routing layer** — S6 shipped `RoleGateway` as a distinct role. Gateway terminates WebSockets, dispatches client traffic via MeshData, and receives targeted `CoordMessage.UpstreamSwitch` on session handoff.
- **Authority epoch on NetworkID** — T&T added `NetworkID.Epoch` and threaded it through the replication wire format. Stale packets from old owners drop via `highestSeenEpoch` guards in border replication and the `VirtualConnManager`'s never-downgrade rule.
- **Entity transfer protocol (S7)** — unified `CellTransfer` envelope covers split, merge, and migrate across hosts. Handoff is v1 (Prepare+Commit fire together, no warmup window); real overlap handoff is queued.

Outstanding: gateway session tokens for transparent crash recovery; see the entry in [What's Next](#whats-next-prioritized).

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

## Post-Phase-3b Major Work

These plans weren't in the original roadmap (they became necessary once distributed meshing and subtle concurrency bugs moved to the foreground). Everything in this section is shipped on `main`.

### S4 — Coordinator Control Plane

**Shipped 2026-04-13.** Introduced `MeshControl` gRPC service, `HostRegistry` + `GatewayRegistry`, host heartbeats + liveness detection, and `HostOpAck` for synchronous admin commands (CellAssign / CellRelease / CellRename). Sets up the bones for a multi-process mesh: a single "coordinator" process maintains authoritative ownership state; any number of "host" processes dial it via `--coordinator-addr` and receive assignments.

**Plan:** `docs/superpowers/plans/2026-04-13-S4-coordinator-control-plane.md`

### S5 — PostgreSQL Persistence Redesign

**Shipped 2026-04-13.** Replaced the BoltDB + generic KV layer with typed domain repositories (`PlayerRepository`, `MarketRepository`, `ConfigRepository`) backed by PostgreSQL via `pgx/v5`. Hybrid relational + JSONB schema (hot fields are typed columns with indexes; sparse/evolving shapes live in JSONB). `PlayerFlusher` tracks dirty players in-memory and submits batched `pgx.Batch` upserts every ~15s. Marketplace writes are synchronous. Dev docker-compose via `just db-up`. No backward compat — BoltDB, bbolt, and the generic KV interface are gone.

**Plan:** `docs/superpowers/plans/2026-04-13-S5-postgres-persistence-redesign.md`

### S6 — Gateway Multi-Process Gameplay

**Shipped 2026-04-13.** `RoleGateway` became a first-class role (vs the implicit gateway-embedded-in-coordinator model). A gateway can now run standalone behind a load balancer (`--mode=gateway --coordinator-addr=...`), or embedded (`--mode=coordinator,gateway`). Client session routing lives on the gateway; on cross-host entity handoff, the coordinator dispatches a **targeted** `CoordMessage.UpstreamSwitch` to the gateway holding that session. `VirtualConnManager` translates between wire ConnIDs (gateway-local) and host-local ConnIDs with epoch-gated never-downgrade semantics.

**Plan:** `docs/superpowers/plans/2026-04-13-S6-gateway-multiprocess-gameplay-redesign.md`

### S7 — Distributed Cell Splits, Merges, Migrates

**Shipped 2026-04-14 through 2026-04-18.** Unified `meshpb.CellTransfer` envelope with a `CellTransferKind` discriminator (`SPLIT`, `MERGE`, `MIGRATE`) carries all three topology operations. Orchestrator on the coordinator tracks in-flight transfers, aggregates `CellTransferReady` responses, and commits topology atomically under a single ownership lock. Same code path works for single-host (in-process function call through the loopback bridge) and cross-host (gRPC MeshData) with no branching. Graceful shutdown on remote hosts migrates every owned cell before exiting. Locality-weighted cell placement (`AssignCellsAcrossHostsWithLocality`) biases adjacent cells onto the same host.

**Plan:** `docs/superpowers/plans/2026-04-14-S7-distributed-cell-splits-merges.md`

### Distributed Command System + Perf Command

**Shipped 2026-04-15 / 2026-04-16.** Every admin verb (console + future CLI + future dashboard) routes through `pkg/cmdsys.Dispatcher`. Commands declare typed Go structs for Args / Result, a capability tag, and a `RouteKind` (Local, AllHosts, PlayerOwner, EntityOwner, SpecificCell, SpecificHost). Route resolver lives on the coordinator (`pkg/universe/cmdsys_resolver.go`) and uses live state to dispatch. `perf.snapshot` demonstrates cross-host fan-out + aggregation — operators see per-cell tick profiles from every host with one command.

**Plans:** `docs/superpowers/plans/2026-04-15-distributed-command-system.md`, `docs/superpowers/plans/2026-04-16-distributed-perf-command.md`

### Epoch-Gated Authority Handoff

**Shipped 2026-04-17.** `component.NetworkID` gained an `Epoch uint32` field threaded through `pkg/quantize` wire format and the TypeScript decoder. Every authority transfer bumps epoch; stale packets from old owners drop trivially in both border replication (via `highestSeenEpoch`) and session routing (via VCM's never-downgrade rule). Foundation for correct cross-host handoff.

**Plan:** `docs/superpowers/plans/2026-04-17-epoch-gated-authority-handoff.md`

### Time & Transparency

**Shipped 2026-04-18 through 2026-04-20.** Addressed five categories of handoff-visible client glitches: (1) server timestamp on every frame for proper interpolation anchoring, (2) `FRAME_FLAG_FRESH_SNAPSHOT` bit when a cell switches authority so the client discards stale predictions and resets interp, (3) first-post-transfer-tick dt scaled to true wall-time elapsed so destination physics doesn't jump, (4) exclude self-netID from handoff farewell-Removed list, (5) ReplicationSystem's per-tick `exited` list also excludes self.

**Plan:** `docs/superpowers/plans/2026-04-20-time-and-transparency.md`

### State Integrity Framework

**Shipped 2026-04-21.** Four layers of runtime guards that catch wrong states at the point of violation:

1. **Invariants** — five named predicates run at every commit boundary. `Config.InvariantMode = InvariantPanic` in dev fails loudly; production uses `InvariantLog`.
2. **CommitPlan model** — `applySplit/Merge/MigrateCommit` refactored from imperative functions into ordered `[]PlanStep` lists executed by `ExecuteCommitPlan`. Step names are stable and appear in diagnostic output.
3. **Commit log** — in-memory ring of `CommitEvent` records queryable via `commit.log` console verb, `GET /events` HTTP endpoint, or live-tail via `log events:*`. `--admin-listen=:9101` exposes the endpoint on pure-coordinator processes.
4. **netIDIndex** — per-cell `{netID → entity, presence}` table with typed transition policy. Six spawn paths funnel through it. `Config.StrictNetIDIndex` controls enforcement.

Phase E of the plan surfaced 5+ latent handoff-race bugs during bot-load smoke testing; each was traced to root cause and fixed before merge. Notable root cause: MERGE executor's donor kept ticking between `Execute` (serialize) and commit; donor's `handoff_driver` could ship entities already included in merge populate → duplicate netIDs on survivor. Fix: `WorldBase.drainingForMerge` atomic.Bool + `HandoffDriver.Tick` early-return while set.

**Plan:** `docs/superpowers/plans/2026-04-20-state-integrity.md` · **Spec:** `docs/superpowers/specs/2026-04-20-state-integrity-design.md`

---

## What's Next (prioritized)

Mapped to the target architecture's [Phase 6 (Feel + scale-out)](mmokit-target-architecture.md#evolutionary-path). Ordered by impact-to-effort ratio using current state as baseline.

### Tier 1 — infrastructure already partially built

**A. Wire up Prepare → Overlap → Commit handoff** (#11 follow-up)
The state machine, `MsgHandoffPrepare/Commit/ForwardInput`, `pkg/universe/handoff.go`, `SpawnShadow`/`PromoteShadow`, and `BaselineStore` are all built but not wired into production. Current handoff is v1 — Prepare+Commit fire together with no warmup window. Wiring overlap eliminates cold-start stalls on the destination cell during cross-host handoff, which is the largest remaining pop/jitter source under load. Most of the hard design work already exists; this is primarily integration.

**B. Delta compression of border frames** (#11 follow-up)
Each cross-cell border frame currently ships the full registry-driven component tail every tick. Per-`NodeViewer` `BaselineStore` already allocated but unused. Expected 60-80% bandwidth reduction on cross-host traffic. Single consumer, limited blast radius. Highest bandwidth bang-for-buck.

### Tier 2 — large but natural next step

**C. Client prediction for local player**
Currently all clients are dumb interpolators. Owner-predicted local player with server reconciliation is the most visible "feel" improvement available. Scope: server acks authoritative position against client input sequence; client reconciles. Significant work, directly solves input-latency perception.

**D. #10 Async Entity Serialization**
NetworkSystem is 15-25ms of the 50ms tick budget. Moving snapshot + frame construction off the game-loop goroutine reclaims 30-50% of the tick. Large scope — needs safe ECS read-only snapshotting with Ark's archetype storage. Hash-based change detection and delta snapshots (prereqs) are in place.

### Tier 3 — infrastructure that becomes pressing with scale

**E. UDP transport for client movement**
All traffic is WebSocket today. The quantize/delta frame format already supports baselines with explicit ACK (for UDP); the transport itself isn't wired. Client movement/aim at 20Hz doesn't strictly need reliability-in-order — UDP with sequenced drops would reduce tail-latency spikes under packet loss. Medium scope.

**F. Gateway session tokens for transparent crash recovery**
S6 shipped with "gateway crash = client reconnect + full re-login" as an accepted limitation. Session tokens that let a reconnecting client be recognized and reattached to its in-flight state would close the gap. Medium scope; follow-up to S6.

**G. Rich `NetworkEntityID` (EntityGuid + SpawnSequence)**
Already have the `Epoch` field from T&T. Full GUID stability across restarts and explicit `SpawnSequence` ordering become pressing once you need persistent entity identity across coordinator restarts or clustered deployments. Lower priority while the deployment model is "one coordinator per shard."

### Tier 4 — housekeeping

**H. Auto-rebalance tuning + load-based initial placement**
`PartitionConfig.AutoRebalance` ships off-by-default. `AssignCellsAcrossHostsWithLocality` biases adjacent cells onto one host but doesn't consider expected load. With bot-load smoke testing infrastructure in place (60 bots + live player), tuning these knobs against real load would turn them into production-ready features.

**I. Persistence schema evolution tools**
`golang-migrate` runs embedded migrations at startup. Rolling-schema-change tooling (for large-DB migrations that can't run at process start) isn't there yet. Only matters once you have a production database too large for startup migration.

---

## Design Approach

For each feature, before implementation:

1. Research MMO industry best practices and established server architectures (e.g., Unreal Replication, SpatialOS, Photon, EVE Online's TIDI model)
2. Design from first principles -- do not assume current mmokit patterns are correct
3. Existing APIs can and should change to create simpler, better designs
4. Write a dedicated spec/plan in `docs/superpowers/` before coding

## Verification

After each feature:

- All targets build: `go vet ./...`
- All tests pass: `go test ./... -count=1 -timeout 300s`
- Slither: `cd examples/slither && just dev` — web client connects and plays
- 4node-basic single-process: `cd examples/4node-basic && just dev`
- 4node-basic distributed: `cd examples/4node-basic && just distributed` — 4 processes in tmux
- SDK codegen: `just client-sdk examples/4node-basic` — generates, `npx tsc --noEmit` passes
- For server meshing + commit-path features: `go test ./pkg/universe/ -run '^TestS7' -count=1` — S7 test family under the unified transfer protocol
- For cross-cut integrity features: bot-load smoke test under `InvariantPanic` + `StrictNetIDIndex=true` — the dev defaults on 4node-basic
