# Dual-Topology Test Harness (Stage 1 of Role-Separation Refactor)

**Status:** Design / Awaiting implementation plan
**Date:** 2026-04-18
**Stage:** 1 of 3 in the broader role-separation refactor
**Follow-ups (separate specs):** Stage 2 canonical ownership accessors + unified commit paths; Stage 3 `Coordinator` struct split

## Problem

Three recent bugs — cell migrate ownership drift (`snapshotOwnershipLocked` reading empty `cellToHostMap`), missing CellRelease on split for remote parents, and missing CellRelease on merge for remote donors — have shared the exact same root cause: code on the `Coordinator` path reaches into a local-only data structure (`cellToHostMap`, `CellOwner`, `c.Cells`) to answer "where is cell X?" — a question whose authoritative answer lives in `hostRegistry` when hosts are remote. The tests pass because `TestHosts` populates both maps. They fail in production whenever the operator runs `--mode=coordinator` + `--mode=host` in separate processes, because then `cellToHostMap` is empty on the coord side.

The `Coordinator` struct carries state belonging to multiple roles without any boundary enforcement. Nothing in the type system, the call sites, or the existing test suite prevents code from reading host-role fields on a pure-coordinator process. Fixes have been landing reactively, one bug at a time, with no structural signal that the next one is coming.

The goal of this design is to build the **first** and most load-bearing mitigation: a test harness that runs the ownership/commit tests under both topologies (colocated and distributed) inside the same test binary, turning this whole bug class into a CI-time signal instead of a production ticket.

## Non-goals (deliberate)

- No changes to production types (`Coordinator`, `Host`, accessor APIs). Stage 2 does that.
- No role struct split. Stage 3 does that.
- No subprocess / docker-based testing. Everything runs in one `go test` binary.
- No fault injection, chaos, or performance benchmarking.
- No public API surface. The fixture is test-only inside `pkg/universe`.
- Tests outside the 20-test target set keep running colocated-only.

## Architecture

One helper (`forEachTopology`) plus two fixture implementations behind one interface (`clusterFixture`), all living in `pkg/universe` as unexported test-only code:

```text
         test binary (one go test process)
         ┌─────────────────────────────────────────┐
         │   forEachTopology(t, cfg, body)         │
         │     │                                   │
         │     ├─ t.Run("colocated", ...)          │
         │     │    newColocatedFixture(t, cfg)    │
         │     │                                   │
         │     └─ t.Run("distributed", ...)        │
         │          newDistributedFixture(t, cfg)  │
         └─────────────────────────────────────────┘
                         │
          ┌──────────────┴──────────────┐
    Colocated                    Distributed
    ─────────                    ───────────
    1 Coordinator (RoleAll)      1 Coordinator (RoleCoordinator) on :0
    cells + ownership local      N Coordinators (RoleHost) dialing it
    matches today's              real gRPC MeshControl streams
    newMigrateTestCoord          ownership only in hostRegistry on coord
```

Both branches produce a `clusterFixture`, the handle test code interacts with. Internally, the colocated fixture holds one `*Coordinator`; the distributed fixture holds the coord-role `*Coordinator` plus a `map[string]*Coordinator` of the host-role processes.

The critical property: **tests never touch topology-specific fields directly**. Every assertion that used to read `coord.cellToHostMap[...]`, `coord.Cells[...]`, or `coord.Hosts[...].CellByID(...)` now goes through the fixture. The fixture's distributed implementation uses `HostForCellID` (the unified accessor) to answer ownership queries and reaches into the per-host Coordinator for Cell struct access, so both modes give the same answers for the same test logic.

## The fixture API

```go
type clusterFixture interface {
    // Coord returns the coord-role Coordinator — the one that owns the
    // orchestrator, session routes, and the partition monitor. Test code
    // drives BeginSplit/BeginMerge/BeginMigrate through Coord().orchestrator.
    // In colocated mode this is the only Coordinator; in distributed mode
    // it's the RoleCoordinator process.
    Coord() *Coordinator

    // HostIDs returns every host participating in the cluster, in
    // deterministic order. Colocated: the TestHosts list. Distributed:
    // the host-role processes the fixture spun up.
    HostIDs() []string

    // CellOwner returns the host ID currently owning the given cell key,
    // or "" if the cell isn't owned. Always resolved via HostForCellID so
    // the answer matches what production code would see. Works in both
    // topologies.
    CellOwner(cellKey string) string

    // HostOwnsCell returns true if the named host has an in-process Cell
    // for this key on its own side. Colocated: coord.Hosts[h] lookup.
    // Distributed: looks up the host's own Coordinator and checks its
    // local Hosts map.
    HostOwnsCell(hostID, cellKey string) bool

    // CellOn returns the in-process *Cell on the given host, or nil if
    // the host doesn't own it. Tests use this for execOnLoop and direct
    // ECS access. In distributed mode, the returned cell lives on a
    // different Coordinator than Coord() returns; ownership-level
    // assertions still go via CellOwner.
    CellOn(hostID, cellKey string) *Cell

    // WaitForCellOwner blocks until cellKey is owned by hostID, or ctx
    // expires. Needed because distributed mode receives ownership
    // asynchronously via CellReady messages; colocated is immediate.
    // Keeps tests the same regardless of topology.
    WaitForCellOwner(ctx context.Context, cellKey, hostID string) error
}
```

### Design decisions

- **`Coord()` is always the coord-role.** Tests call `fx.Coord().orchestrator.BeginMigrate(...)`. In colocated it's the same struct that owns cells; in distributed it's the one with the orchestrator wired.
- **`CellOn` is the escape hatch for game-loop ops.** `execOnLoop(t, srcCell, func() { spawnTestEntity(srcCell, ...) })` has to keep working. `CellOn` returns the real `*Cell` from whichever process owns it.
- **`WaitForCellOwner` absorbs async setup.** Colocated is synchronous; distributed waits for `CellReady` to propagate. Hiding the poll in the fixture keeps test bodies identical.
- **No `Hosts()` map exposure.** Deliberate — anything tests need about a host goes through `HostOwnsCell` / `CellOn`. Prevents the same bug class from sneaking back in through `fx.Hosts()["host-a"].CellByID(...)`.

## Topology implementations

### Colocated

Wraps the existing `newMigrateTestCoord` pattern:

```text
single Coordinator with Roles = {RoleCoordinator, RoleHost, RoleGateway}
  TestHosts = cfg.HostIDs              // uses existing multi-host in-process path
  grid populated by Build() round-robin
  cellToHostMap, Hosts, CellOwner all populated on the one struct
fixture stores: coord *Coordinator
```

All methods resolve against the single `*Coordinator`. `WaitForCellOwner` returns immediately. Zero wire cost.

### Distributed

```text
coord-role Coordinator
  Roles = {RoleCoordinator}
  ControlListen = ":0"
  no local cells, no host, no gateway
  Build() starts meshControlServer; grab listener port after Listen()

per host-role Coordinator (one per cfg.HostIDs entry)
  Roles = {RoleHost}
  CoordinatorAddr = coord's listener addr
  HostID = entry from cfg.HostIDs
  Build() dials coord via meshControlClient, sends RegisterHost

layout seeding (deterministic placement)
  fixture computes the same round-robin map the colocated path uses
  then drives it through real control plane:
    for each (cellKey, hostID):
      hostRegistry.AssignCell + dispatchCellAssign → host creates cell
      via Receive path → CellReady → coord updates hostRegistry
  fixture blocks (WaitForCellOwner loop) until all expected cells owned

fixture stores:
  coord     *Coordinator           // RoleCoordinator process
  hosts     map[string]*Coordinator // one per host ID
  listener  net.Listener           // for cleanup ordering
```

Why seed explicitly: colocated uses round-robin at Build time, tests were written assuming that deterministic placement. Seeding distributed through `AssignCell + CellReady` hits the real production code path for placement while producing the same final map, so tests stay identical.

`HostOwnsCell(hostID, key)` in distributed mode looks up `fx.hosts[hostID]` and calls its local `localHost().CellByCellID(...)`. `CellOn` returns that pointer. `CellOwner` goes through `fx.coord.HostForCellID(key)` — same production API.

### Lifecycle

- Shutdown order: host-role coords first (graceful leave over control stream), then listener close, then coord-role coord shutdown. `t.Cleanup` calls stack LIFO so registering in creation order gives correct teardown order.
- Ports: `:0` binding for every listener. Addresses captured post-`Listen()`.
- Timeouts: `WaitForCellOwner` + setup waits use `t.Deadline()` when set, else a 2s default. No arbitrary sleeps.
- Goroutine leak check: fixture's `t.Cleanup` waits on an internal `sync.WaitGroup` for up to 1s and fails the test if anything is still running.

## Test invocation

```go
func TestS7MigrateAcrossHosts(t *testing.T) {
    forEachTopology(t, FixtureConfig{
        CellsX: 2, CellsY: 2, CellSize: 1024,
        HostIDs: []string{"host-a", "host-b"},
    }, func(t *testing.T, fx clusterFixture) {
        srcCellID := CellID{X: 0, Y: 0}
        srcKey := MeshCellID(srcCellID)

        if fx.CellOwner(srcKey) != "host-a" {
            t.Fatalf("pre-migrate: cell %s on %q, want host-a",
                srcKey, fx.CellOwner(srcKey))
        }
        srcCell := fx.CellOn("host-a", srcKey)
        if srcCell == nil {
            t.Fatal("pre-migrate: no cell on host-a")
        }
        execOnLoop(t, srcCell, func() {
            spawnTestEntity(srcCell, 4242, 10, 20)
        })

        req, err := fx.Coord().orchestrator.BeginMigrate(srcCellID, "host-b")
        if err != nil { t.Fatalf("BeginMigrate: %v", err) }
        <-req.Done
        if req.Result != nil { t.Fatalf("req failed: %v", req.Result) }

        if fx.HostOwnsCell("host-a", srcKey) {
            t.Errorf("post-migrate: src host still owns cell %s", srcKey)
        }
        if !fx.HostOwnsCell("host-b", srcKey) {
            t.Errorf("post-migrate: dst host missing cell %s", srcKey)
        }
    })
}
```

Output: one subtest per topology.

```text
=== RUN   TestS7MigrateAcrossHosts/colocated
=== RUN   TestS7MigrateAcrossHosts/distributed
```

A failure in one subtest but not the other is the signal this entire harness exists to produce.

## Migration scope (the 20 target tests)

All currently use `newMigrateTestCoord` or the `all`-preset 2-host fixture. Each migration is ~5–10 line changes — mechanical substitution of peek assertions.

**s7 family (cell transfer protocol)** — where the recent bugs lived:

- `pkg/universe/s7_migrate_test.go`: `TestS7MigrateAcrossHosts`
- `pkg/universe/s7_migrate_epoch_test.go`: `TestMigrateEpochSourceCellReleased`
- `pkg/universe/s7_split_test.go`: split correctness tests (~3)
- `pkg/universe/s7_merge_test.go`: merge correctness tests (~3)
- `pkg/universe/s7_graceful_shutdown_test.go`: graceful drain (~2)
- `pkg/universe/s7_concurrent_test.go`: handoff-during-split (~2)

**s6 family (gateway + session handoff)**:

- `pkg/universe/s6_gateway_test.go`: `TestS6HandoffAcrossNodes`

**partition (dynamic splits/merges via monitor)**:

- `pkg/universe/partition_test.go`: split/merge orchestration tests (~4)

What migration **does not** change: test business logic, `execOnLoop` / `spawnTestEntity` helpers, pass/fail conditions for the colocated path.

## Acceptance criteria

1. `forEachTopology` + both fixtures land with unit tests of their own (fixture-level tests covering setup, teardown, and the API methods).
2. All 20 target tests are migrated to use the fixture.
3. CI is green in both the `/colocated` and `/distributed` subtest for every migrated test.
4. **Regression drill**: temporarily revert the recent `snapshotOwnershipLocked` fix on a scratch branch. Confirm the harness fails `/distributed` while `/colocated` still passes. Revert the revert. This proves the harness catches the actual bug class end-to-end.

## Risks and mitigations

- **Setup time per distributed test** (~20–50ms for bind + registration). Across 20 tests → ~1s CI delta. Accepted.
- **Flake surface from real gRPC** (bind/dial/registration timing under CI load). Mitigated by `WaitForCellOwner` with bounded timeouts, not arbitrary sleeps. If practical flakes appear we tune timeouts.
- **Duplicated layout logic** (round-robin in `Build()` vs. in fixture seeding). Small duplication; a test-level assertion compares the two maps to catch drift.

## Open questions

- Do any target tests assert on `sessionRoutes` entries in a way the current fixture API can't cover? If so, add a narrow `fx.SessionRoute(key)` accessor before migration rather than letting tests reach in directly. (Will verify during the planning phase.)

## Stage-2 followups (discovered during Stage 1 migration)

Each of these is a real production bug the harness surfaced. They must be addressed in Stage 2 before the corresponding distributed functionality is safe to ship:

1. **`BeginMigrate` / `BeginSplit` / `BeginMerge` `req.Done` semantic asymmetry.** In colocated mode, `req.Done` fires after synchronous `srcCell.Shutdown()` — so "migrate done" means "source teardown done". In distributed mode, the commit path fires `CellRelease` fire-and-forget over MeshControl, so `req.Done` closes before the source host has actually released anything. Callers of these APIs observe different post-conditions in the two modes. Fix: make the orchestrator's commit wait for `CellStopped` to arrive from every source host before closing `req.Done`, with a timeout + fallback so a dead host doesn't block indefinitely. This collapses `distributedFixture.WaitForCellReleased` to a no-op and removes the workaround pattern from migrated tests.

2. **Merge survivor rename never propagated to remote hosts (CRITICAL).** `applyMergeCommit` renames the survivor cell's `ID` / `Cell` fields and re-keys it in `Host.Cells` — but only when the survivor is in-process on the coord. When the survivor lives on a remote host, the coord's `c.Cells` lookup returns nil, the rename is skipped, and the remote host keeps the cell forever under its old sibling ID. Post-merge the cluster is in an inconsistent state: coord thinks the cell is the merged parent; host knows it as the old sibling. **Any production `cell merge` on a deployment where the survivor lands on a remote host silently corrupts state.** Fix: add a new `CellRename` MeshControl command dispatched by the orchestrator during merge commit; the host handler re-keys its `Host.Cells` entry and rewrites `cell.ID` / `cell.Cell` / `WorldBase.cell` via `Engine.RunOnLoop`. Discovered at Task 9 of the Stage-1 plan. Stage-1 `s7_merge_test.go` migration is deferred until this fix lands.

3. **Whatever else Tasks 10-13 surface.** Add notes here as they come up. Each additional finding validates the harness investment.

## Drill result (Task 14)

_(to be filled in after Task 14)_
