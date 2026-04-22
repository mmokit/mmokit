# Spawn Location System — Design

**Status:** approved 2026-04-22
**Supersedes:** `coords.SpawnPoint` + `coords.WorldCenterOfCell`

## Goal

Replace the current two-layer spawn system — gateway-side world-space routing and game-side cell-local entity creation — with a single typed world-space `Location` primitive that flows end-to-end from the login resolver into the game's entity-spawn callback. The same primitive will later power teleport/warp without architectural changes.

## Motivation

The current code has three concrete bugs and one latent design tangle, all traceable to the split between world-space routing and cell-local spawn:

1. **Stale-`CellSize` footgun.** `coords.WorldCenterOfCell(cellX, cellY)` reads the package-global `coords.CellSize` at call time. When used inside a `Config{…}` literal — the natural place to set `DefaultSpawn` — it runs before `mmokit.New(cfg)` calls `coords.SetCellSize(cfg.CellSize)`. For any game with a non-default cell size, the computed spawn point lands outside every cell.

2. **Silent-fallback routing.** When the gateway's `cellAtPosition(x, y)` returns `""` (empty — point outside all cells), it falls through to `anyCellID()`, which picks a cell via Go map iteration order. This is how fresh users ended up in random cells in 4node-basic distributed mode.

3. **Respawn loses position.** `Bridge.RequestRespawn` computes a world-space point, then sends a `MsgSpawnTransfer{ConnID, Username}` — no position field. The destination cell has no way to honour the resolved respawn point; it falls back to the game's hardcoded spawn zone.

4. **Cell-local spawn in game code.** Games like `4node-basic` write `SpawnEntity(Position{X: cellSize*0.85, Y: cellSize*0.85})` — a **cell-local** position. The gateway's `DefaultSpawn` is **world-space**. The two layers never exchange their resolved value; each re-derives the spawn independently.

One unified `Location` type, carried on `PlayerSession` and interpreted by the game via a new `SpawnAtLocation` helper, collapses all four issues into a single source of truth.

## Non-Goals

- **No teleport API in this spec.** The design leaves a documented extension slot, but `TeleportPlayer` is out of scope.
- **No named-anchor registry in the engine.** Games that want named anchors (`"bank"`, `"tutorial_start"`) build their own `map[string]Location`.
- **No client-side teleport validation, cooldowns, or abilities.** Deferred until teleport lands.
- **No change to cell-local `Position` storage.** Entities continue to store cell-local coords for float32 precision reasons; `Location` is a *descriptor*, not a replacement for `Position`.

## Design

### The `Location` type

```go
// pkg/coords/location.go (new file; pkg/coords/spawn.go removed)
package coords

// Location is a world-space anchor for placing a player entity — used for
// initial spawn, respawn, and (later) teleport/warp. The coord frame is
// absolute world-space, NOT cell-local, so a Location survives cell
// split/merge: the gateway resolves which cell currently owns (X, Y) at
// dispatch time.
type Location struct {
    X, Y   float32 // world-space coordinates
    Facing float32 // radians, 0 = +X axis; game opts in via Rotation component
    Tag    string  // opaque, game-defined; used for arrival-logic switching
}

// IsZero reports whether l is the zero value — useful as a "no preference"
// sentinel in callers that optionally override the default.
func (l Location) IsZero() bool { return l == (Location{}) }
```

Facade re-export: `type Location = coords.Location` in `pkg/mmokit/mmokit.go`.

**Field rationale.**

- `X, Y` — world-space, full stop. Cell is an artifact of mesh topology, not a property of the location.
- `Facing` — radians, matching the existing `component.Rotation{Angle float32}` convention. Lives on `Location` because facing is a property of the *destination* ("the bank pad faces north"), not the arriving entity. The engine does **not** automatically apply it; games opt in via a `mmokit.WithFacing(radians)` spawn option.
- `Tag` — opaque string, game-defined. Lets a game switch on arrival ("did the player warp into the tutorial?", "is this the duel arena spawn?") without the engine caring what the values mean.

### Resolution flow — login

```text
client auth msg  →  gateway.resolveSpawn(username)  ──returns──►  Location
                        │
                        │  (falls back to Config.DefaultSpawn if resolver nil / returns ok=false)
                        ▼
                   gateway.cellAtPosition(loc.X, loc.Y)
                        │
                        ├─ returns ""  →  REJECT login: "spawn point outside world bounds"
                        │                  (anyCellID fallback removed — it hid bugs)
                        │
                        ▼
                   cell found
                        │
                        ▼
                   gateway stores loc on localSession.SpawnLocation
                        │
                        ▼
                   dispatchPlayerAssignment  →  cell.Inbox (MsgPlayerAssignment carries Location)
                        │
                        ▼
                   cell copies Location onto PlayerSession.SpawnLocation
                        │
                        ▼
                   PlayerManager Transition → StateActive → OnEnter fires
                        │
                        ▼
                   game reads s.SpawnLocation, calls gw.SpawnAtLocation(s.SpawnLocation, ...)
```

### API changes

| Surface | Before | After |
| --- | --- | --- |
| `coords.SpawnPoint` | `struct{X, Y float32}` | **deleted** — replaced by `coords.Location` |
| `coords.WorldCenterOfCell` | `func(cellX, cellY int32) SpawnPoint` | **deleted** — write `Location{X: …, Y: …}` literals |
| `mmokit.SpawnPoint` | alias | **deleted** |
| `mmokit.WorldCenterOfCell` | alias | **deleted** |
| `Config.DefaultSpawn` | `coords.SpawnPoint` | `coords.Location` |
| `SpawnResolver` | `func(user) (worldX, worldY float32, ok bool)` | `func(user) (Location, bool)` |
| `PlayerSession` | no spawn field | `SpawnLocation Location` |
| `SpawnTransfer` msg | `{ConnID, Username}` | `{ConnID, Username, Location}` |
| `gateway.anyCellID` fallback | silent random-cell fallback | **removed** — unmapped spawn → login rejection |
| `MsgPlayerAssignment` | no location | carries `Location` |
| game-side spawn | `SpawnEntity(Position{localX, localY}, ...)` | `gw.SpawnAtLocation(loc, ...)` (new helper) |
| — | — | `mmokit.WithFacing(radians)` (new spawn option, opt-in Rotation) |

### `WorldBase.SpawnAtLocation`

```go
// pkg/universe/world_base.go
// SpawnAtLocation creates a new entity at the given world-space Location.
// The Location must fall within the current cell's WorldBounds; this
// invariant is enforced by the gateway's cellAtPosition check before
// dispatch. SpawnAtLocation converts world → cell-local internally using
// b.rootCell() and delegates to SpawnEntity.
//
// Facing is not applied automatically — pass mmokit.WithFacing(loc.Facing)
// if the game uses Rotation components.
func (b *WorldBase) SpawnAtLocation(loc coords.Location, opts ...SpawnOption) ecs.Entity {
    rootCell := b.rootCell()
    cellSize := coords.CellSize
    minX := float32(rootCell.X) * cellSize
    minY := float32(rootCell.Y) * cellSize
    maxX := minX + cellSize
    maxY := minY + cellSize

    if loc.X < minX || loc.X >= maxX || loc.Y < minY || loc.Y >= maxY {
        msg := fmt.Sprintf("SpawnAtLocation called with Location outside cell bounds: "+
            "loc=(%f,%f) bounds=[%f,%f)×[%f,%f) cell=%s",
            loc.X, loc.Y, minX, maxX, minY, maxY, b.cellID)
        b.eng.Log.Log(CatInvariant, "%s", msg)
        if b.eng.CommitLog != nil {
            b.eng.CommitLog.Append(CommitEvent{
                Kind:    EventInvariantViolation,
                Step:    "spawn-at-location-out-of-bounds",
                Success: false,
                Error:   msg,
            })
        }
        if b.invariantMode == InvariantPanic {
            panic(msg)
        }
        // InvariantOff / InvariantLog: clamp and continue (degraded, not fatal).
        loc.X = clampFloat32(loc.X, minX, maxX-1)
        loc.Y = clampFloat32(loc.Y, minY, maxY-1)
    }

    pos := component.Position{X: loc.X - minX, Y: loc.Y - minY}
    return b.SpawnEntity(pos, opts...)
}
```

### Respawn flow (reuses the same primitive)

`cellBridge.RequestRespawn(connID, username)`:

1. Resolve `Location` via the same path as login (`spawnResolver` → `DefaultSpawn`).
2. `cellAtPosition(loc.X, loc.Y)` → target cell. If empty, log error and reject the respawn (same as login rejection).
3. Build `SpawnTransfer{ConnID, Username, Location: loc}`, send to destination cell's Inbox as `MsgSpawnTransfer`.
4. Destination cell's inbox handler writes `Location` onto the new `PlayerSession.SpawnLocation` and transitions Pending → Active. `OnEnter` fires, game reads `s.SpawnLocation`, spawns via `SpawnAtLocation`.

### Teleport extension slot (not implemented)

Documented for design continuity only:

```go
// Coordinator.TeleportPlayer(connID, loc)
//  1. If cellAtPosition(loc.X, loc.Y) == currentCell(connID):
//       loop-safe update entity Position directly, send SE_TELEPORT event
//       so client can skip interpolation.
//  2. Otherwise: kick CellTransferKind=TELEPORT transfer carrying Location
//       in MeshFrame.PlayerMigration. Destination cell calls
//       SpawnAtLocation(loc, ...) from OnEnter / OnTeleport hook.
```

The session-attached `SpawnLocation` and the `SpawnAtLocation` helper are the same surfaces teleport will use — no re-plumbing required.

## Error handling

- **Location outside world bounds at login.** Gateway rejects the login with `LoginRejectedMsg` (new reason code: `"spawn point out of bounds"`). Logged under `CatNetConn`. Was: silent `anyCellID` fallback.
- **Location outside world bounds at respawn.** Same behaviour. Client stays dead; the game's respawn-policy code is expected to pick a better Location.
- **Location outside current cell at `SpawnAtLocation`.** Invariant violation logged via `recordIntegrityViolation`. Under `InvariantPanic` mode this panics (dev default). Under `InvariantLog`/`InvariantOff` (production) the location is clamped to cell bounds and spawn continues — degraded but not fatal.
- **Location.Tag with game-unknown values.** Games are expected to treat unknown tags as "default" — no engine-level rejection.

## Testing

- **Unit:** `coords/location_test.go` — struct shape, `IsZero` behaviour, no drift across cell sizes (what `WorldCenterOfCell`'s footgun test pinned earlier).
- **Unit:** `universe/world_base_test.go` — `SpawnAtLocation` world→local conversion for `rootCell` at (0,0), (1,0), (0,1), (1,1) on a 2×2 grid with `CellSize=2000`; out-of-bounds cases under all three `InvariantMode` values.
- **Integration:** `4node-basic/mesh_e2e_test.go` — fresh login always routes to cell_0_0 when `DefaultSpawn` sits in cell_0_0. Previously failed non-deterministically due to the `anyCellID` fallback.
- **Integration:** new `4node-basic` respawn test — dead player's `RequestRespawn` delivers them at the gateway-resolved `Location`, not the fallback hardcoded zone.
- **State integrity:** the existing `invSessionRouteHostLive` invariant stays; add `invSpawnLocationInBounds` check on every `OnEnter` transition (logs violations under `InvariantLog`, panics under `InvariantPanic`).

## Migration footprint

Code touched:

- `pkg/coords/` — delete `spawn.go`, add `location.go`, add `location_test.go`.
- `pkg/mmokit/mmokit.go` — swap facade aliases.
- `pkg/universe/coordinator.go`, `gateway.go`, `spawn_resolver.go`, `cell_bridge_impl.go`, `message.go`, `world_base.go` — consume the new types, add `SpawnAtLocation`, remove `anyCellID` fallback, extend `SpawnTransfer` + `MsgPlayerAssignment`.
- `pkg/engine/player_manager.go` — add `SpawnLocation` field to `PlayerSession`.
- `cmd/server/main.go:367-375` — update `SetSpawnResolver` signature.
- `internal/game/` — any resolver/respawn hooks.
- `examples/4node-basic/main.go` — `DefaultSpawn: mmokit.Location{X: CellSize/2, Y: CellSize/2}`.
- `examples/4node-basic/world.go` — `spawnPlayer` becomes the 3-liner using `SpawnAtLocation`.
- `examples/4node-basic/mesh_e2e_test.go`, `examples/slither/main.go` — same.
- `pkg/universe/universe_test.go`, `pkg/universe/cluster_fixture*_test.go` — test fixtures.
- `pkg/universe/*meshpb*` — `SpawnTransfer` wire format extension (add `Location` proto field).

No backward-compat aliases. Every caller is updated in-place.

## Open questions (decide at implementation time)

- **Proto field shape for `Location` in meshpb.** Three options: a nested `Location` message, a pair of `float32` pos + separate `facing`/`tag`, or fold the fields directly into `SpawnTransfer` / `MsgPlayerAssignment`. Recommendation: define `meshpb.Location` once, reuse in every message that carries it. Decision deferred to the plan.
- **Precision of `Facing`** — radians in `float32` is plenty; a `qangle` encoding like the replication system uses is over-engineered for an infrequent control message. Plain `float32` on the wire.
