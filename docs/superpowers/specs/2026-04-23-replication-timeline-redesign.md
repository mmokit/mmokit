# Replication Timeline Redesign (ClusterClock + Per-Entity Timestamps + Hard-Cut Handoff)

**Date:** 2026-04-23
**Status:** Design spec
**Scope:** Re-architect the cross-cell replication timeline so every sample a client sees is on one coherent monotonic wall clock, regardless of which cell authored it. Delete the accumulated overlap-handoff workarounds (shadow entities, dead-reckoning on the server, grace extrapolation, multi-source dedup, etc.) in favor of client-side-only interpolation between authoritative samples. Clock synchronization is maintained via periodic coordinator broadcast with per-host offset EMA — never on the per-tick critical path.

## Goal

A replica bot viewed from across a cell border should look **identical** to a local entity of the same type, at any framerate, through any number of authority transfers between cells on any number of hosts. All the mechanical complexity that makes this true lives inside `pkg/mmokit` and is invisible to game code.

## Motivation

The overlap-handoff branch (`2026-04-21-overlap-handoff-design.md`) shipped with working correctness but observable smoothness regressions that we papered over with a growing set of workarounds:

- `ReplicaDeadReckoningSystem` — server-side DR of replica position between border frames.
- `ShadowDeadReckoningSystem` — same, for pre-authority shadows.
- `Replica.UpdatedThisTick` + `Replica.MissedTicks` — gate-and-grace for DR freeze.
- `Shadow.UpdatedThisTick` + `Shadow.MissedTicks` + `Shadow.CreatedTick` — symmetric for shadows.
- `TickShadowWatchdog` — clean up orphaned shadows.
- `Shadow` fast-path in `upsertBorderReplica` — refresh shadow state in place.
- `netIDStillPushedByOtherSource` multi-source dedup — don't evict during handoff overlap.
- Border dispatcher "include Shadow" carve-out — make shadows participate in outgoing push.
- `ActiveViewers` Shadow skip — don't double-publish during warmup.
- Overlap-handoff protocol (`Prepare` → warmup N ticks → `Commit`) with cooldown and cancel paths.
- "Preserve source position during warmup" — don't normalize Position on the live entity.
- `OnCancelFromDest` — reconcile stuck state-machine entries.

Each workaround individually is justifiable. Together they represent a design that's fighting its own substrate. The root issues are three:

1. **No cluster-wide timeline.** Each cell runs on its own goroutine, stamps with its own wall clock. Cross-cell drift is invisible on one client but stutter appears on cross-cell viewing.
2. **Two layers of dead reckoning.** The server dead-reckons replicas so its own Replication emits current-tick positions; the client dead-reckons between samples for smooth rendering. Two extrapolators disagree at authority transfer boundaries.
3. **Overlap is mechanical.** Source and destination both publish during warmup, producing duplicate streams to shared neighbors; the multi-source dedup / shadow-in-push-set / skip-Shadow-in-ActiveViewers machinery exists to prevent clients from seeing this.

The industry (Unity NetCode, Source, Star Citizen's Replication Layer, SpatialOS, Gaffer/Gambetta) converges on one answer: **one authority per entity at any instant, client-side interpolation with render-lag, coherent timeline on the wire**. This spec adopts that model.

## Design principles

### P1 — ClusterClock: coordinator-anchored, cached, never on the critical path

A single cluster-logical wall clock that every host, cell, entity, and client agrees on to within a few milliseconds, maintained with minimal overhead.

- Coordinator broadcasts `CoordTimeSync { coordTimeMs: uint64 }` over `MeshControl` every 5-10 seconds.
- Each host receives and computes `offsetMs = coordTimeMs - local_recv_wall_ms`, smoothed via EMA (pattern reused from client's `clockSync.ts`).
- Each host exposes `ClusterClock.Now() uint64` = `local_wall_ms + offsetMs`. **Every sample-timestamp call in the replication path** uses this, not `time.Now()`.
- At broadcast interval 10s, cluster of 10,000 hosts: 24 KB/s aggregate. Per-tick at 20Hz would be 100×+ more.
- **Failure behavior:** if coordinator dies, hosts keep last-cached offset. Simulation continues at OS clock drift rate (ms/hour). Not a SPOF for simulation; just for fresh-host clock initialization.
- **Convergence:** a newly-joining host acquires an offset within one broadcast interval; samples it emits before that use local wall clock (slightly off but bounded).

### P2 — Per-entity timestamps on the wire

Every sample a client receives is stamped with the **authoritative producer's** `ClusterClock.Now()` at the moment the sample was produced. That stamp is an **opaque wire payload** — it traverses any intermediary (border-replica cache, gateway) unchanged.

- Frame-level `serverTimeMs` in the current `quantize.FrameHeader` is replaced (or supplemented) by a per-entity `producedAtMs uint64` in the delta / full payload.
- For locally-authoritative entities, the producer is this cell; stamped at replication write time.
- For cross-cell replicas (entity authoritative on cell A, rendered via replication from cell B to a client), the stamp was set by cell A's border push and preserved on the replica. Cell B's Replication reads the cached stamp from the replica and writes it verbatim into its own outgoing frame.
- **Rationale:** the client's interp cursor is a single cluster-clock cursor. Every entity the client has seen has samples on that cursor's axis. Lerp works across any entity regardless of authority history.

### P3 — Hard-cut handoff at a cluster-tick boundary

Authority transfer is instantaneous at a coordinator-agreed **cluster tick number**. There is no warmup, no overlap, no shadow. Exactly one cell is authoritative at any cluster tick.

- **Cluster tick number** = `ClusterClock.Now() / tickIntervalMs`. Every host computes the same integer at the same real moment (within ±1 under EMA jitter — bounded by the broadcast interval).
- **Source picks the cutover tick** when `handleCrossing` fires: `commitTick = currentClusterTick + HANDOFF_LEAD_TICKS` (default lead = 2 ticks = 100ms, enough for the `HandoffMessage` to reach destination on a typical LAN + leave margin for dest-side processing).
- Tick N (= commitTick − 1): source cell emits its final authoritative sample for entity E; after this tick's replication writes, source demotes E from Live to Replica.
- Tick N+1 (= commitTick): destination cell promotes its existing `Replica` of E (if present — normal case when E was in border push range) or spawns E as `Live` directly from a transfer blob (fast-mover case).
- Gateway's `UpstreamSwitch` carries `commitTick`; gateway flips session's authoritative host at the cluster tick so subsequent client input routes to destination.
- **If `HandoffMessage` arrives at destination after commit tick** (network lag, goroutine schedule delay): destination applies it at next local tick. A single-tick commit slip is invisible to clients (render-lag absorbs).
- **Client observable:** samples for E arrive at tick N (from source) and tick N+1 (from destination) — adjacent cluster ticks, ~50ms apart on the clock axis. Client lerps through the seam without knowing it happened.

### P4 — No dead reckoning on the server

The server's replication pipeline is a **pass-through proxy** for authoritative samples. Gap-filling is the client's responsibility via its sample ring + render-lag.

- `ReplicaDeadReckoningSystem` and `ShadowDeadReckoningSystem` are deleted.
- Replicas are **passive caches**: the most recent authoritative state received from the source. If no update arrives this tick, the replica's Pos/Vel stay exactly where they were at last update.
- `Replication` emits replicas' stamped state exactly as received. If source skipped a tick (source goroutine paused briefly), the wire stream reflects that: a gap between sample stamps. Client lerps the gap in its own render loop — at worst a tiny visual pause that most render-lag windows fully absorb.
- This removes the need for `UpdatedThisTick`, `MissedTicks`, `ClearReplicaUpdateFlags`, grace extrapolation, and all related machinery.

### P5 — Simplification: Shadow component is deleted

In the new handoff model, destinations don't need a distinct "pre-authority shadow" — the existing `Replica` (border replica already maintained by the normal `upsertBorderReplica` path) serves the same purpose.

- If the entity is in border-push range before the crossing, destination already has a `Replica` of it with recent state. At commit tick, the `Replica` component is removed — same ECS entity handle becomes `Live`.
- If the entity is NOT in border-push range (fast-mover, spawned across the boundary, etc.), the transfer blob in the `HandoffCommit` message carries full state; destination spawns a `Live` entity from scratch.
- No `Shadow` component. No `SpawnShadow`. No `PromoteShadow` (replaced by `PromoteReplicaToLive` or `SpawnLiveFromTransfer`). No `TickShadowWatchdog`. No `Shadow` fields `NetID`, `Epoch`, `CreatedTick`, `UpdatedThisTick`, `MissedTicks`, `SourceCellID`.

### P6 — Per-host cell tick phase drift is tolerated, not hidden

Each cell's goroutine continues to run independently at ~20Hz. Cross-cell and cross-host drift is explicitly absorbed by:

- **Cluster clock**: bounds inter-cell timestamp drift at `EMA_jitter` + `OS_drift_since_last_sync` ≈ 1-10ms on LAN.
- **Client render-lag**: default 100ms (`RENDER_DELAY` in 4node-basic / web-pixi), larger than any realistic inter-cell drift.
- **Client interp**: lerps between samples using their cluster-clock stamps. A ±10ms variance in stamp arrival looks like ±10% rate variation on that one tick, invisible at 20Hz.

Drift does NOT need to be zero, and does NOT need to be synthesized away via server-side DR. It's a first-class property that the design handles explicitly at a single layer (client interp) rather than fought at multiple layers.

## Wire format changes

### Proto changes

**`proto/meshpb/mesh.proto`** — add:

```proto
// Broadcast from coordinator to all registered hosts on the MeshControl
// control-plane stream. Hosts compute an offset EMA from coord_time_ms
// - local_recv_wall_ms and use that offset to stamp replication samples
// on a cluster-coherent timeline.
//
// Period: 5-10 seconds in production. NOT on the per-tick path; purely
// an advisory refresh. Hosts that miss broadcasts (coord down, network
// partition) use last-cached offset indefinitely and continue running.
message CoordTimeSync {
  uint64 coord_time_ms = 1;  // Coordinator's local_wall_ms at send time.
  uint64 seq = 2;             // Monotonic per broadcast, for stale-drop on receiver.
}
```

Wrap in `CoordMessage` as a new `oneof` arm.

### Replication frame wire format

**`pkg/quantize/wireformat.go`** — refactor frame header:

Old:
```
[u32 tick][u32 seq][u32 flags][u64 serverTimeMs]
[...entities...]
```

New:
```
[u32 tick][u32 seq][u32 flags]
[...entities with per-entity producedAtMs...]
```

Each per-entity payload (full or delta) gains an 8-byte `producedAtMs` prefix. Rationale: authoritative-producer stamp must travel with the entity, not the frame. A single frame from receiver cell B to a client may contain entities authoritative on cell B (stamped now) AND replicas of entities authoritative on cells A and C (stamped when A and C respectively produced them).

The frame-level `serverTimeMs` is removed outright. Client's clock-sync uses the **max of all per-entity stamps in the frame** as the anchor; this naturally tracks the most-current authority producer regardless of which cell that is.

### Border-frame wire format

**`pkg/universe/border_components.go`** — `DeltaBuf` per-entity prefix:

Old:
```
[4 worldX][4 worldY][4 radius][2 qvx][2 qvy][...componentTail...]
```

New:
```
[4 worldX][4 worldY][4 radius][2 qvx][2 qvy][8 producedAtMs][...componentTail...]
```

The 8 bytes cost: for a 10-entity border frame, 80 bytes of overhead. Trivial.

## Server-side components

### 1. `ClusterClock` primitive

**New:** `pkg/mmokit/cluster_clock.go` (or `pkg/universe/cluster_clock.go`).

```go
// ClusterClock maintains an offset between this host's local wall
// clock and the coordinator's wall clock, refreshed periodically via
// CoordTimeSync broadcasts. Thread-safe; Now() is called from every
// cell's game-loop goroutine.
type ClusterClock struct {
    mu        sync.RWMutex
    offsetMs  float64   // coordTime - localTime; EMA-smoothed
    initialized bool
    highestSeq uint64   // reject stale CoordTimeSync deliveries
}

func NewClusterClock() *ClusterClock { ... }

// Observe updates the offset from a received CoordTimeSync.
func (c *ClusterClock) Observe(coordTimeMs uint64, seq uint64) { ... }

// Now returns current cluster-wall-clock milliseconds.
// Before any Observe call, falls back to local wall clock.
func (c *ClusterClock) Now() uint64 { ... }
```

EMA alpha: **0.3** (more aggressive than the client's 0.1, since we broadcast at a longer interval and want faster convergence). First observation snaps directly.

### 2. Coordinator broadcast loop

**Modify:** `pkg/universe/coordinator.go`. Add a `startClusterTimeBroadcast(ctx context.Context)` goroutine fired from `Coordinator.Start`. Ticker period: `Config.ClusterClockSyncInterval` (default 10 seconds). On each tick, construct `CoordTimeSync { coord_time_ms: time.Now().UnixMilli(), seq: atomic_inc }` and broadcast to every registered host via `controlServer.broadcast`.

### 3. Host-side receive

**Modify:** `pkg/universe/mesh_control_client.go` (remote host path) and `pkg/universe/coordinator.go` (in-process `all` preset path) — dispatch on `CoordMessage_CoordTimeSync` to `clusterClock.Observe(msg.CoordTimeMs, msg.Seq)`.

### 4. `FrameWriter` uses `ClusterClock`

**Modify:** `pkg/system/frame_writer.go`:

```go
// Old: serverTimeMs := uint64(time.Now().UnixMilli())
// New: serverTimeMs is removed from frame header. Per-entity stamps
//      are read from the ECS entity's Position/tracking component.
```

Locally-authoritative entities: stamped with `clusterClock.Now()` at emit time.
Replicas: stamped with cached `producedAtMs` from the Replica component (set by `upsertBorderReplica`).

The `FrameWriter` gets a `ClusterClock` reference via `ReplicationConfig`.

### 5. `Replica` component refactor

**Modify:** `pkg/component/core.go`:

```go
// Old:
type Replica struct {
    SourceCellID    string
    SourceNetID     uint32
    TTL             int
    UpdatedThisTick bool  // DELETE
    MissedTicks     int   // DELETE
}

// New:
type Replica struct {
    SourceCellID   string
    SourceNetID    uint32
    TTL            int    // Kept for orphan cleanup
    ProducedAtMs   uint64 // The cluster-clock stamp from the authoritative producer's most recent frame for this entity
}
```

### 6. `upsertBorderReplica` store stamp

**Modify:** `pkg/universe/world_base.go` `upsertBorderReplica` — when applying a border frame, decode `producedAtMs` from the DeltaBuf prefix and store it on the `Replica`.

### 7. Handoff protocol collapse

**Modify:** `pkg/universe/handoff.go`, `pkg/universe/handoff_driver.go`, `pkg/universe/cell.go`.

New protocol:

```go
// HandoffMessage is the single handoff operation. Replaces
// HandoffPrepare + HandoffCommit + HandoffCancel from the overlap model.
// Carries:
//   - netID: entity to transfer
//   - commitTick: cluster tick number at which authority flips
//   - transferBlob (optional): full entity state for cases where the
//     destination does not already have a Replica of this netID
type HandoffMessage struct {
    NetID        uint32
    Epoch        uint32
    CommitTick   uint64  // cluster-clock tick number, not per-cell tick
    TransferBlob []byte  // optional; nil if destination already has Replica
}
```

Source-side `handleCrossing`:
1. Verify entity is live + not in cooldown for this destination.
2. Compute `commitTick` = `clusterClock.Now()/tickIntervalMs + 1` (i.e., next tick).
3. If destination cell is a **live neighbor** (shares border frames with source), transfer blob is **omitted** — destination already has Replica.
4. Otherwise, attach transfer blob.
5. Send `HandoffMessage` to destination.
6. Queue local demote for `commitTick` (source keeps running physics for this tick; at commit-tick end-of-frame, entity demoted to Replica).

Destination-side on `HandoffMessage`:
1. If transfer blob present: spawn entity as `Live` at commit-tick (not immediately — queue for commit-tick).
2. If no blob: verify local `Replica` exists for netID; queue `Replica → Live` promotion for commit-tick.
3. Commit-tick arrives: promote/spawn. Gateway sessionRoute flip. Cooldown enters.

There is no warmup. No "promoted" state. No separate Prepare. No Cancel handler (if source dies before commit, destination's promote-queue times out and the Replica stays a Replica). No shadows.

### 8. Border dispatcher simplification

**Modify:** `pkg/universe/border_replication.go`. Remove the "shadow in push set" comment carve-out; the filter is the simpler default (`Without(Ghost, Replica, Dormant)`) now that Shadows don't exist. Cell pushes locally-authoritative entities only.

### 9. `ActiveViewers` simplification

**Modify:** `pkg/system/viewer_source.go`. Remove the Shadow skip (nothing to skip; Shadows don't exist). Keep the Ghost skip.

### 10. `netIDIndex` state machine simplification

**Modify:** `pkg/universe/netid_index.go`. Remove `PresenceShadow` state; retain `PresenceLive` and `PresenceReplica`. Simpler 2×2 transition table:

| From → To | Live | Replica |
|---|---|---|
| empty | Installed | Installed |
| Live | Duplicate (rejected) | Updated (via explicit Demote only) |
| Replica | Updated (via explicit Promote only) | Updated |

`Demote` and explicit `Promote` remain as the sanctioned authority-transfer primitives.

## Deletion checklist

The following files, functions, fields, components, and message types are deleted outright:

**Components (`pkg/component/`):**
- `shadow.go` (entire file).
- `Replica.UpdatedThisTick` field.
- `Replica.MissedTicks` field.

**Systems (`pkg/system/`):**
- `replica_dead_reckoning.go` — **entire file** and all tests.

**WorldBase (`pkg/universe/world_base.go`):**
- `SpawnShadow` method.
- `PromoteShadow` method (replaced by internal promote logic in handoff-driver commit handler).
- `RemoveShadowByNetID` method.
- `TickShadowWatchdog` method.
- `ClearReplicaUpdateFlags` method (no longer needed — no UpdatedThisTick).
- `netIDStillPushedByOtherSource` helper.
- Shadow-fast-path block in `upsertBorderReplica`.
- Multi-source dedup check in `ApplyBorderFrame`.

**Retained (still needed but purpose simplifies):**
- `drainingForMerge` atomic — still guards the handoff-driver against firing a `HandoffMessage` while a merge commit is in flight on the donor cell (otherwise a post-drain commit could race with the merge's serialize+populate). Semantics unchanged; only the message type it guards against changes from Prepare/Commit pair to the single `HandoffMessage`.
- `Replica.TTL` + `ExpireReplicas` — orphan cleanup when a source goes silent (crash, network partition). TTL decrement runs unchanged; no tick-phase interaction.

**Handoff driver (`pkg/universe/handoff_driver.go`, `pkg/universe/handoff.go`):**
- `HandoffStateMachine.TickWarmup`, `CanCommit`, `WarmupCount`, `EnterCooldown`, `InCooldown` — all warmup/cooldown methods.
- `HandoffPhase` enum values `HandoffBorder`, `HandoffPromoted`, `HandoffHandoff` — replaced by a simple `InFlight` boolean (or kept, empty).
- `MinWarmupTicks`, `MaxWarmupTicks`, `CrossingCooldownTicks` constants.
- `HandoffDriver.tickPromoted` — no warmup loop.
- `HandoffDriver.fireCommit` — merged into `handleCrossing`.
- `HandoffDriver.OnCancelFromDest` — no cancel path.
- `HandoffPreparePayload`, `HandoffCommitPayload`, `HandoffCancelPayload` → single `HandoffPayload`.
- `MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgHandoffCancel` → single `MsgHandoff`.

**Cell (`pkg/universe/cell.go`):**
- `MsgHandoffPrepare` handler, `MsgHandoffCommit` handler, `MsgHandoffCancel` handler → single `MsgHandoff` handler.
- `RegisterShadowSession` / `PromoteShadowSession` / `RemoveShadowSession` (if the user ultimately landed these) — obsolete; session management is transfer-at-commit only.

**Bridge (`pkg/universe/bridge.go`):**
- `SendHandoffPrepare`, `SendHandoffCommit`, `SendHandoffCancel` → single `SendHandoff`.
- `HandoffDriver()` accessor from `cellBridge` and `grpcBridge` (needed only for `OnCancelFromDest`, which is gone).

**Meshpb (`proto/meshpb/mesh.proto`):**
- `HandoffPrepare`, `HandoffCommit`, `HandoffCancel` messages → single `Handoff` message.
- Add `CoordTimeSync`.

**ReplicationSystem (`pkg/system/replication.go`):**
- Blink-detector infrastructure (`recentRemovals`, `BlinkDetectorTicks`, `OnBlinkDetected`) — arguably useful as a general invariant, but in the new design a blink should never happen; keep it as a paranoid invariant OR remove. **Recommend: keep** (cheap, catches real regressions).

**Test files:** any test with `Shadow`, `WarmupTicks`, `Prepare`-only, `Cancel`-only, or `MissedTicks` assertions is either deleted or rewritten against the new protocol.

Rough LoC estimate: **~2,500 lines deleted, ~500 lines added**. Net simplification.

## Client-side changes

### 1. Frame decoder (per-entity `producedAtMs`)

**Modify:** `pkg/quantize/ts/delta-decoder-core.ts` and generated client decoders:

Remove frame-level `serverTimeMs`. Each decoded entity gains a `producedAtMs: number` field. Update `gen/` and any SDK consumers.

### 2. Interp ring keyed on per-entity timestamps

**Modify:** `web-pixi/src/interpolation.ts` and `examples/4node-basic/web/src/interpolation.ts`:

Samples already have a `serverTimeMs`. Change callers to populate it from `entity.producedAtMs` instead of the removed frame-level field. Semantic unchanged; only the source of the stamp moves from frame-level to per-entity.

### 3. Clock-sync: high-water mark of per-entity stamps

**Modify:** `web-pixi/src/clockSync.ts` and `examples/4node-basic/web/src/clockSync.ts`. Replace the `observeServerTime(frameServerTimeMs)` call site with a per-frame `max(...entity.producedAtMs)` extraction, then `observe(maxStamp, clientNow)`. Semantics unchanged.

### 4. Authority-transfer handling

No code changes needed. Client's interp ring naturally spans authority transfers:

- Sample at `producedAtMs = T` from cell A lands in ring.
- Sample at `producedAtMs = T + 50ms` from cell B lands in ring.
- Render cursor at `T + 25ms` lerps between them. Smooth.

## Phase 2 (out of scope for this spec)

These are follow-up cleanups that are architecturally compatible with this design but not included:

- **Gateway as direct replication layer.** Cell emits to subscribed clients directly via gateway, not via receiver-cell Replication. Removes the "receiver cell iterates its replicas for local clients" role. Requires subscription negotiation between gateway and cells (gateway tells each cell "client C subscribes to your AoI centered at X,Y with radius R"). Large change; shipping after Phase 1 stabilizes.
- **AoI pre-filter at cell** for client-destined streams. Cell knows entity positions; cell pre-filters; gateway multiplexes and forwards. Avoids gateway-side spatial queries.
- **Per-host cell-tick alignment.** If all cells on one host share a single scheduler (one ticker, multiple worker goroutines), per-host drift goes to near-zero. Simplifies intra-host inter-cell sync. Orthogonal to Phase 1; can ship independently.
- **Replace wall-clock stamps with a monotonic cluster-tick integer.** Per-entity `producedAtTick uint32` instead of `producedAtMs uint64` — 4-byte savings per entity per frame, plus integer arithmetic on the client. Requires a well-defined "tick 0" reference. Can be added after Phase 1 ships and the tick infrastructure is stable.

## Testing strategy

### Unit

- `ClusterClock` — EMA convergence, stale-seq rejection, pre-initialization fallback, large offset jump handling.
- `upsertBorderReplica` — new `producedAtMs` field is stored and returned in subsequent `FrameWriter` output verbatim.
- `FrameWriter` — per-entity stamp round-trip for local (cluster-clock-stamped) and replica (cached-stamp-passed-through) entities.

### Integration

- **Handoff hard-cut (single host):** entity moves east across 0_0 → 1_0 boundary; assert source emits last sample at tick N, destination emits first sample at tick N+1, both on cluster-clock axis, samples monotonic.
- **Handoff hard-cut (cross host):** same, across gRPC bridge. Stamps still monotonic.
- **Smoke: replica smoothness** — player in cell_1_0 observes bot in cell_0_0 for 60 seconds; client's sample ring has no gaps > 75ms; render-frame interp produces monotonic renderX.
- **Clock resilience** — kill coordinator, let cluster run for 5 minutes; timestamps remain within acceptable drift (< 500ms).
- **No-stutter regression** — the specific scenario the user reported (bot in 0_0, player in 1_0, watching replica motion) produces no perceptible stutter at 60fps client render.

### Delete

- All shadow-specific tests from the overlap-handoff branch.
- All warmup/cooldown timing tests.
- All multi-source dedup tests.
- Grace-extrapolation tests in `replica_dead_reckoning_test.go`.
- Blink-detector tests — keep (or remove) by decision above.

## Rollout

Single merged branch. The wire format changes (per-entity `producedAtMs`, `CoordTimeSync` message) mean partial-rollout is not possible across clients and servers; everyone runs the new protocol or nobody does. Client and server must ship together.

Recommended order within the branch:

1. Land `ClusterClock` + `CoordTimeSync` broadcast + host-side observe. Unused initially.
2. Change wire format (per-entity `producedAtMs`); `FrameWriter` + decoder updated; `upsertBorderReplica` stores stamp. Still Shadow-based handoff.
3. Collapse handoff protocol to `HandoffMessage` hard-cut. Delete Shadow + warmup + cooldown.
4. Delete `ReplicaDeadReckoningSystem` + `ShadowDeadReckoningSystem` + gates.
5. Update tests.
6. Client-side decoder + interp update.
7. Bot-load smoke verification.

Each step keeps the tree building and the existing simple test cases green; integration-level tests are rewritten wholesale in step 5.

## Verification

After rollout:

```bash
cd .
go vet ./...
go test ./... -count=1 -timeout 300s
cd web-pixi && bun test
cd examples/4node-basic/web && bun test
```

Bot-load scenario (manual):

1. `just distributed` with 4node-basic.
2. `bot spawn 30 cell_0_0`, player in cell_1_0.
3. Observe replica bots from across the border — **no perceptible stutter at 60fps**.
4. `cell split 0_0` and `cell merge 0_0` while bots are active — replicas remain smooth through the commit.
5. Triggered handoff (bot crosses into cell_1_0) — seam invisible at 60fps.

## Summary

This redesign replaces the Prepare → Overlap → Commit handoff protocol + server-side dead-reckoning / grace-extrapolation / shadow machinery with:

- One cluster-wide clock, maintained cheaply by coordinator broadcast.
- Per-entity authoritative-producer timestamps, carried opaquely through any forwarder.
- Hard-cut handoff at a specific cluster tick, no overlap.
- Client-side interpolation as the sole gap-filler.

Net effect: ~2,500 lines of server-side workaround code deleted. Client motion is smooth by design rather than by accumulated compensations. New game code added to mmokit inherits smooth cross-cell replica rendering for free.
