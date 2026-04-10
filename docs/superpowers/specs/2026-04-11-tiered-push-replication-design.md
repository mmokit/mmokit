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

**Frame header change in `pkg/quantize/wireformat.go`:** the header becomes opaque to the quantize layer. Today it hardcodes `viewerX, viewerY float32` for client consumption; this is moved into a caller-supplied `[]byte` written directly before the body. Client dispatcher writes the viewerX/Y bytes; border dispatcher writes a `ViewerNodeID uint32`. Quantize reads a length-prefixed blob and hands it to the caller's decoder. This is the only `pkg/quantize/` change required.

### `MsgHandoffPrepare`

```go
type HandoffPreparePayload struct {
    NetID        component.NetworkID // NEW epoch already assigned
    Kind         uint16
    TransferBlob []byte              // reuses existing TransferFrame format from pkg/universe/transfer.go
    ExpectedTick uint64              // predicted crossing tick (informational)
    OldEpoch     uint32              // previous epoch — lets receiver validate the bump
}
```

Sent on `Border → Promoted` transition. Seeds the destination's baseline store with a full snapshot so subsequent delta frames have a baseline to delta against. The destination does not take authority yet; it simulates a read-only shadow copy.

**Snapshot format:** reuses the existing `TransferFrame` binary format from `pkg/universe/transfer.go` (currently used for cold handoff). No new serializer. The handoff state machine deserializes the blob into a shadow entity on receipt; when `MsgHandoffCommit` arrives, the shadow is promoted to the authoritative local entity in place — no re-spawn.

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
- **`Promoted → Handoff → committed`.** When the entity's position is actually inside the neighbor's cell AND `warmupCounter >= MinWarmupTicks`, the owner emits `MsgHandoffCommit`. On send, the owner increments its local epoch for the ID, marks the entity as a replica locally, and stops scheduling writes. On receive, the new owner takes the writer role.
- **Teleports.** If an entity jumps across the boundary before `warmupCounter >= MinWarmupTicks`, commit is delayed. The entity visually stays with the old owner for up to `MinWarmupTicks / TickRate = 250 ms`. This is the rare path and accepts the stall to avoid the Colyseus-style cold-start the research warned about.
- **Retreats.** If an entity in `Promoted` moves back outside `PromoteRadius` and its trajectory no longer enters the neighbor, it transitions back to `Border`. Baselines are retained to avoid re-sending a full snapshot if it re-promotes soon.

### Tunables

```go
const MinWarmupTicks = 5  // 250 ms at 20 Hz — fixed, not per-tier
```

Rationale for not making this per-tier: the floor exists to defend against the stall scenario, which is a protocol-level guarantee. Per-tier tuning would let games accidentally defeat the guarantee.

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

### Integration Tests

File: `pkg/universe/border_replication_test.go`.

1. **`TestMesh_BorderReplication_DeltaSize`** — spawn 100 NPCs near a 2-node boundary with no player on the neighbor side. Assert that `NodeMetrics.InterNodeBytesSent` per tick drops at least 60% compared to the pre-refactor baseline recorded in a golden file.
2. **`TestMesh_Handoff_NoStall`** — spawn a moving ship, let it cross the boundary, assert:
   - Destination has received at least `MinWarmupTicks` full-rate frames for the entity before `MsgHandoffCommit` arrives.
   - Destination's `NetIDToEntity` lookup for the entity succeeds on the first post-commit tick.
   - No ghost component is required for continuity.
3. **`TestMesh_StalePacketDrop`** — after handoff, inject a frame from the old owner with the pre-commit epoch. Assert the receiver drops it and increments `StalePacketsDropped`.
4. **`TestMesh_Teleport_WarmupFloor`** — teleport an entity across the boundary with no approach trajectory. Assert commit is delayed by exactly `MinWarmupTicks`, during which the entity visually stays with the old owner.

All four tests construct the mesh via `LoopbackBridge`, so they run in one process but exercise the full wire path.

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
- Tests in `pkg/universe/universe_test.go` referencing deleted types, replaced by the four new integration tests above.

### Game Updates

Game code changes are minimal. Existing `NetworkID{ID: x}` struct literals continue to work unchanged — the new `Epoch` field defaults to zero. No call site reads or writes `Epoch` directly; that field is managed by `pkg/universe/` handoff code.

The only game-facing break is the removal of the `cfg.ProxiesEnabled` toggle and the NodeBridge `RequestDetail`/`SendDetailResponse` methods (neither has any caller in games today — they are infrastructure hooks).

No changes to spawn logic, ECS queries, or game systems.

## Verification

- `go vet ./...` clean
- `go test ./...` clean, including all four new integration tests
- `cd examples/slither && just dev` — web client connects, no visible pop-in at cell borders during normal gameplay
- `cd examples/4node-basic && just dev` — meshed demo runs, dynamic cell split still works, debug topology overlay unchanged
- `just client-sdk examples/4node-basic` — regenerates with new NetworkID schema, `npx tsc --noEmit` passes on both `examples/4node-basic/web/` and `examples/slither/web/`
- Metrics snapshot on `examples/4node-basic` under light load: `InterNodeBytesSent` per tick reduced 60%+ versus pre-change baseline for unobserved entities, within 10% for observed-and-moving entities
- Manual smoke: entity crossing a cell boundary in `examples/4node-basic` shows no visible stall or rubber-banding

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

### Modified Files

- `pkg/universe/node_bridge_impl.go` — hook new dispatcher in `PostSystems`, remove old replica scan hook
- `pkg/universe/world_base.go` — remove ghost-based authority, add handoff state machine storage
- `pkg/universe/message.go` — new `MsgBorderFrame`, `MsgHandoffPrepare`, `MsgHandoffCommit`, remove deleted messages
- `pkg/universe/bridge.go` — remove `RequestDetail`/`SendDetailResponse` methods
- `pkg/universe/net_id_alloc.go` — epoch allocation on handoff
- `pkg/system/replication.go` — refactor as consumer of `pkg/replication/` primitives
- `pkg/quantize/wireformat.go` — make header payload opaque (length-prefixed blob)
- `pkg/component/core.go` — add `Epoch` field to `NetworkID`
- `pkg/metrics/node_metrics.go` — new inter-node counters
- `cmd/sdkgen/` — NetworkID schema reflection update
- `internal/game/`, `examples/slither/`, `examples/4node-basic/` — regenerated client SDKs only; no Go code changes expected

### Deleted Files

- `pkg/universe/replication_scan.go`

## Open Questions

None at spec time. All architectural decisions were locked in during brainstorming. Minor implementation details (exact field ordering in wire structs, specific hash functions for stale-packet detection, ring-buffer sizing in the loopback bridge) get resolved in the implementation plan.
