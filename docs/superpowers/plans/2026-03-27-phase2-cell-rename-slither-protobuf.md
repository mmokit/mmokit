# Phase 2: Cell Rename + Slither Protobuf Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename "Sector" → "Cell" across the codebase (#13) and migrate slither's server→client protocol from raw binary to protobuf ServerEvent envelopes.

**Architecture:** Two sequential phases. Phase A is a mechanical codebase-wide rename (no logic changes). Phase B adds `MakeEvent` to `pkg/mmokit`, defines slither proto messages, replaces binary frame builders with protobuf, and updates the TypeScript client. Each phase ends with full verification.

**Tech Stack:** Go, Protocol Buffers (buf), TypeScript (@bufbuild/protobuf), WebSocket

**Spec:** `docs/superpowers/specs/2026-03-27-phase2-cell-rename-slither-protobuf-design.md`

---

## Phase A: Sector → Cell Rename

> **Note:** SX/SY struct fields are kept as-is — they're compact coordinate index names that don't cause confusion once the parent type is renamed.

### Task 1: Proto files — rename sector terminology

**Files:**
- Modify: `proto/enginepb/engine.proto:52-94`
- Modify: `proto/gamepb/game.proto` (lines with sector references)

- [ ] **Step 1: Rename in engine.proto**

In `proto/enginepb/engine.proto`:

```protobuf
// Line 60: rename enum value
SE_CELL_CHANGE = 12;    // was SE_SECTOR_CHANGE

// Lines 91-94: rename message and fields
message CellChangeMsg {         // was SectorChangeMsg
    int32 cell_x = 1;           // was sector_x
    int32 cell_y = 2;           // was sector_y
}
```

- [ ] **Step 2: Rename in game.proto**

In `proto/gamepb/game.proto`:

```protobuf
// PlayerSpawnedMsg fields (~line 248-249):
int32 origin_cell_x = 7;    // was origin_sector_x
int32 origin_cell_y = 8;    // was origin_sector_y

// MapStationInfo fields (~line 324-325):
int32 cell_x = 1;           // was sector_x
int32 cell_y = 2;           // was sector_y

// DebugFlagsMsg field (~line 376):
bool show_cell_grid = 1;    // was show_sector_grid

// ReplicaSnapshotPB fields (~line 414-415):
int32 cell_sx = 5;          // was sector_sx
int32 cell_sy = 6;          // was sector_sy
```

- [ ] **Step 3: Regenerate all generated code**

Run: `make proto`
Expected: Clean generation, no errors.

- [ ] **Step 4: Commit**

```bash
git add proto/ gen/
git commit -m "rename: sector → cell in proto files + regenerate"
```

---

### Task 2: Rename in `pkg/` — core types and universe layer

**Files:**
- Modify: `pkg/coords/coords.go`
- Modify: `pkg/coords/coords_test.go`
- Modify: `pkg/component/core.go`
- Modify: `pkg/mmokit/mmokit.go`
- Modify: All files in `pkg/universe/` containing "sector"/"Sector"

- [ ] **Step 1: Rename in `pkg/coords/coords.go`**

All renames in this file:
- `SectorSize` → `CellSize` (global var + all references)
- `SetSectorSize` → `SetCellSize` (function)
- `SectorCoord` → `CellCoord` (type)
- Update all comments: "sector" → "cell"

```go
// CellSize is the world-unit width/height of one cell (default 8192).
var CellSize float32 = 8192

func SetCellSize(size float32) {
    CellSize = size
}

// CellCoord holds a position within a cell.
type CellCoord struct {
    // ...fields stay SX, SY, X, Y...
}
```

- [ ] **Step 2: Rename in `pkg/coords/coords_test.go`**

Update test names: `TestSectorSize` → `TestCellSize`, all `SectorCoord` → `CellCoord`, `SectorSize` → `CellSize`.

- [ ] **Step 3: Rename in `pkg/component/core.go`**

```go
// Line 71: rename component type
type CellCoord = coords.CellCoord    // was SectorCoord = coords.SectorCoord
```

Update comments: "sector" → "cell".

- [ ] **Step 4: Rename in `pkg/mmokit/mmokit.go`**

Update all facade aliases:
```go
type CellCoord = component.CellCoord          // was SectorCoord
type CoordsCell = coords.CellCoord            // was Coordssector (also fix typo)

func CellSize() float32 { return coords.CellSize }     // was SectorSize
func SetCellSize(s float32) { coords.SetCellSize(s) }  // was SetSectorSize
```

Also update `SectorID` → `CellID` alias.

- [ ] **Step 5: Rename in `pkg/universe/` — all files**

Key renames by file:

**`world_base.go`:**
- Field `sector` → `cell` (type `coords.CellCoord`)
- Field `sectorMap` → `cellMap`
- Method `Sector()` → `Cell()`
- Method `SectorCoordMap()` → `CellCoordMap()`
- All internal references updated

**`boundary_system.go`:**
- System name string `"SectorBoundary"` → `"CellBoundary"`
- All `SectorCoord` → `CellCoord` type references
- `SectorOwner` method calls → `CellOwner`
- `SectorSize` → `CellSize`

**`bridge.go`:**
- Interface method `SectorOwner(sector coords.CellCoord)` → `CellOwner(cell coords.CellCoord)`

**`node_bridge_impl.go`:**
- Method `SectorOwner()` → `CellOwner()`
- Field references `b.node.Sector` → `b.node.Cell`

**`coordinator.go`:**
- Field `SectorOwner` → `CellOwner`
- Method `NodeForSector` → `NodeForCell`
- Local vars `sectors` → `cells`, `sector` → `cell`
- `SectorID()` → `CellID()`

**`topology.go`:**
- Function `SectorID()` → `CellID()`

**`node.go`:**
- Field `Sector` → `Cell`
- Format string: "sector (%d,%d)" → "cell (%d,%d)"

**`transfer.go`:**
- Struct fields `SectorX, SectorY` → `CellX, CellY`
- Comments updated

**`replication.go`:**
- Struct fields `SectorX, SectorY` → `CellX, CellY`
- Comments updated

**`universe_test.go`:**
- All test names and references updated

- [ ] **Step 6: Verify pkg/ compiles**

Run: `go vet ./pkg/...`
Expected: No errors. If there are errors, fix missed renames.

- [ ] **Step 7: Commit**

```bash
git add pkg/
git commit -m "rename: sector → cell across pkg/ layer"
```

---

### Task 3: Rename in `internal/` — game code, systems, universe adapter

**Files:**
- Modify: All files in `internal/game/` containing "sector"/"Sector"
- Modify: All files in `internal/system/` containing "sector"/"Sector"
- Modify: All files in `internal/universe/` containing "sector"/"Sector"

- [ ] **Step 1: Rename in `internal/game/`**

Key renames by file:

**`world.go`:**
- Field references `Sector` → `Cell`
- `DebugShowSectorGrid` → `DebugShowCellGrid`
- Component mapper `SectorCoord` → `CellCoord`

**`game.go`:**
- Parameter `sector` → `cell`
- `gw.Sector = sector` → `gw.Cell = cell`
- Conditions using `sector.SX/SY` → `cell.SX/SY`

**`commands.go`:**
- `fmtSectorPos()` → `fmtCellPos()`
- `fmtSectorPosRaw()` → `fmtCellPosRaw()`
- All "sector" strings in command output → "cell"
- `DebugShowSectorGrid` → `DebugShowCellGrid`
- `ShowSectorGrid` → `ShowCellGrid`
- `explicitSector` → `explicitCell`

**`entity_ship.go`:**
- Local vars `sectorX, sectorY` → `cellX, cellY`
- `gw.Sector.SX` → `gw.Cell.SX`
- `coords.SectorSize` → `coords.CellSize`
- Proto fields: `OriginSectorX` → `OriginCellX`, `OriginSectorY` → `OriginCellY`
- `ShowSectorGrid` → `ShowCellGrid`

**`entity_station.go`:**
- `coords.SectorSize` → `coords.CellSize`
- `SectorCoord` → `CellCoord`

**`entity_asteroid.go`:**
- `gw.Sector` → `gw.Cell`
- `coords.SectorSize` → `coords.CellSize`
- Format string "sector" → "cell"
- `SectorCoord` → `CellCoord`

**`entity_npc.go`, `entity_lootcrate.go`:**
- `SectorCoord` → `CellCoord`
- `gw.Sector.SX/SY` → `gw.Cell.SX/SY`

**`belts.go`:**
- Parameter `sector` → `cell`
- All `sector.SX/SY` → `cell.SX/SY`
- `coords.SectorSize` → `coords.CellSize`
- Comments updated

**`playerdata.go`:**
- JSON struct fields `SectorX` → `CellX`, `SectorY` → `CellY`

**`lifecycle.go`:**
- `gw.Sector.SX/SY` → `gw.Cell.SX/SY`
- Comments updated

**`transfer.go`:**
- Comment referencing `SE_SECTOR_CHANGE` → `SE_CELL_CHANGE`

**`config.go`:**
- Check for any `ShowSectorGrid` → `ShowCellGrid`

- [ ] **Step 2: Rename in `internal/system/`**

**`shipcontrol.go`:**
- `sectorDX, sectorDY` → `cellDX, cellDY`
- `SectorCoord` → `CellCoord`
- `coords.SectorSize` → `coords.CellSize`

**`replication_adapters.go`:**
- `SectorCoord` → `CellCoord`
- `sec` vars → `cel`
- `entitySX/SY, viewerSX/SY` → `entitySX/SY, viewerSX/SY` (keep — these are generic coord field accesses)
- `mmokit.SectorSize()` → `mmokit.CellSize()`

**`input_handlers.go`:**
- `SectorCoord` → `CellCoord`

- [ ] **Step 3: Rename in `internal/universe/`**

**`factory.go`:**
- `base.Sector()` → `base.Cell()`
- `mmokit.SectorCoord` → `mmokit.CellCoord`
- `sector.SX/SY` → `cell.SX/SY`
- `SE_SECTOR_CHANGE` → `SE_CELL_CHANGE`
- `SectorChangeMsg` → `CellChangeMsg`
- `SectorX, SectorY` → `CellX, CellY`
- `frame.SectorX` → `frame.CellX`

**Test files** (`coordinator_test.go`, `node_test.go`, `replica_test.go`, `testutil_test.go`, `topology_test.go`):
- All `SectorCoord` → `CellCoord`
- All `SectorX/Y` → `CellX/Y`
- Test names: "sector" → "cell"

- [ ] **Step 4: Verify internal/ compiles**

Run: `go vet ./internal/...`
Expected: No errors.

- [ ] **Step 5: Verify full build**

Run: `go vet ./...`
Expected: No errors.

- [ ] **Step 6: Run tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/
git commit -m "rename: sector → cell across internal/ layer"
```

---

### Task 4: Rename in `examples/slither/` Go files

**Files:**
- Modify: `examples/slither/main.go`
- Modify: `examples/slither/world.go`
- Modify: `examples/slither/entity_snake.go`
- Modify: `examples/slither/entity_food.go`
- Modify: `examples/slither/system_bot.go`
- Modify: `examples/slither/system_death.go`
- Modify: `examples/slither/component.go`

- [ ] **Step 1: Rename across all slither Go files**

Common renames across all files:
- `mmokit.SectorSize()` → `mmokit.CellSize()`
- `mmokit.SectorCoord` → `mmokit.CellCoord`
- `gw.Sector()` → `gw.Cell()`
- `coords.SectorSize` → `coords.CellSize`
- Local var `sectorSize` → `cellSize`
- Comments: "sector" → "cell"

**`main.go`:**
- Comment "sectors" → "cells" (line 19)
- `sectorSize` → `cellSize` (line 74+)

**`world.go`:**
- `sectorSize := coords.SectorSize` → `cellSize := coords.CellSize` (line 202)
- `SectorX, SectorY` → `CellX, CellY` in transfer frame (line 266-267)
- `gw.Sector()` → `gw.Cell()` (lines 291-292)

**`entity_snake.go`:**
- `SectorCoord` → `CellCoord` (lines 13, 35, 96)
- `gw.Sector().SX/SY` → `gw.Cell().SX/SY` (line 35)
- `sectorSize` → `cellSize` (line 130+)

**`entity_food.go`:**
- `SectorCoord` → `CellCoord` (lines 12, 29, 44)
- `gw.Sector().SX/SY` → `gw.Cell().SX/SY` (line 29)
- `sectorSize` → `cellSize` (line 54+)

**`system_bot.go`:**
- `sectorSize` → `cellSize` (line 100+)

**`system_death.go`:**
- `sectorSize` → `cellSize` (line 49+)
- Comment "sector bounds" → "cell bounds"

**`component.go`:**
- Comment "sector-local" → "cell-local" (line 121)

- [ ] **Step 2: Verify slither compiles**

Run: `cd examples/slither && go vet ./...`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add examples/slither/*.go
git commit -m "rename: sector → cell in slither Go files"
```

---

### Task 5: Rename in web-pixi/ TypeScript

**Files:**
- Modify: `web-pixi/src/constants.ts`
- Modify: `web-pixi/src/state.ts`
- Modify: `web-pixi/src/main.ts`
- Modify: `web-pixi/src/input.ts`
- Modify: `web-pixi/src/network.ts`
- Modify: `web-pixi/src/world/grid.ts`
- Modify: `web-pixi/src/world/planets.ts`
- Modify: `web-pixi/src/world/starfield.ts`
- Modify: `web-pixi/src/world/nebula.ts`
- Modify: `web-pixi/src/ui/sector-map.ts` → rename to `cell-map.ts`
- Modify: `web-pixi/src/ui/hud.ts`

- [ ] **Step 1: Rename constants and state**

**`constants.ts`:**
```typescript
export const CELL_SIZE = 8192;  // was SECTOR_SIZE
```

**`state.ts`:** All occurrences:
- `sectorX/Y` → `cellX/Y`
- `originSectorX/Y` → `originCellX/Y`
- `pendingSectorRebase` → `pendingCellRebase`
- `preTransferSectorX/Y` → `preTransferCellX/Y`
- `sectorMapOpen` → `cellMapOpen`
- `showSectorGrid` → `showCellGrid`

- [ ] **Step 2: Rename network and input**

**`network.ts`:**
- Import `CellChangeMsgSchema` / `CellChangeMsg` (was `SectorChangeMsg*`)
- `SECTOR_SIZE` → `CELL_SIZE`
- `SE_SECTOR_CHANGE` → `SE_CELL_CHANGE`
- All `sectorX/Y` → `cellX/Y`
- `pendingSectorRebase` → `pendingCellRebase`
- `preTransferSectorX/Y` → `preTransferCellX/Y`
- `originSectorX/Y` → `originCellX/Y`

**`input.ts`:**
- `sectorMapOpen` → `cellMapOpen`

- [ ] **Step 3: Rename world rendering files**

**`main.ts`:**
- `SECTOR_SIZE` → `CELL_SIZE`
- `SectorGrid` → `CellGrid`
- `sectorGrid` → `cellGrid`
- `SectorMap` → `CellMap`
- `sectorMap` → `cellMap`
- `sectorMapOpen` → `cellMapOpen`
- `sectorOffX/Y` → `cellOffX/Y`
- `showSectorGrid` → `showCellGrid`

**`world/grid.ts`:**
- `SECTOR_SIZE` → `CELL_SIZE`
- `class SectorGrid` → `class CellGrid`
- All "sector" comments → "cell"

**`world/planets.ts`, `world/starfield.ts`, `world/nebula.ts`:**
- Parameters `sectorOffX/Y` → `cellOffX/Y`
- Comments "sector" → "cell"

- [ ] **Step 4: Rename and move sector-map.ts**

Rename file `web-pixi/src/ui/sector-map.ts` → `web-pixi/src/ui/cell-map.ts`.

Inside the file:
- `class SectorMap` → `class CellMap`
- `SECTOR_SIZE` → `CELL_SIZE`
- `sectorLabel` → `cellLabel`
- `"SECTOR MAP"` → `"CELL MAP"`
- `sectorMapOpen` → `cellMapOpen`
- `"sector-map-open"` → `"cell-map-open"`
- `originSectorX/Y` → `originCellX/Y`
- String `"SECTOR (${...}"` → `"CELL (${...}"`

**`ui/hud.ts`:**
- `"Sector: (${state.originSectorX}"` → `"Cell: (${state.originCellX}"`

Update imports in `main.ts` to point to `cell-map.ts`.

- [ ] **Step 5: Update CSS if applicable**

Search for `.sector-map-open` in CSS/HTML files and rename to `.cell-map-open`.

- [ ] **Step 6: Verify web-pixi builds**

Run: `cd web-pixi && npx tsc --noEmit` (or equivalent type-check)
Expected: No type errors.

- [ ] **Step 7: Commit**

```bash
git add web-pixi/
git commit -m "rename: sector → cell in web-pixi TypeScript client"
```

---

### Task 6: Rename in slither web client TypeScript

**Files:**
- Modify: `examples/slither/web/src/state.ts`
- Modify: `examples/slither/web/src/network.ts`
- Modify: `examples/slither/web/src/main.ts`
- Modify: `examples/slither/web/src/background.ts`
- Modify: `examples/slither/web/src/minimap.ts`

- [ ] **Step 1: Rename in state.ts**

```typescript
export const CELL_SIZE = 8192;  // was SECTOR_SIZE

// In GameState class:
originCellX: number = 0;       // was originSectorX
originCellY: number = 0;       // was originSectorY

get cellOffsetX(): number { return this.originCellX * CELL_SIZE; }  // was sectorOffsetX
get cellOffsetY(): number { return this.originCellY * CELL_SIZE; }  // was sectorOffsetY

handleCellChange(cellX: number, cellY: number) {  // was handleSectorChange
    // Internal: all originSectorX/Y → originCellX/Y, SECTOR_SIZE → CELL_SIZE
}
```

- [ ] **Step 2: Rename in network.ts**

```typescript
// SpawnedData interface:
cellX: number;    // was sectorX
cellY: number;    // was sectorY

// In decodeSpawned:
cellX: view.getInt32(offset + 4, true),   // was sectorX
cellY: view.getInt32(offset + 8, true),   // was sectorY
```

- [ ] **Step 3: Rename in main.ts**

- `SECTOR_SIZE` → `CELL_SIZE`
- `handleSectorChange` → `handleCellChange`
- `sectorOffsetX/Y` → `cellOffsetX/Y`

- [ ] **Step 4: Rename in background.ts and minimap.ts**

**`background.ts`:** `sectorSize` → `cellSize` (parameter and all references), comments.

**`minimap.ts`:** `SECTOR_SIZE` → `CELL_SIZE`, comments.

- [ ] **Step 5: Verify slither web client type-checks**

Run: `cd examples/slither/web && npx tsc --noEmit`
Expected: No type errors.

- [ ] **Step 6: Commit**

```bash
git add examples/slither/web/
git commit -m "rename: sector → cell in slither web client"
```

---

### Task 7: Update docs + final rename verification

**Files:**
- Modify: `docs/planning/mmokit-roadmap.md` (already uses "cell" in new content; verify consistency)
- Modify: `docs/planning/mmokit-target-architecture.md`
- Modify: `CLAUDE.md` (if it references "sector" terminology)

- [ ] **Step 1: Update planning docs**

In `docs/planning/mmokit-roadmap.md`: Mark #13 as DONE. Update any remaining "sector" references in non-historical sections.

In `docs/planning/mmokit-target-architecture.md`: Update "sector" → "cell" where appropriate.

In `CLAUDE.md`: Update any references to `SectorCoord`, `SectorSize`, `SectorBoundary`, `SE_SECTOR_CHANGE`, etc.

Historical specs/plans keep original terminology.

- [ ] **Step 2: Full verification**

Run all checks:
```bash
go vet ./...
go test ./...
cd examples/slither && go vet ./...
```

Expected: All pass with no errors.

- [ ] **Step 3: Search for any remaining "sector" references**

Run: `grep -ri "sector" --include="*.go" --include="*.ts" --include="*.proto" -l` (excluding docs/, gen/, vendor/)

Fix any remaining references that should be "cell".

- [ ] **Step 4: Commit**

```bash
git add docs/ CLAUDE.md
git commit -m "rename: sector → cell in docs, mark #13 done"
```

---

## Phase B: Slither Protobuf Server→Client Migration

### Task 8: Add MakeEvent to `pkg/mmokit` + migrate `internal/` callers

**Files:**
- Modify: `pkg/mmokit/mmokit.go`
- Delete: `internal/netutil/event.go`
- Modify: All files in `internal/` that import `internal/netutil`

- [ ] **Step 1: Add MakeEvent and MakeOpResponse to `pkg/mmokit/mmokit.go`**

Add at the end of the constructors/functions section (after the existing factory functions):

```go
// MakeEvent builds a channel-0x00 frame: [0x00] + ServerEvent{code, data}.
func MakeEvent(code uint32, payload proto.Message) []byte {
	var inner []byte
	if payload != nil {
		var err error
		inner, err = proto.Marshal(payload)
		if err != nil {
			log.Printf("MakeEvent: marshal payload: %v", err)
			return nil
		}
	}
	evt := &enginepb.ServerEvent{
		Code: code,
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		log.Printf("MakeEvent: marshal event: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = ChannelEvent
	copy(frame[1:], evtData)
	return frame
}

// MakeOpResponse builds a channel-0x01 frame: [0x01] + OperationResponse{code, reqID, returnCode, errorMsg, data}.
func MakeOpResponse(code, reqID uint32, returnCode int32, errorMsg string, payload []byte) []byte {
	resp := &enginepb.OperationResponse{
		Code:       code,
		RequestId:  reqID,
		ReturnCode: returnCode,
		ErrorMsg:   errorMsg,
		Data:       payload,
	}
	respData, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("MakeOpResponse: marshal response: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(respData))
	frame[0] = ChannelOperation
	copy(frame[1:], respData)
	return frame
}
```

Add `"log"` to imports if not present.

- [ ] **Step 2: Migrate all `internal/` callers from `netutil.MakeEvent` to `mmokit.MakeEvent`**

Files that import `internal/netutil`:
- `internal/system/network.go`
- `internal/system/replication_adapters.go`
- `internal/system/docking.go`
- `internal/system/economy.go`
- `internal/system/equipment.go`
- `internal/game/game.go`
- `internal/game/entity_ship.go`
- `internal/game/combat_helpers.go`
- `internal/game/commands.go`
- `internal/game/lifecycle.go`
- `internal/universe/factory.go`
- `cmd/server/main.go`

For each file:
1. Replace import `"github.com/mmokit/mmokit/internal/netutil"` with `"github.com/mmokit/mmokit/pkg/mmokit"` (if not already imported)
2. Replace `netutil.MakeEvent(` with `mmokit.MakeEvent(`
3. Replace `netutil.MakeOpResponse(` with `mmokit.MakeOpResponse(`
4. Remove `netutil` import if no longer used

- [ ] **Step 3: Delete `internal/netutil/event.go`**

Check if `internal/netutil/` has other files. If `event.go` is the only file, delete the entire directory. Otherwise delete just `event.go`.

- [ ] **Step 4: Verify**

Run: `go vet ./...`
Expected: No errors.

- [ ] **Step 5: Run tests**

Run: `go test ./...`
Expected: All pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/mmokit.go internal/ cmd/
git rm internal/netutil/event.go  # or git rm -r internal/netutil/
git commit -m "refactor: move MakeEvent/MakeOpResponse to pkg/mmokit, delete internal/netutil"
```

---

### Task 9: Add slither proto messages + regenerate

**Files:**
- Modify: `proto/slitherpb/slither.proto`

- [ ] **Step 1: Add server event codes and messages to `slither.proto`**

Append to the existing `proto/slitherpb/slither.proto`:

```protobuf
// Server → Client event codes (slither-specific, values 200+)
enum SlitherServerEventCode {
    SSE_UNKNOWN = 0;
    SSE_LEADERBOARD = 200;
    SSE_KILL_FEED = 201;
}

// Player spawn notification (payload for enginepb.SE_PLAYER_SPAWNED)
message SlitherSpawnedMsg {
    uint32 entity_net_id = 1;
    int32 cell_x = 2;
    int32 cell_y = 3;
}

// World update (payload for enginepb.SE_WORLD_UPDATE)
message SlitherWorldUpdateMsg {
    uint32 tick = 1;
    repeated SlitherSnakeState snakes = 2;
    repeated SlitherFoodState foods = 3;
    repeated uint32 removed_ids = 4;
}

message SlitherSnakeState {
    uint32 net_id = 1;
    float head_x = 2;
    float head_y = 3;
    float angle = 4;
    float speed = 5;
    float mass = 6;
    uint32 skin_id = 7;
    uint32 length = 8;
    bool boosting = 9;
    string name = 10;
    repeated SlitherSegment segments = 11;
}

message SlitherSegment {
    float x = 1;
    float y = 2;
}

message SlitherFoodState {
    uint32 net_id = 1;
    float x = 2;
    float y = 3;
    float value = 4;
    uint32 color_idx = 5;
}

// Leaderboard (payload for SSE_LEADERBOARD)
message SlitherLeaderboardMsg {
    repeated SlitherLeaderEntry entries = 1;
}

message SlitherLeaderEntry {
    string name = 1;
    float mass = 2;
    uint32 skin_id = 3;
}

// Kill feed (payload for SSE_KILL_FEED)
message SlitherKillFeedMsg {
    repeated SlitherKillFeedEntry entries = 1;
}

message SlitherKillFeedEntry {
    string victim_name = 1;
    string killer_name = 2;
    float victim_mass = 3;
}
```

- [ ] **Step 2: Regenerate**

Run: `make proto`
Expected: Clean generation. New files appear in `gen/go/slitherpb/` and `gen/es/slitherpb/`.

- [ ] **Step 3: Verify generated Go code compiles**

Run: `go vet ./gen/...`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add proto/slitherpb/ gen/
git commit -m "feat(slither): add server→client proto messages (world update, leaderboard, kill feed, spawned)"
```

---

### Task 10: Replace slither server-side binary builders with protobuf

**Files:**
- Modify: `examples/slither/replication.go`
- Modify: `examples/slither/world.go`
- Modify: `examples/slither/system_network.go`

- [ ] **Step 1: Add imports to `replication.go`**

```go
import (
    enginepb "github.com/mmokit/mmokit/gen/go/enginepb"
    slitherpb "github.com/mmokit/mmokit/gen/go/slitherpb"
    "github.com/mmokit/mmokit/pkg/mmokit"
)
```

Remove `"encoding/binary"` and `"math"` imports if no longer needed.

- [ ] **Step 2: Replace `buildWorldUpdate` in `replication.go`**

Replace the entire `buildWorldUpdate` function with:

```go
func buildWorldUpdate(tick uint32, snakes []snakeNetData, foods []foodNetData, removed []uint32) []byte {
	msg := &slitherpb.SlitherWorldUpdateMsg{
		Tick:       tick,
		RemovedIds: removed,
	}

	for i := range snakes {
		sn := &snakes[i]
		state := &slitherpb.SlitherSnakeState{
			NetId:    sn.netID,
			HeadX:    sn.x,
			HeadY:    sn.y,
			Angle:    sn.angle,
			Speed:    sn.speed,
			Mass:     sn.mass,
			SkinId:   uint32(sn.skinID),
			Length:   uint32(sn.length),
			Boosting: sn.boosting,
			Name:     sn.name,
		}
		for _, seg := range sn.segments {
			state.Segments = append(state.Segments, &slitherpb.SlitherSegment{
				X: seg.X,
				Y: seg.Y,
			})
		}
		msg.Snakes = append(msg.Snakes, state)
	}

	for i := range foods {
		f := &foods[i]
		msg.Foods = append(msg.Foods, &slitherpb.SlitherFoodState{
			NetId:    f.netID,
			X:        f.x,
			Y:        f.y,
			Value:    f.value,
			ColorIdx: uint32(f.colorIdx),
		})
	}

	return mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), msg)
}
```

- [ ] **Step 3: Replace `buildLeaderboard` in `replication.go`**

Replace the entire function:

```go
func buildLeaderboard(entries []LeaderEntry) []byte {
	msg := &slitherpb.SlitherLeaderboardMsg{}
	for i := range entries {
		e := &entries[i]
		msg.Entries = append(msg.Entries, &slitherpb.SlitherLeaderEntry{
			Name:   e.Name,
			Mass:   e.Mass,
			SkinId: uint32(e.SkinID),
		})
	}
	return mmokit.MakeEvent(uint32(slitherpb.SlitherServerEventCode_SSE_LEADERBOARD), msg)
}
```

- [ ] **Step 4: Replace `buildKillFeed` in `replication.go`**

Replace the entire function:

```go
func buildKillFeed(entries []KillFeedEntry) []byte {
	msg := &slitherpb.SlitherKillFeedMsg{}
	for i := range entries {
		e := &entries[i]
		msg.Entries = append(msg.Entries, &slitherpb.SlitherKillFeedEntry{
			VictimName: e.VictimName,
			KillerName: e.KillerName,
			VictimMass: e.VictimMass,
		})
	}
	return mmokit.MakeEvent(uint32(slitherpb.SlitherServerEventCode_SSE_KILL_FEED), msg)
}
```

- [ ] **Step 5: Remove binary constants and dead code from `replication.go`**

Delete the message type constants:
```go
// DELETE these lines:
const (
    MsgWorldUpdate uint8 = 0
    MsgLeaderboard uint8 = 1
    MsgKillFeed    uint8 = 2
    MsgSpawned     uint8 = 3
)
```

Remove any unused imports (`encoding/binary`, `math`).

- [ ] **Step 6: Replace `SendSpawnedMsg` in `world.go`**

Replace the binary frame builder:

```go
func (gw *SlitherWorld) SendSpawnedMsg(connID, entityNetID uint32) {
	cell := gw.Cell()
	frame := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &slitherpb.SlitherSpawnedMsg{
		EntityNetId: entityNetID,
		CellX:       int32(cell.SX),
		CellY:       int32(cell.SY),
	})
	gw.Engine().ConnMgr.Send(connID, frame)
}
```

Add imports for `enginepb`, `slitherpb`, and `mmokit` if not already present.

- [ ] **Step 7: Verify slither server compiles**

Run: `cd examples/slither && go vet ./...`
Expected: No errors.

- [ ] **Step 8: Commit**

```bash
git add examples/slither/*.go
git commit -m "feat(slither): replace binary server→client protocol with protobuf ServerEvent envelopes"
```

---

### Task 11: Update slither web client to protobuf deserialization

**Files:**
- Modify: `examples/slither/web/src/network.ts`
- Modify: `examples/slither/web/src/state.ts` (SpawnedData interface)

- [ ] **Step 1: Update imports in `network.ts`**

Replace/add imports:

```typescript
import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import { ClientEventSchema, ServerEventSchema, ServerEventCode } from "@gen/enginepb/engine_pb.js";
import {
    SlitherInputMsgSchema, SkinSelectMsgSchema, SlitherClientEventCode,
    SlitherServerEventCode,
    SlitherWorldUpdateMsgSchema, SlitherSpawnedMsgSchema,
    SlitherLeaderboardMsgSchema, SlitherKillFeedMsgSchema,
} from "@gen/slitherpb/slither_pb.js";
```

- [ ] **Step 2: Replace the onmessage handler**

Replace the entire binary parsing `onmessage` handler with protobuf deserialization:

```typescript
this.ws.onmessage = (event: MessageEvent) => {
    if (!(event.data instanceof ArrayBuffer)) return;
    const bytes = new Uint8Array(event.data);
    if (bytes.length < 2) return;

    const channel = bytes[0];
    if (channel !== CHANNEL) return;

    // Decode ServerEvent envelope
    const evt = fromBinary(ServerEventSchema, bytes.subarray(1));

    switch (evt.code) {
        case ServerEventCode.SE_WORLD_UPDATE: {
            const msg = fromBinary(SlitherWorldUpdateMsgSchema, evt.data);
            const data: WorldUpdateData = {
                tick: msg.tick,
                snakes: msg.snakes.map(s => ({
                    id: s.netId,
                    headX: s.headX,
                    headY: s.headY,
                    angle: s.angle,
                    speed: s.speed,
                    mass: s.mass,
                    skinID: s.skinId,
                    length: s.length,
                    boosting: s.boosting,
                    name: s.name,
                    segments: s.segments.map(seg => ({ x: seg.x, y: seg.y })),
                })),
                foods: msg.foods.map(f => ({
                    id: f.netId,
                    x: f.x,
                    y: f.y,
                    value: f.value,
                    color: f.colorIdx,
                })),
                removed: [...msg.removedIds],
            };
            this.callbacks.onWorldUpdate(data);
            break;
        }
        case ServerEventCode.SE_PLAYER_SPAWNED: {
            const msg = fromBinary(SlitherSpawnedMsgSchema, evt.data);
            const data: SpawnedData = {
                entityID: msg.entityNetId,
                cellX: msg.cellX,
                cellY: msg.cellY,
            };
            this.callbacks.onSpawned(data);
            break;
        }
        case SlitherServerEventCode.SSE_LEADERBOARD: {
            const msg = fromBinary(SlitherLeaderboardMsgSchema, evt.data);
            const data: LeaderboardData = {
                entries: msg.entries.map(e => ({
                    name: e.name,
                    mass: e.mass,
                    skinID: e.skinId,
                })),
            };
            this.callbacks.onLeaderboard(data);
            break;
        }
        case SlitherServerEventCode.SSE_KILL_FEED: {
            const msg = fromBinary(SlitherKillFeedMsgSchema, evt.data);
            const data: KillFeedData = {
                entries: msg.entries.map(e => ({
                    victim: e.victimName,
                    killer: e.killerName,
                    mass: e.victimMass,
                })),
            };
            this.callbacks.onKillFeed(data);
            break;
        }
    }
};
```

- [ ] **Step 3: Remove all binary decode functions and constants**

Delete these functions and constants from `network.ts`:
- `const MSG_WORLD_UPDATE = 0`
- `const MSG_LEADERBOARD = 1`
- `const MSG_KILL_FEED = 2`
- `const MSG_SPAWNED = 3`
- `const textDecoder = new TextDecoder()`
- `function decodeWorldUpdate(...)`
- `function decodeLeaderboard(...)`
- `function decodeKillFeed(...)`
- `function decodeSpawned(...)`

- [ ] **Step 4: Update SpawnedData interface**

In the `SpawnedData` interface (either in `network.ts` or `state.ts`), the fields should already be `cellX`/`cellY` from the Phase A rename. Verify this is the case.

- [ ] **Step 5: Verify web client type-checks**

Run: `cd examples/slither/web && npx tsc --noEmit`
Expected: No type errors.

- [ ] **Step 6: Commit**

```bash
git add examples/slither/web/
git commit -m "feat(slither): web client uses protobuf ServerEvent deserialization instead of raw binary"
```

---

### Task 12: End-to-end verification

- [ ] **Step 1: Full Go build**

```bash
go vet ./...
make build
cd examples/slither && go vet ./...
```

Expected: All pass.

- [ ] **Step 2: Run all tests**

```bash
go test ./...
```

Expected: All pass.

- [ ] **Step 3: Manual smoke test**

Start the slither server and web client:
```bash
cd examples/slither && go run .
```

Open the web client. Verify:
- Can spawn a snake and move around
- Leaderboard appears
- Kill feed appears on kills
- Cell boundary crossing works (if multi-cell)
- No console errors in browser dev tools

- [ ] **Step 4: Verify no remaining binary protocol artifacts**

Search for any remaining binary message references:
```bash
grep -r "MsgWorldUpdate\|MsgLeaderboard\|MsgKillFeed\|MsgSpawned" examples/slither/
grep -r "MSG_WORLD_UPDATE\|MSG_LEADERBOARD\|MSG_KILL_FEED\|MSG_SPAWNED" examples/slither/web/
```

Expected: No matches.

- [ ] **Step 5: Final commit (if any fixes needed)**

```bash
git add -A
git commit -m "fix: address any remaining issues from phase 2 migration"
```
