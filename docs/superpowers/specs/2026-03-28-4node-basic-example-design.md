# 4-Node Basic Example Design

## Context

We need a minimal, educational example in `/examples/4node-basic` that demonstrates mmokit's server meshing features with the simplest possible code. Players connect via a plain HTML web client, appear as colored circles, and click-to-move. The client renders extensive debug overlays showing cell boundaries, AoI, replicas, ghosts, transfers, and per-node stats — making the meshing machinery visible.

This example builds **only** on `pkg/` (mmokit core). No dependencies on `internal/` or other game implementations.

## Architecture

### Server (Go, ~450 lines)

**Grid:** 2×2 cells, cell size 2000 units, AoI radius 1500. Total world: 4000×4000.

**Entity:** Single type — player circle. Components: Position, Velocity, NetworkID, EntityKind, Collider, PlayerConn, CellCoord, Ghost, Replica, MoveTarget.

**Systems (execution order):**
1. `InputRouter` — parse BCE_LOGIN and BCE_PLAYER_INPUT from clients
2. `MovementSystem` — steer toward MoveTarget with acceleration + friction
3. `PhysicsSystem` (mmokit built-in) — integrate velocity into position
4. `SpatialSystem` — update spatial grid entries
5. `NetworkSystem` — send world updates + debug info to clients

### File Layout

```
examples/4node-basic/
├── main.go              # Coordinator, HTTP, signal handling (~80 lines)
├── world.go             # BasicWorld, login, spawn, hooks (~130 lines)
├── entity_player.go     # Player mappers + SpawnPlayer (~50 lines)
├── system_input.go      # InputRouter setup (~30 lines)
├── system_movement.go   # Move-toward-target + friction (~40 lines)
├── system_spatial.go    # Spatial grid position sync (~30 lines)
├── system_network.go    # Custom world update + debug events (~100 lines)
├── config.go            # Tunable constants (~20 lines)
└── web/
    └── index.html       # Single-file client (~500 lines)

proto/basicpb/
└── basic.proto          # Game-specific messages (~40 lines)
```

### Proto (`proto/basicpb/basic.proto`)

```protobuf
syntax = "proto3";
package basicpb;
option go_package = "github.com/zenion/mmokit/gen/go/basicpb";

// Client → Server
enum BasicClientEventCode {
    BCE_UNKNOWN = 0;
    BCE_PLAYER_INPUT = 100;
    BCE_LOGIN = 101;
}

message BasicLoginMsg {
    string name = 1;
}

message BasicInputMsg {
    float move_x = 1;
    float move_y = 2;
    bool move_active = 3;
    uint32 sequence = 4;
}

// Server → Client
enum BasicServerEventCode {
    BSE_UNKNOWN = 0;
    BSE_DEBUG_INFO = 200;
}

// Payload for SE_PLAYER_SPAWNED
message BasicSpawnedMsg {
    uint32 entity_net_id = 1;
    int32 cell_x = 2;
    int32 cell_y = 3;
    float cell_size = 4;
    int32 grid_w = 5;
    int32 grid_h = 6;
    float aoi_radius = 7;
}

// Periodic debug metadata
message BasicDebugInfoMsg {
    string player_node_id = 1;
    repeated NodeInfo nodes = 2;
}

message NodeInfo {
    string node_id = 1;
    int32 cell_x = 2;
    int32 cell_y = 3;
    int32 entity_count = 4;
    int32 player_count = 5;
}
```

### World Update Binary Format

Bypasses ReplicationSystem. Custom NetworkSystem sends full entity state every tick as a flat binary frame (no delta encoding). Sent as `SE_DELTA_WORLD_UPDATE` (code 13) inside a ServerEvent envelope.

```
Header (16 bytes, big-endian):
  [4] tick           uint32
  [4] viewerX        float32
  [4] viewerY        float32
  [2] entityCount    uint16
  [2] removedCount   uint16

Per entity (28 + nameLen bytes):
  [4] netID          uint32
  [1] entityType     uint8
  [1] flags          uint8    (bit0=replica, bit1=ghost)
  [4] x              float32
  [4] y              float32
  [4] vx             float32
  [4] vy             float32
  [4] radius         float32
  [1] nodeIndex      uint8    (0-3, which node owns this entity)
  [1] nameLen        uint8
  [N] name           bytes

Removed IDs:
  [4] * removedCount  uint32
```

### Client (`web/index.html`)

Single HTML file, zero dependencies, Canvas2D rendering.

**Sections:**
1. **Protobuf helpers** (~50 lines) — minimal varint/message encode+decode for ClientEvent, ServerEvent, BasicLoginMsg, BasicInputMsg only
2. **WebSocket** (~40 lines) — connect, send with 0x00 channel prefix, demux received events by code
3. **Binary decoder** (~40 lines) — parse the flat world update format above
4. **State + interpolation** (~40 lines) — entity map with prev/curr for lerp between ticks
5. **Input** (~30 lines) — click-to-move sends BasicInputMsg
6. **Canvas renderer** (~300 lines) — all debug overlays

### Debug Overlays (all rendered every frame)

| Overlay | Visual |
|---------|--------|
| Cell boundaries | Thick colored dashed lines at x=2000, y=2000 |
| Node ownership | Each quadrant tinted with node's color (4 distinct pastel colors) |
| Entity circles | Filled circle, stroke color = owning node color |
| NetID labels | Small text above each entity showing network ID |
| AoI radius | Large dashed circle centered on player |
| Replica markers | Dashed outline + "R" badge on replica entities |
| Ghost markers | Semi-transparent + "G" badge on ghost entities |
| Transfer flash | Brief yellow pulse when entity crosses boundary |
| Velocity vectors | Arrow from entity center in movement direction |
| Player names | Text below each circle |
| Tick counter | Top-left: "Tick: 12345" |
| Node stats panel | Top-right: per-node entity/player counts |
| Current node | "Node: cell_0_1" with cell highlight |
| FPS counter | Top-left alongside tick |

### Server Flow

1. **Connect:** WebSocket → ConnManager → Coordinator routes to default node (0,0)
2. **Login:** InputRouter drains BCE_LOGIN → sets username → transition to StateActive
3. **Spawn:** StateActive.OnEnter → SpawnPlayer at random position in cell → send BasicSpawnedMsg with grid metadata
4. **Input:** BCE_PLAYER_INPUT → update MoveTarget on player entity
5. **Movement:** MovementSystem steers toward target, PhysicsSystem integrates velocity
6. **Boundary crossing:** BoundarySystem (auto-added by Coordinator) detects cell exit → transfers entity
7. **Replication:** WorldBase handles ScanBorderEntities/ApplyReplicas for border visibility
8. **Network:** Custom NetworkSystem queries spatial grid per viewer, builds binary frame with flags (replica/ghost/nodeIndex), sends

### Constants (`config.go`)

```go
CellSize     = 2000.0
AoIRadius    = 1500.0
TickRate     = 20
MoveSpeed    = 300.0  // units/sec
Friction     = 0.92   // velocity decay per tick
PlayerRadius = 20.0
GridMinSX    = 0
GridMaxSX    = 1
GridMinSY    = 0
GridMaxSY    = 1
```

## Verification

1. `make proto` regenerates basicpb Go code
2. `go vet ./examples/4node-basic/...` compiles
3. Run server: `go run ./examples/4node-basic -port 8081`
4. Open `http://localhost:8081` in browser — login screen appears
5. Enter name, click connect — circle spawns
6. Click to move — circle moves toward click point
7. Move across cell boundary — entity transfers to new node (ghost/replica visible in debug)
8. Open second browser tab — see both players with debug overlays
9. All debug overlays render correctly (cell lines, AoI, replicas, node stats)
