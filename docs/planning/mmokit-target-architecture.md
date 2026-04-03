# mmokit Target Architecture

**Date**: 2026-03-27
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
- **Current state:** Implemented in-process via Coordinator + Node with 1:1 cell-to-node mapping. Moving to 1:N (one node owns multiple cells) is a prerequisite for dynamic partitioning.

### 2. Replication Layer

- Independent subsystem (eventually service) that tracks entity state versions, subscriptions, and handoff metadata
- Non-owning servers and clients subscribe to relevant entities
- Does not run gameplay logic
- **Current state:** Partially implemented. ReplicationSystem handles client-facing AoI + hash-based diff. ComponentReplicator + ReplicationRegistry handle mesh-to-mesh border replication. These are still coupled to simulation — not yet a standalone layer.

### 3. Gateway / Routing Layer

- Clients connect through a stable gateway/session frontend
- Gateway switches upstream sim servers without forcing client reconnect
- Makes the world feel seamless to clients
- **Current state:** Not implemented. ConnManager is shared in-process. All nodes live in one process, so no routing needed yet. This becomes critical when moving to multi-process (roadmap #12).

**Why three layers:** Isolates authority, reduces N² coordination, and lets replication be optimized independently of gameplay. State modeled for transport is separate from simulation/runtime state.

---

## Entity Ownership Model

### Single Writer, Many Readers

- The owning server is the only place that can mutate authoritative gameplay components
- Other servers keep read-only mirrors (replicas) for visibility, cross-border interaction, and pre-handoff warmup
- Clients never own gameplay truth — they only own input generation and optionally predict their own actor locally
- **Current state:** Already implemented. Replicas are read-only (all mutation systems filter them out). Clients are dumb renderers.

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

**Migration path:** The current ghost-based handoff works for in-process meshing. The overlap model becomes important when handoff crosses a network boundary (multi-process) and when latency between nodes is nonzero.

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

**Current state:** uint32 NetworkID with per-node range allocation (node_index * 10M). Stable across transfers within a process. Sufficient for in-process meshing. The richer scheme becomes necessary for multi-process (roadmap #12).

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

| Aspect | Current (in-process meshing) | Target (distributed) |
|--------|------------------------------|---------------------|
| Node-to-cell mapping | 1:1 (one node per cell) | 1:N (one node owns multiple cells) |
| Node communication | Go channels (zero-copy) | Network-based NodeBridge (TCP/gRPC) |
| Entity transfer | Serialize → ghost → confirm | Prepare → overlap → commit |
| Replication | Full state per-tick for border entities | Delta-compressed, prioritized, budgeted |
| Client transport | WebSocket only | WebSocket + UDP for native clients |
| Client model | Dumb renderer | Owner-predicted player, interpolated remotes |
| Entity identity | uint32 NetworkID (per-node ranges) | EntityGuid + AuthorityEpoch |
| Gateway | None (shared ConnManager) | Session frontend with upstream routing |
| Interest management | Single AoI radius | Hierarchical tiers + importance + budget |
| Tickrate | 20Hz uniform | Decoupled per-subsystem |
| Serialization | Reflection-based binary | Quantized + delta + per-component dirty |

---

## Evolutionary Path

The roadmap phases map to this architecture:

```
Phase 1 (Foundation) ✓     → Simulation mesh basics, replication subsystem
Phase 2 (Performance)       → Serialization strategy (#3), interest management groundwork (#5)
Phase 3 (Advanced)          → Hierarchical AoI (#8), codegen (#9), dynamic partitioning (#7)
Phase 4 (Scale-out)         → Replication as service (#10), gateway layer (#12), overlap handoff (#11)
```

Each phase moves closer to the target architecture while keeping the system functional at every step.

---

## References

- Unity DOTS Netcode: ghost component filtering, chunk-level replication, owner-predicted entities, importance-based scheduling
- Unreal Engine: Iris replication fragments, Replication Graph for per-client replication lists
- O3DE: delta replication against per-client acknowledged baseline
- Valve Source Engine: delta compression, prediction, lag compensation
- Colyseus: single-copy consistency per object, weakly consistent replicas
