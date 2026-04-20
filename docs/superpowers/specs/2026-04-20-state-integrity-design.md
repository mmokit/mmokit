# Spec 2 — State Integrity

**Date:** 2026-04-20
**Status:** Design

## Purpose

Make the classes of bugs this session kept producing **structurally impossible to recur**, and turn the server's commit paths into a queryable, auditable surface that operators and developers can reason about at any time in dev or prod.

Four categories of bugs motivated this spec:

1. **Ordering bugs** — `host.Cells` pre-removed before `ReleaseCell` lookup (zombie cells). `c.CellOwner` not updated before `computeRewireDirectivesLocked` (empty neighbor maps post-merge).
2. **Duplication bugs** — `drainDonorResidualsToSurvivor` re-serialized every donor entity without checking for netID collisions on the survivor.
3. **Silent-failure bugs** — `hostProxy.ReleaseCell` returning "unknown cell" was logged-and-ignored; parent cell's game loop leaked for the entire session.
4. **Ad-hoc state-shape bugs** — two ECS entities briefly coexisting with the same netID in different states (Live + Replica, Shadow + Live), each bug patched with a one-site check rather than a general invariant.

The common root: commit paths (`applySplitCommit`, `applyMergeCommit`, `applyMigrateCommit`) are imperative sequences of mutations on multiple shared data structures, with implicit preconditions about state at each step and no runtime validation that those preconditions hold. Every bug we fixed was either an ordering violation or an invariant violation; none was detected at the point of violation.

## Architectural principles (enforced by this spec)

- **Bugs get caught at the point of violation, not at the downstream symptom.** Invariants make wrong states crash loud (in dev) or loud-and-observable (in prod) the moment they arise.
- **Commit sequences are values, not code.** The executor interprets plans; plans are structured data you can read, log, and inspect. Imperative sequences with implicit ordering are replaced by explicit step lists.
- **Major topology events are a first-class operational surface.** An append-only ring of structured events is queryable from the console at any time, in both dev and prod. This is not "debug output you turn on after a bug" — it is always on and always interrogable.
- **One source of truth per netID per cell.** The Shadow/Replica/Live triple-state confusion that nearly-bit us in multiple ways gets collapsed into a single per-cell index with explicit transition semantics.
- **Framing X, not Y.** This spec layers safety rails on top of the current imperative commit paths — it does *not* rebuild them as declarative transactions with compensation/rollback (that would be Framing Y, explicitly deferred per project decision).

## Architecture overview

Four layered components, each building on the ones below.

```
┌───────────────────────────────────────────────────────────┐
│  ENTITY-TRANSFER STATE MACHINE                            │
│  Per-cell netIDIndex — one source of truth per netID per  │
│  cell, with explicit {Live, Shadow, Replica} states and   │
│  a documented transition policy table. Feature-flagged    │
│  (StrictNetIDIndex) for staged rollout.                   │
├───────────────────────────────────────────────────────────┤
│  COMMIT PLAN STRUCTS                                      │
│  applySplitCommit / applyMergeCommit / applyMigrateCommit │
│  refactored from imperative functions into CommitPlans    │
│  (ordered PlanSteps) interpreted by a single executor.    │
│  Behavior-preserving refactor (same outputs, explicit     │
│  sequence).                                               │
├───────────────────────────────────────────────────────────┤
│  EVENT-SOURCED COMMIT LOG                                 │
│  In-memory ring (1024 events) of every plan step emitted  │
│  by the executor. Queryable from the console              │
│  (`commit log ...`), streamable via the existing logger   │
│  (`log events:split`), and exposed over HTTP for          │
│  out-of-process tooling.                                  │
├───────────────────────────────────────────────────────────┤
│  INVARIANT FRAMEWORK                                      │
│  Typed predicates over Process state. Run between plan    │
│  steps and at commit boundaries. Panic in dev/tests,      │
│  log+metric in prod. The preventative mechanism.          │
└───────────────────────────────────────────────────────────┘
```

Invariants are the preventative layer. Plans give the invariants clean insertion points and make the sequence inspectable. The event log records what happened for retrospective analysis. The state machine closes one specific class of net-ID-shape bugs that patches didn't fully address.

## Component 1 — Invariant framework

### Structure

```go
// New package: pkg/universe/integrity.go (or equivalent)

type Invariant struct {
    Name  string
    Check func(c *Process) error  // nil = holds, non-nil = violation
}

type InvariantMode uint8
const (
    InvariantOff   InvariantMode = iota
    InvariantLog                  // log + metric, continue execution
    InvariantPanic                // log + metric + panic (dev/test default)
)

func (c *Process) CheckInvariants(invs []Invariant, contextMsg string) {
    if c.invariantMode == InvariantOff {
        return
    }
    for _, inv := range invs {
        if err := inv.Check(c); err != nil {
            c.commitLog.Append(CommitEvent{
                Kind:    EventInvariantViolation,
                Step:    inv.Name,
                Error:   err.Error(),
                Context: map[string]string{"where": contextMsg},
            })
            c.Metrics.InvariantViolations.WithLabels(inv.Name).Inc()
            msg := fmt.Sprintf("invariant %q violated during %s: %v",
                inv.Name, contextMsg, err)
            c.Log.Log(CatInvariant, msg)
            if c.invariantMode == InvariantPanic {
                panic(msg)
            }
        }
    }
}
```

### Invariants in the initial set

Tightly scoped to the bug classes we observed:

| Name | Check |
|---|---|
| `coord-maps-consistent` | For each `k ∈ c.Cells`: `c.CellOwner[c.Cells[k].Cell] == k`. And vice versa. |
| `host-ownership-matches-coord` | For each `(cellID, hostID)` in `c.CellOwner ↔ cellToHostMap`: if `hostID` names a local host, `c.Hosts[hostID].Cells[cellID]` must exist. Skipped for remote hosts (coord doesn't hold their internal state). |
| `topology-neighbors-owned` | For each `cellID ∈ Topology.Neighbors`: `c.CellOwner[cellID]` must be non-empty. |
| `no-duplicate-presence-per-cell` | For each cell, each netID appears in at most one slot of the `netIDIndex` (Component 4). |
| `session-route-host-live` | For each `route ∈ sessionRoutes`: `route.HostID` is either empty or registered in `hostRegistry`. |

The set starts at five. Each is a straight map/slice walk with no subtle semantics. Adding invariants is a one-function task; the framework doesn't impose a ceiling. The symbolic name `defaultInvariants` used throughout this spec refers to this five-invariant set.

### Where invariants run

1. **Between plan steps** (primary use). The commit executor calls `CheckInvariants` after each step, labeled with that step's name. This catches ordering bugs immediately.
2. **At commit entry and exit.** Defensive sanity check that commits don't arrive or leave in a broken state.
3. **In tests.** Fixture default forces `InvariantPanic`. Any regression reaching an inconsistent state fails loudly in CI.

Not run in the hot replication path — invariants are O(total-entities) in the worst case and belong at topology-event boundaries.

### Mode config

Per-process config, `Config.InvariantMode`:
- Tests default: `Panic`.
- Dev default: `Panic`.
- Prod default: `Log`.

Operators can flip prod to `Panic` in canary shards for fail-loud rollouts.

### Testing

Unit test per invariant: construct a deliberately-broken `Process` (e.g., `c.CellOwner[X] = "host-a"` but `c.Hosts["host-a"].Cells` lacks X) and assert the specific invariant's `Check` returns an error.

All existing tests run with `InvariantPanic` via fixture default.

## Component 2 — Commit plan structs

### Shape

A commit is a value — a `CommitPlan` struct listing ordered steps — executed by a single executor function. **No functional behavior change vs today**; the imperative sequence becomes explicit.

```go
type CommitKind uint8
const (
    CommitSplit CommitKind = iota
    CommitMerge
    CommitMigrate
)

type CommitPlan struct {
    ID    uint64             // unique, matches CellTransferRequest.ID
    Kind  CommitKind
    Req   *CellTransferRequest
    Steps []PlanStep
    Ctx   *CommitContext     // state shared across steps
}

type PlanStep struct {
    Name string
    Run  func(c *Process, ctx *CommitContext) error
    // Invariants checked after this step. Empty = defaultInvariants for Kind.
    Invariants []Invariant
}

// CommitContext replaces the bag of local variables that each
// applyXxxCommit function threads manually today.
type CommitContext struct {
    // Common
    PreOwnership map[string]string
    Mutation     topologyMutation

    // Split-specific
    ParentKey  string
    Children   [4]CellID
    ParentCell *Cell

    // Merge-specific
    SurvivorKey string
    DonorIDs    []string
    DonorCells  []*Cell
    Survivor    *Cell

    // Migrate-specific
    SrcCellKey string
    SrcHost    string
    DestHost   string
    SrcCell    *Cell
}
```

### The executor

```go
func (c *Process) ExecuteCommitPlan(plan *CommitPlan) error {
    c.commitLog.Append(CommitEvent{
        CommitID: plan.ID, Kind: plan.Kind.Event(), Step: "begin",
        Timestamp: time.Now(),
    })
    c.CheckInvariants(defaultInvariants,
        fmt.Sprintf("commit %d entry", plan.ID))

    for _, step := range plan.Steps {
        start := time.Now()
        err := step.Run(c, plan.Ctx)
        dur := time.Since(start)

        c.commitLog.Append(CommitEvent{
            CommitID: plan.ID, Kind: plan.Kind.Event(), Step: step.Name,
            Timestamp: start, DurationMs: dur.Milliseconds(),
            Success: err == nil, Error: errString(err),
        })

        if err != nil {
            return fmt.Errorf("commit %d step %q: %w",
                plan.ID, step.Name, err)
        }

        invs := step.Invariants
        if len(invs) == 0 {
            invs = defaultInvariants
        }
        c.CheckInvariants(invs,
            fmt.Sprintf("commit %d after %s", plan.ID, step.Name))
    }

    c.CheckInvariants(defaultInvariants,
        fmt.Sprintf("commit %d exit", plan.ID))
    c.commitLog.Append(CommitEvent{
        CommitID: plan.ID, Kind: plan.Kind.Event(), Step: "end",
        Timestamp: time.Now(),
    })
    return nil
}
```

### Plan builders

One per commit kind — pure functions producing plans from requests. No mutation in the builder; that's the executor's job.

```go
func buildSplitPlan(req *CellTransferRequest) *CommitPlan {
    ctx := &CommitContext{ /* derived from req */ }
    return &CommitPlan{
        ID: req.ID, Kind: CommitSplit, Req: req, Ctx: ctx,
        Steps: []PlanStep{
            {Name: "snapshot-pre-ownership",    Run: stepSnapshotPreOwnership},
            {Name: "apply-cell-to-host-map",    Run: stepApplyCellToHostMap},
            {Name: "detach-parent-from-coord",  Run: stepDetachParentFromCoord},
            {Name: "update-topology",           Run: stepUpdateTopologyAfterSplit},
            {Name: "compute-rewire-directives", Run: stepComputeRewireDirectives},
            {Name: "remap-sessions",            Run: stepRemapSessionsForSplit},
            {Name: "apply-registry-delta",      Run: stepApplyRegistryDelta},
            {Name: "release-parent-host",       Run: stepReleaseParentViaHostProxy},
            {Name: "apply-rewire-directives",   Run: stepApplyRewireDirectives},
            {Name: "prime-cooldowns",           Run: stepPrimeCooldowns},
            {Name: "broadcast-peer-list",       Run: stepBroadcastPeerList},
        },
    }
}
```

Analogous `buildMergePlan` and `buildMigratePlan`. Step lists differ; the executor is shared.

### Refactor boundary

Every `stepXxx` function is a pure lift of a segment of today's `applyXxxCommit` body. **No logic changes, just relocation.** Git diff is large but mechanical. The existing S7 suite is the regression guard.

### Benefits earned

- Step names match event-log lines — when a bug hits, the log shows the last-good step and the bad one.
- Invariants attach between steps without cluttering function bodies.
- Future commit kinds (e.g., "cell hibernate") are mechanical: write a builder, reuse the executor.

## Component 3 — Event-sourced commit log

First-class operational surface, not just a debug aid. Major topology events stay in a queryable ring in dev and prod; operators introspect cluster history directly.

### Event schema

```go
type EventKind uint8
const (
    EventCommitSplit EventKind = iota
    EventCommitMerge
    EventCommitMigrate
    EventInvariantViolation
    EventHostJoin
    EventHostLeave
    EventSessionRouteRemap
)

type CommitEvent struct {
    SeqNo      uint64
    Timestamp  time.Time
    CommitID   uint64            // 0 for non-commit events
    Kind       EventKind
    Step       string            // plan step name, or "begin"/"end"
    Success    bool
    DurationMs int64
    Affected   []string          // cell IDs touched
    HostIDs    []string           // hosts involved
    Error      string             // empty on success
    Context    map[string]string  // step-specific details
}
```

Not every log line is an event. Major events only:

- Commit plan begin / each step / end (from the executor).
- Invariant violations.
- Host registry changes (join / leave / crash).
- Session-route bulk remaps.
- Orchestrator cancellations / timeouts.

Everything else remains in the category-based debug logger. The event log is the *timeline of cluster-affecting decisions*, not the general log firehose.

### Ring storage

```go
type CommitLog struct {
    mu     sync.RWMutex
    ring   []CommitEvent
    head   int
    size   int
    cap    int
    seq    uint64
    logger *logger.Logger
}

func (l *CommitLog) Append(e CommitEvent) {
    l.mu.Lock()
    l.seq++
    e.SeqNo = l.seq
    if e.Timestamp.IsZero() {
        e.Timestamp = time.Now()
    }
    l.ring[l.head] = e
    l.head = (l.head + 1) % l.cap
    if l.size < l.cap {
        l.size++
    }
    l.mu.Unlock()

    // Also stream to the category logger so operators can tail live.
    category := "events:" + e.Kind.String()
    status := "✓"
    if !e.Success && e.Kind != EventInvariantViolation {
        status = "✗"
    }
    l.logger.Log(category,
        "%s cid=%d step=%s dur=%dms affected=%v err=%s",
        status, e.CommitID, e.Step, e.DurationMs, e.Affected, e.Error)
}
```

Bounded memory: 1024 events × ~200 bytes ≈ 200KB per host. Capacity is `Config.CommitLogCapacity`, default 1024. Oldest events evicted on overflow.

### Read API

Returns defensive copies:

```go
func (l *CommitLog) Recent(n int) []CommitEvent
func (l *CommitLog) ByCommitID(id uint64) []CommitEvent
func (l *CommitLog) ByCell(cellID string) []CommitEvent
func (l *CommitLog) Since(t time.Time) []CommitEvent
func (l *CommitLog) Tail(ctx context.Context) <-chan CommitEvent
```

### Console surface

Admin commands on any process running the coordinator role:

```
commit log                       # recent 20
commit log -n 100                # recent N
commit log <commit_id>           # all steps for one commit
commit log cell <cell_id>        # everything that touched this cell
commit log since 1m              # last minute
commit log tail                  # stream new events
```

Fixed-column table output by default; `--json` flag for structured output. Example:

```
coord+host+gateway > commit log cell cell_0_0 -n 10
  SEQ   TIME      CID  KIND     STEP                     OK  DUR  ERROR
  8412  12:47:02  17   Split    begin                    ✓   —
  8413  12:47:02  17   Split    snapshot-pre-ownership   ✓   0
  8414  12:47:02  17   Split    apply-cell-to-host-map   ✓   0
  8415  12:47:02  17   Split    detach-parent-from-coord ✓   0
  ...
```

### Logger bridge

Each event is also streamed to the existing category logger under `events:<kind>` categories:

```
events:split        # Split plan steps + results
events:merge
events:migrate
events:invariant    # Invariant violations
events:host         # Host join / leave / crash
events:session      # Bulk session-route remaps
events:*            # All of the above (existing wildcard syntax)
```

Tail during an incident: `log events:*` streams everything in the normal console flow. Post-incident drill-down: `commit log cell cell_0_0 since 5m` gives structured table output. Same source of truth.

### HTTP endpoint

Same mux that already serves `/metrics` and `/commands`:

```
GET /events              # JSON, last 100 by default, ?n=500&since=...&kind=Split
GET /events/<commit-id>  # JSON, one commit's full timeline
GET /events/stream       # NDJSON SSE, live tail
```

Enables dashboards, incident-forensics tooling, external log aggregation.

### Optional disk persistence

`Config.CommitLogPersistPath`: when set, a background goroutine appends each event to an NDJSON file at that path with daily rotation. Crashed-host forensics: restart, load the file, query history.

Off by default — disk writes have operational cost.

## Component 4 — Entity-transfer state machine

Closes the class of bugs where multiple ECS entities with the same netID coexist in different states on the same cell.

### The index

Every cell's `WorldBase` gains a single authoritative per-netID presence index:

```go
type EntityPresence uint8
const (
    PresenceNone EntityPresence = iota
    PresenceLive
    PresenceShadow
    PresenceReplica
)

type netIDIndex struct {
    mu    sync.RWMutex
    slots map[uint32]netIDSlot
}

type netIDSlot struct {
    Entity   ecs.Entity
    Presence EntityPresence
}

type TransitionResult struct {
    Action    TransitionAction
    PrevEntity ecs.Entity  // valid iff Action == Replaced
}

type TransitionAction uint8
const (
    ActionInstalled TransitionAction = iota // fresh install, no prior state
    ActionPromoted                           // Shadow → Live in place
    ActionReplaced                           // PrevEntity should be removed from ECS
    ActionUpdated                            // Replica-on-Replica update
    ActionRejected                           // caller should not proceed
    ActionDuplicate                          // loud: duplicate Live spawn
)

func (idx *netIDIndex) Lookup(netID uint32) (ecs.Entity, EntityPresence, bool)
func (idx *netIDIndex) Enter(netID uint32, entity ecs.Entity, to EntityPresence) TransitionResult
func (idx *netIDIndex) Exit(netID uint32)
```

### Transition policy table

`Enter` consults current slot state and applies:

| Current  | Incoming | Action      | Rationale |
|----------|----------|-------------|-----------|
| None     | any      | `Installed` | first appearance |
| Live     | Live     | `Duplicate` | drain-bug pattern; caller must not proceed |
| Live     | Shadow   | `Rejected`  | we own authoritatively; ignore Shadow |
| Live     | Replica  | `Rejected`  | border frame for locally-owned entity; ignore |
| Shadow   | Live     | `Promoted`  | normal handoff-commit path |
| Shadow   | Shadow   | `Rejected`  | duplicate Prepare; first wins |
| Shadow   | Replica  | `Replaced`  | evict Replica, keep Shadow (border frame raced Prepare) |
| Replica  | Live     | `Replaced`  | remove Replica, install Live |
| Replica  | Shadow   | `Replaced`  | remove Replica, install Shadow |
| Replica  | Replica  | `Updated`   | normal border-frame update (current upsert path) |

No runtime panics — all irregular transitions log + metric. The `no-duplicate-presence-per-cell` invariant (Component 1) catches violations at commit boundaries where panics (in dev) kick in.

### Integration points

Four call-site wrappers:

- `SpawnFromTransferCore` — after successful ECS spawn, call `idx.Enter(netID, entity, PresenceLive)`. On `Replaced(prev)`, remove `prev` from ECS. On `Duplicate`, skip ECS insert and log.
- `SpawnShadow` — `idx.Enter(netID, entity, PresenceShadow)`.
- `upsertBorderReplica` — `idx.Enter(netID, entity, PresenceReplica)`. On `Rejected` (netID is Live), skip replica creation.
- `PromoteShadow` — `idx.Enter(netID, <same entity>, PresenceLive)` atomic transition.
- `MarkForRemoval` / `RemoveReplicaByNetID` / `RemoveShadowByNetID` → `idx.Exit(netID)` via `eng.OnEntityRemoved` hook.

The existing `replicaNetIDs` map becomes redundant — `netIDIndex` subsumes it. One map of truth instead of three.

### Feature flag and staged rollout

`Config.StrictNetIDIndex` bool, default `false` initially.

- **Off**: index tracks state for observability but transitions are advisory — existing spawn paths run unchanged.
- **On**: transitions enforced per table; `Rejected`/`Duplicate` callers must respect the return value.

Rollout:
1. Land the index in observe-only mode. Metrics count each transition kind.
2. One week of dev + staging metrics. Unexpected transitions surface here.
3. Flip `StrictNetIDIndex=true` in dev. S7 suite + manual playthroughs.
4. Flip in prod once dev is stable.

Safety hatch: if a subtle transition is wrong, flip back off and behavior reverts.

## Testing strategy

### Invariants (Component 1)

- Unit tests per invariant: construct a deliberately-broken `Process`, assert the right invariant detects it.
- Fixture default for existing tests: `InvariantMode = Panic`. Any regression reaching a bad state fails in CI.

### Commit plans (Component 2)

- No new behavior tests needed — refactor is behavior-preserving. The existing S7 suite (`TestS7SplitAcrossHosts`, `TestS7MergeAcrossHosts`, `TestS7MigrateAcrossHosts`, `TestS7MergeWiresParentNeighbors`, `TestS7MergeNoDuplicateNetIDs`, etc.) is the regression guard.
- Add one test per commit kind asserting `plan.Steps` has the expected ordered step names (ensures ordering doesn't silently drift).

### Event log (Component 3)

- Unit tests: append/query/eviction on the ring; concurrent append correctness.
- Integration: run a split via the S7 fixture, assert the event log contains the expected step sequence.
- Manual smoke: console `commit log` output during a split demo.

### Entity-transfer state machine (Component 4)

- Unit test for every row in the transition policy table.
- Integration: force a Replica to exist at the moment a Shadow arrives; assert no duplicate ECS entities remain.
- Observe-mode phase: one week of staging with `StrictNetIDIndex=false`, collect transition metrics, audit patterns before flipping strict on.

## Rollout

Strict ordering. Each layer lands and is validated before the next starts:

1. **Invariants** — additive, low risk. Wire in the five invariants. Fix anything they catch in the current codebase. S7 suite green in `InvariantPanic` mode = gate to proceed.
2. **Commit plans** — mechanical refactor, medium risk. Lift `applyXxxCommit` into plan form. Behavior-preserving; S7 suite is the oracle.
3. **Event log** — additive on top of plans, low risk. Ring + console + HTTP + logger bridge. Hook at plan steps and invariant violations.
4. **Entity-transfer state machine** — invasive, highest risk. Land index in observe-only. One week staging metrics. Flip `StrictNetIDIndex=true` in dev first, then prod.

Each step is independently mergeable. If any layer causes issues, rollout stops without blocking the ones already landed.

## Non-goals (explicit)

- **Rollback / compensation for failed commits.** Current design is "log error and continue" — keeping that. True atomic commits would be Framing Y.
- **Cross-cell transaction semantics.** No distributed consensus, no two-phase commit. Each commit is a coord-side operation.
- **Replacing `component.Shadow` / `component.Replica` with a single enum.** The existing component markers stay — game systems filter on them via `query.Without[...]()`. The `netIDIndex` is a parallel structure, not a replacement.
- **Replication system internals.** Spec 1 handles those.
- **Performance optimization of invariant checks.** Invariants run only at commit boundaries (O(cells) per commit), not in hot paths. If any check is observed to cause commit latency, that specific check is optimized; the layer is not removed.

## Files changed (summary)

**New files:**
- `pkg/universe/integrity.go` — `Invariant`, `InvariantMode`, `CheckInvariants`, initial invariant set.
- `pkg/universe/commit_plan.go` — `CommitPlan`, `PlanStep`, `CommitContext`, `ExecuteCommitPlan`.
- `pkg/universe/commit_builders.go` — `buildSplitPlan`, `buildMergePlan`, `buildMigratePlan`, `stepXxx` functions.
- `pkg/universe/commit_log.go` — `CommitLog`, `CommitEvent`, ring + query API.
- `pkg/universe/commit_log_console.go` — `commit log ...` admin commands.
- `pkg/universe/commit_log_http.go` — `/events` HTTP handlers.
- `pkg/universe/netid_index.go` — `netIDIndex`, transition table.
- `pkg/universe/integrity_test.go` — invariant unit tests.
- `pkg/universe/commit_plan_test.go` — plan structure tests.
- `pkg/universe/commit_log_test.go` — ring + query tests.
- `pkg/universe/netid_index_test.go` — transition policy tests per table row.

**Modified files:**
- `pkg/universe/cell_transfer_commit.go` — replace `applySplitCommit` / `applyMergeCommit` / `applyMigrateCommit` bodies with `ExecuteCommitPlan(buildXxxPlan(req))`.
- `pkg/universe/coordinator.go` — `Config.InvariantMode`, `Config.CommitLogCapacity`, `Config.CommitLogPersistPath`, `Config.StrictNetIDIndex` added; `Process` gets `commitLog`, `invariantMode`, `netIDIndex` fields.
- `pkg/universe/world_base.go` — `netIDIndex` instance on `WorldBase`; `SpawnShadow`, `SpawnFromTransferCore`, `upsertBorderReplica`, `PromoteShadow`, `RemoveReplicaByNetID`, `RemoveShadowByNetID` gain `netIDIndex` integration.
- `pkg/engine/engine.go` — `OnEntityRemoved` hook called by `FlushRemovals` so the index can `Exit` cleanly.
- `internal/game/logcat.go` — register `events:split`, `events:merge`, `events:migrate`, `events:invariant`, `events:host`, `events:session` categories.
- `pkg/universe/cluster_fixture_test.go` — fixture sets `InvariantMode = Panic` by default.

**No wire-format changes.** This spec is entirely internal to the server process.
