# S7: Distributed Cell Splits, Merges, Migration — Clean-Slate Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Each task uses checkbox (`- [ ]`) syntax. This plan extends the S7 portion of [`docs/superpowers/specs/2026-04-12-distributed-mesh-design.md`](../specs/2026-04-12-distributed-mesh-design.md) §3 and §10.
>
> **Branch:** stay on `feature/distributed-mesh`.

---

## Context

With S6 capstone landed, the distributed-mesh roadmap has exactly one unfinished phase: **S7 — Distributed cell splits + merges**, plus the admin `cell migrate` primitive and the load-driven auto-rebalance loop that the spec calls out for later tiers. This plan rolls all three of those together as one phase because they share the same transfer primitive underneath and separating them would force near-duplicate state machines.

The existing single-host `SplitCell`/`MergeCell` at [`pkg/universe/partition.go`](../../../pkg/universe/partition.go) works in-process — it runs on the coordinator goroutine, reaches directly into a cell's game loop via `PendingAdminCmds`, creates children locally, and dispatches `MsgTransfer` through in-memory inboxes. It was written before S3/S4's cross-host infrastructure existed and has never known how to fan children out to remote hosts. S6 now gives us everything we need: `HostNetwork` + `MeshData` streams, `peerKind` dispatch, `sessionRoutes` + targeted `UpstreamSwitch`, the `HandoffDriver` + `Shadow` machinery, and the `TransferFrame` serialization format.

**What this plan delivers:**
1. Split a cell where each of the 4 children lands on a coordinator-chosen host (local or remote).
2. Merge 4 siblings into one survivor with donors on different hosts shipping entities in.
3. `cell migrate <cellID> <hostID>` — admin command that moves a single cell between hosts with zero client disconnect.
4. Graceful shutdown — SIGINT on a host drains its cells to surviving hosts before exit.
5. Load-driven auto-rebalance primitive (wired, hysteresis-aware, **default off**).

The user's explicit direction: "**make sure we're not creating any legacy baggage in the core of mmokit and we're throwing away old patterns in favor of new and/or better ones where possible.**" This plan takes the rewrite path wherever it touches existing split/merge code rather than layering on top of it.

---

## Research summary (fed into the architecture)

**Industry precedent:**
- **SpatialOS Runtime v2** (2020 rewrite): load balancer — not workers — decides authority. Hysteresis on boundary entities prevents thrash. About half the work of v2 was migration planning, not the algorithm.
- **EVE Online**: ~200 SOL blades, 2 nodes each (1 core/node). Systems assigned "most-loaded unassigned → least-loaded live" with a 20% locality bonus (same constellation prefers same node).
- **Orleans 8/9**: live grain migration moves actors between silos without losing in-memory state or dropping pending requests. 9.2 default shifted from random to resource-optimized placement. Activation rebalancing (resource balance) is explicitly a separate algorithm from activation repartitioning (communication locality).
- **Nakama**: CRDT + gossip for service discovery, presence = `(user, session, node)` tuple, message routing transparent across cluster topology.
- **Quadtree literature**: static local quadtree splits are a fixed-effort operation per object; distributed quadtree coordination at MMO scale is genuinely under-published — this is our own design work.

**Lessons baked into the design:**
1. **Load balancer owns authority decisions**, hosts are executors. Matches our existing coordinator-authoritative design; reinforces it.
2. **Hysteresis is first-class** — any migration threshold needs a dead zone to prevent authority thrash at boundaries.
3. **Resource balance and communication locality are separate concerns.** Default placement uses rendezvous with a locality bonus; rebalance uses load deltas with hysteresis. Don't collapse them.
4. **Migration planning is ~50% of the work.** Rollback paths, timeouts, partial failures, and backward-compat windows matter as much as the happy path.
5. **Preserve in-memory state across migration.** Flush persistent state (Postgres) before transfer, ship ephemeral state inline, resume on the other side with zero client-visible disconnect.

---

## Architecture decisions

### Decision 1: Unified `CellTransfer` protocol (one message, three operations)

Split, merge, and migrate are three shapes of the same primitive: *"take this cell's state and move it somewhere."* Instead of three proto messages (the current stubs `CellSpawnTransfer`, `CellMergeTransfer`, `CellMigratePayload` at `mesh.proto:359-376`), collapse to one:

```protobuf
message CellTransfer {
  uint64 request_id      = 1;   // orchestrator-assigned, used to correlate Ready responses
  CellTransferKind kind  = 2;   // SPLIT | MERGE | MIGRATE
  string src_cell_id     = 3;   // split: parent; merge: donor; migrate: same as dest
  string dest_cell_id    = 4;   // split: child; merge: survivor; migrate: same as src
  string dest_host_id    = 5;   // target host for dest cell (empty = stay local)
  uint32 quadrant        = 6;   // split only: 0=BL, 1=BR, 2=TL, 3=TR
  uint64 src_epoch       = 7;   // parent/donor/source epoch at serialization time
  bytes  entities        = 8;   // packed TransferFrame records, length-prefixed
  bytes  sessions        = 9;   // packed SessionTransfer records
  CellBounds bounds      = 10;  // world-space bounds of dest cell
}

enum CellTransferKind {
  CELL_TRANSFER_UNSPECIFIED = 0;
  CELL_TRANSFER_SPLIT       = 1;
  CELL_TRANSFER_MERGE       = 2;
  CELL_TRANSFER_MIGRATE     = 3;
}

message CellBounds { float min_x=1; float min_y=2; float max_x=3; float max_y=4; }

message CellTransferReady {
  uint64 request_id   = 1;
  string dest_cell_id = 2;
  string host_id      = 3;
  bool   ok           = 4;
  string error        = 5;  // populated on failure
}
```

**Retires:** `CellSpawnTransfer`, `CellMergeTransfer`, `CellMigratePayload` stubs at `mesh.proto:359-376`. `MeshFrame.oneof` slot 6 is reassigned to `cell_transfer`, slot 7 to `cell_transfer_ready`, slot 8 freed for future use.

Rationale: collapsing three proto messages into one with a discriminant gives us one state machine, one handler, one set of tests. Per the user's "throw away old patterns" direction this is the obvious win — the three stubs only look distinct because they grew separately.

### Decision 2: Message-driven execution (coordinator commands, hosts execute)

Today's `SplitCell`/`MergeCell` in `partition.go` runs ~500 lines of orchestration *inside the coordinator goroutine*. It reaches into cell game loops via `PendingAdminCmds`, holds `c.mu.Lock()` across serialization, calls `createNode` directly, and dispatches `MsgTransfer` to local inboxes. This only works for colocated hosts. Adding a "but-if-remote-then-ship-via-MeshData" branch to every step would bolt distributed support onto a fundamentally single-host shape.

**The rewrite:**

```
Today (partition.go SplitCell):
  Coordinator goroutine
    ├─ hold c.mu write lock
    ├─ run serializer on parent's game loop via PendingAdminCmds
    ├─ createNode(child) x 4  (always local)
    ├─ inbox.send(MsgTransfer) x 4  (always local)
    └─ tear down parent
  ~500 lines

S7 (rewrite):
  Coordinator goroutine
    ├─ orchestrator.BeginSplit(cellID) → constructs CellTransfer command
    └─ dispatches via control plane (MeshControl stream or local loopback)

  Parent host (in its own process, or local)
    ├─ cellTransferExecutor receives CellTransfer command
    ├─ runs serialization on parent's game loop
    ├─ packs entities per quadrant into CellTransfer proto
    ├─ ships via HostNetwork MeshData stream (or local handler) to target host(s)
    └─ reports CellTransferReady

  Target host(s)
    ├─ cellTransferExecutor receives CellTransfer data
    ├─ createNode for dest cell
    ├─ unpacks entities on new cell's game loop
    └─ reports CellTransferReady

  Coordinator
    ├─ waits for all expected Ready responses (with timeout)
    ├─ commits topology atomically
    └─ broadcasts PeerList + targeted UpstreamSwitch per moved session
```

**What this unlocks:**
1. **Single code path for single-host and multi-host.** In single-host mode the "ship via MeshData" step is a direct in-process handler call; in multi-host mode it's a gRPC stream send. Both go through the same executor entry point. No special cases.
2. **Coordinator becomes purely control plane.** It no longer reaches into cell internals. Unit tests construct an orchestrator + fake executor and drive the protocol without spinning up a real cell.
3. **Partial-failure handling is natural.** Coordinator tracks in-flight request IDs; on timeout or `Ready{ok: false}` it runs the rollback path (reassign failed children to parent's host, or abort the whole split). Same mechanism as existing crash recovery.
4. **`partition.go` shrinks from ~1100 lines to ~250.** The orchestration state moves to `cell_transfer.go`, the per-cell execution moves onto the host's game loop.

Rationale: this is the rewrite the user asked for. Matches SpatialOS v2's "load balancer decides, workers execute" pattern and Orleans's grain-migration request/response model. The in-process direct-orchestration shape is a transitional artifact from before S3 existed; keeping it would leave a permanent seam between single-host and distributed modes.

### Decision 3: Quadtree-aware rendezvous + retire Tier 1 early-return

`enumerateCells()` at `coord_assignment.go:279-287` hardcodes a depth-0 grid walk: `for sy < CellsY { for sx < CellsX { ... } }`. It doesn't know about dynamically-split cells. Replace with a walker that returns the coordinator's **current live cell ID set**, sourced from `cellToHostMap` keys after S7 commits make that map authoritative.

`rebalance()` at `coord_assignment.go:233-244` has the Tier 1 early-return: if any `h.Local` is present in the registry, skip rebalance entirely so cells stay pinned to local hosts. **This goes away.** After S7 there's only one rebalance algorithm and it treats local hosts the same as remote hosts. The "Tier 1/Tier 2/Tier 3" language in CLAUDE.md gets retired.

`AssignCellsAcrossHosts` gains a **locality weight**: a small bonus (say, +15% rendezvous score) for a host that already owns a cell adjacent to the one being placed. Cheap to compute, matches EVE's constellation-locality pattern, and for S7 startup it keeps the "all cells on local host by default" behavior without needing a special-case flag — local host scores higher naturally because it already holds everything.

### Decision 4: Separate load-rebalance policy (default off) from placement

Placement (which host gets a new cell) runs via rendezvous+locality. Load-rebalance (which existing cell should move off an overloaded host) is a separate loop that makes migration decisions from `LoadSnapshot` data. Matches Orleans's explicit separation of activation repartitioning vs activation rebalancing.

**Rebalance policy (knobs on `PartitionConfig`):**

```go
type PartitionConfig struct {
    // ... existing split/merge knobs ...

    // Load-driven rebalance (default-off)
    AutoRebalance          bool          // enables the load-rebalance loop; default false
    RebalanceEvalInterval  time.Duration // default 10s
    RebalanceCPUThreshold  float64       // default 0.85 (per-host tick-budget fraction)
    RebalanceSustainTime   time.Duration // default 60s host-overloaded before acting
    RebalanceMinDelta      float64       // default 0.20 (hysteresis; target host load must
                                         // be >= delta less than source load)
    RebalanceCooldown      time.Duration // default 30s between successive migrations
    RebalanceMaxConcurrent int           // default 1 (one migration in flight)
}
```

Default is **false** — the primitive ships but the loop is silent until operators opt in. Matches the user's safest-default preference ("no surprise migrations in prod"). When enabled, the loop picks the heaviest cell from the most-loaded host, picks a target via rendezvous-weighted-by-inverse-load, and issues `BeginMigrate` through the same orchestrator the admin command uses.

### Decision 5: Topology commit is atomic, PeerList broadcast is synchronous

Today `Topology.Neighbors` is a precomputed map rebuilt on split/merge. For S7 it gets rebuilt atomically inside the orchestrator's commit step, still under `c.mu` write lock, before `PeerList` broadcasts to hosts + gateways. Alternative (lazy `NeighborsOf(cellID)` query) is more flexible but adds per-lookup cost on the hot path. Stick with recompute-on-commit — splits are rare (60s cooldown minimum) and the commit already holds the lock for other reasons.

The atomic commit step does exactly this order:

```
1. c.mu.Lock()
2. Update c.cellToHostMap: delete old entries, add new entries
3. Rebuild Topology.Neighbors for affected cells (their neighbors + their old neighbors)
4. Walk c.sessionRoutes, remap affected sessions to new host/cell, bump epochs
5. c.mu.Unlock()
6. Broadcast PeerList to all hosts + gateways (fires async, fan-out)
7. Send targeted UpstreamSwitch to affected gateways (per moved session)
```

Steps 1-5 hold the lock briefly (~ms). Steps 6-7 fan out after lock release. Any in-flight `ClientInput` from gateway to the old host during the window gets dropped by the executor (epoch mismatch on `sessionRoutes`) and the client's dead-reckoning interpolator bridges the 1-2 tick gap — same risk window as single-entity handoff.

---

## Flow diagrams

### Split: 1 cell → 4 children, 2 children land on a remote node

```
Admin (console)     Coordinator           Parent Host             Target Host
     │                  │                      │                      │
     │─ cell split X ──▶│                      │                      │
     │                  │                      │                      │
     │                  │ orchestrator.BeginSplit(X)                  │
     │                  │   ├─ pick children: X0,X1,X2,X3             │
     │                  │   ├─ rendezvous+locality → host assignments │
     │                  │   │    X0,X1 → parent's host (local)        │
     │                  │   │    X2,X3 → node-b (remote)              │
     │                  │   └─ request_id = 42                         │
     │                  │                      │                      │
     │                  │─ MeshControl.CellTransfer{42,SPLIT,X→X0,...}▶│
     │                  │─ MeshControl.CellTransfer{42,SPLIT,X→X1,...}▶│
     │                  │─ MeshControl.CellTransfer{42,SPLIT,X→X2,...}─────────▶│
     │                  │─ MeshControl.CellTransfer{42,SPLIT,X→X3,...}─────────▶│
     │                  │                      │                      │
     │                  │                      │ executor:            │
     │                  │                      │  ├─ drain X (flush dirty players)
     │                  │                      │  ├─ serialize entities per quadrant
     │                  │                      │  ├─ ship X0,X1 to local inbox
     │                  │                      │  └─ ship X2,X3 via MeshData ──▶│
     │                  │                      │                      │ executor:
     │                  │                      │                      │  ├─ createNode(X2)
     │                  │                      │                      │  ├─ populate entities
     │                  │                      │                      │  └─ start game loop
     │                  │                      │                      │
     │                  │◀─ CellTransferReady{42,X0,ok:true} ─────────│
     │                  │◀─ CellTransferReady{42,X1,ok:true} ─────────│
     │                  │◀─ CellTransferReady{42,X2,ok:true} ─────────────────│
     │                  │◀─ CellTransferReady{42,X3,ok:true} ─────────────────│
     │                  │                      │                      │
     │                  │ orchestrator.CommitSplit(42):                │
     │                  │   ├─ c.mu.Lock()                             │
     │                  │   ├─ cellToHostMap.delete(X)                 │
     │                  │   ├─ cellToHostMap.set(X0,parent) ...        │
     │                  │   ├─ Topology.Neighbors.rebuild(X0..X3,n*)   │
     │                  │   ├─ sessionRoutes.remapSplit(X,[X0..X3])    │
     │                  │   └─ c.mu.Unlock()                           │
     │                  │                      │                      │
     │                  │─ PeerList broadcast ▶│                      │
     │                  │─ PeerList broadcast ─────────────────────────▶│
     │                  │─ UpstreamSwitch per moved session ──▶gateways│
     │                  │                      │                      │
     │                  │─ CellRelease(X) ────▶│                      │
     │                  │                      │ tear down parent     │
     │                  │                      │ cell goroutine       │
     │◀─ split ok ──────│                      │                      │
```

### Merge, migrate, graceful shutdown all follow the same shape

- **Merge:** coordinator sends 4× `CellTransfer{MERGE, src=donor_i, dest=survivor, dest_host=survivor_host}`. Survivor host receives 4 responses and collapses the entities into one cell. Commit updates `cellToHostMap`, renames survivor's cell ID to parent ID.
- **Migrate:** coordinator sends 1× `CellTransfer{MIGRATE, src=X, dest=X, dest_host=new_host}`. Source host serializes, ships to target, tears down local cell. Commit moves the `cellToHostMap` entry. No topology change, just ownership change — `Topology.Neighbors` stays valid.
- **Graceful shutdown:** host sends `HostMessage.GracefulLeave{host_id}`. Coordinator runs `BeginMigrate` for each cell on that host, picks targets via rendezvous over surviving hosts. When all migrations commit, coordinator responds `CellsDrained` and host exits.

All four operations go through the same `orchestrator` + `executor` code path.

---

## File structure

### Created

| Path | Responsibility |
| --- | --- |
| `pkg/universe/cell_transfer.go` | Types (`CellTransferKind`, `CellTransferRequest`) + orchestrator state machine on coordinator |
| `pkg/universe/cell_transfer_executor.go` | Executor on hosts — receives commands, runs serialization on the cell's game loop, ships data, reports Ready |
| `pkg/universe/cell_transfer_test.go` | Unit tests for orchestrator (mock executor) + executor (mock orchestrator) |
| `pkg/universe/rebalance.go` | Auto-rebalance loop + policy evaluator + hysteresis logic |
| `pkg/universe/rebalance_test.go` | Unit tests for rebalance policy with synthetic load snapshots |
| `pkg/universe/s7_split_test.go` | Integration test: 2 nodes + coordinator, `TestS7SplitAcrossHosts` |
| `pkg/universe/s7_merge_test.go` | Integration test: `TestS7MergeAcrossHosts` |
| `pkg/universe/s7_migrate_test.go` | Integration test: `TestS7MigrateAcrossHosts` + `TestS7GracefulShutdown` |

### Modified

| Path | What changes |
| --- | --- |
| `proto/meshpb/mesh.proto` | Replace `CellSpawnTransfer`/`CellMergeTransfer`/`CellMigratePayload` stubs with unified `CellTransfer` + `CellTransferReady` on MeshFrame. Add `GracefulLeave` + `CellsDrained` to HostMessage/CoordMessage. Regenerate |
| `pkg/universe/partition.go` | `SplitCell` / `MergeCell` shrink to thin wrappers that call `c.orchestrator.BeginSplit/BeginMerge`. Delete all direct in-process orchestration code (~450 lines removed). `PartitionConfig` gains `AutoRebalance` + rebalance knobs |
| `pkg/universe/coord_assignment.go` | Remove Tier 1 early-return. `enumerateCells()` walks `cellToHostMap` keys instead of depth-0 grid. Add locality weight to `AssignCellsAcrossHosts` |
| `pkg/universe/coordinator.go` | Wire `orchestrator` + `rebalanceLoop` into `Coordinator` struct + `Build()` + `Start()`. **File split**: extract `coordinator_build.go` (build logic, ~500 lines) + `coordinator_servers.go` (gRPC server wiring, ~400 lines) leaving `coordinator.go` as the core lifecycle at ~1100 lines |
| `pkg/universe/host_network.go` | Route inbound `CellTransfer` MeshFrames to local `cellTransferExecutor`. Route `CellTransferReady` on the control stream path |
| `pkg/universe/mesh_control_server.go` | Dispatch `CellTransfer` / `CellTransferReady` / `GracefulLeave` / `CellsDrained` between coordinator ↔ hosts |
| `pkg/universe/mesh_control_client.go` | Node-side handler: receive `CellTransfer` commands, hand off to local executor; send `GracefulLeave` on SIGINT |
| `pkg/universe/topology.go` | `Topology.Neighbors` rebuild function takes a set of affected cells instead of rebuilding the whole map. Called atomically from orchestrator commit |
| `pkg/universe/message.go` | **Delete** `MsgTransfer` + `MsgArrivalConfirm` constants (dead code) |
| `pkg/component/core.go` | **Strip** `Ghost.TTL`, `Ghost.DestNodeID`, `Ghost.Confirmed` fields — Ghost becomes a pure marker component per the spec's "repurposed as visibility marker" language |
| `pkg/universe/world_base.go` | `TickGhosts()` simplified (no TTL decay on a pure-marker component — deletion handled by a simple one-tick cleanup) |
| `internal/game/commands.go` | Add `cell migrate <cellID> <hostID>` console command |
| `CLAUDE.md` | Retire Tier 1/2/3 language. Document unified `CellTransfer` protocol. Document `cell migrate` + graceful shutdown. Move "S7 deferred" notes into the body |
| `examples/4node-basic/main.go` | Add `just s7-demo` recipe + synthetic-load generator that triggers auto-splits |

### Deleted

Only content is deleted (~450 lines out of `partition.go`, ~5 lines of `message.go` constants, ~10 lines of `Ghost` field stripping). No file-level deletions.

---

## Task breakdown

Structured for subagent execution — each task is a self-contained checkbox group with independent verification.

### T1 — Proto consolidation + legacy retirement

- [ ] Replace `CellSpawnTransfer`, `CellMergeTransfer`, `CellMigratePayload` at `mesh.proto:359-376` with unified `CellTransfer` + `CellTransferKind` enum + `CellBounds` + `CellTransferReady`
- [ ] Reassign `MeshFrame.oneof` slot 6 to `cell_transfer`, slot 7 to `cell_transfer_ready`, free slot 8 for future
- [ ] Add `HostMessage.graceful_leave` + `CoordMessage.cells_drained` variants
- [ ] Add a `CellTransferAbort { request_id }` variant on `MeshFrame` so the orchestrator can tell a target host to drop a partial transfer during rollback
- [ ] `buf generate`
- [ ] Delete `MsgTransfer` + `MsgArrivalConfirm` constants from `pkg/universe/message.go:7-14` + any remaining references
- [ ] Strip `Ghost.TTL`, `Ghost.DestNodeID`, `Ghost.Confirmed` fields from `pkg/component/core.go:62-66`; leave `Ghost{}` as pure marker
- [ ] Update `TickGhosts()` in `world_base.go` to match (simple one-tick cleanup, no TTL decay)
- [ ] Update all `Ghost{...}` construction sites to use zero-value marker
- [ ] `go vet ./... && go test -count=1 ./pkg/...`
- [ ] Commit: `refactor(universe): unify cell transfer proto + retire pre-S6 ghost fields`

### T2 — Quadtree-aware rendezvous + kill Tier 1

- [ ] `enumerateCells()` in `coord_assignment.go:279` rewrites to read `coord.cellToHostMap` keys instead of depth-0 grid walk. For cold start (before first commit), falls back to the depth-0 grid. Add test coverage for depth-1 cells post-split
- [ ] Remove Tier 1 early-return in `rebalance()` at `coord_assignment.go:238-244`. Delete the `h.Local` check; treat local hosts identically to remote
- [ ] Add `LocalityWeight` to rendezvous scoring in `AssignCellsAcrossHosts`: `score = fnv64(cellID || hostID); if host owns a neighbor of cellID, score += (score * 0.15)`. Tune constant later
- [ ] Retire Tier 1/2/3 language in code comments (full `CLAUDE.md` update is T12)
- [ ] `go test -count=1 ./pkg/universe/...`
- [ ] Commit: `refactor(universe): quadtree-aware rendezvous; retire Tier 1 early-return`

### T3 — CellTransferOrchestrator on coordinator

- [ ] New file `pkg/universe/cell_transfer.go` with types: `CellTransferRequest`, `orchestrator struct { inflight map[uint64]*CellTransferRequest; mu sync.Mutex }`
- [ ] Methods: `BeginSplit(cellID CellID) error`, `BeginMerge(parentCellID CellID) error`, `BeginMigrate(cellID, destHost string) error`, `OnReady(msg *meshpb.CellTransferReady)`, `timeoutLoop()`
- [ ] `BeginSplit`: compute 4 children, rendezvous+locality assign each, dispatch 4× `CellTransfer{SPLIT}` commands via control-plane dispatcher, register in-flight state, start timeout
- [ ] `BeginMerge`: pick survivor via rendezvous on parent ID, dispatch 4× `CellTransfer{MERGE}` (one per donor) to donor hosts
- [ ] `BeginMigrate`: 1× `CellTransfer{MIGRATE}` to source host, `dest_host` set
- [ ] `OnReady`: record response, when all expected Ready arrived (or timeout) → call `commit(requestID)` or `rollback(requestID)`
- [ ] `commit(requestID)`: atomic `cellToHostMap` + `Topology` + `sessionRoutes` update under `c.mu`, then async `PeerList` broadcast + `UpstreamSwitch` fan-out
- [ ] `rollback(requestID)`: for split — send `CellTransferAbort` to each target host that already Ready'd, parent keeps authority; for merge — donors re-activate from snapshot (log + fail hard if snapshot gone); for migrate — source keeps cell
- [ ] Unit tests with a mock executor interface: verify commit happens after all Ready; verify timeout triggers rollback; verify partial failure path
- [ ] `go test -count=1 ./pkg/universe/...`
- [ ] Commit: `feat(universe): cell transfer orchestrator on coordinator`

### T4 — CellTransferExecutor on host

- [ ] New file `pkg/universe/cell_transfer_executor.go` with type `cellTransferExecutor struct { host *Host; log *Logger }`
- [ ] Method `Execute(cmd *meshpb.CellTransfer) error`: dispatches on `cmd.Kind`
- [ ] **SPLIT path**: find source cell in `host.Cells`; enqueue serialization closure via `PendingAdminCmds`; closure runs on game loop: flush dirty players via `gw.PlayerDB.FlushCell(srcCellID)`, serialize entities in the requested quadrant to `TransferFrame[]`, pack sessions, build `CellTransfer` response data bytes. For local dest host → deliver to local `host.Cells[destCellID]` inbox; for remote dest host → send via `host.Network.SendCellTransfer(destHost, ...)`. After delivery: call `orchestrator.OnReady` (local) or send `CellTransferReady` via MeshControl
- [ ] **MERGE path**: same as SPLIT but serialize ALL entities in donor cell (not quadrant)
- [ ] **MIGRATE path**: same as SPLIT but entity set = all entities, `dest_cell_id` = `src_cell_id`
- [ ] **RECEIVE path** (target host side): when `host.Network.OnCellTransfer(cmd)` fires → enqueue a closure that `createNode(cmd.DestCellID)` with `fromSplit=true`, populates entities from `cmd.Entities`, starts game loop, sends `CellTransferReady{ok:true}`
- [ ] **ABORT path**: on `CellTransferAbort` with matching request_id, if the cell was created, tear it down and drop entities; ack
- [ ] Use existing `TransferFrame` encoding (do NOT invent new format)
- [ ] Unit tests: fake host, test SPLIT serialization per quadrant; test MERGE all-entity serialization; test MIGRATE round-trip; test ABORT teardown
- [ ] `go test -count=1 ./pkg/universe/...`
- [ ] Commit: `feat(universe): cell transfer executor on hosts`

### T5 — Rewire SplitCell/MergeCell as thin wrappers

- [ ] `partition.go` `SplitCell(cellID, bypass)` becomes: cooldown check + `return c.orchestrator.BeginSplit(cellID)`. Delete direct orchestration (~300 lines)
- [ ] `MergeCell(cellID, bypass)` same treatment, ~200 lines deleted
- [ ] Partition monitor's split/merge decision → same new wrappers
- [ ] `partition.go` final line count target: <300 (currently ~1100)
- [ ] **No behavioral regression on single-host**: the existing dynamic-cells test suite must stay green. Single-host mode routes `BeginSplit` through the same orchestrator → executor path; in-process dispatch replaces the old direct calls
- [ ] `go test -count=1 ./pkg/universe/... -run Partition`
- [ ] Commit: `refactor(universe): SplitCell/MergeCell route through orchestrator`

### T6 — Admin `cell migrate` console command

- [ ] `internal/game/commands.go` (and equivalent builtins registration in `pkg/universe`) add `cell migrate <cellID> <hostID>` command
- [ ] Wires to `coord.orchestrator.BeginMigrate`
- [ ] Console feedback: progress reporting, success/failure, time taken
- [ ] Integration test `TestS7MigrateAcrossHosts`: 2 hosts, migrate a cell between them with a player in it, verify player entity preserved + no disconnect
- [ ] `go test -count=1 ./pkg/universe/... -run S7Migrate`
- [ ] Commit: `feat(universe): cell migrate admin command`

### T7 — Graceful shutdown via `GracefulLeave`

- [ ] Node-side SIGINT handler in `cmd/server/main.go` + `examples/4node-basic/main.go`: before the existing `coord.Shutdown`, if the process is `RoleNode`, send `HostMessage_GracefulLeave` via the control client and wait for `CellsDrained` (or a 30s timeout before hard exit)
- [ ] Coordinator: on `GracefulLeave` → for each cell on that host, pick a target via rendezvous over surviving hosts, call `BeginMigrate`, track pending migrations, on all-complete send `CellsDrained`
- [ ] Integration test `TestS7GracefulShutdown`: 3 hosts, send SIGINT to one, verify its cells migrate to surviving hosts + no cell goes offline
- [ ] Commit: `feat(universe): graceful shutdown via GracefulLeave`

### T8 — Auto-rebalance loop (default off)

- [ ] New file `pkg/universe/rebalance.go` with `rebalanceLoop struct { coord *Coordinator; cfg *PartitionConfig; lastMigration time.Time }`
- [ ] `Run(ctx)` starts a ticker on `cfg.RebalanceEvalInterval`
- [ ] Each tick: collect `LoadSnapshot` from every host; find any host with `CPUFraction >= cfg.RebalanceCPUThreshold` sustained for `cfg.RebalanceSustainTime`; pick its heaviest cell; pick target host = lowest-load host where `(source_load - target_load) >= cfg.RebalanceMinDelta`; call `BeginMigrate`; set `lastMigration = now`, don't fire another migration until `cfg.RebalanceCooldown` elapses
- [ ] `PartitionConfig` extends with the knobs from Decision 4 above; `AutoRebalance=false` by default, and `DefaultPartitionConfig()` returns it disabled
- [ ] Unit tests with synthetic `LoadSnapshot` streams: verify hysteresis prevents thrash; verify cooldown; verify default-off behavior
- [ ] `go test -count=1 ./pkg/universe/... -run Rebalance`
- [ ] Commit: `feat(universe): hysteresis-aware auto-rebalance loop (default off)`

### T9 — Atomic topology commit + PeerList broadcast wiring

- [ ] Orchestrator `commit()` calls a new `Coordinator.applyCellTopologyChange()` that, under `c.mu.Lock()`:
  1. Updates `cellToHostMap` (add/remove entries)
  2. Calls `Topology.RebuildNeighborsFor(affectedCellIDs)` — incremental rebuild
  3. Calls `sessionRoutes.RemapCells(oldCellID, newCellIDMap)` — migrate affected sessions, bump epochs, collect list of sessions that need `UpstreamSwitch`
- [ ] After lock release: fan-out `PeerList` to hosts + gateways; per-session `UpstreamSwitch` to gateways holding affected sessions
- [ ] `Topology.RebuildNeighborsFor(cellIDs []string)` is new — incremental rebuild of neighbor entries touching the given cells + their neighbors. Delete the old wholesale `Topology.Neighbors` recompute
- [ ] Race test: `TestS7ConcurrentHandoffDuringSplit` — one goroutine triggers a split, another drives a single-entity handoff. Verify no panics and the entity lands in the right cell
- [ ] Commit: `feat(universe): atomic topology commit after cell transfer`

### T10 — Integration tests across multi-host topologies

- [ ] `TestS7SplitAcrossHosts`: coordinator + 2 hosts (node-a, node-b); spawn cell X on node-a with some entities; force-split X; verify 2 children on node-a, 2 on node-b (given rendezvous + locality); verify entity count conserved; verify `sessionRoutes` + `PeerList` updated; verify no client disconnect
- [ ] `TestS7MergeAcrossHosts`: inverse — 4 siblings distributed 2+2 across nodes, force-merge, verify survivor holds all entities
- [ ] `TestS7MigrateAcrossHosts`: single cell, admin-triggered migration
- [ ] `TestS7GracefulShutdown`: SIGINT on a node, drain, exit
- [ ] `TestS7AutoRebalanceHysteresis`: synthetic load, verify hysteresis prevents thrash
- [ ] `TestS7RollbackOnTimeout`: force a target host to not respond, verify orchestrator times out and rolls back cleanly (source cell keeps authority)
- [ ] `go test -count=1 ./pkg/universe/... -run S7`
- [ ] Commit: `test(universe): S7 distributed split/merge/migrate integration tests`

### T11 — 4node-basic S7 demo + sdkgen updates

- [ ] `examples/4node-basic/main.go` add a synthetic-load knob that spawns N bots in a cell to drive the CPU metric up
- [ ] `examples/4node-basic/justfile` add `just s7-demo` recipe: starts coordinator + 2 nodes + gateway, triggers a split after 30s, renders the split visually via topology overlay
- [ ] Verify the 4node-basic web client updates seamlessly as splits happen (already wired via `OnTopologyChanged` from S6)
- [ ] Commit: `feat(4node-basic): S7 demo recipe + synthetic load generator`

### T12 — CLAUDE.md rewrite + verification pass

- [ ] Retire Tier 1/Tier 2/Tier 3 language throughout `CLAUDE.md`
- [ ] Document unified `CellTransfer` protocol in the "Server Meshing" section
- [ ] Document `cell migrate` command + graceful shutdown flow
- [ ] Document `PartitionConfig.AutoRebalance` defaulting off + how to enable
- [ ] Document locality weight in rendezvous
- [ ] Update auto-memory "Known Issues" and "Deferred Work" files if anything changes
- [ ] Full verification pass — see checklist below
- [ ] Commit: `docs(claude-md): S7 distributed splits/merges/migration`

---

## Verification checklist

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./...` all pass (without Postgres)
- [ ] `just test-pg` all pass (with Postgres, S5 regression check)
- [ ] `go test -count=1 -run S7 ./pkg/universe/` — all S7 integration tests pass
- [ ] `just build` produces `bin/server`
- [ ] `examples/4node-basic` `just s7-demo` shows a live split with visual overlay
- [ ] Admin `cell split <cellID>` from the coordinator console works with 2+ hosts; children distribute
- [ ] Admin `cell merge <cellID>` reverses it
- [ ] Admin `cell migrate <cellID> <hostID>` moves a cell; player inside stays connected
- [ ] SIGINT on a non-coordinator host drains its cells within 10s and exits cleanly
- [ ] `PartitionConfig.AutoRebalance = true` in a load test triggers at least one migration with no thrash
- [ ] `partition.go` is <300 lines (currently ~1100)
- [ ] `MsgTransfer` and `MsgArrivalConfirm` constants are gone from the codebase
- [ ] `Ghost` is a pure marker component with no fields
- [ ] Tier 1/2/3 language is gone from code comments and `CLAUDE.md`
- [ ] `Topology.Neighbors` rebuild is incremental (only touches affected cells)

---

## Out of scope (deliberately deferred)

- **HA coordinator** — single coordinator remains the control plane. Raft/etcd coordinator consensus is a future-work item per spec §11.
- **Cross-cluster entity identity** — `uint64 EntityGuid` for multi-cluster is future work.
- **UDP transport for native clients** — WebSocket only.
- **Load-based split trigger from distributed metrics** — the partition monitor's current trigger (single-host tick budget EWMA) is reused. A proper distributed load signal (aggregate per-cell CPU from all hosts) is a polish task for a later pass.
- **Entity clustering across host boundaries** — if two entities on different hosts interact (e.g. a bullet flying across a boundary), the existing cross-host handoff handles it one entity at a time. Batch-aware cross-host interactions are a future optimization.
- **Cell SPLIT in an already-split subtree** — we support splits of cells regardless of current depth, but the `MaxDepth` knob in `PartitionConfig` still gates how deep the tree can go. Default stays at depth 3.
- **Cell merge from different parents** — merges are always 4-sibling; no ad-hoc merge of cells that aren't quadtree siblings.

---

## Risk notes

- **Rendezvous + locality weight tuning.** The 15% locality bonus is a starting point. In production this will need tuning based on observed thrash — too low and cells scatter randomly on splits, too high and load imbalances persist. Instrument the rebalance loop to log locality-override decisions so operators can see the effect.

- **Timeout + rollback latency.** The orchestrator timeout defaults to 5s. A host that's slow but not dead (GC pause, Postgres stall) can trigger a rollback that wastes work. Tune based on observed P99 cell creation time.

- **Atomic commit granularity.** Every split/merge holds `c.mu.Lock()` for the commit step. With ~16 cells on a 4-host cluster this is ~ms. At higher cluster scales the lock-held time grows with the number of affected sessions in `sessionRoutes`. Future optimization: split `c.mu` into topology lock + routes lock.

- **Graceful shutdown deadlock.** If a host's SIGINT handler is waiting for `CellsDrained` but the coordinator is waiting for `CellTransferReady` from the same host (because the cells are being migrated through the same control stream), timing matters. Mitigation: `GracefulLeave` puts the host in a "drain mode" where it still responds to `CellTransfer` commands but refuses new cell assignments. Test explicitly in `TestS7GracefulShutdown`.

- **Partial split failures and ghost entities.** If the orchestrator times out and rolls back, the rolled-back target host may have already created the cell and populated entities. It must drop those entities cleanly and tear down the cell. `CellTransferAbort` (added in T1) carries the `request_id` for correlation.

- **Rebalance + manual `cell migrate` race.** If auto-rebalance decides to migrate cell X to host-b at the same moment an operator types `cell migrate X host-c`, one of them loses. The orchestrator's in-flight map serializes them: whichever gets the request_id assigned first wins; the other fails with "migration already in progress".

- **`Topology.Neighbors` rebuild correctness.** The incremental rebuild must produce the same result as a full rebuild. Property test: for random topologies, assert `RebuildNeighborsFor(all)` == `RebuildNeighbors()`.

- **`Ghost` field stripping blast radius.** Removing `Ghost.TTL` / `DestNodeID` / `Confirmed` requires finding every construction site. The audit found one set site in `auto_replicator.go:443` and the decay loop in `world_base.go:980-1016`. Expect a ~15-minute grep-and-fix pass across `pkg/` + `internal/game/`.

- **`partition.go` 60% reduction is a big delta.** The delete-heavy rewrite might regress a subtle behavior that wasn't test-covered. Mitigation: keep the existing single-host partition tests green throughout T5; add tests for anything that looks under-covered before deleting the old code.

---

## Sources

- [SpatialOS Runtime v2 architecture summary](https://ims.improbable.io/insights/new-spatialos-runtime-v2/) — authority migration, hysteresis, bridge architecture
- [SpatialOS load-balancing system update](https://ims.improbable.io/insights/spatialos-runtime-update-2-new-load-balancing-system/) — load balancer decides authority
- [EVE Online architecture on High Scalability](https://highscalability.com/eve-online-architecture/) — SOL blades, system-to-node assignment, locality preference
- [EVE Online — Introducing Time Dilation](https://www.eveonline.com/news/view/introducing-time-dilation-tidi) — backpressure via clock slowdown (future option)
- [Microsoft Orleans 8.2 — Grain Migration](https://github.com/dotnet/orleans/issues/7692) — live grain migration without state reload
- [Orleans 9 — Grain Placement](https://learn.microsoft.com/en-us/dotnet/orleans/grains/grain-placement) — resource-optimized placement, activation rebalancing vs repartitioning
- [Nakama architecture overview](https://heroiclabs.com/docs/nakama/getting-started/architecture/) — CRDT + gossip discovery, transparent message routing
- [Game Programming Patterns — Spatial Partition](https://gameprogrammingpatterns.com/spatial-partition.html) — quadtree vs grid vs BSP tradeoffs

---

## Approval gate

Before executing, the user reviews this plan and confirms:

1. **Approved as-is** → proceed to T1.
2. **Approved with changes** → list the changes, update the plan, then proceed.
3. **Defer or redesign** → revisit.

If approved, this plan closes the distributed-mesh roadmap (S1–S9 complete except for S8/S9 game-specific items that don't block S7).
