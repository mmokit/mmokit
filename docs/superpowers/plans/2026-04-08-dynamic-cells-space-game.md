# Dynamic Cells Space Game Integration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire dynamic cell partitioning (runtime split/merge) into the space game so cells can split and merge without entity duplication, docked player loss, or client display issues.

**Architecture:** The pkg/ layer already handles all split/merge mechanics. This plan adds: (1) a `fromSplit` flag on WorldBase so split-created worlds skip initial entity spawning, (2) docked session transfer during splits, (3) replaces static grid dimensions with topology messages, (4) a `debug` console command for server-controlled topology overlay, (5) web client topology-aware rendering.

**Tech Stack:** Go, protobuf, TypeScript/PixiJS, ECS (Ark)

---

### Task 1: Add FromSplit flag to WorldBase

**Files:**
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/partition.go`

- [ ] **Step 1: Add fromSplit field and accessor to WorldBase**

In `pkg/universe/world_base.go`, add a field to the `WorldBase` struct (after `coord *Coordinator` at line 118):

```go
	fromSplit bool // true if this world was created during a cell split
```

Add accessor and setter methods:

```go
// FromSplit returns true if this world was created during a cell split.
// Split-created worlds should skip initial entity spawning since entities
// arrive via transfer from the parent cell.
func (b *WorldBase) FromSplit() bool {
	return b.fromSplit
}
```

- [ ] **Step 2: Set fromSplit in SplitCell**

In `pkg/universe/partition.go`, in the `SplitCell` method, after `createNode` is called for each child (around line 243), set `fromSplit` on each child's WorldBase. The world is accessible via `newNode.World`. Since `WorldBase` is embedded, we need to access it. Add a method to set it:

In `world_base.go`, add:
```go
func (b *WorldBase) setFromSplit() {
	b.fromSplit = true
}
```

In `partition.go`, after line 244 (`childSetups = append(...)`), add inside the loop:

```go
		if wb, ok := newNode.World.(interface{ setFromSplit() }); ok {
			wb.setFromSplit()
		}
```

This uses an interface assertion so `partition.go` doesn't depend on the game layer.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/universe/`

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/world_base.go pkg/universe/partition.go
git commit -m "feat: add FromSplit flag to WorldBase, set during SplitCell"
```

---

### Task 2: Guard entity spawning in space game

**Files:**
- Modify: `internal/game/game.go:134-138`
- Modify: `internal/game/factory.go`

- [ ] **Step 1: Pass fromSplit from factory to NewGameWorld**

In `internal/game/factory.go`, the world factory creates a `GameWorld`. Read the file. The factory calls `NewGameWorld(eng, gameCfg, playerDB, base.SpatialGrid(), ...)`. Add `base.FromSplit()` as a parameter.

Change the `NewGameWorld` call to include fromSplit:

```go
gw := NewGameWorld(eng, gameCfg, playerDB, base.SpatialGrid(), mmokit.CellCoord{
    CellX: cell.X,
    CellY: cell.Y,
}, base.FromSplit())
```

- [ ] **Step 2: Update NewGameWorld signature and guard spawning**

In `internal/game/game.go`, update `NewGameWorld` to accept `fromSplit bool`:

```go
func NewGameWorld(eng *mmokit.Engine, cfg GameConfig, playerDB *PlayerRepo, grid *mmokit.HashGrid, cell mmokit.CellCoord, fromSplit bool) *GameWorld {
```

Then guard the spawn calls at lines 134-138:

```go
	// Spawn initial content for this cell (skip for split-created worlds —
	// entities arrive via transfer from the parent cell)
	if !fromSplit {
		gw.spawnAsteroids()
		if cell == cfg.StationCell {
			gw.SpawnStation()
		}
	}
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./internal/game/`

- [ ] **Step 4: Commit**

```bash
git add internal/game/game.go internal/game/factory.go
git commit -m "feat: skip entity spawning for split-created worlds"
```

---

### Task 3: Transfer docked sessions during split

**Files:**
- Modify: `pkg/universe/message.go`
- Modify: `pkg/universe/partition.go`
- Modify: `pkg/universe/node.go`
- Modify: `pkg/engine/player_manager.go`

- [ ] **Step 1: Add MsgSessionTransfer message type**

In `pkg/universe/message.go`, add a new message type constant:

```go
	MsgSessionTransfer MsgType = 12 // entity-less session transfer during split
```

Add the struct:

```go
// SessionTransfer carries an entity-less player session during cell splits.
// Used for docked/dead players who have no entity to serialize.
type SessionTransfer struct {
	ConnID   uint32
	Username string
	StateTag string // state name (e.g., "docked", "dead")
	Data     any    // game-specific session data
}
```

Add field to `NodeMessage`:

```go
	Sessions []SessionTransfer // entity-less session transfers during split
```

- [ ] **Step 2: Add RegisterSessionTransfer to PlayerManager**

In `pkg/engine/player_manager.go`, add a method that creates a session in a specific named state:

```go
// RegisterSessionTransfer creates a session in a specific state (by name).
// Used during cell splits for entity-less sessions (docked, dead players).
func (pm *PlayerManager) RegisterSessionTransfer(connID uint32, username string, stateName string, data any) {
	s := pm.byConnID[connID]
	if s == nil {
		s = pm.createSession(connID)
	}
	s.Username = username
	s.Data = data
	pm.byUsername[username] = s

	// Find the state by name and set directly (skip transition/callbacks)
	for state, name := range pm.states {
		if name == stateName {
			s.State = state
			if pm.onSessionActive != nil && s.Username != "" {
				pm.onSessionActive(s.Username)
			}
			return
		}
	}
	// Fallback: set to Active if state name not found
	s.State = StateActive
	if pm.onSessionActive != nil && s.Username != "" {
		pm.onSessionActive(s.Username)
	}
}
```

- [ ] **Step 3: Collect docked sessions in SplitCell**

In `pkg/universe/partition.go`, inside the `SplitCell` method, after the entity serialization closure (after line 216 `transfersCh <- transfers`), add session collection. Inside the same `PendingAdminCmds` closure (before `transfersCh <- transfers`), add:

```go
		// Collect entity-less sessions (docked, dead players without entities)
		var sessionTransfers []SessionTransfer
		for _, sess := range oldNode.Engine.Players.AllSessions() {
			if sess.ConnID == 0 {
				continue // no connection
			}
			if sess.State == engine.StatePending || sess.State == engine.StateTransferring {
				continue
			}
			// Skip sessions that have an alive entity (already handled by entity transfer)
			if sess.Entity != (ecs.Entity{}) && oldNode.Engine.ECS.Alive(sess.Entity) {
				continue
			}
			sessionTransfers = append(sessionTransfers, SessionTransfer{
				ConnID:   sess.ConnID,
				Username: sess.Username,
				StateTag: oldNode.Engine.Players.StateName(sess.State),
				Data:     sess.Data,
			})
		}
```

This requires an `AllSessions()` method on PlayerManager. Add it:

In `pkg/engine/player_manager.go`:
```go
// AllSessions returns all sessions (for inspection during splits).
func (pm *PlayerManager) AllSessions() []*PlayerSession {
	result := make([]*PlayerSession, 0, len(pm.sessions))
	for _, s := range pm.sessions {
		result = append(result, s)
	}
	return result
}
```

- [ ] **Step 4: Route sessions to station's child and send**

The session transfers need to be sent alongside the `transfersCh`. Expand the channel or add a second channel. Simplest: add sessions to a struct alongside transfers.

Change the channel type from `chan []entityTransfer` to a struct:

```go
	type splitResult struct {
		entities []entityTransfer
		sessions []SessionTransfer
	}
	transfersCh := make(chan splitResult, 1)
```

Update the send:
```go
	transfersCh <- splitResult{entities: transfers, sessions: sessionTransfers}
```

Update the receive:
```go
	var result splitResult
	select {
	case result = <-transfersCh:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout serializing entities on %s during split")
	}
	transfers := result.entities
```

After entity transfers are sent (after line 286), send session transfers. Route all sessions to the first child (they follow the station — since station is at cell center, it goes to child with xi=1,yi=1 for a cell centered station, but we can't know which child has the station until transfers are routed. Simplest: send to all children, let the one with the station accept them, or just pick the child that contains the station position (CellSize/2, CellSize/2)):

```go
	// Send entity-less sessions to the child containing the station
	if len(result.sessions) > 0 {
		// Station is at cell center — determine which child gets it
		stationChild := CellID{X: cell.X*2 + 1, Y: cell.Y*2 + 1, Depth: cell.Depth + 1}
		destID := MeshNodeID(stationChild)
		if dest, ok := c.Nodes[destID]; ok {
			dest.Inbox <- NodeMessage{
				Type:       MsgSessionTransfer,
				FromNodeID: nodeID,
				Sessions:   result.sessions,
			}
			// Update player routing
			for _, st := range result.sessions {
				if st.ConnID != 0 {
					c.playerNode[st.ConnID] = destID
				}
			}
		}
	}
```

- [ ] **Step 5: Handle MsgSessionTransfer in node.go**

In `pkg/universe/node.go`, add a case in `processMessage`:

```go
	case MsgSessionTransfer:
		for _, st := range msg.Sessions {
			n.Log.Log(CatMeshMsg, "[%s] msg MsgSessionTransfer conn=%d user=%s state=%s",
				n.ID, st.ConnID, st.Username, st.StateTag)
			n.Engine.Players.RegisterSessionTransfer(st.ConnID, st.Username, st.StateTag, st.Data)
		}
```

- [ ] **Step 6: Verify compilation**

Run: `go vet ./pkg/universe/ && go vet ./pkg/engine/`

- [ ] **Step 7: Run tests**

Run: `go test ./pkg/universe/ ./pkg/engine/ -count=1`

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/message.go pkg/universe/partition.go pkg/universe/node.go pkg/engine/player_manager.go
git commit -m "feat: transfer docked/dead sessions during cell splits"
```

---

### Task 4: Remove GridCellsX/Y, send CellTopologyMsg

**Files:**
- Modify: `proto/gamepb/game.proto`
- Modify: `internal/game/entity_ship.go`
- Modify: `internal/game/adapter.go`

- [ ] **Step 1: Remove proto fields**

In `proto/gamepb/game.proto`, find `PlayerSpawnedMsg`. Remove `grid_cells_x` (field 9) and `grid_cells_y` (field 10). Renumber any fields after them if needed (there are none — these are the last fields).

- [ ] **Step 2: Regenerate proto**

Run: `make proto`

- [ ] **Step 3: Remove GridCellsX/Y from spawn message construction**

In `internal/game/entity_ship.go`, find the `PlayerSpawnedMsg` construction (around line 176). Remove:
```go
			GridCellsX:   gw.Config.MeshCellsX,
			GridCellsY:   gw.Config.MeshCellsY,
```

- [ ] **Step 4: Send topology on spawn**

In `internal/game/adapter.go`, in the `Init()` method, after `SetOnPlayerTransferReceived`, the callback sends cell change and map data. Add topology sending there:

Find `a.SendCellTopology(frame.ConnID)` — it may already be there from the 4node-basic pattern. If not, add it after the map data send:

```go
		a.SendCellTopology(frame.ConnID)
```

Also, the space game needs to send topology on initial spawn. In `internal/game/game.go`, in the `StateActive` `OnEnter` callback (where `SpawnPlayer` is called), we need to send topology. But `game.go` has a `GameWorld`, not the adapter. Add a callback:

In the `GameWorld` struct (in `world.go` or wherever it's defined), add:
```go
	OnPostSpawn func(connID uint32) // called after player spawn, used to send topology
```

In `game.go`, after `SpawnPlayer(s)`, add:
```go
			if gw.OnPostSpawn != nil {
				gw.OnPostSpawn(s.ConnID)
			}
```

In `adapter.go`, in `Init()`, set the callback:
```go
	a.gw.OnPostSpawn = func(connID uint32) {
		a.SendCellTopology(connID)
	}
```

- [ ] **Step 5: Verify compilation**

Run: `go vet ./internal/game/ && go vet ./cmd/server/`

- [ ] **Step 6: Commit**

```bash
git add proto/gamepb/game.proto gen/ internal/game/entity_ship.go internal/game/adapter.go internal/game/game.go internal/game/world.go
git commit -m "feat: replace GridCellsX/Y with CellTopologyMsg on spawn"
```

---

### Task 5: Replace grid command with debug command

**Files:**
- Modify: `internal/game/commands.go`

- [ ] **Step 1: Replace grid command registration**

Find the `grid` command registration (search for `Name: "grid"`). Replace the entire command with:

```go
	console.Register(mmokit.Command{
		Name: "debug", Aliases: []string{"dbg"},
		Category: "debug", Usage: "debug", Description: "toggle debug overlay on all clients (cell grid, topology)",
		Fn: func(args []string) {
			if len(allNodes) == 0 {
				fmt.Println("  no nodes available")
				return
			}
			newVal := !allNodes[0].World.DebugShowCellGrid
			for _, node := range allNodes {
				nw := node.World
				nw.Engine.PendingAdminCmds <- func() {
					nw.DebugShowCellGrid = newVal
					broadcastDebugFlags(nw)
				}
			}
			// Also broadcast topology so clients get cell boundary data
			if newVal {
				coord.BroadcastCellTopology()
			}
			if newVal {
				fmt.Println("  debug overlay: ON")
			} else {
				fmt.Println("  debug overlay: OFF")
			}
		},
	})
```

Note: `coord.BroadcastCellTopology()` is a method on `*Coordinator`. The `coord` variable is available in `RegisterCommands` since we changed the signature to accept `*mmokit.Coordinator`.

- [ ] **Step 2: Verify compilation**

Run: `go vet ./internal/game/`

- [ ] **Step 3: Commit**

```bash
git add internal/game/commands.go
git commit -m "feat: replace grid command with debug command, broadcast topology"
```

---

### Task 6: Wire -dynamic-cells flag in main.go

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add flag and wire config**

Read `cmd/server/main.go`. The coordinator is created inline with `mmokit.NewCoordinator(mmokit.Config{...})`. Restructure to extract the config, add the flag, and conditionally enable dynamic partitioning.

At the top of `main()`, add:
```go
	dynamicCells := flag.Bool("dynamic-cells", false, "enable dynamic cell partitioning")
	flag.Parse()
```

Add `"flag"` to imports.

Then change the coordinator creation from an inline config to a variable:
```go
	coordCfg := mmokit.Config{
		CellsX:      gameCfg.MeshCellsX,
		CellsY:      gameCfg.MeshCellsY,
		TickRate:    platformCfg.TickRate,
		ConnManager: connMgr,
		Logger:      gameLog,
		LoginHandler:  ..., // keep existing LoginHandler closure
		LoginRejected: ..., // keep existing LoginRejected closure
	}
	if *dynamicCells {
		coordCfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
		coordCfg.DebugTopology = true
		log.Println("dynamic cell partitioning enabled")
	}
	coordinator = mmokit.NewCoordinator(coordCfg)
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./cmd/server/`

- [ ] **Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: add --dynamic-cells flag to space game server"
```

---

### Task 7: Web client — handle SE_CELL_TOPOLOGY

**Files:**
- Modify: `web-pixi/src/state.ts`
- Modify: `web-pixi/src/network.ts`

- [ ] **Step 1: Add cellTopology to state**

In `web-pixi/src/state.ts`, add the `CellInfo` interface and field:

```typescript
export interface CellInfo {
  cellX: number;
  cellY: number;
  depth: number;
  size: number;
  originX: number;
  originY: number;
  nodeId: string;
}
```

Add to the state type:
```typescript
  cellTopology: CellInfo[] | null;
```

Initialize in `createInitialState()`:
```typescript
    cellTopology: null,
```

Remove `gridCellsX` and `gridCellsY` from state type and initial state.

- [ ] **Step 2: Handle SE_CELL_TOPOLOGY in network.ts**

In `web-pixi/src/network.ts`, add the import for `CellTopologyMsgSchema` from `@gen/engine_pb.js`. Then add a case in the event switch:

```typescript
      case ServerEventCode.SE_CELL_TOPOLOGY: {
        const topo = fromBinary(CellTopologyMsgSchema, evt.data);
        state.cellTopology = topo.cells.map(c => ({
          cellX: c.cellX,
          cellY: c.cellY,
          depth: c.depth,
          size: c.size,
          originX: c.originX,
          originY: c.originY,
          nodeId: c.nodeId,
        }));
        // Derive grid bounds from topology
        if (state.cellTopology.length > 0) {
          let maxX = 0, maxY = 0;
          for (const c of state.cellTopology) {
            const ex = (c.originX + c.size) / CELL_SIZE;
            const ey = (c.originY + c.size) / CELL_SIZE;
            if (ex > maxX) maxX = ex;
            if (ey > maxY) maxY = ey;
          }
          state.gridCellsX = Math.ceil(maxX);
          state.gridCellsY = Math.ceil(maxY);
        }
        callbacks.onTopologyChanged?.();
        break;
      }
```

Wait — we're removing `gridCellsX/Y` from state. But `grid.ts` and `cell-map.ts` still use them. We need to keep them as derived values or update the grid/map to use topology directly. For backward compatibility during this transition, keep `gridCellsX/Y` in state but derive them from topology when topology arrives. Remove them from the spawn handler:

Remove lines 251-252:
```typescript
        if (spawned.gridCellsX > 0) state.gridCellsX = spawned.gridCellsX;
        if (spawned.gridCellsY > 0) state.gridCellsY = spawned.gridCellsY;
```

Add `onTopologyChanged` to the callbacks interface used by `connect()`.

- [ ] **Step 3: Commit**

```bash
cd web-pixi && git add src/state.ts src/network.ts
git commit -m "feat: handle SE_CELL_TOPOLOGY event, add cellTopology to state"
```

---

### Task 8: Web client — topology-aware grid overlay

**Files:**
- Modify: `web-pixi/src/world/grid.ts`
- Modify: `web-pixi/src/main.ts`

- [ ] **Step 1: Add setTopology method to CellGrid**

In `web-pixi/src/world/grid.ts`, read the file to understand the current `CellGrid` class. Add a method to accept topology data:

```typescript
  private topology: CellInfo[] | null = null;

  setTopology(cells: CellInfo[]): void {
    this.topology = cells;
    // Derive grid size from topology
    let maxX = 0, maxY = 0;
    for (const c of cells) {
      const ex = Math.ceil((c.originX + c.size) / CELL_SIZE);
      const ey = Math.ceil((c.originY + c.size) / CELL_SIZE);
      if (ex > maxX) maxX = ex;
      if (ey > maxY) maxY = ey;
    }
    this.gridCellsX = maxX;
    this.gridCellsY = maxY;
  }
```

Import `CellInfo` from state.

- [ ] **Step 2: Update grid rendering for topology**

In the `update()` method, after computing visible cell range, add a topology branch. If `this.topology` is set, draw from actual cell bounds instead of uniform grid:

```typescript
    // Draw topology cell boundaries (subcells may have different sizes)
    if (this.topology) {
      for (const cell of this.topology) {
        // Convert world coords to screen coords
        const localX = cell.originX - this.originSX * CELL_SIZE;
        const localY = cell.originY - this.originSY * CELL_SIZE;
        const screenX = localX - cameraX + screenW / 2;
        const screenY = localY - cameraY + screenH / 2;
        const screenSize = cell.size;

        // Skip if entirely off-screen
        if (screenX + screenSize < 0 || screenX > screenW) continue;
        if (screenY + screenSize < 0 || screenY > screenH) continue;

        // Draw border
        this.g.lineStyle(cell.depth > 0 ? 1 : 2, 0x444444, cell.depth > 0 ? 0.3 : 0.5);
        if (cell.depth > 0) {
          // Dashed line for subcells — draw as dotted segments
          this.drawDashedRect(screenX, screenY, screenSize, screenSize, 10, 5);
        } else {
          this.g.drawRect(screenX, screenY, screenSize, screenSize);
        }

        // Cell name in corners
        // ... (use existing label pattern from current grid.ts)
      }
      return; // skip uniform grid rendering
    }
```

Add a `drawDashedRect` helper method to the class.

The exact implementation depends on how the current grid renders (PixiJS Graphics). Read the file and follow existing patterns.

- [ ] **Step 3: Wire topology callback in main.ts**

In `web-pixi/src/main.ts`, in the callbacks passed to `connect()`, add:

```typescript
    onTopologyChanged: () => {
      if (state.cellTopology) {
        cellGrid.setTopology(state.cellTopology);
      }
    },
```

- [ ] **Step 4: Commit**

```bash
cd web-pixi && git add src/world/grid.ts src/main.ts
git commit -m "feat: topology-aware grid overlay with subcell support"
```

---

### Task 9: Web client — topology-aware tab map

**Files:**
- Modify: `web-pixi/src/ui/cell-map.ts`

- [ ] **Step 1: Update drawGrid for topology**

In `web-pixi/src/ui/cell-map.ts`, the `drawGrid()` method renders uniform grid lines. Add a topology branch at the start:

```typescript
  if (state.cellTopology) {
    this.drawTopologyGrid(playerAbsX, playerAbsY, pixelsPerUnit, pw, ph);
    return;
  }
  // ... existing uniform grid code
```

Add `drawTopologyGrid` method that iterates `state.cellTopology` and draws each cell's rectangle scaled by `pixelsPerUnit`, with dashed borders for depth > 0 and coordinate labels.

- [ ] **Step 2: Commit**

```bash
cd web-pixi && git add src/ui/cell-map.ts
git commit -m "feat: topology-aware tab map rendering"
```

---

### Task 10: Manual verification

- [ ] **Step 1: Start server with dynamic cells**

Run: `make dev` or `go run ./cmd/server --dynamic-cells`

- [ ] **Step 2: Login and verify baseline**

Login, verify player spawns correctly, no duplicate entities. Grid overlay hidden by default.

- [ ] **Step 3: Enable debug overlay**

Console: `debug`
Verify: all clients see cell boundary overlay.

- [ ] **Step 4: Test cell split**

Console: `cell split 1 1`
Verify:
- No duplicate asteroids or station
- Player (if in that cell) transfers correctly
- Web client shows subcell boundaries (dashed)
- Tab map updates with new topology

- [ ] **Step 5: Test docked player during split**

Dock at station, then `cell split 1 1`.
Verify: player stays docked, session transfers to station's child cell.

- [ ] **Step 6: Test cell merge**

Console: `cell merge d1_2_2`
Verify: cells merge, entities consolidate, overlay updates.

- [ ] **Step 7: Test new player login after split**

Login as new player after a split.
Verify: routes to correct subcell, receives topology, overlay renders correctly.
