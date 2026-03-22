# Sector Map Design

## Context

Players need spatial awareness beyond the 100-unit AoI radius. A zoomable sector map toggled with Tab shows the player's position relative to the full sector (8192x8192 units) and key landmarks like stations. The map uses a holographic HUD aesthetic and is built as a PixiJS Container overlay.

## Proto Changes

Add to `proto/game.proto`:

```protobuf
// New event code
SE_MAP_DATA = 13;

// New messages
message MapStationInfo {
    int32 sector_x = 1;
    int32 sector_y = 2;
    float local_x = 3;
    float local_y = 4;
    string name = 5;
}

message MapDataMsg {
    repeated MapStationInfo stations = 1;
}
```

Separate from `PlayerSpawnedMsg` to avoid re-sending the item registry on sector change.

## Server Changes

### Collect station data

Add `CollectStationMapData()` to `GameWorld` in `internal/game/entity_station.go`:

- Create a `Filter3[component.Station, component.Position, component.SectorCoord]` query (following the pattern in `SectorBoundarySystem`)
- Iterate with `query.Next()`, call `query.Get()` to retrieve `(station, pos, sec)`
- Return `[]*gamepb.MapStationInfo` with sector coords, local position, and name
- Hardcode name as `"TRADE STATION"` for now (Station component has no name field yet)

### Send on login

In `SpawnPlayer()` (`internal/game/entity_ship.go`), after sending `PlayerSpawnedMsg`, send `SE_MAP_DATA` with `CollectStationMapData()` via `SendReliable`.

### Send on sector change

In `SectorBoundarySystem.Update()` (`internal/system/sector_boundary.go`), after sending `SE_SECTOR_CHANGE`, send `SE_MAP_DATA`.

### Logging

Add `CatMap` log category in `internal/game/logcat.go`. Log map data sends with connection ID and station count.

## Client State Changes

In `web-pixi/src/state.ts`:

```typescript
export interface MapStation {
  sectorX: number;
  sectorY: number;
  localX: number;
  localY: number;
  name: string;
}

// Add to GameState:
sectorMapOpen: boolean;   // false
mapStations: MapStation[]; // []
```

## Client Network Handler

In `web-pixi/src/network.ts`, handle `SE_MAP_DATA`: decode `MapDataMsg`, populate `state.mapStations`.

## Sector Map Module

New file: `web-pixi/src/ui/sector-map.ts`

### Container hierarchy

```
SectorMap.container (on app.stage, screen-fixed)
├── background       -- rect fill 0x000a14 alpha 0.92, cyan border
├── gridLayer        -- sector boundary lines + coordinate labels
├── markerLayer      -- station rings, player diamond
├── scanlineOverlay  -- horizontal lines every 3px, alpha 0.08, cached
└── cornerAccents    -- bracket corners in cyan
```

### Panel sizing

85% of screen width and height, centered. Recalculated each frame when visible.

### Coordinate system

Player-centered. All positions computed as absolute world coordinates: `sectorIndex * 8192 + localPosition`. Player is always at panel center.

```
pixelsPerUnit = (panelWidth * mapZoom) / SECTOR_SIZE
mapX = panelW/2 + (entityAbsX - playerAbsX) * pixelsPerUnit
mapY = panelH/2 + (entityAbsY - playerAbsY) * pixelsPerUnit
```

### Zoom

- `mapZoom = 1.0` default: one sector fills panel width
- Scroll wheel: multiply/divide by 1.15 per notch
- Range: `[0.15, 4.0]` — from ~7 sectors visible to quarter-sector detail
- Zoom state stored on the SectorMap instance, not in GameState

### Visual elements

**Grid:** Lines at sector boundaries (color `0x0066aa`, alpha 0.2). Sector labels in monospace at `0x4488cc`. Calculate visible sector range from zoom + player position.

**Player marker:** Green diamond (8px) at panel center, fill `0x00ff66`. Animated pulse ring expanding outward over 2 seconds, fading as it grows.

**Station markers:** Green ring (6px radius) with center dot, label below in `0x88ffaa`. Positioned via `worldToMap()`. Culled if off-panel.

**Scanlines:** Horizontal lines every 3px, fill `0x000000` alpha 0.08. Drawn once, cached as texture. Regenerated on resize.

**Corner accents:** 30px bracket lines at each corner, stroke `0x00ccff` width 2 alpha 0.6.

**Title:** "SECTOR MAP" top-center in cyan monospace. Current sector coordinates below.

**Info bar:** Bottom bar showing player position coordinates and sector.

## Input Integration

### Tab toggle (`web-pixi/src/input.ts`)

In keydown handler: `Tab` toggles `state.sectorMapOpen`. Call `e.preventDefault()` to block browser tab-focus.

### Escape close

Insert into the ESC priority chain before `marketPanelOpen` (first in the chain, since it's a full-screen overlay): if `sectorMapOpen`, close map and consume the keypress.

### Block game input

While `sectorMapOpen`: block all game input. Specifically:
- Guard the `keydown` handler: if `sectorMapOpen`, only process Tab (close) and Escape (close). Return early for all other keys.
- Guard the `mousedown` handler: skip right-click movement (`issueMove()`, `state.rightMouseDown`) when map is open.
- Guard the `click` handler: skip left-click targeting when map is open.
- Guard `sendInput()`: add `state.sectorMapOpen` to the early-return conditions alongside `isDead`/`chatMode`/`isDocked`.
- Guard the `setInterval` input loop in `main.ts`: skip cursor-to-world re-projection when map is open.

### Scroll wheel (`web-pixi/src/main.ts`)

In wheel handler: if `state.sectorMapOpen`, route to `sectorMap.handleWheel()` instead of world zoom.

## Main Loop Integration

In `web-pixi/src/main.ts`:

1. Construct `SectorMap` after minimap, before ticker
2. In ticker: call `sectorMap.update(state, screenW, screenH)` after minimap update
3. SectorMap sets `container.visible = state.sectorMapOpen` and early-returns if hidden

## Edge Cases

- **Sector boundary:** Player position uses `originSectorX/Y + renderX/Y`, handles wrapping correctly
- **Different sector than (0,0):** All math uses absolute coordinates, works for any sector
- **Dead/docked:** Fall back to sector center position if no entity exists. Close the map on death, dock, and disconnect by setting `state.sectorMapOpen = false` in `SE_PLAYER_DIED`, `SE_DOCKED`, and `onClose` handlers in `network.ts`.
- **Multiple stations (future):** `mapStations` is an array, protocol supports N stations
- **Scanline perf:** Cached as texture, not redrawn per frame
- **Panel resize:** Only recalculate panel dimensions when the map opens or the window resizes, not every frame

## Files Changed

| File | Change |
|------|--------|
| `proto/game.proto` | Add `MapStationInfo`, `MapDataMsg`, `SE_MAP_DATA = 13` |
| `internal/game/entity_station.go` | `CollectStationMapData()` method + send on spawn |
| `internal/game/logcat.go` | Add `CatMap` log category |
| `internal/system/sector_boundary.go` | Send `SE_MAP_DATA` after sector change |
| `web-pixi/src/state.ts` | Add `MapStation` interface, `sectorMapOpen`, `mapStations` |
| `web-pixi/src/network.ts` | Handle `SE_MAP_DATA` event |
| `web-pixi/src/input.ts` | Tab toggle, ESC close, input blocking |
| `web-pixi/src/main.ts` | Create `SectorMap`, ticker update, wheel intercept |
| `web-pixi/src/ui/sector-map.ts` | **New file** — full map implementation |

## Verification

1. `make proto` — regenerate protobuf, verify no errors
2. `make build` — compile server
3. `make dev` — run server + web client
4. Open web client, log in, press Tab — map should appear
5. Verify player diamond at center, station marker visible
6. Scroll wheel zooms in/out on the map
7. Press Tab or Escape to close
8. Move far from station, open map — station still visible
9. Cross a sector boundary — verify map data is re-sent and map updates correctly
