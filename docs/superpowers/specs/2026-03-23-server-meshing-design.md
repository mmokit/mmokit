# Server Meshing: Multi-Node In-Process Architecture

**Date**: 2026-03-23
**Status**: Design
**Scope**: Phases 2-4 of the scalable universe plan — multi-node ticking, entity handoff, border replication

## Context

Phase 0+1 established sector-based coordinates (8192-unit sectors, SectorCoord component, SectorBoundarySystem, player-relative networking). The server currently runs as a single node with one ECS world. This design introduces multiple nodes — each owning a sector with its own ECS world — running concurrently in one process, with entity handoff and border replication for seamless cross-sector gameplay.

A working prototype at `~/projects/scratch/handoff1` (TypeScript) demonstrates the coordinator/shard/gateway pattern. This design adapts those patterns to the Go codebase.

## Architecture Overview

```
                    ┌──────────────┐
                    │ Coordinator  │  Assigns sectors, routes connections,
                    │              │  manages transfers between nodes
                    └──────┬───────┘
                           │
          ┌────────────────┼────────────────┐
          │                │                │
     ┌────▼────┐     ┌────▼────┐     ┌────▼────┐
     │ Node    │     │ Node    │     │ Node    │  ... (9 nodes)
     │(-1,-1)  │◄───►│ (0,0)  │◄───►│ (1,1)  │
     │         │     │         │     │         │
     │Own ECS  │     │Own ECS  │     │Own ECS  │
     │Own Grid │     │Own Grid │     │Own Grid │
     │Own Loop │     │Own Loop │     │Own Loop │
     └─────────┘     └─────────┘     └─────────┘

Each node runs in its own goroutine at 20Hz.
Nodes communicate via Go channels (transfers, replicas).
ConnManager is shared — single WebSocket endpoint.
No gateway layer (added later for multi-server).
```

## Sector Layout

3x3 grid of sectors, coordinates (-1,-1) through (1,1):

```
(-1,-1) │ (0,-1) │ (1,-1)
────────┼────────┼────────
(-1, 0) │ (0, 0) │ (1, 0)    ← Station at (0,0) center
────────┼────────┼────────
(-1, 1) │ (0, 1) │ (1, 1)
```

Each sector is 8192x8192 world units. At MaxSpeed=68 u/s, crossing one sector takes ~120 seconds.

## 1. Node Structure

Each Node is a self-contained game simulation owning one sector.

```go
// internal/universe/node.go
type Node struct {
    ID        string                    // e.g. "node-0-0"
    Sector    coords.SectorCoord        // which sector this node owns
    Engine    *engine.Engine            // own ECS world, tick counter, netID allocator
    World     *game.GameWorld           // own game state, spatial grid, entity mappers
    Systems   []engine.System           // full system pipeline
    SysNames  []string

    Inbox     chan NodeMessage           // receives transfers, replicas, routing events
    Neighbors map[string]*Node          // direct references to neighbor nodes (in-process)

    // Per-node event channel — Coordinator fans out ConnManager events here
    Events    chan net.PlayerEvent
}
```

**Lifecycle**:
- Created by Coordinator at startup with a sector assignment
- Calls `engine.New()` + `game.NewGameWorld()` with sector-specific config
- Spawns sector content (asteroids, station if sector 0,0)
- Runs its own game loop goroutine via `engine.NewGameLoop()`
- Drains `Inbox` at the start of each tick (via a hook) for transfers and replicas
- Drains `Events` for connect/disconnect (replacing direct `ConnMgr.Events()` reads)

**What's per-node**: ECS world, spatial grid, all component mappers, all pending request queues, `PlayerEntities` map, `NetIDToEntity` map, system instances, `Events` channel.

**What's shared**: `ConnManager` (thread-safe), `PlayerDB` (mutex-protected), `Logger`.

### NetworkID Allocation

Each node gets a disjoint range to avoid collisions:

```go
const netIDRangeSize = 10_000_000
node0.Engine.SetNetIDBase(0 * netIDRangeSize)
node1.Engine.SetNetIDBase(1 * netIDRangeSize)
// etc.
```

`Engine.NextNetID()` is modified to add from a base offset instead of starting at 0. At 100 spawns/sec, 10M IDs last ~27 hours per node — sufficient for any session. If exhaustion becomes a concern, implement wrapping with conflict detection.

### Connection Events Routing

The current `GameLoop.processEvents()` drains `ConnMgr.Events()` — a single shared channel. With multiple nodes, this channel cannot be read by multiple goroutines safely. Solution:

1. `GameLoop` is modified to accept an optional per-node events channel instead of reading `ConnMgr.Events()` directly.
2. The Coordinator runs a dedicated goroutine that drains `ConnMgr.Events()` and fans out each event to the correct node's `Events` channel based on `PlayerNode` routing.
3. Each node's game loop reads from its own `Events` channel for connect/disconnect processing.

```go
// Coordinator event fan-out goroutine
func (c *Coordinator) routeEvents(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case evt := <-c.ConnMgr.Events():
            if evt.Connected {
                nodeID := c.assignNode(evt.ConnID) // default: sector (0,0)
                c.Nodes[nodeID].Events <- evt
            } else {
                nodeID := c.getPlayerNode(evt.ConnID)
                c.Nodes[nodeID].Events <- evt
                c.removePlayerNode(evt.ConnID)
            }
        }
    }
}
```

## 2. Coordinator

The Coordinator is a routing and lifecycle manager, not a tick driver.

```go
// internal/universe/coordinator.go
type Coordinator struct {
    Nodes       map[string]*Node                    // nodeID → Node
    SectorOwner map[coords.SectorCoord]string       // sector → nodeID
    Topology    Topology                            // neighbor relationships

    ConnMgr     *net.ConnManager                    // shared WebSocket endpoint
    PlayerDB    *PlayerRepo                         // shared persistence
    Log         *logger.Logger                      // shared logger

    PlayerNode  map[uint32]string                   // connID → nodeID (protected by mutex)
    mu          sync.RWMutex                        // protects PlayerNode
}
```

### Startup

```
1. Create 9 nodes (one per sector in 3x3 grid)
2. Compute topology (8-connected neighbors)
3. Wire neighbor references on each node
4. Assign NetworkID ranges
5. Start each node's game loop goroutine
6. Start connection event routing goroutine
```

### Connection Routing

The Coordinator intercepts ConnManager events and routes them to the correct node:

```
On connect:
  → Assign to default node (sector 0,0)
  → Add to PlayerNode map
  → Forward connect event to the node's Events channel

On disconnect:
  → Look up owning node from PlayerNode
  → Forward disconnect event to the node's Events channel
  → Remove from PlayerNode map

On login (player has saved sector):
  → If saved sector is on a different node, reassign before spawn
  → Forward the login to the correct node
```

### Input Routing

No special routing needed. Each node's `InputSystem` calls `ConnMgr.DrainInput(connID)` for its own players (from `PlayerEntities` map). Since `DrainInput` is mutex-protected and each connID belongs to exactly one node, there are no races.

### PlayerDB Access Discipline

`PlayerDB` is shared across nodes with mutex protection. To prevent concurrent mutation of the same player's data:

- A player's `*PlayerData` is only accessed by the node that owns that player (tracked via `PlayerNode`).
- During transfer, the source node saves the player's state before sending the transfer payload. The destination node reads fresh data from `PlayerDB` on arrival.
- The `PlayerDB` methods (`GetOrCreate`, `MarkDirty`, etc.) are already mutex-protected. Nodes must not hold `*PlayerData` pointers across ticks — read/write within a single operation.

### OpRouter

The `OpRouter` (marketplace operations) remains a single shared service. It only touches `PlayerDB` (mutex-protected) and `ConnManager` (thread-safe). `PlayerSessions` is updated atomically during player transfer: removed from source, added on destination. The OpRouter is agnostic to which node owns the player.

### Chat Relay

Chat messages are currently broadcast only within a node's `NetworkSystem`. With multi-node, the Coordinator relays chat across all nodes:

- Each node sends `PendingChat` messages to the Coordinator via a shared chat channel.
- The Coordinator fans out chat messages to all nodes' Inboxes.
- Each node's `NetworkSystem` includes relayed chat in its broadcasts.

## 3. Entity Handoff

### Detection

`SectorBoundarySystem` already normalizes positions when they leave [0, SectorSize). The change: before normalizing, check if the new sector belongs to a different node. If so, enqueue a transfer instead.

```go
// In SectorBoundarySystem.Update():
if newSector != oldSector {
    ownerNode := coordinator.SectorOwner[newSector]
    if ownerNode != thisNodeID {
        // Cross-node transfer — don't normalize, enqueue transfer
        coordinator.EnqueueTransfer(entity, thisNodeID, ownerNode)
        continue
    }
    // Same node, different sector — just normalize as before
}
```

### Transfer Payload

All component values serialized as a Go struct, passed directly through the channel (zero-copy in-process):

```go
type TransferPayload struct {
    NetworkID     uint32
    EntityType    uint8
    ConnID        uint32                    // 0 for non-player entities
    Username      string                    // "" for non-player entities
    SourceTick    uint32                    // source node's tick counter (for dead reckoning)

    // Core components (always present)
    Position      component.Position
    Sector        component.SectorCoord
    Velocity      component.Velocity
    Rotation      component.Rotation
    Collider      component.Collider

    // Optional components (nil if entity doesn't have them)
    Health        *component.Health
    Shield        *component.Shield
    ShipControl   *component.ShipControl
    Inventory     *InventorySnapshot        // deep copy of items map
    Equipment     *component.Equipment
    MoveTarget    *component.MoveTarget
    AbilitySet    *component.AbilitySet
    Minable       *component.Minable
    Lifetime      *component.Lifetime
    // Note: TargetLock, MiningLaser, and StatusEffects are NOT transferred —
    // see "ECS Entity Reference Handling" below
}
```

### ECS Entity Reference Handling

Several components contain `ecs.Entity` references that are local to a single ECS world and become dangling on transfer:

- `TargetLock.TargetEntity` — combat lock target
- `MiningLaser.Target` — mining beam target
- `StatusEffect.Source` — who applied the effect

**Design decision**: These references are **cleared on transfer**. The player must re-acquire locks after crossing a sector boundary. This is acceptable because:
- Locks are proximity-based — if the target is in a different sector, the lock would be out of range anyway
- StatusEffect.Source is only used for attribution (damage source tracking), which is minor
- Clearing is simple and correct; translating across worlds would be fragile

On the destination node, transferred entities receive:
- `TargetLock` with `TargetEntity = zero, Locked = false, Progress = 0`
- `MiningLaser` with `Target = zero, Active = false` for all beams
- `StatusEffects` with all `Source` fields zeroed (durations preserved)

### Transfer Flow

```
Source Node (tick N):
  1. SectorBoundarySystem detects cross-node crossing
  2. Save player state to PlayerDB (if player entity)
  3. Serialize entity into TransferPayload (deep copy, clear entity refs)
  4. Add Ghost component to entity (TTL=10 ticks)
  5. Send TransferPayload to destination node's Inbox
  6. If player entity: notify Coordinator to update PlayerNode routing

Destination Node (next tick, on Inbox drain):
  7. Receive TransferPayload
  8. Dead-reckon position: advance by dt = (destTick - sourceTick) * 0.05s
  9. Create new ECS entity with all component values
  10. Use the SAME NetworkID (critical for client continuity)
  11. If player entity: add to PlayerEntities, ConnToUsername maps
  12. Send ArrivalConfirm to source node's Inbox

Source Node (next Inbox drain):
  13. Receive ArrivalConfirm
  14. Remove ghost entity
  15. If player entity: remove from PlayerEntities, ConnToUsername maps
```

### Ghost Component

```go
type Ghost struct {
    TTL           int       // ticks remaining (starts at 10 = 500ms)
    DestNodeID    string    // where the entity went
}
```

**System behavior with ghosts**:
- `NetworkSystem`: INCLUDES ghosts in AoI (prevents visual pop for nearby players)
- `SpatialSystem`: INCLUDES ghosts in grid for AoI queries only
- `CollisionSystem`: SKIPS ghosts (no damage to stale snapshots)
- All other mutation systems: SKIP entities with Ghost component
- Ghost TTL decremented each tick; auto-removed at 0 (safety fallback if arrival confirm is lost)

### Transfer Cooldown

New component added on arrival:

```go
type TransferCooldown struct {
    Remaining int   // ticks remaining (starts at 10 = 500ms)
}
```

`SectorBoundarySystem` skips transfer detection for entities with `TransferCooldown`. Decremented each tick, removed at 0.

### Respawn Routing

When a player dies on a non-station node (e.g., sector 1,0), they respawn at the station in sector (0,0). The respawn flow:

1. Player death on node (1,0) — entity removed, `DeadPlayers[connID] = true`
2. Respawn request received — node (1,0) detects player should spawn at (0,0)
3. Node (1,0) sends a `RespawnTransfer` message to node (0,0) via Inbox
4. Node (0,0) spawns the player at the station
5. Coordinator updates `PlayerNode[connID]` to node (0,0)
6. Node (1,0) removes from `DeadPlayers`, `PlayerEntities`, `ConnToUsername`

### Docked Players

Docked players have no ECS entity — they're tracked by `DockedPlayers` set on the station's owning node. Docked players are always on node (0,0). Undocking spawns on node (0,0). A docked player's node never changes.

### Loot Drops at Borders

When an entity dies near a sector border, the loot crate spawns on the node that owns the entity (source node). The crate is a non-moving entity — it stays in that sector. If the crate is within the replica margin, it will be visible to players on the neighboring node via replication.

## 4. Border Replication

### Replica Margin

**100 world units** (= AoI radius from `GameConfig.AoIRadius`). Any entity within 100 units of a sector edge is replicated to the neighboring node. This guarantees players at sector boundaries have full AoI visibility with no gaps.

### Coordinate Translation

Replicas have positions in their source sector's local coordinate space. When inserted into the receiving node's spatial grid, positions must be translated:

```go
// Source sector (1,0), entity at local pos (50, 4000)
// Receiving node owns sector (0,0)
// Translation: relX = (1-0)*8192 + 50 = 8242
//              relY = (0-0)*8192 + 4000 = 4000
// The replica is inserted at (8242, 4000) in the receiver's grid
```

This uses the same `RelativeOffset` function from `pkg/coords/`. The `NetworkSystem` already handles player-relative position serialization, so replicas at translated positions are correctly sent to clients.

### Replica Flow (per tick, per node)

```
After all systems run (end of tick via a ReplicationSystem or hook):
  1. Scan entities within 100 units of any sector edge
  2. For each, build a ReplicaSnapshot:
     - NetworkID, Position, SectorCoord, Velocity, Rotation
     - Collider, EntityKind
     - Type-specific: health/shield (ships/NPCs), resource (asteroids)
  3. Batch snapshots per neighbor
  4. Send to each neighbor's Inbox channel

On receiving node (start of next tick, Inbox drain):
  5. For each snapshot:
     - Translate position to local coordinate space
     - If replica entity exists (same NetworkID): update position/state, reset TTL
     - If new: create replica entity with Replica component
  6. Replicas participate in spatial grid and AoI queries
  7. All mutation systems skip replica entities

TTL expiry:
  8. Each tick, decrement replica TTL
  9. Remove replicas with TTL=0 (not refreshed for 30 ticks = 1.5s)
```

### Replica Component

```go
type Replica struct {
    SourceNodeID string
    SourceNetID  uint32
    TTL          int       // ticks remaining (reset to 30 on refresh)
}
```

### System Filtering

Systems that mutate entity state must exclude replicas and ghosts:

```go
// Ark supports filter exclusion via Without()
filter := ecs.NewFilter2[Position, Velocity](world).Without(Ghost{}, Replica{})
```

**Systems that EXCLUDE replicas and ghosts**: Input, Docking, TargetLock, ShipControl, Mining, Economy, Equipment, Ability, StatusEffect, Physics, SectorBoundary, Lifetime, Collision, ShieldRegen.

**Systems that INCLUDE replicas and ghosts**: Spatial (grid insertion), Network (AoI visibility).

### Dirty Tracking

Only entities whose position changed since last replication are included in the batch (`lastReplicatedPos map[uint32][2]float32`). Full resync every 100 ticks (5 seconds) as safety net.

### Cross-Node Combat

Players near a sector boundary can see enemies on the other side via replicas, but cannot directly damage them — the replica is read-only. Abilities and projectiles that target a replica will show "out of range" or miss. To attack an entity on another node, the player must cross into that sector (triggering a transfer). This is a deliberate simplification for Phase 2-4. True cross-node combat (projectiles that cross boundaries) is deferred to a future phase.

## 5. Asteroid Belts

### Belt Configuration

Instead of random scatter, asteroids are organized into belts — dense clusters of specific resource types.

```go
type AsteroidBelt struct {
    CenterX, CenterY float32     // local position within sector
    Radius            float32    // spread radius
    ResourceTypes     []uint8    // 1-2 dominant resource types
    Count             int        // number of asteroids
}
```

### Generation

Belts are generated deterministically from sector coordinates using a seeded RNG (`hash(sectorX, sectorY)`). Each sector gets 1-3 belts. Resource distribution:

- Each belt has 1-2 **dominant** types (70-80% of asteroids)
- Remaining 20-30% are random other types
- Belt positions avoid sector edges (margin of 200 units) to prevent asteroids spawning in the replica zone
- Belt positions avoid sector center in (0,0) where the station sits

### Sector Content

| Sector | Content |
|--------|---------|
| (0,0) | Station + 1-2 small belts (starter resources) |
| Others | 2-3 belts each, richer/rarer resources further from center |

### NPC Policy

NPCs are sector-locked — they never cross sector boundaries. NPC AI should include a boundary leash that turns them back before they reach the sector edge. NPCs are spawned per-sector by the owning node. This avoids the complexity of NPC transfer logic.

## 6. Package Structure

```
internal/universe/
  ├── coordinator.go    # Coordinator struct, startup, connection/event routing
  ├── node.go           # Node struct, lifecycle, inbox processing
  ├── topology.go       # 8-connected neighbor computation
  ├── transfer.go       # TransferPayload, handoff logic, ghost/cooldown
  ├── replica.go        # ReplicaSnapshot, replication logic, TTL, coord translation
  ├── message.go        # NodeMessage types (Transfer, Replica, ArrivalConfirm, Chat)
  └── belts.go          # Asteroid belt generation from sector coords

internal/component/
  └── components.go     # Add Ghost, Replica, TransferCooldown components

internal/system/
  └── sector_boundary.go  # Modified: cross-node transfer detection
  └── (all others)         # Modified: filter exclusion for Ghost/Replica

pkg/engine/
  └── loop.go             # Modified: accept per-node events channel
```

Note: `internal/universe/` (not `pkg/universe/`) because it imports `internal/game`, `internal/component`, and `gen/` packages.

## 7. Refactored Startup (`cmd/server/main.go`)

```
Current:
  ConnMgr → Engine → GameWorld → Systems → GameLoop.Run()

New:
  ConnMgr → PlayerDB → Coordinator
    → Coordinator creates 9 Nodes
       → Each Node: Engine → GameWorld → Systems
    → Coordinator computes topology, wires neighbors
    → Coordinator starts all node goroutines
    → Coordinator starts event routing goroutine
  OpRouter (shared — marketplace is node-independent)
  Console (shared — commands need node targeting via "node <id>" prefix)
```

### Console Commands

The admin console needs adaptation for multi-node:
- `nodes` — list all nodes with sector, entity count, player count
- `node <id> <command>` — run a command on a specific node
- `players` — aggregate player list across all nodes
- Commands like `spawn`, `tp`, `kill` need a node context

## 8. Error Handling & Safety

- **Transfer timeout**: If destination doesn't confirm within 10 ticks, ghost is removed and entity is restored as primary on source node (revert transfer).
- **Replica TTL**: Stale replicas auto-expire after 30 ticks. No manual cleanup needed.
- **Transfer cooldown**: 10-tick cooldown after arrival prevents border oscillation.
- **Concurrent access**: All shared state (`PlayerNode` map, `ConnManager`, `PlayerDB`) protected by mutexes. Per-node state is goroutine-local.
- **Shutdown**: Coordinator signals all nodes via context cancellation. Each node saves player state and flushes DB before exiting. Coordinator waits for all nodes to finish.

## 9. Verification Plan

1. **Single node (1 node owns all 9 sectors)**: Functionally identical to current behavior. All gameplay works.
2. **9 nodes (1 per sector)**: Player spawns at (0,0) station. Flies to adjacent sector. Entity transfers seamlessly — no visual pop, no input loss, NetworkID stable.
3. **Border visibility**: Player near sector edge sees entities from neighboring sector via replicas.
4. **Transfer stress**: Player oscillates back and forth at a border — cooldown prevents rapid transfers.
5. **Lock clearing**: Player locks onto a target, crosses sector boundary — lock clears, must re-acquire.
6. **Respawn routing**: Player dies in sector (1,0), respawns at station in sector (0,0).
7. **Docked persistence**: Player docks, undocks — stays on node (0,0).
8. **Asteroid belts**: Each sector has distinct resource clusters. Mining works in any sector.
9. **Disconnect/reconnect**: Player disconnects in sector (1,0), reconnects — spawns on correct node.
10. **Chat relay**: Player in sector (1,0) sends chat — visible to all players across all nodes.
