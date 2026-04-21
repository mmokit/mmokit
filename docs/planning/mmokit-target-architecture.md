# mmokit Target Architecture

**Date**: 2026-03-27 (original); revised 2026-04-21 to reflect shipped work through State Integrity
**Status**: Reference architecture — guides roadmap priorities and design decisions
**Scope**: Long-term target for mmokit's server meshing, replication, and networking layers

## Terminology

| Term | Definition |
|------|-----------|
| **Cell** | Fixed-size spatial partition of the world grid. The unit of spatial addressing. All cells are the same size. Formerly called "sector" in the codebase (rename tracked in roadmap). |
| **Node** | A simulation process owning one or more cells. The unit of compute. A node runs its own ECS world and game loop. |
| **Gateway** | Session frontend that clients connect through. Routes traffic to the correct node. |

**Key design decision:** Cells and nodes have a **many-to-one** relationship. A node owns 1+ cells. This decouples the spatial grid from compute assignment:

- **Sparse worlds:** One node runs 20 empty cells while a hot area gets one cell per node.
- **Dynamic partitioning:** "Split" means reassigning cells from an overloaded node to a new one — cells don't change size, just ownership. "Merge" is the reverse.
- **Startup flexibility:** Dev mode runs 1 node owning all cells. Production distributes cells by expected load. Same grid config either way.
- **Boundary simplification:** Entity transfer only triggers when crossing into a cell owned by a *different node*. Crossing between cells on the same node is just a coordinate update — no serialization, no ghost, no handoff.

## Overview

This document captures the target architecture for mmokit based on research into modern engine practice (Unity DOTS netcode, Unreal Iris/Replication Graph, O3DE, Valve Source) and distributed game system design. It describes the end state we're building toward — not what exists today.

**Core principle:** Treat meshing as an authority-routing problem, not as "multiple servers simulating the same world equally."

**Design:** Authoritative spatial server mesh + single-writer per entity + read-only replicas + interest-driven delta snapshots + overlap-based handoff + client prediction for owned entities.

---

## Three-Layer Architecture

The target architecture separates three concerns that are currently interleaved:

### 1. Simulation Mesh

- World space split into fixed-size cells
- Each node owns one or more cells and runs a single ECS world covering all of them
- Each entity has exactly one authoritative owner node
- **Current state (2026-04-21):** Shipped as `Process` (renamed from `Coordinator`) + `Host` + `Cell` with 1:N host-to-cell mapping. Dynamic partitioning (quadtree split/merge) runs at runtime; `cell migrate` moves individual cells across hosts through the same transfer protocol. Single-writer-per-entity enforced via the per-cell `netIDIndex` transition policy (State Integrity plan).

### 2. Replication Layer

- Independent subsystem (eventually service) that tracks entity state versions, subscriptions, and handoff metadata
- Non-owning servers and clients subscribe to relevant entities
- Does not run gameplay logic
- **Current state (2026-04-21):** `pkg/replication/` extracted as shared primitives (`Viewer`, `BaselineStore`, `Frame`). Client replication (`pkg/system/replication.go`) and cross-host border replication (`pkg/universe/border_replication.go`) both consume it via the tiered-push protocol. Delta encoding is wired for client replication; border frames still ship full tails each tick (delta encoding queued as #11 follow-up). Layer is consumed by multiple callers but not yet a standalone service — still runs in-process with the simulation.

### 3. Gateway / Routing Layer

- Clients connect through a stable gateway/session frontend
- Gateway switches upstream sim servers without forcing client reconnect
- Makes the world feel seamless to clients
- **Current state (2026-04-21):** Shipped as `RoleGateway` (S6). Gateway terminates WebSockets, maintains `sessionRoutes` keyed by `{GatewayID, ConnID}`, and receives targeted `CoordMessage.UpstreamSwitch` when sessions hand off across hosts. VirtualConnManager remaps wire ConnIDs to local-host ConnIDs per session with epoch-gated ordering. Can run embedded with coordinator (`--mode=coordinator,gateway`) or standalone behind a load balancer (`--mode=gateway --coordinator-addr=...`). Gateway crash currently triggers client reconnect + full re-login; crash-recovery session tokens remain deferred.

**Why three layers:** Isolates authority, reduces N² coordination, and lets replication be optimized independently of gameplay. State modeled for transport is separate from simulation/runtime state.

---

## Entity Ownership Model

### Single Writer, Many Readers

- The owning server is the only place that can mutate authoritative gameplay components
- Other servers keep read-only mirrors (replicas) for visibility, cross-border interaction, and pre-handoff warmup
- Clients never own gameplay truth — they only own input generation and optionally predict their own actor locally
- **Current state (2026-04-21):** Shipped + structurally enforced. Replicas are read-only (all mutation systems filter them out). Clients remain dumb renderers (prediction is still future work). The per-cell `netIDIndex` (State Integrity plan) tracks every netID's presence on each cell (`Live` / `Shadow` / `Replica`) and rejects transitions that would produce split authority — a `Live` entity for `netID=X` on cell A can't coexist with another `Live` for `X` on any other cell without the invariant framework panicking in dev.

### Ownership Categories

| Category | Owner | Notes |
|----------|-------|-------|
| Player actor | Node for player's current cell | Transfers on boundary crossing |
| Projectiles / physics items | Node where they simulate | Created and destroyed locally |
| Long-lived world objects (stations, asteroids) | Node containing their anchor cell | Cell-locked, never transfer |
| NPCs | Node containing their spawn cell | Boundary-leashed, never transfer |
| Cross-cell objects (capital ships, trains) | Mobile authority root OR dedicated entity host | **Future** — not yet needed |

### Rule: No Split Authority for Tightly Coupled Interactions

A gunfight, rigidbody stack, or melee exchange should not be simulated by multiple authoritative servers at once. Keep interacting entities on one server whenever possible.

**Current approach:** Cross-node combat is deliberately disallowed. Players must cross into the target's cell to attack entities there. This is the correct simplification for now.

---

## Entity Handoff Protocol

### Target: Prepare → Overlap → Commit

The current handoff is "serialize → ghost → confirm" — essentially instant ownership transfer with a ghost grace period. The target is a three-phase protocol:

#### 1. Prepare

When an entity approaches a cell boundary, the current owner starts streaming a compact authoritative replica to the candidate next owner. This warms the destination's cache and spatial grid.

#### 2. Overlap

For a short window:
- Old server remains authoritative
- New server runs a shadow copy (warm cache, relevancy, prediction)
- Both share a monotonically increasing version/tick stamp

#### 3. Commit

At a chosen tick boundary:
- Ownership flips — new server becomes sole writer
- Old server downgrades entity to replica
- Clients do not reconnect — gateway reroutes authoritative traffic

**Advantages over current approach:** Eliminates cold-start stalls on the destination node. Clients see no discontinuity. Spatial queries on the destination are already warm.

**Current state (2026-04-21):** Infrastructure built but not wired into the production boundary path. `pkg/universe/handoff.go` ships the state machine (`Unseen → Border → Promoted → Handoff`), `MsgHandoffPrepare`/`MsgHandoffCommit`/`MsgForwardInput` envelopes, and `SpawnShadow` / `PromoteShadow` on `WorldBase`. The current production path still uses the simpler `BoundarySystem` → `handoff_driver` → `Prepare+Commit` (fire together, v1 simplification — no warmup window) with `MarkForRemoval` at the source. Moving to real overlap (new server runs a warm shadow copy for a window before commit) is a Phase-4 remaining item.

---

## ECS Replication Design

### Component Classes

Separate components into replication classes rather than serializing whole archetypes:

| Class | Direction | Examples | Current mmokit mapping |
|-------|-----------|----------|----------------------|
| **Simulation-only** | Never replicated | Broadphase handles, solver caches, pathfinding internals | Ad-hoc — components not registered with ReplicationRegistry |
| **Authoritative gameplay** | Server → clients, server → replica servers | Transform, Velocity, Health, Inventory, Abilities, Combat state | Registered via `RegisterComponent` |
| **Owner-input** | Client → server only | Move input, aim input, fire/use commands, target selection | Handled by InputRouter, never replicated |
| **Presentation-only** | Client-local only | Particles, camera shake, IK, cosmetic animation | Client-side, not in server ECS |

**Action item:** Formalize these classes with struct tags or registration options. Currently the distinction exists but is implicit.

### Serialization Strategy

#### 1. Snapshot + Delta Compression (Roadmap #3)

Per connection, maintain:
- Last acknowledged baseline
- Current authoritative state
- Serialize only the delta

The ReplicationSystem already does hash-based change detection. Delta compression adds field-level diffing on top.

#### 2. Quantized Transport State (Roadmap #3)

Do not serialize raw float-heavy ECS memory directly. Serialize a network schema:

- Quantized position (uint16 cell-local, 0.125-unit precision)
- Quantized rotation
- Compressed velocity
- Packed flags/enums
- Stable network entity IDs

#### 3. Chunk/Archetype-Oriented Packing (Roadmap #10)

On the send side, pack entities by archetype/chunk because ECS data is already laid out that way. Ark's archetype storage enables this.

#### 4. Per-Component Dirty Masks

Each replicated component should have:
- A change version
- Optional field-level dirty mask
- Optional custom serializer

Do not resend static or slow-changing components at the same cadence as movement/combat.

#### 5. Multiple Replication Lanes

| Lane | Transport | Use |
|------|-----------|-----|
| Reliable ordered | WebSocket / TCP | Spawn, despawn, equip, inventory changes |
| Unreliable sequenced | UDP (future) | Movement, aim, projectile state |
| Reliable fragmented | WebSocket / TCP | Large infrequent payloads |

**Current state:** All traffic goes over WebSocket (reliable). UDP channel is a future addition for native clients.

---

## Interest Management

### Three-Stage Pipeline

1. **Hard relevancy filter** — spatial AoI query, discard everything outside
2. **Importance/prioritization** — rank remaining entities by type, distance, interaction potential
3. **Bandwidth budget** — fill frame up to budget, highest priority first

### Relevancy Sources

| Source | Description | Current state |
|--------|-------------|---------------|
| **Spatial AoI** | Nearby and potentially visible | Implemented (spatial grid + AoIRadius) |
| **Interaction-based** | Things you can shoot, collide with, or affect soon | Not implemented |
| **Ownership-based** | Your controlled pawn and attached entities always relevant | Implicit — player always in own AoI |
| **Prefetch/soft visibility** | Entities just beyond border streamed at low rate before they matter | Not implemented — critical for seamless meshing |

**Soft visibility is critical for meshing.** Streaming border entities before they enter AoI prevents pop-in during handoff. This is partially addressed by the current replica system (100-unit border margin) but could be expanded.

### Hierarchical Interest (Roadmap #8)

Per-entity-type tiers with radius and update frequency:
- Combat entities: full AoI radius, every tick
- Resources/structures: reduced radius, every N ticks
- Background/cosmetic: minimal radius, low rate

---

## Tickrate Design

### Context-Dependent Rates

| Subsystem | Recommended rate | Notes |
|-----------|-----------------|-------|
| Gameplay simulation (combat/movement) | 20-60Hz | 20Hz is fine for 2D space; 60Hz for twitch combat |
| Client replication | 20Hz (current) | Can decouple from sim rate if needed |
| Background systems (economy, NPC AI) | 5-10Hz | Don't waste cycles on low-urgency updates |
| Inter-node replication | 10-20Hz | Lower than client rate is fine for border replicas |

**Current state:** Everything runs at 20Hz. This is appropriate for the current game. Decoupling subsystem rates is a future optimization.

**Warning:** Prediction/rollback cost rises hard with latency. At 300ms RTT, ~22 resimulated frames per render frame at 60Hz. Keep the predicted high-rate core small and deterministic.

---

## Client Prediction Model

### Target Model (Future)

| Entity type | Client behavior |
|-------------|----------------|
| Local player | Client-side prediction with server reconciliation |
| Remote actors | Interpolation between server snapshots |
| Server | Authoritative rewind/validation for hitscan and critical combat |

**Current state:** Dumb client — server-authoritative with no prediction. Client interpolates between 20Hz snapshots. This is acceptable for the current game but limits responsiveness.

**Migration path:** Adding owner-predicted entities is a major feature. The ReplicationSystem would need to distinguish predicted vs interpolated ghosts, and the client needs a reconciliation loop.

---

## Entity Identity

### Target ID Scheme

```
NetworkEntityID {
    EntityGuid      uint64   // globally unique, survives restart
    AuthorityEpoch  uint32   // incremented on ownership transfer
    SpawnSequence   uint32   // for ordering within a batch
}
```

**Why:** ECS entity handles are process-local and unstable. Handoff requires continuity across processes. Despawn/respawn must not be mistaken for migration. Authority epoch makes stale packets trivially droppable.

**Current state (2026-04-21):** `component.NetworkID{ID uint32, Epoch uint32}` with per-cell `NetIDBase` range allocation (via `pkg/universe.NetIDAllocator`). Epoch is bumped on every authority transfer; stale packets are trivially dropped via `highestSeenEpoch` guards in border replication and by `VirtualConnManager`'s never-downgrade rule on session routing. Stable across transfers in both single-process and multi-process meshing. GUID-level identity (entity surviving a full coordinator restart) and explicit `SpawnSequence` field remain future work — not yet pressing while the deployment topology is one coordinator per shard.

---

## Persistence

### What to Persist

- Long-lived gameplay state (inventory, equipment, skills, currency)
- Durable world objects (stations, POIs)
- Ownership metadata
- Transform/velocity if continuity matters across restart

### What NOT to Persist

- Per-frame ECS scratch
- Broadphase caches
- Rollback history
- Visual-only components
- Replica state (reconstructed from authority)

### Handoff Blob

For entity transfer, ship a compact blob containing:
- Authoritative gameplay components
- Enough derived state to resume simulation without a full rebuild stall

**Current state:** TransferFrame contains full component state as binary. PlayerDB is saved before transfer. This is correct.

---

## Anti-Patterns to Avoid

| Anti-pattern | Why it fails |
|--------------|-------------|
| Multi-writer ownership for the same hot entity | Desync, conflict resolution overhead, latency spikes |
| Full state replication between neighboring servers | Bandwidth explosion at scale |
| Client-visible reconnects on cell transfer | Breaks seamlessness |
| Serializing raw ECS memory as the network format | Fragile, wastes bandwidth, couples wire format to runtime layout |
| Same tickrate for everything in the world | Wastes CPU on low-priority subsystems |
| RPC-first replication for durable object state | Unreliable for persistent entities; snapshots are authoritative |

---

## Current State vs Target

| Aspect | Shipped (2026-04-21) | Target |
|--------|----------------------|--------|
| Host-to-cell mapping | ✅ 1:N with runtime quadtree split/merge and cross-host migrate | Same, plus load-aware auto-placement under scale |
| Host communication | ✅ Loopback channels (in-process) + gRPC `MeshData` streams (cross-host) with the same `CellTransfer` code path | Same |
| Entity transfer | ⚠️ v1 handoff shipped (Prepare+Commit fire together, no warmup); State Integrity enforces single-writer invariant. Donor handoff_driver freezes during MERGE drain to prevent cross-sibling duplicates | Prepare → overlap → commit with real warmup window (infrastructure exists, needs wiring) |
| Client replication | ✅ Tiered-push, delta-compressed, hierarchical AoI, per-type update divisors, dormancy, bandwidth-aware priority accumulator | Add bandwidth budget cap + deeper prediction-aware scheduling |
| Border replication (mesh-to-mesh) | ⚠️ Tiered-push `BorderDispatcher` + epoch-gated `upsertBorderReplica` shipped; delta encoding queued (`BaselineStore` allocated but unused) | Delta-compressed border frames |
| Client transport | ⚠️ WebSocket only | WebSocket + UDP for native clients |
| Client model | ❌ Dumb renderer with snapshot interpolation + freshSnapshot handoff resync (Time & Transparency plan) | Owner-predicted local player, interpolated remotes, server rewind/validation |
| Entity identity | ✅ `{uint32 ID, uint32 Epoch}` — per-cell range allocation + recycling; epoch bumped on every authority transfer; stale-packet drop enforced everywhere | Add cross-restart GUID + SpawnSequence when persistent entity identity becomes pressing |
| Gateway | ✅ `RoleGateway` — embedded or standalone, session routing via `sessionRoutes` + `VirtualConnManager`, targeted UpstreamSwitch on host handoff | Add gateway-side session token for transparent crash recovery |
| Interest management | ✅ Stage 1 (spatial AoI) + Stage 2 (priority accumulator). Stage 3 (bandwidth budget) still queued | Full three-stage pipeline |
| Tickrate | ⚠️ 20Hz uniform across subsystems | Decoupled per-subsystem (background systems at 5-10Hz) |
| Serialization | ✅ Quantized binary + delta-compressed + per-field dirty mask via `AutoReplicator` struct tags | Async serialization off the game-loop goroutine (#10) |
| Persistence | ✅ PostgreSQL with hybrid relational + JSONB; batched async flush via `PlayerFlusher`; marketplace synchronous writes | Partitioned/sharded schema if scale demands it |
| State integrity | ✅ Invariant framework + event-sourced commit log + per-cell netIDIndex with typed transition policy; 5+ latent bugs surfaced and fixed during Phase E | Same model extended to session-layer invariants once needed |
| Operational observability | ✅ Prometheus `/metrics` + `/commands` + `/events` on gateway and optional `--admin-listen` on pure-coordinator processes; commit log queryable by commit-id / cell / time window via console (`commit.log`) or HTTP | Dashboard UI consumer (external) |

Legend: ✅ shipped to target · ⚠️ partial · ❌ not started

---

## State Integrity Framework

Added as a cross-cutting concern in April 2026 (the State Integrity plan, merged in commit `e4ede97` and follow-ups). Four layers of runtime guards that catch inconsistent state at the point of violation rather than hours later as mysterious replication bugs:

1. **Invariants** (`pkg/universe/integrity.go`): Five named predicates run at every commit entry, between every plan step, and at every commit exit. `Config.InvariantMode` controls handling (`Off` / `Log` / `Panic`). Dev/test default is `Panic`; production default is `Log`.
2. **CommitPlan model** (`pkg/universe/commit_plan.go`): Split, merge, and migrate are each expressed as an ordered `[]PlanStep` interpreted by `ExecuteCommitPlan`. Step names are stable (`apply-coord-mutation`, `rename-survivor-host`, `remap-sessions`, ...) and appear in diagnostic output.
3. **Commit log** (`pkg/universe/commit_log.go`): In-memory ring (default cap 1024) of `CommitEvent` records — one per plan step, one per invariant violation, one per host join/leave. Query via the `commit.log` console verb, the `GET /events` HTTP endpoint, or by tailing the `events:*` logger categories live.
4. **netIDIndex** (`pkg/universe/netid_index.go`): Per-cell `{netID → entity, presence}` table with a typed transition policy (`Live`, `Shadow`, `Replica`). Six spawn paths funnel through it (`SpawnFromTransferCore`, `SpawnShadow`, `PromoteShadow`, `upsertBorderReplica`, `WorldBase.SpawnEntity`, `OnEntityRemoved`→Exit). `Config.StrictNetIDIndex` controls enforcement (reject duplicates at spawn time) vs observational (log only). 4node-basic runs with both `InvariantPanic` and `StrictNetIDIndex=true` so every dev run exercises the full guard.

The framework was the direct response to the bug classes hit during the Time & Transparency plan (ordering mistakes, duplications, silent failures, ad-hoc state-shape bugs). Phase E of the State Integrity plan surfaced 5+ additional latent handoff-race bugs during bot-load smoke testing — each was traced to root cause and fixed before the branch merged.

## Evolutionary Path

Where the roadmap phases land relative to this architecture, as of 2026-04-21:

```
Phase 1 (Foundation)        ✅ Simulation mesh basics, replication subsystem
Phase 2 (Performance)       ✅ Serialization strategy, incremental spatial grid, metrics
Phase 3 (Advanced)          ✅ Hierarchical AoI, codegen, dynamic cell partitioning
Phase 4 (Meshing at scale)  ✅ S4 control plane, S5 Postgres, S6 gateway role, S7 cell transfers
Phase 5 (Integrity + time)  ✅ Time & Transparency (epoch-gated handoff, freshSnapshot, client clock), State Integrity (invariants, commit log, netIDIndex)

Phase 6 (Feel + scale-out)  ⏳ Wire overlap handoff (#11 follow-up), border frame delta compression,
                               client prediction for local player, async entity serialization (#10),
                               UDP transport for native clients, gateway session-token crash recovery.
```

Each phase moved closer to the target architecture while keeping the system functional at every step. Phases 4 and 5 are not in the original roadmap (written at Phase 3) — they captured work that became necessary once distributed meshing and subtle concurrency bugs became first-order concerns.

---

## References

- Unity DOTS Netcode: ghost component filtering, chunk-level replication, owner-predicted entities, importance-based scheduling
- Unreal Engine: Iris replication fragments, Replication Graph for per-client replication lists
- O3DE: delta replication against per-client acknowledged baseline
- Valve Source Engine: delta compression, prediction, lag compensation
- Colyseus: single-copy consistency per object, weakly consistent replicas
