# Sector Grid Lines Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add toggleable sector boundary lines to the web client, controlled by a server console command.

**Architecture:** New `SE_DEBUG_FLAGS` server event broadcasts a `DebugFlagsMsg` to all clients. Server stores a `DebugShowSectorGrid` bool on `GameWorld`, toggled via `grid` console command. Client renders sector boundary lines at every 8192 world units when enabled.

**Tech Stack:** Go (server), Protocol Buffers, TypeScript/PixiJS (web client)

**Spec:** `docs/superpowers/specs/2026-03-24-sector-grid-lines-design.md`

---

### Task 1: Add proto message and event code

**Files:**
- Modify: `proto/game.proto:72-87` (ServerEventCode enum)
- Modify: `proto/game.proto:428` (add new message at end)

- [ ] **Step 1: Add SE_DEBUG_FLAGS to ServerEventCode enum**

In `proto/game.proto`, add after `SE_MAP_DATA = 13`:

```protobuf
SE_DEBUG_FLAGS = 14;
```

- [ ] **Step 2: Add DebugFlagsMsg at end of file**

Append to `proto/game.proto`:

```protobuf
// Debug visualization flags (toggled by server console)
message DebugFlagsMsg {
  bool show_sector_grid = 1;
}
```

- [ ] **Step 3: Regenerate proto**

Run: `make proto`
Expected: Clean exit, updated files in `gen/go/`, `gen/csharp/`, `gen/es/`

- [ ] **Step 4: Commit**

```bash
git add proto/game.proto gen/
git commit -m "proto: add SE_DEBUG_FLAGS event and DebugFlagsMsg"
```

---

### Task 2: Add server-side debug flag and console command

**Files:**
- Modify: `internal/game/world.go:96-138` (GameWorld struct)
- Modify: `internal/game/commands.go` (add grid command)

- [ ] **Step 1: Add DebugShowSectorGrid field to GameWorld**

In `internal/game/world.go`, add after the `Bridge NodeBridge` field (line 136):

```go
// Debug visualization flags (broadcast to clients on toggle)
DebugShowSectorGrid bool
```

- [ ] **Step 2: Add helper to broadcast debug flags**

In `internal/game/commands.go`, add a helper function (near the other helpers at the bottom of the file):

```go
// broadcastDebugFlags sends the current debug flag state to all logged-in players.
func broadcastDebugFlags(gw *GameWorld) {
	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_DEBUG_FLAGS), &gamepb.DebugFlagsMsg{
		ShowSectorGrid: gw.DebugShowSectorGrid,
	})
	if data == nil {
		return
	}
	for connID := range gw.Players.Usernames {
		gw.ConnMgr.SendReliable(connID, data)
	}
}
```

- [ ] **Step 3: Register the grid console command**

In `internal/game/commands.go`, inside `RegisterCommands()`, add:

```go
console.Register(engine.Command{
	Name: "grid", Aliases: []string{"sg"},
	Category: "debug", Usage: "grid", Description: "toggle sector grid lines on all clients",
	Fn: func(args []string) {
		result := console.ExecOnGameLoop(func() string {
			gw.DebugShowSectorGrid = !gw.DebugShowSectorGrid
			broadcastDebugFlags(gw)
			if gw.DebugShowSectorGrid {
				return "  sector grid: ON"
			}
			return "  sector grid: OFF"
		})
		fmt.Println(result)
	},
})
```

- [ ] **Step 4: Build and verify**

Run: `make build`
Expected: Clean compilation

- [ ] **Step 5: Commit**

```bash
git add internal/game/world.go internal/game/commands.go
git commit -m "feat(server): add grid console command to toggle sector grid debug overlay"
```

---

### Task 3: Send debug flags on player spawn

**Files:**
- Modify: `internal/game/entity_ship.go:159-184` (after SE_PLAYER_SPAWNED send)

- [ ] **Step 1: Send SE_DEBUG_FLAGS after spawn message**

In `internal/game/entity_ship.go`, after the map data send block (after line 183 `gw.Log.Log(CatMap, ...)`), add:

```go
// Send current debug flags so late-joiners pick up the state
debugData := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_DEBUG_FLAGS), &gamepb.DebugFlagsMsg{
	ShowSectorGrid: gw.DebugShowSectorGrid,
})
if debugData != nil {
	gw.ConnMgr.SendReliable(connID, debugData)
}
```

- [ ] **Step 2: Build and verify**

Run: `make build`
Expected: Clean compilation

- [ ] **Step 3: Commit**

```bash
git add internal/game/entity_ship.go
git commit -m "feat(server): send debug flags to players on spawn"
```

---

### Task 4: Add client state and network handler

**Files:**
- Modify: `web-pixi/src/state.ts:29-151` (GameState interface)
- Modify: `web-pixi/src/state.ts:153-248` (createInitialState)
- Modify: `web-pixi/src/network.ts:1-45` (imports)
- Modify: `web-pixi/src/network.ts:55-59` (NetworkCallbacks interface)
- Modify: `web-pixi/src/network.ts` (event switch, after SE_MAP_DATA case)

- [ ] **Step 1: Add showSectorGrid to GameState interface**

In `web-pixi/src/state.ts`, add to the `GameState` interface after the `sectorMapOpen` / `mapStations` fields:

```typescript
// Debug overlays (toggled by server)
showSectorGrid: boolean;
```

- [ ] **Step 2: Add default value in createInitialState**

In `web-pixi/src/state.ts`, in `createInitialState()`, after `mapStations: [],` add:

```typescript
showSectorGrid: false,
```

- [ ] **Step 3: Add imports to network.ts**

In `web-pixi/src/network.ts`, add `DebugFlagsMsgSchema` to the Schema imports block (line 6-23) and `DebugFlagsMsg` to the type imports block (line 24-45):

```typescript
// Add to Schema imports:
DebugFlagsMsgSchema,

// Add to type imports:
DebugFlagsMsg,
```

- [ ] **Step 4: Add onOriginChanged to NetworkCallbacks interface**

In `web-pixi/src/network.ts`, add to the `NetworkCallbacks` interface (line 55-59):

```typescript
onOriginChanged(sx: number, sy: number): void;
```

- [ ] **Step 5: Add SE_DEBUG_FLAGS handler**

In `web-pixi/src/network.ts`, after the `SE_MAP_DATA` case (around line 450), add:

```typescript
case ServerEventCode.SE_DEBUG_FLAGS: {
  const flags = fromBinary(DebugFlagsMsgSchema, evt.data) as DebugFlagsMsg;
  state.showSectorGrid = flags.showSectorGrid;
  break;
}
```

- [ ] **Step 6: Commit**

```bash
git add web-pixi/src/state.ts web-pixi/src/network.ts
git commit -m "feat(client): handle SE_DEBUG_FLAGS event for sector grid toggle"
```

---

### Task 5: Replace grid rendering with sector boundary lines

**Files:**
- Rewrite: `web-pixi/src/world/grid.ts`
- Modify: `web-pixi/src/main.ts:13` (imports)
- Modify: `web-pixi/src/main.ts:88-90` (grid creation)
- Modify: `web-pixi/src/main.ts:133,145` (resize/zoom redraws)
- Modify: `web-pixi/src/main.ts:232` (grid update in render loop)

- [ ] **Step 1: Rewrite grid.ts with sector boundary rendering**

Replace `web-pixi/src/world/grid.ts` entirely:

```typescript
import { Container, Graphics, Text, TextStyle } from "pixi.js";
import { px, zoom } from "../view";
import { SECTOR_SIZE } from "../constants";

const LINE_COLOR = 0x00cccc;
const LINE_ALPHA = 0.3;
const LABEL_STYLE = new TextStyle({
  fontFamily: "monospace",
  fontSize: 12,
  fill: 0x00cccc,
});

/** Container holding sector boundary lines and coordinate labels. */
export class SectorGrid {
  readonly container: Container;
  private gfx: Graphics;
  private labels: Text[] = [];
  private originSX = 0;
  private originSY = 0;

  constructor() {
    this.container = new Container();
    this.gfx = new Graphics();
    this.container.addChild(this.gfx);
  }

  /** Update the player's sector origin (from spawn or sector change). */
  setOrigin(sx: number, sy: number) {
    this.originSX = sx;
    this.originSY = sy;
  }

  /** Redraw sector lines for the current viewport. */
  update(cameraX: number, cameraY: number, screenW: number, screenH: number) {
    this.gfx.clear();

    // Recycle labels
    for (const label of this.labels) {
      label.visible = false;
    }
    let labelIdx = 0;

    const z = zoom();
    const viewW = screenW / z;
    const viewH = screenH / z;
    const halfW = viewW / 2;
    const halfH = viewH / 2;

    const left = cameraX - halfW;
    const right = cameraX + halfW;
    const top = cameraY - halfH;
    const bottom = cameraY + halfH;

    // Sector boundaries in local coords: n * SECTOR_SIZE relative to origin
    // The origin sector's local (0,0) maps to world sector (originSX, originSY)
    // So sector boundary at world sector N is at local x = (N - originSX) * SECTOR_SIZE

    // Find range of sector boundaries visible
    const firstSX = Math.floor(left / SECTOR_SIZE);
    const lastSX = Math.ceil(right / SECTOR_SIZE);
    const firstSY = Math.floor(top / SECTOR_SIZE);
    const lastSY = Math.ceil(bottom / SECTOR_SIZE);

    // Draw vertical lines
    for (let sx = firstSX; sx <= lastSX; sx++) {
      const x = sx * SECTOR_SIZE;
      this.gfx.moveTo(x, top).lineTo(x, bottom);

      // Labels along vertical lines at nearest visible horizontal boundary
      for (let sy = firstSY; sy <= lastSY; sy++) {
        const y = sy * SECTOR_SIZE;
        const worldSX = sx + this.originSX;
        const worldSY = sy + this.originSY;
        const label = this.getLabel(labelIdx++);
        label.text = `${worldSX},${worldSY}`;
        label.position.set(x + px(4), y + px(4));
        label.scale.set(1 / z);
        label.visible = true;
      }
    }

    // Draw horizontal lines
    for (let sy = firstSY; sy <= lastSY; sy++) {
      const y = sy * SECTOR_SIZE;
      this.gfx.moveTo(left, y).lineTo(right, y);
    }

    this.gfx.stroke({ color: LINE_COLOR, alpha: LINE_ALPHA, width: px(2) });
  }

  private getLabel(idx: number): Text {
    if (idx < this.labels.length) return this.labels[idx];
    const label = new Text({ text: "", style: LABEL_STYLE });
    label.alpha = 0.5;
    this.labels.push(label);
    this.container.addChild(label);
    return label;
  }
}
```

- [ ] **Step 2: Update main.ts imports**

In `web-pixi/src/main.ts`, replace the grid import (line 13):

```typescript
// Old:
import { createGrid, drawGrid, updateGridPosition } from "./world/grid";
// New:
import { SectorGrid } from "./world/grid";
```

- [ ] **Step 3: Update grid creation in main.ts**

Replace the grid creation block (lines 88-90):

```typescript
// Old:
const grid = createGrid(window.innerWidth, window.innerHeight);
gridContainer.addChild(grid);

// New:
const sectorGrid = new SectorGrid();
gridContainer.addChild(sectorGrid.container);
```

- [ ] **Step 4: Update resize handler**

In the resize handler (line 133), remove:

```typescript
drawGrid(grid, w, h);
```

- [ ] **Step 5: Update zoom handler**

In the wheel handler (line 145), remove:

```typescript
drawGrid(grid, window.innerWidth, window.innerHeight);
```

- [ ] **Step 6: Update render loop**

Replace the grid update line (line 232):

```typescript
// Old:
updateGridPosition(grid, camera.x, camera.y, app.screen.width, app.screen.height);

// New:
gridContainer.visible = state.showSectorGrid;
if (state.showSectorGrid) {
  sectorGrid.update(camera.x, camera.y, app.screen.width, app.screen.height);
}
```

- [ ] **Step 7: Set sector origin on spawn and sector change**

In `web-pixi/src/network.ts`, in the `SE_PLAYER_SPAWNED` handler (after line 114 where `originSectorY` is set), add:

```typescript
callbacks.onOriginChanged(spawned.originSectorX, spawned.originSectorY);
```

In the `SE_SECTOR_CHANGE` handler (around line 435), add after updating origin:

```typescript
callbacks.onOriginChanged(state.originSectorX, state.originSectorY);
```

In `main.ts`, add `onOriginChanged` to the callbacks object passed to `connect()` (the `NetworkCallbacks` interface was already updated in Task 4 Step 4):

```typescript
onOriginChanged: (sx: number, sy: number) => {
  sectorGrid.setOrigin(sx, sy);
},
```

- [ ] **Step 8: Build and verify**

Run: `cd web-pixi && bun run build`
Expected: Clean compilation, no TypeScript errors

- [ ] **Step 9: Commit**

```bash
git add web-pixi/src/world/grid.ts web-pixi/src/main.ts web-pixi/src/network.ts
git commit -m "feat(client): replace small grid with sector boundary lines overlay"
```

---

### Task 6: Manual integration test

- [ ] **Step 1: Start server and client**

Run: `make dev`
Open `http://localhost:8080`, log in.

- [ ] **Step 2: Verify grid is hidden by default**

Expected: No grid lines visible on screen.

- [ ] **Step 3: Toggle grid via server console**

In the server console, type: `grid`
Expected: Console prints `sector grid: ON`. Client shows cyan sector boundary lines at 8192-unit intervals with coordinate labels.

- [ ] **Step 4: Toggle grid off**

Type: `grid` again.
Expected: Console prints `sector grid: OFF`. Lines disappear.

- [ ] **Step 5: Test late-join**

With grid ON, open a second browser tab and log in as a different user.
Expected: New player sees sector grid immediately.
