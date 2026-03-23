# Sector Grid Lines

Visual debug overlay showing sector boundaries in the web client, toggled globally via server console command.

## Server Side

### Proto Changes (`proto/game.proto`)

Add new event code and message:

```protobuf
SE_DEBUG_FLAGS = 14;  // in ServerEventCode enum

message DebugFlagsMsg {
  bool show_sector_grid = 1;
}
```

### GameWorld State (`internal/game/world.go` or equivalent)

Add field to `GameWorld`:

```go
DebugShowSectorGrid bool // default false
```

### Console Command (`internal/game/commands.go`)

Register `grid` command (alias `sg`, category "debug"):

- Toggles `gw.DebugShowSectorGrid`
- Broadcasts `SE_DEBUG_FLAGS` with current value to all connected players via `SendReliable`
- Prints confirmation to console (e.g., `"sector grid: ON"`)

Implementation pattern — iterate `gw.Players.Entities` to get all connected player connIDs, send reliable to each.

### On Player Spawn

After sending `SE_PLAYER_SPAWNED` in the spawn flow, also send `SE_DEBUG_FLAGS` with current flag values so late-joining players pick up the current debug state.

## Client Side

### State (`web-pixi/src/state.ts`)

Add to `GameState`:

```typescript
showSectorGrid: boolean;  // default false
```

### Network Handler (`web-pixi/src/network.ts`)

Add case for `SE_DEBUG_FLAGS` in the event switch:

```typescript
case ServerEventCode.SE_DEBUG_FLAGS: {
  const flags = fromBinary(DebugFlagsMsgSchema, evt.data);
  state.showSectorGrid = flags.showSectorGrid;
  break;
}
```

### Grid Rendering (`web-pixi/src/world/grid.ts`)

Replace the existing small-scale grid (6.7-unit cells) with sector boundary lines:

- Draw lines at world positions that are multiples of `SECTOR_SIZE` (8192)
- Only draw lines that intersect the current viewport (typically 0-2 vertical and 0-2 horizontal lines visible)
- Style: semi-transparent white or cyan lines, thicker than the old grid (e.g., `alpha: 0.3`, `width: px(2)`)
- Add sector coordinate labels at line intersections or along edges (e.g., `"(0,0)"`, `"(1,0)"`)

### Visibility Toggle (`web-pixi/src/main.ts`)

Set `gridContainer.visible = state.showSectorGrid` in the render loop. Since `showSectorGrid` defaults to `false`, grid is hidden on connect.

## Files Changed

| File | Change |
|------|--------|
| `proto/game.proto` | Add `SE_DEBUG_FLAGS = 14`, `DebugFlagsMsg` |
| `gen/` | Regenerated (Go, C#, ES) via `make proto` |
| `internal/game/world.go` | Add `DebugShowSectorGrid bool` field |
| `internal/game/commands.go` | Add `grid` / `sg` console command |
| `internal/game/entity_ship.go` | Send `SE_DEBUG_FLAGS` on player spawn |
| `web-pixi/src/state.ts` | Add `showSectorGrid` to `GameState` |
| `web-pixi/src/network.ts` | Handle `SE_DEBUG_FLAGS` event |
| `web-pixi/src/world/grid.ts` | Replace small grid with sector boundary lines |
| `web-pixi/src/main.ts` | Toggle `gridContainer.visible` from state |
