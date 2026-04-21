# Overlap Handoff Design (Prepare → Overlap → Commit)

**Date:** 2026-04-21
**Status:** Design spec
**Scope:** Wire up the long-deferred overlap-based entity handoff protocol, eliminating the client-visible blink / rubber-band artifacts that occur today when replicas cross cell boundaries. Covers both same-process sibling handoffs and cross-host handoffs. Also introduces a server-side blink-detector invariant.

## Goal

**Perfect opacity** of cell crossings. Clients should be literally unable to tell that an entity transitioned from one authoritative cell to another. No entity blink, no position snap, no rubber-band, no interpolation reset. This is a strict criterion, not a softened "reduced-blink" target.

## Background

The existing v1 handoff path (`pkg/universe/handoff_driver.go`) fires `MsgHandoffPrepare` and `MsgHandoffCommit` on the same tick at the moment an entity physically crosses a cell boundary, then `MarkForRemoval` on the source. That produces a visible gap on downstream neighbors:

1. Source A removes entity X. A's next border broadcast omits X.
2. Destination B receives Prepare+Commit next tick, creates Shadow and promotes to Live. B starts broadcasting X.
3. In between, a shared neighbor P sees X drop out of A's push set. Per the aggressive diff-based removal in `ApplyBorderFrame` (see `pkg/universe/world_base.go:895-952`), P immediately evicts its replica of X, sends `SE_ENTITY_REMOVED` to subscribed clients. When B's frame arrives, P creates a new replica ECS entity for X → `SE_ENTITY_SPAWN`. Client sees blink + fresh interp baseline.

The same failure mode affects clients on the source cell (A's own `ReplicationSystem` sends `SE_ENTITY_REMOVED` when X's Live entity is deleted) and destination cell (pre-handoff replica of X gets replaced by a Shadow of the same netID but a different ECS entity handle).

Much of the infrastructure for the overlap model is already built but unwired:

- `pkg/universe/handoff.go` ships `HandoffStateMachine` with `Unseen → Border → Promoted → Handoff` phases, `MinWarmupTicks=5`, `MaxWarmupTicks=100`, `CrossingCooldownTicks=20`.
- Message types `MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgForwardInput`, `MsgHandoffCancel` exist with full codec support.
- `WorldBase.SpawnShadow`, `PromoteShadow`, `RemoveShadowByNetID` exist.
- `pkg/replication.BaselineStore` exists but is unread.

The State Integrity framework (merged 2026-04-21) provides the runtime invariant infrastructure that this spec extends.

## Design

The fix is four coordinated changes that together guarantee every cell which renders the entity — source, destination, shared neighbors — sees continuous presence through the handoff.

### Change 1: Shadows participate in the destination's outgoing border push

`pkg/universe/border_replication.go`'s `BorderDispatcher` walks each cell's entities with a filter that excludes `Ghost` and `Replica`. Today it also implicitly excludes `Shadow`-tagged entities because they came in via `SpawnShadow` and are treated as pre-authority. The new filter includes shadows.

Rationale: a shadow on the destination cell holds the same Position / Velocity / component state as the source's Live entity (initialized from the `TransferBlob` at Prepare time, updated continuously by source's border broadcasts during overlap — see Change 2). Broadcasting the shadow outward lets downstream neighbors keep receiving border frames for this netID from the destination even before authority flips at Commit.

Two filters now diverge intentionally:

- **Commit-path serializers** (`serializeAllEntities`, `serializeQuadrantEntities` in `cell_transfer_executor.go`): continue to exclude `Shadow`. Shipping shadows in a bulk snapshot would double-materialize handoff-in-flight entities during a split/merge (the bug fixed in commit `9d664d7`).
- **Border dispatcher push walk:** includes `Shadow`. This is the new behavior.

### Change 2: `upsertBorderReplica` updates a Shadow in place

During overlap, source A continues to broadcast X's border frames to the destination B (B is a neighbor of A; X's position is still near their shared boundary). B has a Shadow for X. The current `upsertBorderReplica` path would either:

- Hit `netIDIdx.Enter(X, newEntity, Replica)` which transitions `Shadow → Replica = ActionReplaced`, killing the Shadow — later `PromoteShadow` at Commit finds nothing to promote, handoff silently fails.
- Or create a new ECS replica entity alongside the Shadow, with two entities sharing netID X — trips `invNoDuplicatePresencePerCell`.

Fix: `upsertBorderReplica` consults `netIDIdx.Lookup(netID)` *before* falling into the replica lookup. If the local slot is `Shadow`:

1. Do not go through `netIDIdx.Enter`.
2. Apply the frame's position, velocity, rotation, and component-tail directly onto the shadow's ECS entity (using the same `applyEntityComponents` path as the replica-update branch).
3. Return. The Shadow is now refreshed with the source's latest authoritative state.

The Shadow remains the single ECS representation of X on this cell until `PromoteShadow` fires at Commit. One entity, one netID slot, one lifecycle.

### Change 3: `DemoteLiveToReplica` — source's mirror of `PromoteShadow`

At Commit, source cell A currently calls `MarkForRemoval` on X's Live entity. This is what causes A's `ReplicationSystem` to send `SE_ENTITY_REMOVED` to clients on A, and is what causes A's `BorderDispatcher` to stop including X in outgoing frames.

New operation `WorldBase.DemoteLiveToReplica(netID uint32, newSourceCellID string) error`:

1. Look up the ECS entity by netID via the existing `netIDMap`.
2. Add `component.Replica{SourceCellID: newSourceCellID, TTL: 30, UpdatedThisTick: true}` to the entity.
3. Transition `netIDIdx` slot from `Live` to `Replica`. This requires a new netIDIndex policy entry: `Live → Replica (explicit demote) = ActionUpdated`. Note: this transition is explicit-only (callers must pass a `Demote` action); unsolicited `Enter(netID, entity, Replica)` on a `Live` slot still returns `ActionRejected`.
4. Register in `replicaNetIDs[netID] = entity` so subsequent `upsertBorderReplica` calls from the new authoritative cell update this entity in place.
5. Do not touch Position / Velocity / Rotation / component state. Same ECS entity, same render data.

After demote:

- Source's `BorderDispatcher` push walk naturally skips the entity (replicas aren't in the push set).
- Source's client-facing `ReplicationSystem` still scans the entity as part of any nearby player's AoI and continues sending `SE_ENTITY_UPDATE` for it. No `SE_ENTITY_REMOVED` is ever emitted.
- Dest's first post-Commit border frame for X reaches source → `upsertBorderReplica`'s existing replica-update branch fires → position / velocity / component tail get updated from dest's simulation.

`PromoteShadow` on the destination already does the symmetric in-place conversion (Shadow → Live, same ECS entity). Together, demote and promote preserve the ECS entity identity on both sides of the authority flip.

### Change 4: Multi-source presence tracking on any cell with a replica

`ApplyBorderFrame`'s current diff-based eviction:

```text
for netID in previous-frame-from-source-C but not in current-frame:
    RemoveReplicaByNetID(netID)
```

New logic:

```text
for netID in previous-frame-from-source-C but not in current-frame:
    if any other source S != C currently has netID in borderLastSeen[S]:
        continue  // another cell is still authoritative for us about this netID
    RemoveReplicaByNetID(netID)
```

`b.borderLastSeen[sourceCellID]` is already the per-source presence map. The multi-source check is a linear walk over the other keys — O(neighbors) per missing netID per frame, trivial in practice (a cell has at most 8 neighbors).

Semantics under normal operation:

- Single-source steady state: same as today. Source drops netID → no other source has it → eviction fires.
- Handoff overlap: Source A broadcasts X, destination B broadcasts X (via Change 1's shadow-push). Neighbor P tracks both. When A drops X at Commit, B still has X → eviction skipped. When B eventually drops X (entity genuinely despawns later), A already dropped it → eviction fires.
- Handoff cancel: dest drops shadow via `RemoveShadowByNetID` → B stops broadcasting X → A still has it → eviction skipped. Clean return to single-source steady state.

## Handoff lifecycle

The state machine already exists (`pkg/universe/handoff.go`); the wiring change is in `handoff_driver.Tick`.

### Source side (cell A's `handoff_driver.Tick(currentTick)`)

```text
Drain crossing events from BoundarySystem:
  For each (entity X, dest cell B):
    Skip if state machine says InCooldown(X→B) = true.
    Skip if Base.IsDrainingForMerge() returns true (existing merge-freeze from State Integrity).

    If state is Unseen or Border:
      Bump X.NetworkID.Epoch.
      Serialize X via SerializeEntity (existing TransferFrame wire format).
      Send MsgHandoffPrepare to B with TransferBlob + ClientBaselines.
      Transition (X→B) to HandoffPromoted, warmup counter = 0.
      Cancel Promoted handoffs for X on OTHER neighbors (multi-neighbor case — existing behavior).

Walk Promoted handoffs on this cell:
  For each (X→B) in HandoffPromoted:
    Increment warmup counter.
    If CanCommit(X→B) returns true:
      Send MsgHandoffCommit to B with (netID, epoch, commitTick).
      Call Base.DemoteLiveToReplica(netID, B.CellID).
      Transition (X→B) to HandoffHandoff, EnterCooldown(currentTick).
```

### Destination side (cell B's message handlers in `cell.go`)

Existing handlers need two refinements:

- `MsgHandoffPrepare`: current code calls `RemoveReplicaByNetID(X)` followed by `SpawnShadow`. Keep as-is — the replica removal happens within a single tick's `DrainInbox` before `ReplicationSystem` scans, so the replica-to-shadow swap is client-invisible (same netID in the scan's output).
- `MsgHandoffCommit`: current code calls `PromoteShadow(netID)` which already does the in-place Shadow → Live transition. No change needed.

### Destination-side watchdog

One new thing on the destination: if a Shadow has lived past `MaxWarmupTicks` (100 ticks, 5s) with no matching Commit, the destination self-cleans. This covers the case where the source died mid-handoff or the underlying MeshData stream lost a Commit message (defensive — the channel is reliable).

Implementation: extend `component.Shadow` with a `CreatedTick uint64` field set by `SpawnShadow` from the destination cell's current tick. A new per-tick pass on the destination walks entities with the `Shadow` component, compares `currentTick - shadow.CreatedTick` against `MaxWarmupTicks`, and for any that exceed it calls `RemoveShadowByNetID(netID)` + sends `MsgHandoffCancel` back to the source. The walk runs on the game-loop goroutine alongside the existing replica TTL sweep in `world_base.go`, so no new synchronization is needed.

### Tunables (kept from existing `handoff.go`)

| Constant | Value | Meaning |
|----------|-------|---------|
| `MinWarmupTicks` | 5 (250ms @ 20Hz) | Ticks in Promoted before Commit is eligible. Gives destination time to receive border updates and warm its state. |
| `MaxWarmupTicks` | 100 (5s @ 20Hz) | Destination-side watchdog for orphaned shadows. |
| `CrossingCooldownTicks` | 20 (1s @ 20Hz) | Post-commit dwell on the same (entity, neighbor) pair to prevent thrash. |

### Cancel paths (enumerated)

1. **Multi-neighbor choice** — entity near a tri-junction triggers handoffs to multiple neighbors in successive ticks. Only one wins; source emits `MsgHandoffCancel` to the losers (existing behavior). Cancelled destination: `RemoveShadowByNetID` → stops broadcasting → multi-source check on shared neighbors sees only A still pushing → no-op.
2. **Max-warmup destination watchdog** — covers source death or Commit loss. Destination cleans up the stale shadow and notifies source via `MsgHandoffCancel`. Source's state machine forgets the pair on receive; resumes normal Live simulation.
3. **Merge drain interaction** — if the source enters `drainingForMerge=true` while a handoff is Promoted, `handoff_driver.Tick` already short-circuits per the State Integrity plan (commit `e4ede97`). The in-flight handoff goes stale; the destination's watchdog eventually cleans it up. The entity gets shipped via the merge executor's serialize/populate flow.
4. **Cross-host message loss** — MeshData is a reliable bidi stream, so this is defense-in-depth only. Watchdog handles any missed Commit.

## Client-side changes (web-pixi)

The wire protocol is already netID-keyed and cell-agnostic. The client doesn't know or care which server cell any entity lives on. Client work is verify + harden rather than rearchitect.

1. **Interp state persists across update gaps.** Per the existing memory guidance (`feedback_interpolation_prev_anchor.md`), the client keys interp by netID and uses real server snapshots as `prev` / `curr`. Verification: a new unit test feeds the decoder a sequence of per-netID snapshots with a 1-tick gap and asserts that the next snapshot does not reset the baseline.
2. **UPDATE for a never-SPAWNed netID is synthesized.** In edge cases (client packet loss, malformed frame decode, etc.) the first frame a client processes for a given netID might be an UPDATE. Today this might be dropped. Fix: synthesize a client-side SPAWN from the UPDATE's payload, seeding the initial snapshot from the update data.
3. **SPAWN for an already-known netID is coalesced.** Shouldn't happen under the new server-side design, but defense-in-depth. If a SPAWN arrives for a netID already in the client's entity map, treat as UPDATE — preserve the accumulated interp state, do not teleport to the SPAWN's position.

Estimated size: 50-100 lines across the decoder + entity-map modules with unit tests.

## Server-side blink detector (State Integrity extension)

To catch regressions automatically, the `ReplicationSystem` gains a per-connection runtime check.

**`invNoBlinkForConn`:** when the system is about to emit `SE_ENTITY_SPAWN(netID=X)` for connection Y, it consults a per-connection ring of recent removals. If `(Y, X)` was removed within the last `K = 30` ticks (1.5s), this is a client-visible blink.

Implementation:

- Add `recentRemovals map[uint32]map[uint32]uint64` (connID → netID → tickRemoved) on the `ReplicationSystem`'s per-connection state. Ring-buffer via a simple GC pass that drops entries older than K ticks.
- On every `SE_ENTITY_REMOVED` emission: record `(connID, netID, currentTick)`.
- On every `SE_ENTITY_SPAWN` emission: check the ring. If removed within K ticks → record an `EventKind=EventInvariantViolation` event in the commit log with `Step="no-blink-for-conn"`, `Context={"connID": ..., "netID": ..., "ticksSinceRemove": ...}`. Panic if `InvariantMode=InvariantPanic`.

The check lives on the wire-send path because that's where ground truth lives. The server knows exactly which frames it emitted; the client would have to infer. Runs O(1) per emission with a small per-connection map.

This invariant runs outside the commit-path `CheckInvariants` loop (it's triggered by wire emission, not a commit boundary). It lives alongside the existing framework and shares its infrastructure (the commit log, the `InvariantMode` gating, the event-category logger) but is invoked directly from `ReplicationSystem` at send time rather than from `ExecuteCommitPlan`. No refactor of `integrity.go` is needed — the check is standalone and reuses the existing `CommitLog.Append` and `InvariantMode` machinery. Gated on `InvariantMode != InvariantOff`, same as the commit-path invariants. Tunable `K` via `Config.BlinkDetectorTicks` (default 30).

## Testing strategy

Three layers, proportionally sized:

1. **Unit tests (`pkg/universe/`):**
   - `ApplyBorderFrame` multi-source dedup. Synthesize frames from two sources for the same netID; verify replica survives one source dropping it, survives both sources dropping it on the same tick (single-source-like eviction), is correctly evicted after both dropped for a full tick.
   - `DemoteLiveToReplica`. Verify in-place transition: same ECS entity, same position / velocity pre-/post-demote, `netIDIdx` slot flipped to `Replica`, `replicaNetIDs[netID]` points at the entity, subsequent border frames update in place.
   - `upsertBorderReplica` Shadow-update path. Feed a border frame for a netID with an existing Shadow; assert the Shadow's position / components are refreshed, no new ECS entity is created, `netIDIdx.Enter` is not called.
   - Destination-side Shadow watchdog. Advance ticks past `MaxWarmupTicks` with no Commit; assert Shadow is removed and `MsgHandoffCancel` is emitted.

2. **Integration tests (`pkg/universe/`):** extend `loopback_bridge.go`'s 2-host harness to run scripted handoffs. Spawn entity on A, trigger crossing, script ticks through overlap, observe:
   - Both A and B broadcast X during overlap ticks (both frames contain X).
   - Neighbor P's replica is never evicted; its ECS entity handle is stable through the overlap.
   - Client-facing wire log (stubbed recorder) contains no `SE_ENTITY_REMOVED(X)` followed by `SE_ENTITY_SPAWN(X)` pair.
   - `invNoBlinkForConn` is never tripped when running the harness under `InvariantMode=InvariantPanic`.

3. **Bot-load smoke test (manual, extends Phase E of State Integrity plan):** 4node-basic distributed with `InvariantMode=InvariantPanic`, `StrictNetIDIndex=true`. `bot spawn 60 cell_0_0`, `cell split 0_0`, let bots wander for 60s. Any client-visible blink trips the invariant; zero panics is the gate.

## File layout

**New files:**

- (none; all changes are in existing files)

**Modified files:**

- `pkg/universe/world_base.go`
  - `upsertBorderReplica`: Shadow-update fast-path at the top (before replica lookup).
  - `ApplyBorderFrame`: multi-source check before `RemoveReplicaByNetID`.
  - New method `DemoteLiveToReplica(netID uint32, newSourceCellID string) error`.
- `pkg/universe/netid_index.go`
  - New explicit transition action for `Demote`: `Live → Replica via Demote = ActionUpdated`. Unsolicited `Enter(Replica)` on a `Live` slot continues to return `ActionRejected`.
- `pkg/universe/border_replication.go`
  - `BorderDispatcher` entity-walk filter: include `Shadow`.
- `pkg/universe/handoff_driver.go`
  - `handleCrossing`: fire Prepare only, transition to Promoted (do not fire Commit or MarkForRemoval).
  - `Tick`: after processing crossings, walk all Promoted handoffs, increment warmup, call `CanCommit`, fire Commit + `DemoteLiveToReplica` + enter cooldown.
- `pkg/universe/cell.go`
  - `MsgHandoffPrepare` / `MsgHandoffCommit` handlers: keep current behavior, verify interaction with in-place Shadow update.
  - New destination-side watchdog: on each tick, walk entities with the `Shadow` component, compare `currentTick - shadow.CreatedTick` against `MaxWarmupTicks`, and clean up stale ones.
- `pkg/component/shadow.go`
  - Add `CreatedTick uint64` field to `Shadow`, populated by `SpawnShadow` from the destination cell's tick counter at spawn time.
- `pkg/system/replication.go`
  - `invNoBlinkForConn` implementation: per-connection recent-removals ring, check on SPAWN emission, record to commit log on violation.
- `pkg/universe/commit_log_categories.go`
  - Add `CatEventsReplication = "events:replication"` log category for blink-detector events.
- `pkg/universe/integrity.go`
  - No changes. The blink detector shares the framework's infrastructure (commit log, `InvariantMode` gating) but runs directly from `ReplicationSystem` — no extension to `integrity.go` itself is required.
- `web-pixi/src/` (decoder + entity-map modules)
  - UPDATE-synthesizes-SPAWN for unknown netID.
  - SPAWN coalesces into UPDATE for known netID.
  - Unit tests for single-tick update gap preserving interp state.

**Deleted files:**

- (none)

## Out of scope

- **Promote-radius early detection.** The v1 overlap runs entirely after physical boundary crossing. Promoting earlier (before the entity actually crosses) lets warmup run during approach, narrowing the window where source is authoritative for an entity in destination's territory. Deferred because the post-crossing overlap is already enough to eliminate the client-visible blink and simpler to reason about.
- **Client-side prediction for owned players.** The "own player" path already works well (T&T's freshSnapshot handling). Adding predicted-replay for local player is a separate Phase 6 item in the mmokit roadmap.
- **`MsgForwardInput` safety path.** Already built into the message layer; using it would require the gateway to retain routing for an outbound-bound session for one tick. Not needed unless the gateway's `UpstreamSwitch` fan-out becomes observably slower than server-side Commit.
- **Baseline handover (replication baselines)** — `BaselineStore` is allocated per `NodeViewer` but unread. Wiring up baseline continuity across handoff for border replicas would reduce cross-host bandwidth by ~60-80%. Queued as a follow-up; separate enough from the overlap-mechanics change to merit its own spec.
- **Gateway-side session crash recovery tokens.** Separate follow-up to S6.

## Rollout

- Ship as a single merged branch with all four server-side changes + client-side tweaks + the blink detector.
- Dev default: `InvariantMode=InvariantPanic` + `StrictNetIDIndex=true` (unchanged from today). The blink detector is gated on the same flag.
- Production default: `InvariantMode=InvariantLog` + `StrictNetIDIndex=false` initially. Blink detector logs violations without panicking.
- Post-merge validation: re-run the Phase E bot-load smoke test; confirm zero panics under sustained bot traffic with splits and merges.

## Verification

After implementation:

- `go vet ./... 2>&1` — clean.
- `go test ./... -count=1 -timeout 300s` — full suite green.
- `go test ./pkg/universe/ -run '^TestS7|TestHandoff|TestOverlap' -count=1` — S7 family + new overlap tests.
- Bot-load smoke test in `examples/4node-basic && just distributed` with 60 bots and repeated split/merge/migrate operations — zero panics, no visible blink on the client for the full session.
