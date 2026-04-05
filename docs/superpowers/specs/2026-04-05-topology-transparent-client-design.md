# Topology-Transparent Client Protocol

## Problem

The client protocol exposes server mesh topology through multiple channels:

1. **SpawnedMsg** includes `cell_x`, `cell_y`, `grid_w`, `grid_h`, `cell_size` — server-internal grid metadata
2. **CellTopologyMsg** sends cell bounds, depths, and node IDs to clients by default
3. **MeshState binding** sends per-entity LOCAL/REPLICA/GHOST status and owner node index

Production MMO architectures (SpatialOS, Ashes of Creation, Star Citizen) treat topology as a server-internal concern. Clients receive a unified entity stream in world-space coordinates with zero awareness of which server owns which entity. This simplifies the client, reduces wire overhead, and makes the server mesh an implementation detail that can change without client updates.

## Design

### Core Principle

The client receives entities with world-space positions. It has no knowledge of cells, nodes, or grid layout. All topology info is debug-only, controlled by a single coordinator flag, delivered through its own message type.

### 1. SpawnedMsg — Always Clean

Strip grid metadata from `SpawnedMsg`. Add `world_x`/`world_y` for the initial player position. The server computes world position (`pos.X + cellX * cellSize`) before sending.

**Proto change** (`proto/enginepb/engine.proto`):

```protobuf
// Before:
message SpawnedMsg {
    uint32 entity_net_id = 1;
    int32  cell_x        = 2;
    int32  cell_y        = 3;
    float  cell_size     = 4;
    int32  grid_w        = 5;
    int32  grid_h        = 6;
}

// After:
message SpawnedMsg {
    uint32 entity_net_id = 1;
    float  world_x       = 2;
    float  world_y       = 3;
}
```

Fields renumbered from 1. No reserved field numbers — clean break.

**Server change** (`pkg/universe/world_base.go`, `SendSpawnedMsg`):

```go
func (b *WorldBase) SendSpawnedMsg(connID uint32, entity ecs.Entity) {
    netID := b.netIDMap.Get(entity).ID
    pos := b.posMap.Get(entity)
    cell := b.rootCell()
    cs := coords.CellSize
    msg := &enginepb.SpawnedMsg{
        EntityNetId: netID,
        WorldX:      pos.X + float32(cell.X)*cs,
        WorldY:      pos.Y + float32(cell.Y)*cs,
    }
    frame := makeEventFrame(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), msg)
    b.eng.ConnMgr.Send(connID, frame)
}
```

### 2. DebugTopology Coordinator Flag

Add `DebugTopology bool` to `Config` in `pkg/universe/coordinator.go`. This single flag controls:

- **MeshState binding** — included in EngineBindings when true
- **CellTopologyMsg** — sent to clients on connect and topology changes when true
- **Schema export** — `--dump-schema` includes meshState fields when the game sets DebugTopology

**When `DebugTopology: false` (default):**
- EngineBindings omits MeshState (saves 2 bytes/entity/tick)
- CellTopologyMsg never sent
- Client receives a topology-agnostic protocol

**When `DebugTopology: true` (e.g., 4node-basic):**
- EngineBindings includes MeshState
- CellTopologyMsg sent on connect and topology changes
- Client can render cell boundaries, R/G badges, node ownership

### 3. Wiring DebugTopology to EngineBindings

The coordinator already creates WorldBase instances and wires hooks. The `DebugTopology` flag needs to flow into the `EngineBindingsConfig` used when building replicators.

**In `pkg/mmokit/mmokit.go`**, the `BuildReplicators` function (which processes `EntityKindDef`s and calls `EngineBindings`) receives the coordinator. It can read `coord.Cfg().DebugTopology` to set `IncludeMeshState` automatically:

```go
// In BuildReplicators or the EntityKindDef processing path:
ebCfg.IncludeMeshState = coord != nil && coord.Cfg().DebugTopology
```

Games no longer set `IncludeMeshState` manually. The `IncludeMeshState` field stays on `EngineBindingsConfig` as the internal mechanism, but it's driven by the coordinator flag.

### 4. CellTopology Gating

**In `pkg/universe/coordinator.go`:**

- `SendCellTopology(connID)` — early-return if `!c.cfg.DebugTopology`
- `BroadcastCellTopology()` — early-return if `!c.cfg.DebugTopology`
- `OnTopologyChanged` default callback — only set when `DebugTopology && DynamicPartitioning != nil`

**In game code** (e.g., `examples/4node-basic/world.go`):

The explicit `gw.SendCellTopology(s.ConnID)` call in the OnEnter callback becomes unnecessary when `DebugTopology` is true — the coordinator can auto-send on connect. When false, it's a no-op. This keeps game code clean.

### 5. Schema Export & SDK Codegen

The `--dump-schema` flag exports the binding layout that drives SDK codegen. The schema must match what the server actually sends.

**In `examples/4node-basic/schema.go`:**

```go
func dumpProtocolSchema() {
    // ...
    def := playerKindDef(w)
    // DebugTopology=true means schema includes meshState fields
    proto.SetReplicators(mmokit.BuildReplicators(w, nil, def))
    // ...
}
```

Since 4node-basic sets `DebugTopology: true`, its schema includes meshState. A production game with `DebugTopology: false` would generate a schema without meshState, producing a leaner SDK.

The `SE_CELL_TOPOLOGY` server event registration in `schema.go` should also be conditional — only registered when the game wants debug topology in its SDK:

```go
if debugTopology {
    mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_CELL_TOPOLOGY, "cellTopology", "enginepb.CellTopologyMsg")
}
```

### 6. Client Changes (4node-basic)

**state.ts:**
- Remove `gridW`, `gridH`, `cellSize` from `GameState` (no longer in SpawnedMsg)
- Keep `cells: CellInfo[]` but only populated when CellTopologyMsg arrives (debug mode)
- Add `worldX`/`worldY` from SpawnedMsg for initial camera position

**network.ts:**
- `onPlayerSpawned`: read `world_x`/`world_y` instead of grid metadata. Set initial camera position.
- `onCellTopology`: still handled when present (debug mode), populates `cells[]` for renderer
- `isReplica`/`isGhost`: only derived when `meshState` field exists in SDK entity type

**renderer.ts:**
- Cell boundary rendering: conditional on `state.cells.length > 0`
- R/G badges: conditional on entity having `meshState` field (or `isReplica`/`isGhost`)
- Entity rendering: unchanged — already uses absolute world positions

**SDK regeneration:**
- Run `make client-sdk GAME=examples/4node-basic` after proto + server changes
- The generated SDK will include meshState fields because 4node-basic has DebugTopology=true

### 7. Slither Compatibility

Slither uses hand-coded replication with cell-local coordinates and explicit client-side rebasing (`handleCellChange`). This design does NOT change slither — it continues working as-is.

**However**, slither's SpawnedMsg handling will need updating since the proto fields change. Slither uses its own `SlitherSpawnedMsg` proto (not `enginepb.SpawnedMsg`), so it's unaffected by the engine proto change. Slither can be migrated to the transparent model in a future follow-up.

The `WorldBase.SendSpawnedMsg` method is used by slither indirectly (via the framework). Since we're changing it to send `world_x`/`world_y` instead of cell metadata, slither's client will receive the new format. Slither's client needs the cell coordinates for rebasing — so either:

**(a)** Slither overrides `SendSpawnedMsg` with its own that includes cell info (its `SlitherSpawnedMsg` already has this).
**(b)** Slither's `SendSpawnedMsg` call is already handled by `examples/slither/entity_snake.go` which calls the base `SendSpawnedMsg`. This would need a check.

Looking at the code: `examples/slither/entity_snake.go:58` calls `gw.SendSpawnedMsg(connID, entity)` which uses the `WorldBase` method. So slither DOES use the base method. The slither client reads `cell_x`/`cell_y` from the SpawnedMsg for rebasing.

**Resolution:** Slither's client can derive cell coords from the new `world_x`/`world_y` fields:

```typescript
const cellX = Math.floor(msg.worldX / CELL_SIZE);
const cellY = Math.floor(msg.worldY / CELL_SIZE);
state.handleCellChange(cellX, cellY);
```

No server-side override needed. The base `WorldBase.SendSpawnedMsg` sends world coords, and slither's client computes the cell from them. The rebasing logic continues to work identically.

### 8. Internal Game (`internal/game/`)

The internal game (`internal/game/entity_ship.go:176`) builds its own `gamepb.PlayerSpawnedMsg` with game-specific fields. It does NOT use `WorldBase.SendSpawnedMsg`. So it's unaffected by this change — it already has its own spawn message.

## Files Changed

| File | Change |
|------|--------|
| `proto/enginepb/engine.proto` | Strip cell fields from SpawnedMsg, add world_x/world_y |
| `gen/go/enginepb/` | Regenerated (make proto) |
| `gen/es/enginepb/` | Regenerated (make proto) |
| `gen/csharp/` | Regenerated (make proto) |
| `pkg/universe/coordinator.go` | Add `DebugTopology` to Config, gate CellTopology sending |
| `pkg/universe/world_base.go` | Update SendSpawnedMsg to send world coords |
| `pkg/mmokit/mmokit.go` | Wire DebugTopology → IncludeMeshState in BuildReplicators |
| `examples/4node-basic/main.go` | Set DebugTopology: true |
| `examples/4node-basic/world.go` | Remove manual IncludeMeshState from EntityKindDef |
| `examples/4node-basic/schema.go` | Conditionally register CellTopology event |
| `examples/4node-basic/web/src/state.ts` | Remove grid state, add worldX/worldY |
| `examples/4node-basic/web/src/network.ts` | Update spawn handler, make topology conditional |
| `examples/4node-basic/web/src/renderer.ts` | Make cell viz conditional on cells[] presence |
| `examples/4node-basic/web/sdk/` | Regenerated (make client-sdk) |
| `examples/slither/entity_snake.go` | Override SendSpawnedMsg to use SlitherSpawnedMsg with cell coords |

## Verification

1. `go vet ./...` — all packages compile
2. `go test ./...` — all tests pass
3. `make proto` — protobuf regeneration succeeds
4. 4node-basic with `DebugTopology: true`:
   - Run `make dev`, open browser
   - Cell boundaries visible, R/G badges on replicas, node ownership colors
   - Entities move smoothly across cell boundaries
5. 4node-basic with `DebugTopology: false`:
   - Comment out the flag, rebuild + regenerate SDK
   - No cell boundaries, no R/G badges
   - Entities still render and move correctly — client is topology-agnostic
6. Slither still works:
   - `cd examples/slither && make dev`
   - Cell rebasing still functional, no regressions
