# Player Spawn Routing — Standalone Gateway Resolves Saved Positions via Coordinator RPC

## Context

In `--mode=all` (single-process) and `--mode=coordinator,host`, login-time spawn routing works: the embedded gateway calls a game-provided `PlayerRouter(username) → hostID` closure that reads `playerDB.Get(username)` and resolves the saved cell via `coord.NodeAtPosition`. New players fall back to `gameCfg.StationCell`.

In **pure-gateway mode** (`--mode=gateway --coordinator-addr=…`) the gateway has no `playerDB` (it's only loaded when `needsGameState=true`, i.e., `RoleHost`/`RoleNode`) — so the existing router would crash on `playerDB.Get`. Today the gateway either:
- Has `c.playerRouter == nil` and falls back to the cached topology's first cell ([gateway.go:262](pkg/universe/gateway.go#L262)), losing the player's saved position
- Crashes if a game does set a router that touches `playerDB`

S9's spec line "Player routing respects StationCell" calls out this gap. The fix: give the gateway a way to ask the coordinator (which has `playerDB`) "where should user X spawn?" via a new MeshControl RPC. Embedded coordinators short-circuit to a direct in-process call so the single-process case has zero RPC overhead.

This is the third S9 follow-up after the cmdsys foundation. Marketplace cross-host is deferred — it'll be reworked into its own service with its own Postgres tables in a separate plan.

---

## Goals

- **Standalone gateways resolve saved spawn positions** via a new `MeshControl.ResolveSpawn` RPC. Lookup runs on the coordinator (the only process with `playerDB`), result returns to the gateway, gateway dispatches `PlayerAssignment` to the resolved cell.
- **Embedded coordinators stay zero-RPC.** When the gateway is in the same process as the coordinator (which has the resolver registered), the lookup runs inline — no proto serialization, no stream send.
- **Replace `PlayerRouter` with `SpawnResolver`.** The richer return (`cellID + worldX + worldY`) carries the position, not just the node — the gateway needs the cell, and the destination cell needs the position to spawn the entity correctly. No backward-compat shim; both consumers (4node-basic, space game) migrate at once.
- **Fallback preserved.** If the coordinator has no resolver registered (4node-basic, simple games), the gateway falls back to `gameCfg.StationCell` via the cached topology — current behavior.

## Non-goals

- **Marketplace cross-host.** Deferred to a separate plan that reworks marketplace into a standalone service.
- **Caching saved positions on the gateway.** Every login goes through the resolver; the lookup is cheap (in-memory map on coordinator) and login is rare relative to in-game traffic.
- **Authoritative login on coordinator.** LoginHandler still runs on the gateway. Only the spawn-cell lookup goes coordinator-side.
- **Pre-staged spawn from external services.** Out of scope.
- **Pure-coordinator deployment** (no host, no node). The current architecture already requires at least one cell-hosting process for `playerDB` to load. Unchanged.

---

## Architecture

### New API: `Coordinator.SetSpawnResolver`

Replaces the existing `SetPlayerRouter`. Signature:

```go
// SpawnResolver resolves a username's login spawn position in absolute
// world coordinates. Called once per login, on the process that owns the
// playerDB (typically the coordinator). Returns ok=false to indicate
// "unknown user / no saved position" — the gateway then uses
// Config.DefaultSpawnX/Y instead.
//
// The resolver is deliberately topology-blind. It returns world coords
// only; the gateway calls coord.CellAtPosition(x, y) at dispatch time to
// find the current owning cell. Split/merge between the resolver call and
// dispatch is handled naturally — the player lands in whatever cell owns
// (x, y) at dispatch time.
type SpawnResolver func(username string) (worldX, worldY float32, ok bool)

func (c *Coordinator) SetSpawnResolver(r SpawnResolver)
```

Registered on the coordinator process (the one with `playerDB` loaded). The space game's `main.go` calls it from inside the `needsGameState` block, same place `SetPlayerRouter` is called today.

### New MeshControl RPC

Two new oneof variants on the existing `MeshControl` bidi stream:

```proto
// HostMessage additions (next free slot is 17 — C3 used 15, 16):
//   ResolveSpawn  resolve_spawn  = 17;  // gateway → coord

// CoordMessage additions (next free slot is 18 — C3 used 15, 16, 17):
//   SpawnResolved spawn_resolved = 18;  // coord → gateway

message ResolveSpawn {
  uint64 request_id = 1;
  string gateway_id = 2;
  uint32 conn_id    = 3;
  string username   = 4;
}

message SpawnResolved {
  uint64 request_id = 1;
  bool   ok         = 2;  // false = unknown user / no saved position
  float  world_x    = 3;
  float  world_y    = 4;
  string error      = 5;  // populated when the RPC itself failed, not ok=false
}
```

No `cell_id` field — the gateway resolves the cell locally via `coord.CellAtPosition(x, y)` after the RPC returns. Keeps topology lookups out of the resolver and out of the wire format; natural handling of split/merge between resolver call and dispatch.

`request_id` follows the same `commandOrchestrator` / `cellTransferOrchestrator` pattern: gateway allocates a uint64, stores a `chan *SpawnResolved` in a pending map, sends, awaits with a 2s deadline.

### Login-flow change (gateway side)

In [`gateway.processLogin`](pkg/universe/gateway.go#L246), the current call to `c.playerRouter(username)` becomes a call to a new internal `g.resolveSpawn(ctx, username)` that returns `(worldX, worldY float32)`:

1. **If embedded coordinator** (gateway and coordinator are in the same process AND coordinator has a resolver registered): call the resolver inline. Zero serialization, zero RPC.
2. **If standalone**: send `ResolveSpawn` on the MeshControl client stream, await `SpawnResolved` with a 2s deadline.
3. **If the resolver is absent, returns `ok=false`, or the RPC fails**: use `Config.DefaultSpawnX` / `Config.DefaultSpawnY`. Zero values default to `(coords.CellSize/2, coords.CellSize/2)` — the center of base cell `0_0`.

After `resolveSpawn` returns world coords, the gateway calls `coord.CellAtPosition(worldX, worldY)` to find the cell that currently owns that point, and dispatches `PlayerAssignment` to it. If a cell split commits between the resolver call and dispatch, the player lands in whichever child owns the coord — no special handling needed.

World coords are passed through to the destination cell via `PlayerAssignment.data` (existing opaque field) so the game-side spawn can use them to place the entity. Today the destination spawn position is implicit (saved `PlayerData` on the host or cell center); making it explicit at the wire level removes the "two sources of truth" surprise where the coordinator's router returned one position and the host's `PlayerData` said another.

### Coordinator side

In [`pkg/universe/coordinator.go`](pkg/universe/coordinator.go), add:

- `Coordinator.spawnResolver SpawnResolver` field (nil by default)
- `Coordinator.SetSpawnResolver(r)` setter
- `Coordinator.handleResolveSpawn(req *meshpb.ResolveSpawn)` — invoked by the `MeshControl` server when a `ResolveSpawn` arrives from a gateway. Calls the resolver, builds `SpawnResolved`, sends back via `sendCoordMessageToGateway`. Returns `ok=false, error="no resolver registered"` if the coordinator never registered one.

### `Config.DefaultSpawn` field + `SpawnPoint` type

```go
// SpawnPoint is an absolute world-space coordinate. Used for the login
// fallback position and (post-v1) any other game-defined anchor point
// that must survive cell split/merge without re-computation.
type SpawnPoint struct {
    X, Y float32
}

// WorldCenterOfCell returns the center of the given base-cell coordinate
// as a SpawnPoint. Survives any split depth: if cell (1, 1) has been
// split to depth-2 at spawn time, the returned world point still lands
// in whichever grandchild currently owns it.
func WorldCenterOfCell(cellX, cellY int32) SpawnPoint {
    return SpawnPoint{
        X: float32(cellX)*CellSize + CellSize/2,
        Y: float32(cellY)*CellSize + CellSize/2,
    }
}

type Config struct {
    // ...existing fields...

    // DefaultSpawn is the world-space login spawn point used when no
    // SpawnResolver is registered, the resolver returns ok=false, or the
    // RPC fails. Absolute world coords — topology-independent: if the
    // cell that contains this point has been split at spawn time, the
    // gateway resolves the current owning child via CellAtPosition.
    DefaultSpawn SpawnPoint
}
```

Using a struct instead of two bare fields prevents the "set X, forget Y" class of bug. The `WorldCenterOfCell` helper returns the struct directly for the common case.

Usage:

```go
// Space game — center of configured station cell:
coordCfg.DefaultSpawn = mmokit.WorldCenterOfCell(gameCfg.StationCell.CellX, gameCfg.StationCell.CellY)

// 4node-basic — center of base cell 0_0:
coordCfg.DefaultSpawn = mmokit.WorldCenterOfCell(0, 0)

// Explicit world coord (e.g. a safe clearing in the middle of the map):
coordCfg.DefaultSpawn = mmokit.SpawnPoint{X: 1500, Y: 500}
```

**Topology independence in practice**: under a depth-1 split of cell `0_0` with `CellSize=2000`, the point `(1000, 1000)` falls into child `{X:1, Y:1, D:1}` (standard `minX ≤ x < maxX` bounds). Under depth-2, grandchild `{X:2, Y:2, D:2}`. Under merge, back to `{X:0, Y:0, D:0}`. The dev's `DefaultSpawn` never changes across any of these — only the gateway's internal `CellAtPosition` lookup does.

Zero value `SpawnPoint{}` = `(0, 0)` = corner of cell `0_0`. Legitimate but rarely what a game wants — every game sets `DefaultSpawn` explicitly.

### `cmd/server/main.go` migration

Replace the `coord.SetPlayerRouter(...)` block with:

```go
coord.SetSpawnResolver(func(username string) (worldX, worldY float32, ok bool) {
    pdata := playerDB.Get(username)
    if pdata == nil || !pdata.HasSave {
        return 0, 0, false  // gateway falls back to DefaultSpawnX/Y
    }
    worldX = float32(pdata.CellX)*coords.CellSize + pdata.X
    worldY = float32(pdata.CellY)*coords.CellSize + pdata.Y
    return worldX, worldY, true
})

// Fresh-player default: center of the station cell.
coordCfg.DefaultSpawnX = float32(gameCfg.StationCell.CellX)*coords.CellSize + coords.CellSize/2
coordCfg.DefaultSpawnY = float32(gameCfg.StationCell.CellY)*coords.CellSize + coords.CellSize/2
```

No `CellAtPosition` lookup in the resolver — the gateway does that after the RPC returns. The existing `Coordinator.NodeAtPosition(worldX, worldY) hostID` stays; we add a sibling `Coordinator.CellAtPosition(worldX, worldY) cellID` that walks the same cellToHostMap to return the cell key instead of the host ID. Both are needed: `NodeAtPosition` for pre-PlayerAssignment host routing, `CellAtPosition` for the cell destination on the `PlayerAssignment` frame.

### `examples/4node-basic/main.go` migration

Currently uses `mmokit.DefaultPlayerRouter(coord, 0, 0)`. Replace with no resolver (all new connections are "fresh") and set the default spawn explicitly:

```go
coordCfg.DefaultSpawnX, coordCfg.DefaultSpawnY = mmokit.WorldCenterOfCell(0, 0)
```

Lands every connection at `(CellSize/2, CellSize/2)` — the center of cell `0_0` regardless of split depth at connection time.

### Removing `SetPlayerRouter`

Delete `Coordinator.SetPlayerRouter`, the `PlayerRouter` type, and `DefaultPlayerRouter`. Per "no backward compat" they're dead weight after the migration.

---

## Critical files

**New:**
- `pkg/universe/spawn_resolver.go` — `SpawnTarget` struct, `SpawnResolver` type, `Coordinator.SetSpawnResolver`, `handleResolveSpawn`, the gateway-side `g.resolveSpawn(ctx, username)` helper, the request-id orchestrator (`spawnOrchestrator` mirroring `commandOrchestrator`).

**Modified:**
- `proto/meshpb/mesh.proto` — `ResolveSpawn` (HostMessage slot 17), `SpawnResolved` (CoordMessage slot 18). Run `just proto`.
- `pkg/universe/coordinator.go` — add `spawnResolver` field, `SetSpawnResolver` setter, `CellAtPosition(worldX, worldY) string` helper. Delete `SetPlayerRouter`, `PlayerRouter` type, `DefaultPlayerRouter`.
- `pkg/universe/gateway.go` — `processLogin` calls `g.resolveSpawn` instead of `c.playerRouter`. Add fallback to `Config.DefaultSpawnCell`.
- `pkg/universe/login.go` — drop `PlayerRouter` type if it lives here.
- `pkg/universe/mesh_control_server.go` — handle inbound `ResolveSpawn`, call coord resolver, send `SpawnResolved`.
- `pkg/universe/mesh_control_client.go` — send `ResolveSpawn` from gateway, route inbound `SpawnResolved` to spawn orchestrator.
- `cmd/server/main.go` — `SetSpawnResolver` block replacing `SetPlayerRouter` block; set `coordCfg.DefaultSpawnCell = gameCfg.StationCell.String()`.
- `examples/4node-basic/main.go` — drop the `DefaultPlayerRouter` call, set `DefaultSpawnCell`.
- `examples/slither/main.go` — same migration if it uses `SetPlayerRouter` (likely does for `tp` to spawn).
- `pkg/mmokit/mmokit.go` — re-export `SpawnTarget`, `SpawnResolver`. Remove `PlayerRouter`, `DefaultPlayerRouter`.
- `pkg/universe/login.go` `Config` — add `DefaultSpawnCell string` field.

---

## Verification

```bash
just proto                          # regenerate mesh.pb.go
go vet ./...
go build ./...
go test -count=1 ./pkg/universe/... ./pkg/cmdsys/... ./pkg/engine/...
go test ./...                       # whole repo green, including 31s e2e + 47s universe
```

**New unit tests:**
- `pkg/universe/spawn_resolver_test.go` — registration + lookup + nil-resolver returns ok=false.
- `pkg/universe/spawn_resolver_meshcontrol_test.go` — under `TestHosts`, gateway sends `ResolveSpawn`, coordinator runs resolver, response round-trips, gateway dispatches `PlayerAssignment` to the resolved cell. Mirrors the `cmdsys_meshcontrol_test.go` shape.
- `pkg/universe/spawn_resolver_test.go` — fallback path: resolver returns ok=false, gateway uses `Config.DefaultSpawnCell`. Empty `DefaultSpawnCell` falls back to `cachedTopology.anyCellID()`.

**Manual smoke** (after the marketplace + `just distributed-space` recipes land):
- `--mode=coordinator,host` + `--mode=gateway` on separate processes; log in as a known player with a saved position outside the station; verify they spawn at the saved position, not at station.
- Log in as a fresh player; verify they spawn at `gameCfg.StationCell`.
- Log in to 4node-basic with no saved position; verify they spawn at "cell_0_0".

---

## Risks & mitigations

- **2s RPC deadline too tight under load.** If the coordinator is busy serializing a cell split, the resolver call could exceed 2s. Mitigation: the resolver runs OFF the game loop (it just reads the in-memory `playerDB` map under its own RWMutex), so even busy coordinators respond instantly. If this surfaces in practice, bump the deadline to 5s — same as everything else in cmdsys.
- **Login latency adds ~1ms gRPC hop in distributed mode.** Mitigation: this is one-time-per-login; in-game gameplay is unaffected. The embedded fast-path keeps `--mode=all` at zero overhead.
- **Stale resolver result after concurrent player movement.** A player who just crossed cells while logging in could resolve to the old cell. Mitigation: same window exists today with `SetPlayerRouter`. The destination cell's `PlayerAssignment` handler triggers the standard handoff if the player's coords are out of bounds.
- **Resolver registered on wrong process.** If a deployer runs `--mode=coordinator` standalone (no host/node) and the game's setup tries to register the resolver, `playerDB` is nil and the closure crashes. Mitigation: deployment doc note + a check in `main.go` that wraps `SetSpawnResolver` in `if needsGameState` (matches existing `SetPlayerRouter` placement).
- **`DefaultSpawnCell` not set, resolver returns false.** Gateway falls back to `cachedTopology.anyCellID()` — works but unpredictable. Mitigation: log a warning on first such fallback so operators notice.
- **Renaming `SetPlayerRouter` breaks examples + downstream consumers.** Mitigation: this branch (`feature/distributed-mesh`) hasn't shipped; the rename is internal. Both `examples/` consumers are migrated in the same commit.

---

## Out of scope (for this plan)

- **Marketplace cross-host.** Reworked into its own service with its own Postgres schema in a follow-up plan.
- **`just distributed-space` recipe.** Separate plan; depends on this one for login routing.
- **Distributed smoke test.** Separate plan; covers login + marketplace + admin commands end-to-end.
- **Multiple gateways behind a load balancer.** The resolver RPC works for any gateway, but session affinity / balancer config is its own design.
- **Pre-loaded spawn cache on gateway.** Could be done if RPC latency becomes a real problem; deferred until measurement says so.
- **Resolver-driven first-time spawn variance** (different spawn points for different player types). The current `gameCfg.StationCell` is one fixed point. If we want per-class spawn, the resolver's `ok=true` branch can return any target — the wiring's already there, just unused for fresh players. No work needed in this plan.

---

## Implementation order

Single commit, one feature. Internal, isolated, easy to revert. ~600 LOC total.

1. Add proto messages, run `just proto`.
2. Add `SpawnTarget` / `SpawnResolver` types + `Coordinator.SetSpawnResolver` + `Coordinator.CellAtPosition` helper.
3. Add `Config.DefaultSpawnCell`.
4. Add `spawnOrchestrator` (request-id pending map, mirrors commandOrchestrator).
5. Wire `MeshControl` server-side handler + client-side send.
6. Replace `gateway.processLogin`'s `playerRouter` call with `g.resolveSpawn`.
7. Delete `SetPlayerRouter`, `PlayerRouter`, `DefaultPlayerRouter`.
8. Migrate `cmd/server/main.go` and `examples/*/main.go`.
9. Tests.
10. Run `go test ./...`, verify 31s e2e + 47s universe still pass.
11. Commit.
