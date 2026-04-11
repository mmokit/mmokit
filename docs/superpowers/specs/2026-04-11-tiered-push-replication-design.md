# Tiered Push Replication + Co-Simulation Handoff — Design Spec

**Roadmap item:** #11 — Lightweight Cross-Node Proxies (reframed)
**Date:** 2026-04-11
**Branch:** `feature/tiered-push-replication`

## Context

The mmokit engine currently replicates entities across node boundaries by scanning every entity near a cell border and pushing its full state to neighboring nodes every tick. A toggle exists (`cfg.ProxiesEnabled`) to swap full ~80–120 B replicas for 29 B summaries, but auto-promotion from summary to full state is not wired, and entity handoff between nodes is cold — the destination receives the entity in a single transfer message and begins running it without any warmup. No inter-node bandwidth metrics exist, and there are no 2+ node integration tests exercising the border-replication or handoff paths.

Two problems follow from this. First, bandwidth scales poorly: every border entity is replicated regardless of whether any player on the neighbor node can see it, so a crowd of NPCs sitting near a cell edge pays the full cost even when no one is watching. Second, dynamic cell splits (roadmap #7) currently incur a cold-start stall on the destination node because handoff is instant and the destination has no pre-warmed spatial grid or baseline state.

Roadmap item #11 originally described the fix as "border summaries + on-demand detail fetch + overlap handoff." Research into industry practice shows that on-demand pull for ongoing updates is the Colyseus (2005) design, and the Colyseus authors themselves warned that it stalls the game loop. Every production system built since — Unreal Iris, Improbable SpatialOS, BigWorld, Valve Source — uses push with prioritization. Pull is used only for first-time discovery, never for updates. The industry name for the middle phase of overlap handoff is **co-simulation** (Improbable), not "overlap."

This spec therefore **reframes #11** as tiered push replication + co-simulation handoff, built atop the same tier mechanism already delivered for client-facing replication in Feature #8. The existing client dispatcher and a new inter-node dispatcher become parallel consumers of a single shared replication primitives package. Neighbor nodes are treated as another class of viewer.

## Scope Split with #12

This spec delivers the protocol, wire format, in-process implementation, and a loopback test harness with configurable artificial latency and loss. It does **not** deliver a real TCP or gRPC transport — that remains the scope of roadmap #12 (Connection Migration Protocol). The design is constrained so that when #12 lands, it drops in a new `NodeBridge` implementation without touching game code.

Dual-transport is a first-class constraint: the design is modeled as if every send crosses the network, and in-process is an optimization of the same protocol rather than a shortcut.

## Design Principles

1. **Push, not pull, for ongoing updates.** On-demand fetch is removed from the NodeBridge. The only role for pull is first-time cell discovery, which is handled by `HandoffPrepare` carrying a full snapshot — so no dedicated pull RPC is needed.
2. **Neighbor nodes are viewers.** Client connections and neighbor nodes both implement the same `Viewer` interface. The shared replication primitives package does not know or care which one it is serving.
3. **Warm before you write.** No node takes authoritative ownership of an entity without having received full-rate updates for it for at least `MinWarmupTicks` frames beforehand.
4. **Serialize for measurement and tests, but not for production in-process.** The wire format exists as a first-class type. Metrics compute size without full encode. The loopback test harness forces encode/decode to exercise the wire path.
5. **No backward-compatibility shims.** The old `ReplicaFrame`, `ProxySummary`, pull RPCs, and ghost-based server authority are deleted. Ghost component stays for client-side last-known-position rendering only.
6. **Meshing is invisible to the client.** Every user-observable behavior during cell crossing, handoff, cell split, or topology change must be indistinguishable from normal in-cell play. See Transparency Guarantees below.

## Transparency Guarantees

The end-state goal is that the client cannot observe cell boundaries or server meshing. Specifically, during normal gameplay and handoff events, the following must be invisible:

| Event | Client observation |
| --- | --- |
| Entity in its own cell | Normal replication frames |
| Entity approaches cell boundary | Normal frames; no change |
| Entity crosses into neighbor cell | Normal frames; no gap, no pop-in, no visual discontinuity |
| Local player crosses into neighbor cell | No reconnect, no input loss, no visual jump; HUD shows no indication of node change |
| Cell split (dynamic partitioning) | No visible impact on existing entities |
| Adjacent entity crosses from another cell | Standard entity-enter-AoI event, same as a fresh spawn |

The mechanisms that enforce each guarantee:

1. **No gap in entity streams during handoff.** The client's replication baseline is transferred from the old owner to the new owner as part of the handoff blob. The new owner resumes delta-encoding against the same baseline the client already acknowledged. The client's decoder sees an uninterrupted stream. See "Client Baseline Continuity" below.
2. **No pop-in at tier boundaries.** Soft visibility via the border tier means entities are tracked by the neighbor node before they become client-visible. When the client's AoI query returns the entity for the first time, the neighbor node already has a warm baseline ready.
3. **No input loss during player handoff.** The Coordinator's connection proxy retains the player's WebSocket connection across handoffs. See "Input Routing During Handoff" below.
4. **No visible handoff flap.** Commit cooldown prevents rapid re-handoff: after a commit, the entity cannot be handed back for at least `CrossingCooldownTicks` frames. See "Crossing Hysteresis" below.
5. **Topology changes are informational.** `SE_CELL_TOPOLOGY` events drive debug overlays only; entity streams are unaffected.

## Architecture

The replication layer separates into three units:

```text
pkg/replication/                 (NEW — shared primitives)
  tier.go                        — ReplicationTier, tier evaluation
  priority.go                    — per-viewer priority accumulator
  baseline.go                    — per-viewer acknowledged snapshot store
  frame.go                       — Frame type (encode/decode/size)
  viewer.go                      — Viewer interface
  dispatcher.go                  — per-viewer entity walk + frame build

pkg/system/replication.go        — client dispatcher (consumer)
pkg/universe/border_replication.go  (NEW) — inter-node dispatcher (consumer)
pkg/universe/handoff.go          (NEW) — co-sim authority state machine
pkg/universe/loopback_bridge.go  (NEW) — test-only NodeBridge with latency/loss
```

Each dispatcher owns its viewer loop and trust semantics. Both call into `pkg/replication/` for per-entity work. The target architecture's "replication as an independent layer" goal lands here.

## Shared Primitives (`pkg/replication/`)

### Viewer Interface

```go
type Viewer interface {
    // ID — stable identifier (player conn ID or neighbor node ID).
    ID() uint64

    // Position — world-space position used for tier distance checks.
    // For a neighbor node, this is the midpoint of the shared cell boundary.
    Position() (x, y float32)

    // Tier — per-entity-type replication tier for this viewer.
    Tier(entityKind uint16) ReplicationTier

    // Baselines — per-entity acknowledged snapshot store.
    Baselines() *BaselineStore

    // Send — dispatcher hands a Frame to the viewer for delivery.
    // Production in-process path keeps the Frame as a struct.
    // Network transport calls frame.Encode() inside its own Send impl.
    Send(frame Frame)
}
```

### Frame

```go
type Frame struct {
    ViewerID    uint64        // target viewer
    SenderNode  uint32        // source node (for metrics)
    Tick        uint64        // monotonic send tick
    Entries     []FrameEntry  // per-entity deltas
}

type FrameEntry struct {
    NetID    component.NetworkID  // includes authority epoch
    Kind     uint16               // entity kind
    DeltaBuf []byte               // quantize-encoded delta payload
}

func (f *Frame) Encode() []byte    // wire format, wraps quantize.FrameEncoder
func (f *Frame) SizeEncoded() int  // cheap size computation, no encode
func DecodeFrame(buf []byte) (Frame, error)
```

Production in-process `NodeBridge.Send` passes the `Frame` struct directly to the destination's inbox. No encode. Metrics count bytes via `SizeEncoded()` during the send, which is a fast loop over entries summing header + per-entry header + `len(DeltaBuf)`. The loopback test bridge explicitly calls `Encode`/`Decode` on both sides to exercise the wire format even in-process.

### ReplicationTier (extended)

```go
type ReplicationTier struct {
    Radius           float32  // existing — AoI cutoff
    UpdateDivisor    int      // existing — send every Nth tick
    BaseWeight       float32  // existing — priority accumulator weight

    PromoteRadius    float32  // NEW — within this, escalate to full rate
    PromoteLookahead int      // NEW — ticks of velocity-projection lookahead
}
```

Defaults: `PromoteRadius = Radius * 0.5`, `PromoteLookahead = 10`. Games override via `EntityKindDef.ReplicationTier`.

### BaselineStore

Reuses the existing `AckReliable` and `AckExplicit` modes from `pkg/quantize/`. The only change is that the store is now a primitive in `pkg/replication/` rather than embedded in `pkg/system/replication.go`. Moved file: `pkg/system/baseline.go` → `pkg/replication/baseline.go`. Client replication system updates its import.

### Dispatcher

```go
type Dispatcher struct {
    primitives *Primitives  // tier + priority + baselines + frame builder
}

// Walk builds a Frame for one viewer by iterating candidate entities.
func (d *Dispatcher) Walk(
    viewer Viewer,
    candidates iter.Seq[EntityRef],
    tick uint64,
) Frame
```

`EntityRef` is a lightweight handle the dispatcher uses to fetch current state and hash without depending on the caller's ECS layout. Client dispatcher supplies refs from player-AoI queries; border dispatcher supplies refs from cell-border scans.

## Wire Format over NodeBridge

Three new `NodeMessage` types replace the old replica/proxy/detail-fetch messages:

### `MsgBorderFrame`

Carries a `Frame` from node A to node B covering entities tier-visible to B. Emitted once per tick per neighbor per sending node.

**Quantize layer changes:** The existing `FrameHeader` in `pkg/quantize/wireformat.go` is already generic (`Tick, Seq, FullCount, DeltaCount, RemovedCount, ExitedCount`) — no viewerX/viewerY is stored in it today, so no header change is required. The only extension needed is carrying the authority epoch per entry: `FullEntry` and `DeltaEntry` each gain a new `Epoch uint32` field alongside their existing `NetID uint32`. Encoder and decoder gain 4 bytes per entry. Callers build entries from `component.NetworkID` values directly.

### `MsgHandoffPrepare`

```go
type HandoffPreparePayload struct {
    NetID           component.NetworkID   // NEW epoch already assigned
    Kind            uint16
    TransferBlob    []byte                // reuses existing TransferFrame format from pkg/universe/transfer.go
    ClientBaselines []ClientBaselineEntry // baseline handover payload (see below)
    ExpectedTick    uint64                // predicted crossing tick (informational)
    OldEpoch        uint32                // previous epoch — lets receiver validate the bump
}

type ClientBaselineEntry struct {
    ConnID      uint32               // player connection ID
    EntityNetID component.NetworkID  // which entity this baseline is for
    LastAcked   []byte               // acknowledged snapshot bytes
    LastTick    uint64               // tick of last ack
}
```

Sent on `Border → Promoted` transition. Seeds the destination's baseline store with a full snapshot so subsequent delta frames have a baseline to delta against. The destination does not take authority yet; it simulates a read-only shadow copy.

**Entity snapshot format:** reuses the existing `TransferFrame` binary format from `pkg/universe/transfer.go` (currently used for cold handoff). No new serializer. The handoff state machine deserializes the blob into a shadow entity on receipt; when `MsgHandoffCommit` arrives, the shadow is promoted to the authoritative local entity in place — no re-spawn.

### Client Baseline Continuity (Baseline Migration)

**Industry context:** Research into published engine practice (SpatialOS, Unity DOTS Netcode, O3DE, Unreal Iris, Star Citizen) found that no major engine publishes an explicit solution for migrating per-client delta baselines across authority transfer. Three strategies are documented:

1. **Architectural dodge** (Star Citizen "Replication Layer"): client sockets and per-entity state live on an intermediary tier; sim servers push updates *to* that tier, and handoff is invisible because no client-facing state moves.
2. **Accept the pop** (Unity DOTS, O3DE, SpatialOS): delta compression resets at the handoff boundary; the new owner re-sends a full keyframe. SpatialOS provides an `authority loss imminent` callback to let the outgoing worker flush final updates, but the incoming worker has no per-client baseline continuity.
3. **Avoid the handoff entirely** (Unreal Iris, Epic's "100 players" writeups): scale vertically and keep one server authoritative.

mmokit's architecture places both client sockets and sim state on the same sim servers, so (1) is not available without a much larger refactor. We reject (2) because the visible pop breaks the transparency guarantee, and (3) is incompatible with the meshing goal. This spec therefore implements **baseline migration** — an optimization we believe is sound and unpublished elsewhere. Called **"baseline handover"** in code and docs.

**The two migration cases.** The design has to handle two distinct scenarios that both arise in this system because players ARE entities:

#### Case A: Non-player entity handoff (e.g., NPC ship crosses boundary)

Other clients C1, C2, … have the NPC in their AoI and have acknowledged per-entity baselines stored on the old owner (the sender of their frames). When authority transfers, the new owner becomes the sender. For each affected client, the per-entity baseline must migrate so the new owner's first frame can be a delta against it.

The owner packs the affected clients' baselines for *this one entity* into `HandoffPreparePayload.ClientBaselines` keyed by `ConnID`. The new owner inserts these into its per-connection `BaselineStore` during shadow setup.

#### Case B: Player handoff (player's own ship crosses boundary)

The player's connection migrates from old owner to new owner via the existing `Coordinator` routing update. From the player's perspective, their *whole* AoI view migrates: dozens to hundreds of per-entity baselines were stored on the old owner, and the new owner needs all of them to avoid a keyframe burst.

For player handoffs, `HandoffPreparePayload.ClientBaselines` carries the player's *entire* per-entity baseline store — not just the baseline for their own ship. This is a larger blob (~10 KB for a player with 100 entities in view) but still negligible for an event that fires at most once per cell crossing.

**Unified wire format.** A single `ClientBaselines []ClientBaselineEntry` field handles both cases. Each entry is `(ConnID, EntityNetID, LastAcked, LastTick)`. Case A fills it with N entries for N subscribed clients × 1 entity. Case B fills it with 1 client × M entities (where M is the player's AoI size). The state machine decides which case applies based on whether the migrating entity has a `PlayerConn` component.

**Stale baseline pruning.** Between `HandoffPrepare` and `HandoffCommit`, clients may continue to ack newer snapshots from the old owner. On commit, the old owner piggybacks any baseline diffs onto the `HandoffCommitPayload` so the new owner converges before its first authoritative frame is emitted. This is a small delta — typically a single (ConnID, EntityNetID, LastTick) update per affected baseline.

**Correctness.** If a client has no baseline for an entity (just entered AoI mid-handoff), its entry is absent from `ClientBaselines` and the new owner simply emits a fresh keyframe for that client on its first authoritative tick. Same as a normal AoI-enter event.

**Sender-equivalence invariant.** Baselines are migratable only because both old and new owners hash entity state identically — frames from either sender are byte-equivalent for the same entity state. The shared `pkg/replication/` primitives guarantee this by centralizing hash, quantize, and frame-build logic. Games must not add sender-specific state to frame payloads.

**Future direction.** A Star Citizen-style replication layer (client connections and baselines live on a dedicated tier separate from sim servers) is a cleaner long-term solution. It is out of scope for this spec and roadmap item #12 — it would be a fifth scale-out feature. Noted here for future planning.

### `MsgHandoffCommit`

```go
type HandoffCommitPayload struct {
    NetID      component.NetworkID // new-epoch ID
    CommitTick uint64              // authoritative commit tick
}
```

Sent by the old owner when the entity physically crosses the boundary and `MinWarmupTicks` have elapsed. **Fire-and-forget** — no ack. The old owner increments its local epoch and downgrades the entity to a replica at the moment of send; the new owner takes the writer role on receive. Rationale: the reliable ordered NodeBridge channel guarantees delivery, and the warmup floor plus the `(Epoch, Tick)` monotonic stamp make duplicate or delayed commit packets harmless. An ack-based protocol would add a round trip that buys nothing in-process and hurts in a future network transport.

### Deleted NodeBridge Messages and Methods

- `MsgReplica`, `MsgProxySummary`, `MsgDetailRequest`, `MsgDetailResponse`
- `ReplicaFrame`, `ProxySummary` types
- `NodeBridge.RequestDetail`, `NodeBridge.SendDetailResponse`
- `cfg.ProxiesEnabled` config field

## Entity Identity: `NetworkID.Epoch` Field

`NetworkID` is already a struct in `pkg/component/core.go`:

```go
// Before
type NetworkID struct {
    ID uint32
}

// After
type NetworkID struct {
    ID    uint32 // per-node-range stable ID (unchanged)
    Epoch uint32 // NEW — increments on each successful authority handoff
}
```

### Semantics

- **Allocation:** `net_id_alloc.go` allocates `ID` from the per-node range as today. `Epoch` starts at 0 on spawn.
- **Stale-packet drop:** Receivers maintain a `map[uint32]uint32` from `ID → highest-seen epoch`. On decode, if `frame.Epoch < local[frame.ID]`, the frame entry is discarded.
- **Handoff:** On commit, the old owner atomically increments its local epoch for the ID and sends `MsgHandoffCommit` with the new epoch. The new owner's handoff state machine has already recorded the new epoch from `MsgHandoffPrepare`.
- **Wire cost:** +4 bytes per entity reference. Negligible with delta compression; entity references are usually in the header, not per-entry.

### Client SDK Impact

- `cmd/sdkgen` reads component layouts via Go reflection. The generated JSON schema gains an `epoch uint32` field on `NetworkID` entries, followed by regeneration of the TypeScript client.
- The generated TypeScript decoder reads the field but may discard its value — the client only ever sees one authoritative stream per entity and doesn't need stale-packet detection. The field is present for wire-format parity between client dispatcher and border dispatcher.
- Regeneration: `just client-sdk examples/4node-basic` + `just client-sdk examples/slither`; `npx tsc --noEmit` must pass on both web clients.

## Promotion + Co-Simulation State Machine

Per `(entity, neighbor)` pair, one of four states:

```text
Unseen    — outside neighbor's tier, nothing sent
Border    — within tier Radius, low-rate delta updates (UpdateDivisor > 1)
Promoted  — within PromoteRadius OR velocity-projected to cross
            within PromoteLookahead ticks; full-rate updates;
            warmup counter incrementing
Handoff   — actual crossing has occurred; committing on next tick
            once warmup floor is satisfied
```

### Transitions

- **`Unseen → Border`.** Entity enters neighbor's tier radius. State record created. Delta frames begin flowing at the divisor rate.
- **`Border → Promoted`.** Hybrid trigger fires: either the entity's current position is within `PromoteRadius` of the neighbor's owned cell, or its velocity-projected position at `tick + PromoteLookahead` lies inside. On transition, the owner sends `MsgHandoffPrepare` with a full snapshot and incremented epoch. Warmup counter starts at 0.
- **`Promoted → Handoff → committed`.** When the entity's position is actually inside the neighbor's cell AND `warmupCounter >= MinWarmupTicks` AND the entity is not in a commit cooldown window, the owner emits `MsgHandoffCommit`. On send, the owner increments its local epoch for the ID, marks the entity as a replica locally, and stops scheduling writes. On receive, the new owner takes the writer role and starts a commit-cooldown timer.
- **Teleports.** If an entity jumps across the boundary before `warmupCounter >= MinWarmupTicks`, commit is delayed. The entity visually stays with the old owner for up to `MinWarmupTicks / TickRate = 250 ms`. This is the rare path and accepts the stall to avoid the Colyseus-style cold-start the research warned about.
- **Retreats.** If an entity in `Promoted` moves back outside `PromoteRadius` and its trajectory no longer enters the neighbor, it transitions back to `Border`. Baselines are retained to avoid re-sending a full snapshot if it re-promotes soon.

### Crossing Hysteresis

After a successful `HandoffCommit`, both the old owner (now replica) and the new owner (now authoritative) enter a `CommitCooldown` sub-state for `CrossingCooldownTicks = 20` frames (1 s at 20 Hz). During cooldown:

- Even if the entity retreats back into the old owner's cell, no new handoff is initiated back. The old owner continues receiving frames from the new owner as a normal border replica.
- The new owner runs the entity normally and ignores any demotion/handoff triggers that would fire on the entity for the cooldown duration.
- After cooldown expires, normal state transitions resume.

Rationale: prevents thrash for entities hovering exactly on a cell boundary. Without this, a ship oscillating across the boundary would burn CPU on handoff churn and emit an epoch bump per crossing, blowing the stale-packet detector's ability to distinguish real stale frames from rapid legitimate bumps. Standard flapping-prevention pattern borrowed from network routing protocols (BGP route flap damping, OSPF holddown).

### Input Routing During Handoff

The Coordinator owns all client WebSocket connections and proxies inputs to the authoritative node. Handoff affects this routing as follows:

- **Before commit:** inputs route to old owner (authority).
- **At commit:** the old owner's `HandoffCommit` send path calls `Coordinator.UpdatePlayerRoute(connID, newNodeID)` before marking the entity as a replica. The routing table update is protected by `Coordinator.mu`. The new owner's handoff state machine has been buffering a warm shadow copy for at least `MinWarmupTicks` frames and is ready to process inputs immediately.
- **After commit:** inputs route to new owner.

When the entity being handed off is a player's own ship, the WebSocket connection never closes. The client sees no protocol interruption. The server merely re-routes input packets internally.

**Single-tick overlap window.** There is a one-tick theoretical window in which an input frame could arrive at the old owner after the routing table has updated but before the old owner has fully downgraded. Policy: the old owner forwards such frames to the new owner via `MsgForwardInput` rather than processing them. This forwarding path is a one-liner and only exists for safety; in practice the tick boundary dominates.

### Tunables

```go
const MinWarmupTicks        = 5   // 250 ms at 20 Hz — fixed, not per-tier
const CrossingCooldownTicks = 20  // 1 s at 20 Hz — fixed, not per-tier
```

Rationale for not making these per-tier: both floors exist to defend protocol-level guarantees. Per-tier tuning would let games accidentally defeat the guarantees.

## Border Dispatcher Loop

Runs in `PostSystems` on each tick, after game systems have mutated entity state:

1. **For each neighbor node N:**
   1. Construct neighbor viewer (position = midpoint of shared cell boundary).
   2. Build candidate entity iterator: all local entities within `max(tierRadius)` of the shared boundary.
   3. **For each candidate entity E:**
      - Look up `(E, N)` state machine entry.
      - Evaluate tier for `(E.kind, N)`. If distance > tier radius, drop state to `Unseen` and skip.
      - If divisor-skip tick for current state, skip (increment warmup counter if promoted).
      - Check promotion trigger → possibly transition `Border → Promoted`, emit `MsgHandoffPrepare`.
      - Build delta frame entry via `pkg/replication/` primitives.
      - Append to N's per-tick frame buffer.
   4. Flush one `MsgBorderFrame` to N if the buffer has entries.
2. **Handoff sweep:** walk promoted entities, check boundary crossing + warmup floor, emit `MsgHandoffCommit` as appropriate.

All state tables, baseline stores, and frame buffers are per-neighbor and reused across ticks. No per-tick allocations after warmup.

## Dual-Transport Parity

The production in-process `NodeBridge` (`pkg/universe/bridge.go` implementation over Go channels) passes `Frame` and handoff payload structs directly to the destination inbox. No encode. Destination pops the struct and routes it into the dispatcher's `ApplyBorderFrame` or handoff handler.

Metrics count bytes via `Frame.SizeEncoded()` and equivalent cheap size calls on handoff payloads, inserted at the send boundary. This gives byte counts matching what a real network transport would carry, without paying the encoding cost.

The loopback test bridge (below) forces encode/decode on both ends. This is how we guarantee the wire format works correctly even though production never exercises it in-process.

When #12 delivers a real TCP or gRPC `NodeBridge`, its `Send` implementation calls `Frame.Encode()` to get the wire bytes. Zero game-code changes. The shared primitives, state machine, and dispatcher do not need to know which bridge is in use.

## Loopback Test Harness

New file: `pkg/universe/loopback_bridge.go`.

```go
type LoopbackBridge struct {
    NodeA, NodeB *Node
    LatencyMs    int      // artificial delay before delivery
    LossRate     float32  // 0.0 to 1.0; drops are silent
    rand         *rand.Rand
}

func NewLoopbackBridge(a, b *Node, opts LoopbackOpts) *LoopbackBridge
```

Implementation routes messages through an in-memory ring buffer with a timer. On `Send`, the bridge calls `Frame.Encode()` (or the payload equivalent), enqueues the bytes with a deliver-at timestamp, and periodically dequeues and calls `DecodeFrame` on the receiving side before forwarding into the destination inbox. This exercises the full encode/decode path and gives tests a way to inject latency and loss.

### Correctness Tests

File: `pkg/universe/border_replication_test.go`.

1. **`TestMesh_BorderReplication_DeltaSize`** — spawn 100 NPCs near a 2-node boundary with no player on the neighbor side. Assert that `NodeMetrics.InterNodeBytesSent` per tick drops at least 60% compared to the pre-refactor baseline recorded in a golden file.
2. **`TestMesh_Handoff_NoStall`** — spawn a moving ship, let it cross the boundary, assert:
   - Destination has received at least `MinWarmupTicks` full-rate frames for the entity before `MsgHandoffCommit` arrives.
   - Destination's `NetIDToEntity` lookup for the entity succeeds on the first post-commit tick.
   - No ghost component is required for continuity.
3. **`TestMesh_StalePacketDrop`** — after handoff, inject a frame from the old owner with the pre-commit epoch. Assert the receiver drops it and increments `StalePacketsDropped`.
4. **`TestMesh_Teleport_WarmupFloor`** — teleport an entity across the boundary with no approach trajectory. Assert commit is delayed by exactly `MinWarmupTicks`, during which the entity visually stays with the old owner.
5. **`TestMesh_CrossingHysteresis_NoFlap`** — push an entity back and forth across the boundary every tick. Assert that at most one handoff commit occurs per `CrossingCooldownTicks` window.
6. **`TestMesh_BaselineHandover_NonPlayerEntity`** — Case A. Spawn 5 fake client connections subscribed to an NPC ship, cross the NPC across the boundary, assert that the first authoritative frame from the new owner to each client is a delta (not a keyframe) and that the delta decodes correctly against the client's pre-handoff baseline.
7. **`TestMesh_BaselineHandover_PlayerEntity`** — Case B. Spawn a player with 50 entities in their AoI, cross the player across the boundary, assert that the first client frame from the new owner contains zero keyframes (all deltas) for the entities that were already in view, and that total bytes sent in that first frame are within 10% of steady-state frame size.
8. **`TestMesh_PlayerHandoff_NoInputLoss`** — spawn a player entity straddling the boundary with a steady input stream, cross the boundary, assert that every input frame is processed exactly once by the authoritative node and none are dropped in the overlap window.

All tests construct the mesh via `LoopbackBridge`, so they run in one process but exercise the full wire path.

### Performance Tests

File: `pkg/universe/border_replication_perf_test.go`. These are Go benchmarks + assertion tests that measure characteristics under load and fail CI on regressions relative to golden baselines.

1. **`BenchmarkBorderReplication_ScalingByEntityCount`** — measure `InterNodeBytesSent` per tick and dispatcher wall-clock time at N = 10, 100, 500, 1000 border entities. Assert:
   - Bandwidth per entity scales within ±10% of linear (O(n), not O(n²))
   - Dispatcher time stays under 5 ms per tick at N = 1000 (10% of the 50 ms tick budget)
2. **`BenchmarkHandoff_LatencyUnderLoad`** — with 500 background border entities churning, measure wall-clock time from `Border → Promoted → Handoff → Committed` for a test entity. Assert:
   - P50 commit latency within `MinWarmupTicks + 1` ticks of the boundary crossing
   - P99 commit latency within `MinWarmupTicks + 3` ticks
3. **`BenchmarkBorderReplication_Churn`** — 200 entities rapidly entering, leaving, and re-entering the border tier. Assert:
   - No GC pressure spike (heap allocations per tick stay within 2× baseline)
   - Per-entity state table size does not exceed `2 * PeakConcurrentBorderEntities` (no unbounded growth from churn)
4. **`BenchmarkBorderReplication_WithLossAndLatency`** — 100 entities, loopback bridge with 10 ms latency + 5% packet loss. Assert:
   - Bandwidth increase due to retransmits stays under 20%
   - All correctness tests still pass under these conditions
5. **`BenchmarkClientFrameSize_NoRegression`** — measure the client-facing frame size for a steady-state player in a 3x3 mesh and assert it stays within 2% of the pre-change baseline. Guards against the primitives extraction accidentally bloating client replication.
6. **`BenchmarkHandoffBlobSize_CaseA`** — measure non-player-entity `HandoffPreparePayload` size as a function of the number of clients subscribed. Assert the per-client baseline overhead stays under 150 bytes.
7. **`BenchmarkHandoffBlobSize_CaseB`** — measure player `HandoffPreparePayload` size as a function of the player's AoI entity count (10, 50, 100, 200). Assert the total blob stays under 50 KB at 200 entities and scales linearly with entity count.

Baseline numbers are captured in a golden file (`pkg/universe/testdata/border_replication_perf.golden`) updated manually only when intentional regressions are justified. CI runs these on every PR.

## Metrics

New counters on `pkg/metrics/NodeMetrics`, exposed via the existing `/metrics` Prometheus endpoint:

| Counter | Description |
| --- | --- |
| `InterNodeBytesSent` | Total bytes sent to all neighbors (per-neighbor label) |
| `InterNodeBytesRecv` | Total bytes received from all neighbors (per-neighbor label) |
| `BorderFramesSent` | Number of `MsgBorderFrame` sent |
| `HandoffsInitiated` | Number of `Border → Promoted` transitions |
| `HandoffsCommitted` | Number of successful `HandoffCommit` events |
| `StalePacketsDropped` | Frames/entries dropped due to stale epoch |
| `EntitiesInBorder` | Current count of `(entity, neighbor)` pairs in `Border` state |
| `EntitiesInPromoted` | Current count of `(entity, neighbor)` pairs in `Promoted` state |
| `EntitiesInHandoff` | Current count of `(entity, neighbor)` pairs in `Handoff` state |

## Migration / Delete List

No backward-compat shims, per project policy. The following are removed entirely in this change:

- `pkg/universe/replication_scan.go` — replaced by border dispatcher
- `ReplicaFrame`, `ProxySummary` types from `pkg/universe/replication.go`
- `MsgReplica`, `MsgProxySummary`, `MsgDetailRequest`, `MsgDetailResponse` message types
- `RequestDetail`, `SendDetailResponse` from the `NodeBridge` interface
- `cfg.ProxiesEnabled` config field
- Ghost-based server authority paths: `TickGhosts`, `RemoveGhostByNetID` as a server-authority mechanism in `pkg/universe/world_base.go`. The `Ghost` component type stays — the client-side "last-known-position" rendering is unaffected — but the server no longer uses it to bridge authority transitions. Handoff state machine replaces this role.
- Tests in `pkg/universe/universe_test.go` referencing deleted types, replaced by the eight correctness tests and seven performance benchmarks above.

### Game Updates

Game code changes are minimal. Existing `NetworkID{ID: x}` struct literals continue to work unchanged — the new `Epoch` field defaults to zero. No call site reads or writes `Epoch` directly; that field is managed by `pkg/universe/` handoff code.

The only game-facing break is the removal of the `cfg.ProxiesEnabled` toggle and the NodeBridge `RequestDetail`/`SendDetailResponse` methods (neither has any caller in games today — they are infrastructure hooks).

No changes to spawn logic, ECS queries, or game systems.

## Verification

- `go vet ./...` clean
- `go test ./...` clean, including all correctness and performance tests
- `go test -bench=. -benchmem ./pkg/universe/...` — all benchmarks complete under their CI thresholds
- `cd examples/slither && just dev` — web client connects, no visible pop-in at cell borders during normal gameplay
- `cd examples/4node-basic && just dev` — meshed demo runs, dynamic cell split still works, debug topology overlay unchanged
- `just client-sdk examples/4node-basic` — regenerates with new NetworkID schema, `npx tsc --noEmit` passes on both `examples/4node-basic/web/` and `examples/slither/web/`
- Metrics snapshot on `examples/4node-basic` under light load: `InterNodeBytesSent` per tick reduced 60%+ versus pre-change baseline for unobserved entities, within 10% for observed-and-moving entities
- **Transparency smoke test:** in `examples/4node-basic`, pilot a ship repeatedly across cell boundaries while recording client frames. Confirm zero visible stalls, zero pop-ins, zero HUD indication of node changes, and zero input drops. The client must be indistinguishable from a single-node build.
- **Flap smoke test:** hover a ship exactly on a cell boundary for 30 seconds. Confirm `HandoffsCommitted` metric increments at most once per second (bounded by `CrossingCooldownTicks`).

## Critical Files

### New Files

- `pkg/replication/tier.go`
- `pkg/replication/priority.go`
- `pkg/replication/baseline.go` (moved from `pkg/system/baseline.go`)
- `pkg/replication/frame.go`
- `pkg/replication/viewer.go`
- `pkg/replication/dispatcher.go`
- `pkg/universe/border_replication.go`
- `pkg/universe/handoff.go`
- `pkg/universe/loopback_bridge.go`
- `pkg/universe/border_replication_test.go`
- `pkg/universe/border_replication_perf_test.go`
- `pkg/universe/testdata/border_replication_perf.golden`

### Modified Files

- `pkg/universe/node_bridge_impl.go` — hook new dispatcher in `PostSystems`, remove old replica scan hook
- `pkg/universe/world_base.go` — remove ghost-based authority, add handoff state machine storage
- `pkg/universe/transfer.go` — `TransferFrame.NetworkID` currently a bare `uint32`; either change to `component.NetworkID` or add a separate `Epoch uint32` field so the handoff path can carry the epoch
- `pkg/universe/message.go` — new `MsgBorderFrame`, `MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgForwardInput`, remove deleted messages
- `pkg/universe/bridge.go` — remove `RequestDetail`/`SendDetailResponse` methods
- `pkg/universe/coordinator.go` — `UpdatePlayerRoute` helper for handoff-time routing updates
- `pkg/universe/net_id_alloc.go` — epoch allocation on handoff
- `pkg/system/replication.go` — refactor as consumer of `pkg/replication/` primitives
- `pkg/quantize/wireformat.go` — add `Epoch uint32` to `FullEntry` and `DeltaEntry`; encoder/decoder append/read the extra 4 bytes per entry
- `pkg/component/core.go` — add `Epoch` field to `NetworkID`
- `pkg/metrics/node_metrics.go` — new inter-node counters
- `cmd/sdkgen/` — NetworkID schema reflection update
- `internal/game/`, `examples/slither/`, `examples/4node-basic/` — regenerated client SDKs only; no Go code changes expected

### Deleted Files

- `pkg/universe/replication_scan.go`

## Industry Best Practice Alignment

Each major design element, cross-referenced with published industry practice. The goal is to make clear where we follow precedent and where we extend or innovate, so future contributors can judge which pieces are load-bearing.

| Design element | Precedent | Notes |
| --- | --- | --- |
| Push-based per-viewer replication with priority | Unreal Iris, Unity DOTS Netcode, Valve Source | "Importance scaling" (DOTS), "Replication fragments" (Iris), delta snapshots against acknowledged baseline (Source). The Colyseus (2005) pull design is explicitly rejected per its own authors' warning. |
| Viewer abstraction over clients and neighbor servers | Unreal Replication Graph (per-connection nodes) | Unreal's `UReplicationGraph` takes arbitrary connection types; generalizing to "neighbor server is a connection" is a natural extension. |
| Per-type AoI radius + update divisor | Unity DOTS `GhostImportance` + `GhostRelevancy` | Direct parallel. |
| Priority accumulator | Gaffer on Games "Snapshot Compression", Halo Reach networking talks | |
| Dormancy for unchanged entities | Unreal Engine (built-in `NetDormancy`) | Direct parallel; already implemented in Feature #8. |
| Delta encoding against acknowledged baseline | Valve Source Engine, O3DE Multiplayer | Acknowledged baseline is the canonical technique. |
| Entity identity with authority epoch | Quake 3 `commandSequence`, Unity DOTS `SpawnTick`, target-architecture `AuthorityEpoch` | Stale-packet rejection via monotonic sequence is universal. |
| Co-simulation handoff (destination runs warm shadow before authority flip) | Improbable "co-simulation" blog post on distributing physics; SpatialOS `AuthorityLossImminent` 2-phase callback | The full "destination runs shadow copy for N ticks before commit" model is closest to Improbable's physics-distribution approach. |
| Crossing hysteresis / flapping prevention | BGP route flap damping (RFC 2439), OSPF holddown | Borrowed from network routing literature; not typically seen in game literature but the pattern is sound and the problem (oscillation at a boundary) is well-documented. |
| Trajectory-based promotion lookahead | Physics continuous collision detection (PhysX, Havok swept tests) | Not canonical in netcode. Sound extension by analogy to physics broadphase. Provides bandwidth savings over pure margin-based promotion by only firing when velocity is actually toward the boundary. |
| Client baseline continuity across handoff ("baseline handover") | **No published precedent.** Research across SpatialOS, Unity DOTS, O3DE, Unreal Iris, and Star Citizen found three strategies: architectural dodge (Star Citizen Replication Layer), accept-the-pop (Unity/O3DE/SpatialOS — full keyframe on handoff), or avoid-handoff (Unreal, scale vertically). None migrate per-client baselines. | mmokit-specific optimization filling a real gap. Correctness depends on the sender-equivalence invariant (shared hash/quantize logic across nodes). Long-term consideration: Star Citizen's replication layer is architecturally cleaner but out of scope — noted as future roadmap item beyond #12. |
| Connection proxy with stable session | SpatialOS worker frontend; Unreal seamless travel (dedicated-server limitation) | The Coordinator already does this for per-cell routing; handoff simply updates the routing table. |
| Loopback bridge with latency/loss injection | Unreal `Net PktLag`/`PktLoss` cvars; Unity Transport simulator pipeline | Standard industry practice. |
| Performance regression tests | Unreal Automation (ReplicationGraphTests), Unity DOTS Netcode Test Suite | Unit-test runtime throughput assertions are standard in high-performance netcode codebases. |

## Open Questions

None at spec time. All architectural decisions were locked in during brainstorming. Minor implementation details (exact field ordering in wire structs, specific hash functions for stale-packet detection, ring-buffer sizing in the loopback bridge) get resolved in the implementation plan.
