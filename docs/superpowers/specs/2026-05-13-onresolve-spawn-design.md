# OnResolveSpawn — single owner for player spawn location

Status: Draft → ready for implementation plan
Date: 2026-05-13

## Problem

Spawn-location resolution today is split across two unrelated knobs:

- `Config.DefaultSpawn coords.Location` — a static, global fallback set on the process Config.
- `Process.SetSpawnResolver(func(username) (Location, bool))` — an optional game-registered callback that can short-circuit the fallback when it returns `ok=true`.

The gateway's `resolveSpawn` (`pkg/universe/spawn_resolver.go`) combines them: call the resolver, take its location if `ok`, otherwise return `DefaultSpawn`. `cell_bridge_impl.go::RequestRespawn` does the same combination for in-world respawn.

Two problems:

1. **DefaultSpawn is awkward as a Config field.** It's a per-game world coordinate that has nothing in common with the rest of the engine-level Config (cell topology, tick rate, AoI radius). Setting `DefaultSpawn: mmokit.Location{X: CellSize*0.85, Y: CellSize*0.85}` in `mmokit.New` puts a game-policy decision in the same struct as engine wiring.
2. **The decision is fragmented.** A game that needs "DB-saved location if any, else faction-based spawn, else fallback" splits its logic across the resolver callback AND the static field. The framework decides which one wins, not the game.

## Goal

One game-owned callback owns the entire spawn-location decision. The gateway uses the returned location for cell routing. The engine has no static fallback configuration.

## Design

### API

Replace `Config.DefaultSpawn` + `SetSpawnResolver` with a single registration on `*Process`:

```go
process.OnResolveSpawn(func(s *mmokit.PlayerSession) mmokit.Location {
    if loc, ok := myDB.LoadSpawn(s.UserID); ok {
        return loc
    }
    return mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85}
})
```

Signature: `type SpawnResolver func(*PlayerSession) coords.Location`

Differences from today's resolver:

- Takes `*PlayerSession` (gives access to `UserID`, `Username`, future per-session fields) instead of bare `username string`.
- Returns one `Location`, no `(Location, bool)`. The game internally decides fallback. No engine-side fallback path.
- Renamed to `OnResolveSpawn` to match the existing `OnPlayerJoin` / `OnConsoleReady` verb-noun pattern on `*Process`.

### No-handler default

If no `OnResolveSpawn` is registered, the engine defaults to the center of cell `(0, 0)`:

```go
coords.Location{X: cfg.CellSize / 2, Y: cfg.CellSize / 2}
```

Computed once at `Build()` time from `Config.CellSize`. No panic, no startup error — a freshly-scaffolded game runs without touching the spawn API.

### Pipeline behaviour

Unchanged in shape; simpler in body:

1. Gateway receives login.
2. Gateway calls `resolveSpawn(session)`:
   - If `g.coord != nil` (embedded mode) and a resolver is registered → call inline, return Location.
   - Standalone gateway → `ResolveSpawn` RPC to coordinator; coord runs the resolver and returns Location.
   - No resolver registered → return the cell (0,0) center default.
3. Gateway calls `CellAtPosition(loc.X, loc.Y)` to pick the owning cell.
4. Gateway dispatches `PlayerAssignment{ConnID, SpawnLocation: loc, ...}` to that cell.
5. Cell fires `OnPlayerJoin`. `session.SpawnLocation` carries the resolved location through unchanged.

`cell_bridge_impl.go::RequestRespawn` (post-death respawn) goes through the same resolver.

### Reconnect routing

Unchanged. The coordinator's reconnect detection (`activeUsers` index for embedded mode, `SpawnResolved` piggyback fields for standalone) still overrides the resolver's location when the user has a lingering active session. Reconnect is a routing question, separate from the spawn-location question, and lives in coord-side code rather than in the game's resolver.

### Why a callback, not a System

The user asked whether this might fit as a System. It doesn't:

- Systems run per-tick on cells. Spawn resolution fires once per login on the gateway, before any cell is involved.
- A Service (auth/chat-shaped) carries a lifecycle, event bus, and registration footprint. A spawn resolver needs none of that.

A plain registered handler matches the existing `OnPlayerJoin` / `OnConsoleReady` shape and is the minimum that fits.

## Touch list

- **`pkg/universe/coordinator.go`** — delete `Config.DefaultSpawn` field + its doc block (around line 218–222).
- **`pkg/universe/spawn_resolver.go`** —
  - Change `SpawnResolver` signature: `func(*PlayerSession) coords.Location`.
  - Rename `SetSpawnResolver` → `OnResolveSpawn` on `*Process`.
  - Rewrite `resolveSpawn`: no more `defaultSpawn` branch; if no resolver registered call the cell-(0,0)-center default.
- **`pkg/universe/gateway.go`** —
  - Delete the `defaultSpawn coords.Location` field on `Gateway`.
  - Delete the `cfg.DefaultSpawn`-reading copy in the Gateway constructor.
  - Update standalone-mode fallback in `resolveSpawn` to compute the cell-(0,0)-center default from `cfg.CellSize`.
- **`pkg/universe/cell_bridge_impl.go`** — `RequestRespawn` reads through the unified resolver (no more `b.coord.cfg.DefaultSpawn` access).
- **`pkg/universe/coordinator.go`** — wherever `cfg.DefaultSpawn` is read (e.g. the construction of `*Gateway` at line ~2006), replace with the resolver call / cell-center default.
- **`pkg/mmokit/mmokit.go`** — facade re-export: `OnResolveSpawn` becomes a method on the `*Process` alias. Update the doc comment that mentions `Config.DefaultSpawn`.
- **`pkg/universe/universe_test.go`** — tests that set `c.cfg.DefaultSpawn` and assert on it switch to registering a resolver (or rely on the cell-(0,0)-center default).
- **`pkg/universe/gateway_test.go`** — comments referencing `DefaultSpawn` updated; tests verifying the static-fallback path now exercise the cell-center default.
- **`examples/4node-basic/main.go`** — replace `DefaultSpawn:` Config entry with `process.OnResolveSpawn(...)` call.
- **`examples/4node-basic/mesh_e2e_test.go`** — same replacement (two callsites).
- **`cmd/server/main.go`** — replace the space-game's `coordCfg.DefaultSpawn = ...` with an `OnResolveSpawn` registration. Space game's `PlayerDB` resolver already does the DB lookup; collapsing both into one callback is the win.
- **`internal/game/entity_station.go`** — comment cleanup (the file references `Config.DefaultSpawn`).
- **CLAUDE.md** — the line "Spawn position is pinned via `Config.DefaultSpawn = ...` in `main.go`" updates to describe the resolver.

## Test plan

- Existing `TestSpawn_DefaultLocation` (or whichever spawn test exercises the default path) updates to register no resolver and assert players land at cell-(0,0) center.
- New test: `TestOnResolveSpawn_GameOverride` — register a resolver returning a non-default location, assert `session.SpawnLocation` matches.
- New test: `TestOnResolveSpawn_ReceivesSession` — resolver gets called with non-zero `UserID` and `Username`.
- 4node-basic `mesh_e2e_test.go` continues to pass with the resolver-registered form.
- `just lint-no-ark`, `go vet ./...`, full `go test ./...` clean.

## Out of scope

- Reconnect routing changes (kept identical).
- Multi-spawn-zone game-side helpers (faction zones, party respawn). The resolver gives games the seam to build that themselves.
- Service-shaped registration (`RegisterSpawnService`). Plain callback is sufficient.

## Risk

- **Standalone gateway path:** the gateway holds a cached `defaultSpawn` (line 72) that's read when the standalone-mode RPC fails. After the refactor the cached value is the engine cell-center default (or unused if the RPC succeeds). Need to verify the standalone test path still works without `Config.DefaultSpawn` set.
- **PlayerSession at resolve time:** the gateway calls the resolver before the cell creates the canonical `PlayerSession`. The session passed in must already have `UserID` + `Username` populated. Today these are set by the auth flow on the gateway before login dispatch — confirm in implementation.
