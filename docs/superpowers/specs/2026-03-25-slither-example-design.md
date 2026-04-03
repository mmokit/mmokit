# Slither.io Example Game — Design Spec

## Context

Build a fully playable slither.io clone as a self-contained example in `examples/slither/`. The goal is to showcase the engine's power — multi-node server meshing, ECS architecture, WebSocket networking, spatial queries — while honestly surfacing areas where the architecture creates friction for games with spatially extended entities. This is the second example (after `examples/twonode/`) and the first with a playable web client.

## Scope

**In scope:** Move, eat food, grow, die on collision, boost (spend mass for speed, leaves food trail), mass decay, food spawning, leaderboard (top-10), AI bots, skins/cosmetics, minimap, kill feed.

**Out of scope:** Persistence (no save/load), chat, marketplace, mobile controls.

---

## Architecture

### Directory Structure

```
examples/slither/
  main.go                  # Coordinator setup, HTTP server, WebSocket + static serving
  config.go                # All tunable game constants
  world.go                 # SlitherWorld embedding WorldBase, overrides
  component.go             # Game-specific ECS components
  entity_snake.go          # Snake entity (player + bot) mappers & spawn
  entity_food.go           # Food entity mappers & spawn
  system_input.go          # Drain client messages, update SnakeInput
  system_bot.go            # AI snake behavior
  system_boost.go          # Boost: spend mass for speed, drop food trail
  system_movement.go       # Steering, speed, ring buffer recording
  system_spatial.go        # Rebuild spatial hash grid
  system_eating.go         # Head-food collision, consume food
  system_collision.go      # Head-body collision detection
  system_decay.go          # Gradual mass loss
  system_death.go          # Process kills, scatter food
  system_food_spawn.go     # Maintain food population
  system_leaderboard.go    # Per-node top-10 aggregation
  system_network.go        # AoI culling, world state broadcast
  logcat.go                # Log category constants
  proto/
    slitherpb/
      slither.proto        # Game-specific protobuf messages
    buf.gen.yaml           # Codegen config (Go + ES modules)
  gen/                     # Generated protobuf code
    go/slitherpb/
    es/slitherpb/
  web/
    index.html
    package.json
    vite.config.ts
    tsconfig.json
    src/
      main.ts              # PixiJS app init
      network.ts           # WebSocket connection + message dispatch
      state.ts             # Client game state (entities, UI)
      input.ts             # Mouse tracking + boost key
      interpolation.ts     # Lerp between 20Hz ticks
      camera.ts            # Follow player head, zoom by size
      snake-renderer.ts    # Draw snake bodies (circles along path)
      food-renderer.ts     # Draw food pellets
      minimap.ts           # World overview overlay
      leaderboard.ts       # Top-10 overlay
      killfeed.ts          # Recent deaths overlay
      login.ts             # Name entry + skin selector
  Makefile                 # build, run, dev, proto targets
```

### Multi-Node Topology

Uses `mmokit.NewCoordinator()` with a configurable grid (default 2x2 = 4 nodes). Each node owns one 8192x8192 sector, runs its own ECS world and game loop at 20Hz. Snakes transfer on boundary crossing. Food and snake bodies are replicated near borders.

Players connect via WebSocket and are routed to the node owning their snake's sector.

### System Execution Order

Systems run every tick (50ms at 20Hz) in this order:

1. **InputSystem** — drain WebSocket messages, update SnakeInput components
2. **BotSystem** — AI state machine, set bot directions
3. **BoostSystem** — if boosting: increase speed, deduct mass, spawn food trail
4. **MovementSystem** — steer toward target angle (capped turn rate), push head position into ring buffer, set Velocity component
5. **PhysicsSystem** — (from `pkg/system`) velocity -> position integration
6. **SpatialSystem** — clear and rebuild spatial hash grid from all entities
7. **EatingSystem** — for each snake head, query spatial grid for food within eating radius; consume, add mass, remove food entity
8. **CollisionSystem** — for each snake head, check against other snake body segments; queue kills
9. **DecaySystem** — reduce mass by small amount per tick; kill if below minimum
10. **DeathSystem** — process queued kills: scatter mass as food along body, remove entity, emit kill feed events
11. **FoodSpawnSystem** — if food count below target, spawn at random positions
12. **LeaderboardSystem** — every 40 ticks, compute top-10 snakes by mass
13. **NetworkSystem** — for each player, query spatial grid for AoI, serialize visible entities, send WorldUpdate; also send leaderboard and kill feed
14. **BoundarySystem** — (auto-appended by Coordinator) detect sector crossings, trigger transfers

---

## Data Model

### Snake Body: Ring Buffer

One ECS entity per snake. A `SnakeBody` component holds a fixed-size ring buffer (512 entries) of past head positions. Each tick, the current head position is pushed. The visible body is the most recent N entries where N = f(mass).

```
Segment spacing: ~20 world units (1 per tick at base speed ~400 units/sec)
Max body length: 512 * 20 = ~10,240 world units
Mass -> Length: length = clamp(mass / massPerSegment, minLength, maxSegments)
Starting mass: 10 -> ~5 segments
Big snake: 500 -> ~250 segments
```

### Game-Specific Components

```go
// SnakeBody stores body as a ring buffer of past head positions.
type SnakeBody struct {
    Segments [512]Segment  // ring buffer
    Head     int           // write index
    Length   int           // active segment count (derived from mass)
}
type Segment struct { X, Y float32 }

// SnakeState holds gameplay state for a snake.
type SnakeState struct {
    Mass        float32   // current mass (score + determines length)
    TargetAngle float32   // desired heading from client input
    Speed       float32   // current speed
    Boosting    bool      // boost active
    TurnRate    float32   // max radians/sec
    SkinID      uint8     // cosmetic selection
    Name        string    // display name
}

// SnakeInput holds per-tick client input.
type SnakeInput struct {
    TargetAngle float32   // mouse angle relative to head
    Boost       bool      // holding boost key
    Sequence    uint32    // input sequence number
}

// Food marks an entity as a food pellet.
type Food struct {
    Value    float32   // mass gained when eaten
    ColorIdx uint8     // visual color (0-7)
}

// Bot marks an entity as AI-controlled.
type Bot struct {
    State   uint8     // 0=wander, 1=seek_food, 2=evade
    Timer   float32   // time in current state
    TargetX float32   // movement target
    TargetY float32
}
```

### Entity Definitions

**Snake (Player)** — EntityKind type=0:
- From `pkg/component`: Position, Velocity, Rotation, NetworkID, EntityKind, Collider (radius ~15), SectorCoord, PlayerConn
- Game-specific: SnakeBody, SnakeState, SnakeInput

**Snake (Bot)** — EntityKind type=1:
- Same as player but PlayerConn.ConnID=0, + Bot component, no SnakeInput (BotSystem writes SnakeState.TargetAngle directly)

**Food (Natural)** — EntityKind type=2:
- From `pkg/component`: Position, NetworkID, EntityKind, Collider (radius ~5), SectorCoord
- Game-specific: Food

**Food (Death/Boost)** — EntityKind type=3:
- Same as natural food but different EntityKind for client rendering (brighter color, slightly larger)

---

## Network Protocol

### Proto File: `examples/slither/proto/slitherpb/slither.proto`

Imports `enginepb/engine.proto` for envelope types.

**Client -> Server (event channel 0x00):**
- Reuse: `CE_LOGIN`, `CE_PING`, `CE_RESPAWN` from enginepb
- New: `SCE_PLAYER_INPUT (100)` — `SlitherInputMsg { target_angle, boost, sequence }`
- New: `SCE_SELECT_SKIN (101)` — `SkinSelectMsg { skin_id, name }`

**Server -> Client (event channel 0x00):**
- Reuse: `SE_PONG`, `SE_PLAYER_DIED`, `SE_SECTOR_CHANGE` from enginepb
- New: `SSE_WORLD_UPDATE (100)` — `SlitherWorldUpdateMsg { tick, snakes[], foods[], removed_ids[] }`
- New: `SSE_SPAWNED (101)` — `SlitherSpawnedMsg { your_entity_id, sector_x, sector_y }`
- New: `SSE_LEADERBOARD (102)` — `LeaderboardMsg { entries[] }`
- New: `SSE_KILL_FEED (103)` — `KillFeedMsg { entries[] }`

**Bandwidth optimizations:**
- Snake segments subsampled (every 3rd-5th position); client interpolates a smooth curve
- Food sent only on AoI enter; `removed_ids` on leave/consume
- Leaderboard sent every 2 seconds
- Kill feed sent on event only

---

## Cross-Node Meshing

### Snake Transfers

When `BoundarySystem` detects a snake head crossing a sector boundary:

1. `SerializeEntity()` override calls `SerializeEntityCore()` for core fields, then appends `SnakeBody`, `SnakeState`, `Bot` (if present) as `ComponentSlice` entries. All segment positions are offset by the sector delta.
2. Source node adds Ghost (TTL=10 ticks). Destination spawns via `SpawnFromTransfer()` override which decodes and adds game-specific components.
3. `TransferCooldown` set to 10 ticks (shorter than default 20 — snakes move fast).
4. Body segments with negative coords in the new sector are fine — they represent the tail extending into the old sector. Clients near the border see both via AoI.

### Replica Strategy for Extended Bodies

**The engine's `ScanBorderEntities` only checks entity Position (head).** A snake whose head is in the center but body extends to a border will be missed. Solution: override `ScanBorderEntities` in `SlitherWorld`:

1. For food entities: delegate to `WorldBase.ScanBorderEntities` (position check works)
2. For snake entities: compute bounding box of all body segments. If any segment is within AoI margin of any border, include the snake in the replica set for that neighbor.
3. Register `ComponentReplicator` for `SnakeBody` (ID=1): serialize only segments near the border (not the full 512-entry ring buffer). ~50 border segments * 8 bytes = ~400 bytes per replica snake.
4. Register `ComponentReplicator` for `SnakeState` (ID=2): serialize mass, speed, skin for rendering and collision purposes.

### Cross-Node Collision

**Key insight: "death is always local."** Only the node owning a snake's head is authoritative for killing that snake.

`CollisionSystem` checks local snake heads against both local AND replica snake bodies. If a local head hits a replica body, the local snake dies immediately — no cross-node messaging needed. If a replica head would hit a local body, it's ignored (the other node handles it). Head-on collisions: each node independently kills its own snake.

### Cross-Node Food Eating

- **Local food:** Consume directly (remove entity, add mass).
- **Replica food:** Use `CrossNodeAction { type: EAT, targetNetID }`. Authoritative node removes food, returns mass gained. Optimistic local prediction: immediately add mass and remove replica visually. If food was already eaten by another snake, subtract mass back.

### Death Food Scatter

When a snake dies, mass scatters as food along its body. For segments in the local sector: spawn food directly. For segments in neighbor sectors: use `CrossNodeAction { type: SPAWN_FOOD, positions, values }` — pack multiple spawn requests into one action payload. Authoritative node spawns food entities.

Boost trail: at most 1 food drop per tick, almost always local. Same mechanism for remote sectors.

### Leaderboard

Per-node top-10 computed every 40 ticks. Each node includes replica snakes for deduplication by NetworkID. Good enough for a 2x2 grid where most large snakes are visible via replicas.

---

## Client Design

### Tech Stack

PixiJS + TypeScript + Vite, same tooling as `web-pixi/`. Uses `bun` as package manager. Protobuf via `@bufbuild/protobuf` for ES module codegen.

### Rendering Pipeline

Server sends subsampled segments (every 3rd-5th) per snake in `WorldUpdate` at 20Hz.

Client pipeline (60Hz render):
1. On `WorldUpdate`: store `{prev, curr}` segment arrays per snake
2. Each render frame: interpolate head position via lerp between prev/curr tick
3. For t > 1.0: extrapolate using angle + speed
4. Draw body as chain of circles along the path, tapering toward tail
5. Head drawn as larger circle with eye sprites
6. If boosting: add particle trail effect behind tail

### Camera

Follows player head. Zoom level adjusts based on snake size (larger snakes zoom out slightly for better situational awareness).

### UI Overlays

- **Minimap** (200x200 corner): full world bounds, sector grid lines, dots for large snakes colored by skin, own position highlighted
- **Leaderboard** (top-right): top-10 entries, highlight own position
- **Kill feed** (top-left): fade-in/out death notifications
- **Login screen**: name input + 8 skin color options

### Login & Respawn

1. Connect WebSocket -> show login screen
2. Submit name + skin -> server spawns snake, sends `Spawned` message
3. On death: show death screen with killer info and mass achieved, click to respawn after 2s cooldown

---

## AI Bots

Simple 3-state machine spawned at world init (5 per node = 20 total):

- **WANDER:** Random direction, slight perturbation, change every 2-5s
- **SEEK_FOOD:** Query spatial grid for nearest food within 500 units, steer toward it. Boost if mass > threshold.
- **EVADE:** If a larger snake head is within 300 units and roughly facing the bot, turn perpendicular and boost briefly.

Transitions:
```
WANDER -> SEEK_FOOD  (food detected within 500u)
WANDER -> EVADE      (large snake head nearby, facing us)
SEEK_FOOD -> WANDER  (food eaten or timer expires)
SEEK_FOOD -> EVADE   (danger detected)
EVADE -> WANDER      (after 1-2s timer)
```

Bots respawn after death with a short delay (queued via TickQueue or pending admin command).

---

## Configuration

All tunable parameters in `config.go`:

```go
type SlitherConfig struct {
    // Movement
    BaseSpeed       float32  // 400 units/sec
    BoostSpeed      float32  // 800 units/sec
    TurnRate        float32  // 4.0 rad/sec

    // Growth
    StartingMass    float32  // 10
    MassPerSegment  float32  // 2.0
    MinSegments     int      // 3
    MaxSegments     int      // 512

    // Decay
    DecayRate       float32  // 0.1 mass/sec
    MinMass         float32  // 3.0 (die below this)

    // Boost
    BoostMassCost   float32  // 0.5 mass/tick
    BoostFoodValue  float32  // 0.3 per dropped food

    // Food
    FoodPerNode     int      // 500
    NaturalFoodValue float32 // 1.0
    EatingRadius    float32  // 30 units

    // Collision
    HeadCollisionRadius float32  // 15 units
    BodyCollisionRadius float32  // 12 units

    // Bots
    BotsPerNode     int      // 5
    BotRespawnDelay float32  // 3.0 seconds

    // Network
    AoIRadius       float32  // 3000 units
    SegmentSubsample int     // 3 (send every 3rd segment)
    LeaderboardInterval int  // 40 ticks (2s)
}
```

---

## Engine Friction Points

This example will expose these engine limitations:

1. **No spatial extent in replication** (Hard) — Border scan only checks entity Position. Snake bodies need a full override of `ScanBorderEntities`. Engine improvement: pluggable "am I near the border?" callback per entity type.

2. **No direction-aware component replication** (Moderate) — `ComponentReplicator.Scan` doesn't know which neighbor it's scanning for. Can't send different segment subsets to different neighbors. Workaround: send all border-adjacent segments to all neighbors.

3. **CrossNodeAction for bulk spawns** (Moderate) — Death scatter needs many food entities on a neighbor. Current `CrossNodeAction` is designed for single interactions. Workaround: pack multiple spawn requests into one action payload.

4. **No cross-node data aggregation** (Simple) — No built-in leaderboard aggregation. Per-node top-10 with replica dedup is good enough.

5. **Static file serving** (Simple) — `ConnManager` only sets up `/ws`. Example needs custom HTTP mux. Small wrapper.

---

## Verification Plan

1. **Build & run:** `cd examples/slither && make dev` — server starts, web client accessible at localhost:8080
2. **Single player:** Connect, enter name, select skin, verify snake appears and follows mouse
3. **Food eating:** Move over food, verify mass increases and food disappears
4. **Growth:** Eat food, verify snake body lengthens proportionally
5. **Boost:** Hold space, verify speed increase, mass decrease, food trail appears behind
6. **Collision:** Run head into another snake's body (bot), verify death and food scatter
7. **Multi-node transfer:** Move snake across sector boundary, verify seamless transition (no visual pop, body continuous)
8. **Cross-node collision:** Collide with a snake whose body crosses a sector boundary, verify kill works
9. **Cross-node food:** Eat food near a border that's replicated from neighbor, verify mass gain
10. **Leaderboard:** Eat food, verify appearing on leaderboard; die, verify removal
11. **Kill feed:** Kill a bot or be killed, verify kill feed notification
12. **Minimap:** Verify own position and large snakes visible on minimap
13. **AI bots:** Verify bots wander, eat food, grow, and die naturally
14. **Multiple players:** Open 2+ browser tabs, verify both snakes visible and interactable
15. **Stress test:** Spawn many bots, verify performance stays acceptable at 20Hz

### Critical Files to Reference During Implementation

- [world_base.go](pkg/universe/world_base.go) — `SerializeEntityCore`, `SpawnFromTransferCore`, `SpawnEntity`, `ScanBorderEntities` defaults
- [world.go](pkg/universe/world.go) — `GameWorld` interface to implement
- [replication.go](pkg/universe/replication.go) — `ComponentReplicator`, `ReplicaFrame`, `ReplicationRegistry`
- [message.go](pkg/universe/message.go) — `NodeMessage` envelope, `CrossNodeAction`
- [boundary_system.go](pkg/universe/boundary_system.go) — Transfer trigger logic
- [coordinator.go](pkg/universe/coordinator.go) — `NewCoordinator`, `GridConfig`, `NodeFactory`
- [twonode/main.go](examples/twonode/main.go) — Example pattern to follow
- [mmokit.go](pkg/mmokit/mmokit.go) — Facade re-exports
