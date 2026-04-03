# Phase 2: Cell Rename + Slither Protobuf Server→Client Migration

## Context

Phase 1 of the mmokit roadmap is complete (Visibility System, Player Manager, Input Framework). Phase 2 focuses on performance foundations: terminology alignment, then serialization strategy leading into delta compression.

Two tasks are bundled here because they're tightly coupled — the rename must land first so new proto messages use "cell" terminology from the start.

**Task A:** Rename "Sector" → "Cell" across the codebase (#13 from the roadmap).
**Task B:** Migrate slither's server→client protocol from raw binary to protobuf `ServerEvent` envelopes.

---

## Task A: Sector → Cell Rename

### Approach

Mechanical find-and-replace. No behavioral changes. Proto field **numbers** stay the same — only names change. No backward compatibility layer needed.

### Scope

#### Proto files

`proto/enginepb/engine.proto`:
- `SE_SECTOR_CHANGE` → `SE_CELL_CHANGE`
- `SectorChangeMsg` → `CellChangeMsg`
- Fields: `sector_x`/`sector_y` → `cell_x`/`cell_y`

`proto/gamepb/game.proto`:
- `PlayerSpawnedMsg`: `origin_sector_x/y` → `origin_cell_x/y`
- `MapStationInfo`: `sector_x/y` → `cell_x/y`
- `DebugFlagsMsg`: `show_sector_grid` → `show_cell_grid`
- `ReplicaSnapshotPB`: `sector_sx/sy` → `cell_sx/sy`

Regenerate all generated code (`buf generate`).

#### `pkg/coords/`

- `SectorCoord` → `CellCoord`
- `SectorSize` → `CellSize`
- `SetSectorSize()` → `SetCellSize()`

#### `pkg/component/`

- `SectorCoord` component → `CellCoord`

#### `pkg/mmokit/`

- All facade aliases updated
- Fix `Coordssector` typo → `CellCoord`
- `SectorSize()` → `CellSize()`, `SetSectorSize()` → `SetCellSize()`
- `SectorID` → `CellID`

#### `pkg/universe/` (~13 files)

- `WorldBase.Sector()` → `Cell()`, `SectorCoordMap()` → `CellCoordMap()`
- Internal fields: `sector` → `cell`, `sectorMap` → `cellMap`
- `BoundarySystem` name: `"SectorBoundary"` → `"CellBoundary"`
- `NodeBridge.SectorOwner()` → `CellOwner()` (interface + implementations)
- `Coordinator.SectorOwner` map → `CellOwner`
- `Node.Sector` field → `Node.Cell`
- `SectorID()` → `CellID()` in topology.go

#### `internal/`

- `game.GameWorld.Sector` field references
- `game.Components.SectorCoord` mapper
- `fmtSectorPos()` → `fmtCellPos()`, `fmtSectorPosRaw()` → `fmtCellPosRaw()`
- `GenerateBelts(sector)` → `GenerateBelts(cell)`
- All entity files: `.SectorCoord` references
- All system files: `sectorDX/DY` locals, `SectorSize` refs

#### Web clients

`web-pixi/src/`:
- `SECTOR_SIZE` → `CELL_SIZE`
- State vars: `originSectorX/Y` → `originCellX/Y`, `sectorMapOpen` → `cellMapOpen`
- Classes: `SectorGrid` → `CellGrid`, `SectorMap` → `CellMap`
- CSS: `.sector-map-open` → `.cell-map-open`
- File renames where appropriate (e.g., `sector-map.ts` → `cell-map.ts`)

`examples/slither/web/src/`:
- `SECTOR_SIZE` → `CELL_SIZE`
- `sectorX/Y` → `cellX/Y`
- `handleSectorChange` → `handleCellChange`

#### Tests

All test files referencing sector types/names.

#### Docs

- Planning docs updated (roadmap, target architecture)
- Historical specs/plans keep original terminology

---

## Task B: Slither Protobuf Server→Client Migration

### Current State

- **Client→Server:** Already protobuf (`ClientEvent` envelopes with `SlitherInputMsg`, `SkinSelectMsg`). InputRouter integrated.
- **Server→Client:** Raw binary frames with `[channel][msgType][payload]` header. Four message types: WorldUpdate (type 0), Leaderboard (type 1), KillFeed (type 2), Spawned (type 3).

### Event Code Strategy

Reuse engine-level `ServerEventCode` where semantics match. Define slither-specific codes at 200+ for game-only messages.

| Message | Event Code | Payload |
|---------|-----------|---------|
| World Update | `SE_WORLD_UPDATE` (0) | `SlitherWorldUpdateMsg` |
| Player Spawned | `SE_PLAYER_SPAWNED` (1) | `SlitherSpawnedMsg` |
| Leaderboard | `SSE_LEADERBOARD` (200) | `SlitherLeaderboardMsg` |
| Kill Feed | `SSE_KILL_FEED` (201) | `SlitherKillFeedMsg` |

This mirrors the main game's pattern: same engine event code, different payload type per game. The client knows which game it's running and which schema to use.

### MakeEvent Helper

Add `MakeEvent(code uint32, payload proto.Message) []byte` to `pkg/mmokit/mmokit.go`. Same logic as `internal/netutil/event.go` — builds `[0x00] + ServerEvent{code, data}`. This makes the helper available to examples and any external consumer of the engine.

Delete `internal/netutil/MakeEvent` and switch all `internal/` callers to `mmokit.MakeEvent` directly. Same for `MakeOpResponse` if it moves too.

### Proto Messages

Added to `proto/slitherpb/slither.proto`:

```protobuf
// Server → Client event codes (slither-specific, values 200+)
enum SlitherServerEventCode {
    SSE_UNKNOWN = 0;
    SSE_LEADERBOARD = 200;
    SSE_KILL_FEED = 201;
}

// Player spawn notification (payload for SE_PLAYER_SPAWNED)
message SlitherSpawnedMsg {
    uint32 entity_net_id = 1;
    int32 cell_x = 2;
    int32 cell_y = 3;
}

// World update (payload for SE_WORLD_UPDATE)
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

### Server-Side Changes

**`examples/slither/replication.go`:**
- Replace `buildWorldUpdate()` with protobuf: build `SlitherWorldUpdateMsg` from snake/food data, serialize via `mmokit.MakeEvent(SE_WORLD_UPDATE, &msg)`.
- Replace `buildLeaderboard()` → `mmokit.MakeEvent(SSE_LEADERBOARD, &SlitherLeaderboardMsg{...})`.
- Replace `buildKillFeed()` → `mmokit.MakeEvent(SSE_KILL_FEED, &SlitherKillFeedMsg{...})`.
- Remove all manual binary encoding functions and constants (`MsgWorldUpdate`, `MsgLeaderboard`, etc.).
- The `snakeNetData`/`foodNetData` internal Go structs remain as replicator types — they get converted to proto messages in the frame writer.

**`examples/slither/world.go`:**
- Replace `SendSpawnedMsg` binary frame with `mmokit.MakeEvent(SE_PLAYER_SPAWNED, &SlitherSpawnedMsg{...})`.

### Web Client Changes

**`examples/slither/web/src/network.ts`:**
- Replace all `DataView` binary parsing with protobuf deserialization.
- `onmessage` handler: read channel byte, then `fromBinary(ServerEventSchema, data.slice(1))` to get envelope, switch on `evt.code`, deserialize inner payload with the appropriate schema.
- Remove all `decode*` functions and `MSG_*` constants.
- Keep the callback interface (`onWorldUpdate`, `onLeaderboard`, etc.) stable — adapt the data source, not the consumer API.

### Size Impact

Protobuf will be ~10-15% larger than raw binary for world updates due to field tags and varint encoding of floats. Acceptable for WebSocket transport. Delta compression (#3) will address bandwidth in the next phase.

---

## Verification

After each task:
- `go vet ./...` passes
- `make build` succeeds
- `cd examples/slither && go build ./...` succeeds
- Slither web client connects and plays (`make dev` or equivalent)
- Main game still builds and runs
- Manual smoke test: spawn a snake, move around, verify leaderboard/kill feed appear, cross cell boundaries
