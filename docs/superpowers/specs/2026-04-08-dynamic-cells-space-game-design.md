# Dynamic Cell Partitioning for the Space Game

## Context

The `pkg/universe/` layer has complete dynamic cell partitioning support — runtime splitting and merging of cells based on load, console commands (`cell split/merge`), monitoring, and topology broadcasting. The 4node-basic example demonstrates it end-to-end. The space game (`internal/game/`) does not yet support it. This spec covers wiring dynamic cells into the space game and updating the web client to display topology-aware overlays.

The connection proxy refactor (coordinator-level login, `PlayerRouter`, `activeUsers` tracking, coordinator-routed console commands) is already done and provides the foundation — no node is special, players are routed by position, and console commands find players on any node.

## Scope

**In scope:**
- `FromSplit` flag on `WorldBase` — skip initial entity spawning for split-created worlds
- Docked player transfer during splits — session-only transfer (no entity) to station's destination child
- Replace `GridCellsX`/`GridCellsY` in `PlayerSpawnedMsg` with `CellTopologyMsg`
- `debug` console command replacing `grid` — server-controlled global debug overlay
- Web client topology overlay on canvas and tab map
- Wire `-dynamic-cells` flag in `cmd/server/main.go`

**Out of scope:**
- Cross-cell teleport (tp triggers entity transfer)
- Automatic split/merge tuning for the space game (use defaults)
- Client-side debug toggle (server-controlled only)

## 1. FromSplit Flag

### Problem
`NewGameWorld()` at `internal/game/game.go:135-138` unconditionally spawns asteroids and the station:
```go
gw.spawnAsteroids()
if cell == cfg.StationCell {
    gw.SpawnStation()
}
```
During a cell split, `createNode()` calls the world factory → `NewGameWorld()` → spawns new entities. Then the parent's existing entities are transferred to the children, creating duplicates.

### Solution
Add a `fromSplit bool` field and `FromSplit() bool` accessor to `WorldBase` in `pkg/universe/world_base.go`. In `createNode()` (`pkg/universe/coordinator.go`), add an optional parameter or second creation path that sets `fromSplit = true` when called from `SplitCell()`.

The game factory checks this and skips initial entity spawning:
```go
if !base.FromSplit() {
    gw.spawnAsteroids()
    if cell == cfg.StationCell {
        gw.SpawnStation()
    }
}
```

### Implementation
- `pkg/universe/world_base.go`: Add `fromSplit bool` field + `FromSplit() bool` method + `setFromSplit()` setter
- `pkg/universe/partition.go`: In `SplitCell()`, after `createNode()`, call `base.setFromSplit()` on each child (or pass flag through `createNode`)
- `internal/game/game.go`: Guard spawn calls with `!fromSplit` check (fromSplit passed from factory via `base.FromSplit()`)

## 2. Docked Player Transfer

### Problem
Docked players have no entity — it's removed when they dock (`lifecycle.go:64`). The partition system only serializes entities with `Position`. When a cell splits, docked players would be stranded on the old (shut-down) node.

### Solution
During `SplitCell()`, after serializing entities, also collect docked sessions (sessions in `StateDocked` with no entity). These are transferred as session-only data to the child cell that contains the station. On the destination node, the session is recreated in `StateDocked` state, completely transparent to the player.

### Implementation

**In `pkg/universe/partition.go` (SplitCell):**

After the entity serialization phase, add a session collection phase:
1. Iterate the old node's `PlayerManager` sessions
2. For each session NOT in `StateActive`/`StateTransferring` and with no alive entity (e.g., `StateDocked`, `StateDead`):
   - Record `{ConnID, Username, State, Data}` 
   - Route to the child cell containing the station (or the default child if no station)
3. Send these as a new `MsgSessionTransfer` message to the destination child
4. Update `playerNode` routing

**In `pkg/universe/node.go` (processMessage):**

Handle `MsgSessionTransfer`:
- For each session in the message, call `RegisterPlayer(connID, username)`
- Set `s.State` directly to the transferred state (e.g., `StateDocked`)
- Set `s.Data` from the transfer

**New message type:**
```go
MsgSessionTransfer MsgType = 12 // entity-less session transfer during split

type SessionTransfer struct {
    ConnID   uint32
    Username string
    State    engine.PlayerState
    Data     any
}
```

**GameWorld interface:** Add optional `OnPreSplit()` method to `GameWorld` (default no-op in `WorldBase`). Called on the old node's game loop before entity serialization. The space game can use this to prepare docked players if needed (e.g., save state).

## 3. Replace GridCellsX/Y with CellTopologyMsg

### Problem
`PlayerSpawnedMsg` sends static `GridCellsX`/`GridCellsY` at `entity_ship.go:181-182`. After cell splits, these are meaningless — the grid is no longer uniform.

### Solution
Remove `GridCellsX`/`GridCellsY` from `PlayerSpawnedMsg` (and proto). Send `CellTopologyMsg` (already exists in `enginepb`) on spawn and after topology changes.

### Implementation

**Proto:** Remove `grid_cells_x` and `grid_cells_y` fields from `PlayerSpawnedMsg` in `proto/gamepb/game.proto`. Renumber remaining fields from 1 (per project convention). Run `make proto`.

**Server (`internal/game/entity_ship.go`):** Remove `GridCellsX`/`GridCellsY` from `PlayerSpawnedMsg` construction. After sending spawn data, call `SendCellTopology(connID)` via a callback to the adapter.

**Server (`internal/game/adapter.go`):** In `SetOnPlayerTransferReceived`, also send topology after cell change: `a.SendCellTopology(frame.ConnID)`.

**Web client:** Remove `gridCellsX`/`gridCellsY` from spawn handler in `network.ts`. Derive grid bounds from topology data.

## 4. Debug Command + Topology Overlay

### Problem
The `grid` console command toggles a simple uniform grid overlay. With dynamic cells, the overlay needs to show actual cell boundaries (which may be non-uniform after splits).

### Solution

**Server:** Replace `grid` command with `debug` command. When enabled:
- Sets `DebugShowCellGrid = true` on all nodes (existing mechanism)
- Sends `CellTopologyMsg` to all connected clients via `BroadcastCellTopology()`
- Sends `DebugFlagsMsg` to all clients (existing mechanism)
- The coordinator's `DebugTopology` flag (if set) auto-broadcasts topology on changes

When disabled:
- Clears `DebugShowCellGrid` on all nodes
- Clients stop rendering the overlay

This is a global server-controlled toggle — clients don't have their own toggle.

**Web client — Canvas overlay (`grid.ts`):**
- When topology data is available, draw cell boundaries from actual cell bounds (mixed sizes for subcells)
- Dashed lines for depth > 0 cells, solid for top-level
- Cell names (coordinates) in all 4 corners of each cell (existing pattern)
- When no topology data, fall back to uniform grid (backward compat)
- New method: `setTopology(cells: CellInfo[])` replaces `setGridSize()`

**Web client — Tab map (`cell-map.ts`):**
- `drawGrid()` uses topology cells when available
- Each cell renders as a rectangle at `(originX, originY)` with `size × size`
- Cell labels show coordinates, dashed borders for subcells

**Web client — State + Network:**
- `state.ts`: Add `cellTopology: CellInfo[] | null` field
- `network.ts`: Handle `SE_CELL_TOPOLOGY` event, parse `CellTopologyMsg`, store in state
- When topology arrives, derive world bounds from cell data (replaces `gridCellsX/Y`)

### CellInfo shape (TypeScript)
```typescript
interface CellInfo {
  cellX: number;
  cellY: number;
  depth: number;
  size: number;
  originX: number;
  originY: number;
  nodeId: string;
}
```

## 5. Wire Dynamic Partitioning in main.go

### Implementation

Add `-dynamic-cells` flag to `cmd/server/main.go`. When enabled:
```go
if *dynamicCells {
    cfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
    cfg.DebugTopology = true
    log.Println("dynamic cell partitioning enabled")
}
```

`OnTopologyChanged` is auto-wired to `BroadcastCellTopology()` by the coordinator when `DebugTopology` is true (already implemented in `coordinator.go:206-210`).

## Files Changed

### pkg/ layer
| File | Change |
|------|--------|
| `pkg/universe/world_base.go` | Add `fromSplit bool` field + `FromSplit()` accessor |
| `pkg/universe/partition.go` | Set `fromSplit` on child nodes; collect and transfer docked sessions |
| `pkg/universe/message.go` | Add `MsgSessionTransfer` type + `SessionTransfer` struct |
| `pkg/universe/node.go` | Handle `MsgSessionTransfer` |
| `pkg/universe/world.go` | Add optional `OnPreSplit()` to GameWorld interface |

### Game layer
| File | Change |
|------|--------|
| `cmd/server/main.go` | Add `-dynamic-cells` flag |
| `internal/game/game.go` | Guard entity spawning with `!fromSplit` |
| `internal/game/entity_ship.go` | Remove `GridCellsX`/`GridCellsY`, send topology on spawn |
| `internal/game/adapter.go` | Send topology on transfer received |
| `internal/game/commands.go` | Replace `grid` with `debug` command |

### Proto
| File | Change |
|------|--------|
| `proto/gamepb/game.proto` | Remove `grid_cells_x`/`grid_cells_y` from `PlayerSpawnedMsg` |

### Web client
| File | Change |
|------|--------|
| `web-pixi/src/network.ts` | Handle `SE_CELL_TOPOLOGY`, remove `gridCellsX/Y` |
| `web-pixi/src/state.ts` | Add `cellTopology` field, remove `gridCellsX/Y` |
| `web-pixi/src/world/grid.ts` | Topology-aware rendering, `setTopology()` |
| `web-pixi/src/ui/cell-map.ts` | Topology-aware tab map rendering |

## Verification

1. Start server with `-dynamic-cells`. Login. `debug` enables overlay.
2. `cell split 1 1` — station cell splits into 4. Asteroids and station transfer correctly (no duplicates). Player (if in that cell) transfers correctly.
3. Docked player at station survives split — session transfers to station's child cell, still docked.
4. Web client shows subcell boundaries (dashed) after split. Tab map updates.
5. `cell merge d1_2_2` — cells merge back. Entities consolidate. Overlay updates.
6. New player login after split — routes to correct subcell, receives topology.
7. Player crosses subcell boundary — transfers correctly, client receives updated cell position.
