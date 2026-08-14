# Role Separation: Canonical Accessors + Unified Commit Paths + Struct Split

**Status:** Design / Awaiting implementation plan
**Date:** 2026-04-18
**Combines:** Stage 2 (canonical accessors + unified commit paths) and Stage 3 (struct split) of the role-separation refactor
**Builds on:** `2026-04-18-dual-topology-test-harness-design.md` (Stage 1)
**Depends on:** The dual-topology test harness (landed) — the safety net that validates every phase

## Problem

The `Coordinator` struct carries ~25 fields mixing responsibilities across three deployment roles (coordinator / host / gateway) with no boundary enforcement. When a role isn't active in a process, the corresponding fields sit empty but still visible — any code can read them, get the zero value, and silently take the wrong branch.

Stage 1 (the dual-topology test harness) surfaced three concrete bugs of this class:

1. **`req.Done` async-semantics asymmetry.** `applyMigrateCommit` / `applySplitCommit` / `applyMergeCommit` fire `CellRelease` fire-and-forget over MeshControl in distributed mode; `req.Done` closes before the source host's teardown lands. Colocated is synchronous (`srcCell.Shutdown()` direct call). Callers observe different post-conditions in the two topologies.
2. **Merge survivor rename not propagated to remote hosts (CRITICAL).** `applyMergeCommit` renames the survivor cell's `ID` / `Cell` fields only when the survivor is in-process on the coord. When the survivor lives on a remote host, the rename is silently skipped; the cluster ends in an inconsistent state (coord thinks `cell_0_0`; host knows it as `cell_d1_0_0`). Any production `cell merge` on a deployment where the survivor lands on a remote host silently corrupts state.
3. **`Topology.Neighbors` nil on pure-coord processes.** The topology map is only populated when the coord itself creates local cells. Pure `--mode=coordinator` deployments have an empty topology and can't run neighbor-based assertions or invariants.

Plus a fourth bug landed inline during Stage 1 Task 12 (`Gateway.reconcileRemotePeers` missing `applyPeerList(pl.Cells)` for embedded-gateway-no-local-host deployments) — documented here for completeness; already fixed.

The three open bugs are symptoms of one structural problem: there is no boundary between the role-specific subsystems. The fix is to introduce that boundary at the type level, collapse the local-vs-remote branching that keeps generating these bugs, and make the surfaced bugs disappear as natural consequences of the refactor rather than fighting them one by one.

## Goals

1. Eliminate the "god-object Coordinator" pattern. Replace with a composed `Process` struct holding role-scoped planes.
2. Eliminate the local-vs-remote branching in commit paths. One code path handles both topologies, via a `hostOps` abstraction that blocks on completion regardless of topology.
3. Fix all three open Stage-2 bugs via the refactor, not as standalone patches.
4. Drop the `Config.TestHosts` multi-host-in-one-process legacy (distributed tests use multi-Process-in-binary).
5. Rename `Coordinator` → `Process`, introduce `mmokit.New(Config)` as the canonical entry point.
6. Leave the Stage 1 harness as the safety net — every phase must keep it green.

## Non-goals

- No new features. Refactor + three bug fixes only.
- No game-world changes beyond mechanical renames (`*Coordinator` → `*Process`).
- No new tests beyond what the refactor itself requires. The Stage 1 harness already covers the behavior.
- No deprecation shims / backward-compat aliases. Rename / delete directly; update all callers.
- No UDP-specific changes, no replication-system changes, no networking protocol changes (the two new MeshControl messages are additive).

## End-state architecture

```text
┌───────────────────────────────────────────────────────────────┐
│  *mmokit.Process  (re-exported from *universe.Process)         │
│  Constructor: mmokit.New(Config) returns *Process              │
│                                                                │
│  ┌─────────────────┐   ┌─────────────────┐   ┌───────────────┐│
│  │  *ControlPlane  │   │      *Host      │   │    *Gateway   ││
│  │    (always)     │   │  (if RoleHost)  │   │(if RoleGateway││
│  │                 │   │                 │   │               ││
│  │ hostRegistry    │   │ cells           │   │ httpServer    ││
│  │ gatewayRegistry │   │ network         │   │ loginSvc      ││
│  │ orchestrator    │   │ netIDAlloc      │   │ sessionRoutes ││
│  │ topology        │   │ worldFactory    │   │ spawnResolver ││
│  │ partState       │   │ systemDefs      │   │               ││
│  │ controlServer   │   │ executor        │   │               ││
│  │ controlClient   │   │ vcm             │   │               ││
│  │ assignEngine    │   │                 │   │               ││
│  └─────────────────┘   └─────────────────┘   └───────────────┘│
│                                                                │
│  shared on Process: cfg, log, connMgr, coordEpoch              │
└───────────────────────────────────────────────────────────────┘
```

### Naming

- `ControlPlane` keeps the suffix because "Control" alone is too generic.
- `Host` and `Gateway` reuse the existing type names — they already describe themselves. The existing structs expand to hold what used to live on `Coordinator`.
- "All hosts in the cluster" still lives in `ControlPlane.hostRegistry` as `RemoteHost` entries — no collision with `Host` (which is always "this process's local host").

### Role presence rules

- `*ControlPlane` always exists. Every process has control-plane state — even bare `--mode=host` needs `controlClient` to talk to its coordinator.
- `*Host` is nil unless `RoleHost` is active.
- `*Gateway` is nil unless `RoleGateway` is active.
- `Config.TestHosts` is **deleted** — multi-host testing becomes multi-Process-in-binary (which the Stage 1 distributed fixture already handles).

### Cross-plane state

Fields that are genuinely shared by multiple planes live on `Process` directly:

- `cfg` (Config)
- `log` (Logger)
- `connMgr` (ConnManager — relevant for Gateway; HostPlane doesn't own client connections)
- `coordEpoch` (fencing token, referenced by both ControlPlane and Gateway)

### User-facing API (unchanged shape, renamed symbols)

```go
p := mmokit.New(mmokit.Config{...})
p.SetWorld(NewMyWorld)       // routes to p.Host
p.AddSystem("Physics", ...)  // routes to p.Host
p.SetPlayerRouter(...)       // routes to p.ControlPlane
p.SetConsole(...)            // routes to both (ControlPlane owns console; Host registers entity builtins)
p.Start(ctx)                 // orchestrates all three planes
```

Game code is identical in shape — `*Coordinator` becomes `*Process`, one symbol. No plane navigation required; setup methods on `*Process` delegate internally.

### Internal code (compile-time role enforcement)

Functions inside `pkg/universe/` take the specific plane(s) they need:

```go
// Old:  func (c *Coordinator) applyMigrateCommit(req *CellTransferRequest)
// New:  func applyMigrateCommit(cp *ControlPlane, req *CellTransferRequest)
```

Pure-coord code paths get `*ControlPlane`. Cannot reach into `*Host` or `*Gateway` — those pointers aren't in scope. The type system now enforces "pure coord code can't peek host-local maps" — the exact bug class Stage 1 kept catching.

Cross-plane calls go through well-defined interfaces (see `hostOps` below). No more `if c.Hosts[h] != nil` guards scattered through commit paths.

## Canonical accessor API

### ControlPlane (cluster-wide ownership queries)

Replaces today's `coord.cellToHostMap[...]` and `coord.Hosts[...]` peeks.

```go
// OwnerOf returns the host currently owning cellKey.
// Unified view: consults hostRegistry (authoritative in distributed)
// and falls back to the local cellToHostMap cache if the registry is
// empty (unit-test fixtures without a full control server).
func (c *ControlPlane) OwnerOf(cellKey string) (hostID string, ok bool)

// AllCells iterates every (cellKey, hostID) pair known to the
// control plane. Used by admin commands (`cell list`) and PeerList
// broadcasts.
func (c *ControlPlane) AllCells() iter.Seq2[string, string]

// CellsOwnedBy iterates cell keys currently assigned to hostID.
// Empty seq if the host is unknown.
func (c *ControlPlane) CellsOwnedBy(hostID string) iter.Seq[string]

// Topology returns the ControlPlane's neighbor topology. Always
// non-nil after Build(). Automatically maintained in response to
// hostRegistry.AssignCell / ReleaseCell events.
func (c *ControlPlane) Topology() *Topology

// Orchestrator returns the cell-transfer orchestrator. Admin commands
// and the partition monitor drive split/merge/migrate through it.
func (c *ControlPlane) Orchestrator() *cellTransferOrchestrator
```

### Host (local cell state + operations)

Replaces `coord.Cells[key]`, `coord.Hosts[h].CellByID(...)`, and scattered `srcCell.Shutdown()` / direct ECS peeks.

```go
// ID returns this host's stable ID (e.g. "host-a").
func (h *Host) ID() string

// Cells iterates every local cell currently running on this host.
func (h *Host) Cells() iter.Seq[*Cell]

// Cell returns the local *Cell for cellKey, or nil if this host
// doesn't own it.
func (h *Host) Cell(cellKey string) *Cell

// OwnsCell is a bool-returning convenience for Cell(key) != nil.
func (h *Host) OwnsCell(cellKey string) bool

// ReleaseCell shuts down the local cell and blocks until shutdown
// completes (game loop drained, netID range released). Error if the
// host doesn't own cellKey or if shutdown exceeds ctx deadline.
func (h *Host) ReleaseCell(ctx context.Context, cellKey string) error

// StartCell creates a local *Cell for the given CellID and blocks
// until its game loop is running and CellReady has been reported.
// Error if the cell already exists or if creation exceeds ctx deadline.
func (h *Host) StartCell(ctx context.Context, cellID CellID) (*Cell, error)

// RenameCell rekeys a cell from one string ID to another, running the
// rename on the cell's own game loop so concurrent reads don't race.
// Blocks until the rename is visible to PostSystems. Used by merge
// commit when renaming the survivor sibling to the parent ID.
func (h *Host) RenameCell(ctx context.Context, from, to string) error
```

### Private fields

After Phase 2 of the migration, the raw maps (`cells`, `Hosts`, `cellToHostMap`) are **private fields** on their owning plane. They cannot be accessed from outside the plane — nobody can reach around the accessor API.

## Unified commit paths (B2: host-method abstraction)

### `hostOps` interface

The key new primitive. `ControlPlane.hostProxy(hostID string)` returns a `hostOps` implementation routed appropriately based on where the host lives:

```go
type hostOps interface {
    ReleaseCell(ctx context.Context, cellKey string) error
    StartCell(ctx context.Context, cellID CellID) error
    RenameCell(ctx context.Context, from, to string) error
}

func (cp *ControlPlane) hostProxy(hostID string) hostOps
```

- **Local impl:** wraps `*Host` — direct method calls. Synchronous, blocks on completion trivially (the game loop runs the teardown/rename/create).
- **Remote impl:** dispatches MeshControl messages to the named host; waits for `HostOpAck` matching the outgoing req_id. Same blocking semantics as local; error if ctx deadline fires before the ack arrives.

Both implementations share the same interface, same error shape, same blocking contract. **Commit code has zero branches.**

### Commit path after refactor

```go
func applyMigrateCommit(cp *ControlPlane, req *CellTransferRequest) error {
    srcHost := req.commands[0].SrcHostID  // authoritative via BeginMigrate
    destHost := req.mutation.add[srcCellKey]

    // Ownership flip (in-memory, synchronous).
    cp.updateOwnership(srcCellKey, destHost)

    // Session route remapping + UpstreamSwitch dispatch (unchanged).
    cp.remapSessionRoutes(...)

    // Source teardown — same call works for local or remote source.
    if err := cp.hostProxy(srcHost).ReleaseCell(ctx, srcCellKey); err != nil {
        return err
    }

    cp.broadcastPeerListIfReady()
    return nil
}
```

Compare with today's ~40-line `applyMigrateCommit` with its `srcCell != nil ? srcCell.Shutdown() : sendCellRelease(...)` branch. The branch is gone.

### What this fixes

1. **req.Done async asymmetry** — `hostOps.ReleaseCell` blocks until teardown completes (local: direct return; remote: MeshControl ack). By the time the commit helper returns, the source host has actually released the cell. `req.Done` fires with the same post-condition in both topologies. `distributedFixture.WaitForCellReleased` becomes a no-op (but stays in the fixture as defense in depth).
2. **Merge survivor rename** — `RenameCell` is a host op defined for both local and remote. Remote impl uses the new `CellRename` MeshControl message. `applyMergeCommit`'s old code path (in-process-only rename + skipped-on-remote) is replaced with `cp.hostProxy(survivorHost).RenameCell(ctx, survivorKey, parentKey)`, which works correctly in both topologies.
3. **Source teardown async** — same mechanism as #1.

## New MeshControl messages

Two additive proto messages support the remote `hostOps` impl.

```proto
// coord → host: shut down this cell and ack.
// Replaces today's fire-and-forget CellRelease.
message CellReleaseAndAck {
  string cell_id = 1;
  uint64 req_id = 2;
}

// coord → host: rename a cell's identity. Used by merge commit when
// the survivor sibling lives on a remote host.
message CellRename {
  string from_cell_id = 1;
  string to_cell_id   = 2;
  uint64 req_id       = 3;
}

// host → coord: ack for CellReleaseAndAck or CellRename.
message HostOpAck {
  uint64 req_id = 1;
  bool   ok     = 2;
  string error  = 3;
}
```

### Req-id routing

`ControlPlane` maintains `pendingHostOps map[uint64]chan HostOpAck` (guarded by mutex). Remote `hostOps` impl:

1. Allocate req_id via atomic counter.
2. Register one-shot channel: `pendingHostOps[req_id] = make(chan HostOpAck, 1)`.
3. Dispatch the MeshControl message with req_id.
4. Wait on channel with caller's ctx.
5. On ack or ctx-done: remove entry, return result.

`handleHostControl` recv loop routes `HostOpAck` by req_id to the waiter channel. If the host disconnects with pending ops, the control-stream close defer sends failures to every outstanding channel.

### Host-side handlers

Mirror the existing `releaseCellOnNode` pattern:

```go
case *meshpb.CoordMessage_CellReleaseAndAck:
    req := v.CellReleaseAndAck
    go func() {
        err := c.releaseCellOnNode(req.CellId)
        c.controlClient.sendHostOpAck(req.ReqId, err)
    }()

case *meshpb.CoordMessage_CellRename:
    req := v.CellRename
    go func() {
        err := c.renameCellOnNode(req.FromCellId, req.ToCellId)
        c.controlClient.sendHostOpAck(req.ReqId, err)
    }()
```

`renameCellOnNode` runs the rename via `cell.Engine.RunOnLoop` so concurrent PostSystems reads of `cell.ID` / `cell.Cell` don't race:

```go
func (c *Coordinator) renameCellOnNode(from, to string) error {
    c.mu.Lock()
    cell, ok := c.Host().Cell(from)
    // re-key on host + coord maps under lock
    // ...
    c.mu.Unlock()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    return cell.Engine.RunOnLoop(ctx, func() error {
        cell.ID = to
        cell.Cell = newCellID
        cell.World.UpdateCellBounds(newCellID, coords.CellSize)
        return nil
    })
}
```

Timeout handling: caller's ctx deadline is the source of truth. If the remote host is unreachable or the game loop is deadlocked, the MeshControl ack never arrives, the caller's ctx fires, and the commit rolls back — same failure mode as any orchestrator timeout today.

## Topology maintenance on ControlPlane

`Topology` moves from today's `Coordinator` field to `ControlPlane.topology`. Always non-nil after Build().

### Event-driven maintenance

`hostRegistry.AssignCell(hostID, cellID)` and `hostRegistry.ReleaseCell(hostID, cellID)` fire callbacks on the ControlPlane:

```go
func (cp *ControlPlane) onCellOwnershipChanged(cellID string, action ownershipAction) {
    cp.mu.Lock()
    defer cp.mu.Unlock()
    cp.topology.RebuildNeighborsFor([]CellID{ParseCellID(cellID)}, coords.CellSize)
}
```

`ComputeTopology` still runs once at Build() to seed the initial map. `RebuildNeighborsFor` runs on every assignment/release to keep neighbors fresh. Split / merge commits (which restructure the cell tree, not just assignments) call `UpdateAfterSplit` / `UpdateAfterMerge` explicitly, as they do today.

Fixes the Stage 1 Task 13 `TestSplitCell_TopologyCorrect/distributed` skip.

## Migration path

Land in 8 sequential phases, each compile-green + test-green on `main`. Every phase ends with `go test ./pkg/... -count=1` passing.

### Phase 1 — Plane structs as internal holders

Create `ControlPlane` as a new struct. Expand existing `Host` and `Gateway` structs with fields the refactor will add. `Coordinator` keeps its current shape but gains `coord.Control`, `coord.Host`, `coord.Gateway` accessor pointers (fields on `Coordinator`, initialized in `Build()`). All existing callers still work — raw fields stay on Coordinator too, temporarily. Pure groundwork, zero behavioral change.

### Phase 2 — Canonical accessors + peek migration

Add `OwnerOf` / `Cell()` / `OwnsCell()` etc. on the planes. Migrate every direct map access in production and test code to accessors. Migration happens one caller type at a time, each as a separate commit (keeps each commit reviewable). Once every caller uses accessors, make the raw maps private (unexport). This is the "bug class prevention" step — after this, nobody can reach into `coord.cellToHostMap` because it isn't visible.

### Phase 3 — `hostOps` + unified commit for migrate and split

Implement `hostOps` interface. Local impl (direct method calls on `*Host`) first — single-process tests validate it. Then remote impl with req_id/ack routing. Collapse `applyMigrateCommit` and `applySplitCommit` to use `hostProxy(h).ReleaseCell(ctx, k)` — one branch removed per commit. After this phase, the `req.Done` async asymmetry is fixed and `WaitForCellReleased` becomes a no-op in `distributedFixture` (kept in place for defense).

### Phase 4 — CellRename wire + unified merge

Add `CellRename` + `HostOpAck` to `proto/meshpb/mesh.proto`. Regen Go + TS (TS is unused for these but keeps codegen clean). Coord-side dispatcher + host-side handler. Wire into `hostOps.RenameCell`. Collapse `applyMergeCommit`. Re-enable Task 9 merge test's `/distributed` subtest (remove the skip line).

### Phase 5 — Topology onto ControlPlane

Move the `Topology` struct. Hook recomputation into `hostRegistry.AssignCell` / `ReleaseCell` via a callback wired during `Build()`. Remove the Task 13 `TestSplitCell_TopologyCorrect/distributed` skip.

### Phase 6 — Drop `Config.TestHosts`

Delete the multi-host-in-one-process path in `Build()`. Colocated fixture becomes single-`*Process` with `RoleAll`. Any residual tests that still used TestHosts at this point get updated to use the distributed fixture (should be zero after Stage 1's migrations; verify via grep). Simplifies `Process` shape (one `*Host`, not `[]*Host`).

### Phase 7 — Rename `Coordinator` → `Process`, introduce `mmokit.New()`

Large mechanical rename across `pkg/universe`, `pkg/mmokit`, `internal/`, `examples/`. Ship as one commit (mechanical, low-conflict, can't partially land). Update mmokit facade: `mmokit.New(Config) *mmokit.Process` replaces `mmokit.NewCoordinator(Config) *mmokit.Coordinator`. Games and the bot client get the symbol rename.

### Phase 8 — Delete the `Coordinator` wrapper

Post-rename, `Coordinator` is a type alias / wrapper. Inline everything into `Process` + plane structs; delete the wrapper. All callers now take `*Process` or a specific `*ControlPlane` / `*Host` / `*Gateway`. The old god-object is gone.

### Phase 9 (implicit) — Re-enable skipped tests

As of Phase 4 the merge test un-skips. As of Phase 5 the topology test un-skips. No new work — these fall out.

## Safety net: the Stage 1 harness

Every phase keeps the dual-topology harness passing. Each phase's implementation plan includes:

1. Run `go test ./pkg/universe/ -count=1 -timeout 600s` after every commit.
2. Run `go test ./pkg/universe/ -count=2 -timeout 900s` before finishing each phase (stability check).
3. If a previously-passing test starts failing, treat it as a regression and debug before proceeding. Do NOT add new fixture workarounds — the refactor should shrink the workaround surface, not grow it.

## Acceptance criteria

1. All 8 phases landed on `main`.
2. Full `go test ./pkg/...` suite green.
3. `Coordinator` type no longer exists. `Process` is the single top-level type.
4. `mmokit.New(Config) *mmokit.Process` is the public entry point.
5. Task 9's s7_merge_test.go runs in both `/colocated` and `/distributed` (the skip is deleted).
6. Task 13's `TestSplitCell_TopologyCorrect/distributed` runs (the skip is deleted).
7. `distributedFixture.WaitForCellReleased` still exists (defense in depth) but no test relies on its polling behavior — a one-shot read succeeds.
8. `grep -r cellToHostMap ./pkg/universe/` returns only unexported-field references inside `ControlPlane`; no external callers.
9. `grep -r 'coord.Hosts\[' ./pkg/universe/` returns zero hits.
10. `grep -r 'Config.TestHosts' ./` returns zero hits (field and all call sites deleted).

## Risks and mitigations

- **Long-running refactor touches hundreds of call sites.** Mitigated by the 8-phase staging — each phase is independently reviewable and reversible. If any phase becomes unexpectedly expensive, we can pause between phases without blocking main.
- **Phase 7 rename is a large commit.** Mechanical; risk is merge conflicts if parallel work is ongoing. Mitigated by staging Phase 7 during a quiet window and landing it in one shot.
- **New wire messages (CellRename, CellReleaseAndAck, HostOpAck) are additive but still a protocol change.** Mitigated by no-backward-compat preference — every client in the tree rebuilds with the new protos. No cross-version compatibility concerns in this project.
- **Topology recomputation on every AssignCell/ReleaseCell could be hot.** Mitigated by `RebuildNeighborsFor` scoping to the affected cells only. At 4-16 cells per deployment, this is nanoseconds.
- **`hostProxy`'s pending-ack map is new state that could leak on host disconnect.** Mitigated by the control-stream close defer: it fails every outstanding ack channel for that host, unblocking waiters with a clear error.

## Stage 1 followup status

Of the three Stage-1 TODOs documented in `2026-04-18-dual-topology-test-harness-design.md`:

1. `req.Done` asymmetry → fixed in Phase 3 of this spec.
2. Merge survivor rename → fixed in Phase 4 of this spec.
3. Coord `Topology.Neighbors` maintenance → fixed in Phase 5 of this spec.

After this spec lands, the Stage 1 spec's "Stage-2 followups" section can be marked resolved.

## Completion (2026-04-19)

All 8 phases landed on main. Full suite green 3× under `-count`. Stage-1 followups 1, 2, 3 resolved. Acceptance criteria verified via grep audit:

- `grep -r cellToHostMap pkg/universe/` — only on `ControlPlane` (field + accessors + tests pre-seeding fixture state).
- `grep -r coord.Hosts\[` — only in tests and safe `hostObj, ok := ...` membership-check pattern (Task 2.4 audit).
- `grep -r TestHosts` — empty.

Coordinator is gone. Process + ControlPlane + Host + Gateway is the final shape. mmokit.New(Config) is the canonical entry point.

Unexpected discoveries surfaced during implementation (resolved in-phase):

- **hostProxy's `localHostRef` needed to be a map** (`localHostsRef`), not a single ref. Surfaced by `TestS7ConcurrentHandoffDuringSplit` after Task 3.4/3.5. Fixed in commit `12844a4` along with the `OnReady` commit-goroutine dispatch that resolved the MeshControl-stream deadlock.
- **Phase 7 rename was consolidated into one commit** rather than three. Cross-package dependencies meant any intermediate state broke `go vet ./...`. 71 files, ±342 lines balanced.
- **Phase 6 ballooned into 4 sub-tasks (6.2a/6.2b/6.2c/6.3)** to handle the fixture rewrite, bypasser-test migration, 4node-basic e2e migration to multi-process, and final field deletion separately.
