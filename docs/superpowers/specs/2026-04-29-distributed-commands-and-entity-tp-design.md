# Distributed Commands & Entity-TP Design

**Status:** Spec
**Date:** 2026-04-29
**Author:** Josh Stout

## Problem

Many of the player-targeted admin commands in space-game predate the move to the cmdsys / multi-cell mesh / multi-process architecture and no longer work correctly:

- `player.tp <user> <x> <y>` interprets `<x>,<y>` as **local cell coordinates**, so teleporting a player who lives in cell `{1,2}` to "100,100" doesn't move them out of `{1,2}` — it moves them to `(100,100)` *inside* that cell. Teleporting across cells is impossible.
- `player.tpto <user> <target>` resolves `<target>` only on the executing host's local cells — works inside one cell, fails for any other configuration.
- `player.list` and `chat.broadcast` use `RouteLocal`, so on a pure-coordinator pane they see only the coord-local `ActiveUsers()` map, and on a host pane they see only that host's cells.
- `debug.npcs` walks only this process's cells; `debug.spawn_npcs` requires the player be on this host.
- `player.damage`'s netID branch passes a nil `*GameWorld` to `ExecOnLoop` — broken.
- No player command has an offline branch: `player.tp <offline_user> <x> <y>` returns `ErrRouteNoOwner` and gives up.

Beyond the bugs, there is no engine primitive for moving an entity to an arbitrary world location. `HandoffDriver` only fires on natural boundary crossings into *neighbor* cells (where replicas already exist). Whole-cell `MIGRATE` exists in `CellTransfer` but is cell-granular, not entity-granular. Long-distance, possibly cross-host TP has no code path today.

## Goals

1. Every command works correctly regardless of which pane (coordinator / host / gateway) the operator runs it from.
2. Player commands work for offline players, not just currently-online ones, where the operation has a meaningful persisted form.
3. Entity-level operations (especially TP) work cross-cell and cross-host without per-game special-casing.
4. The host-side entity-move code path is unified: natural boundary crossings and arbitrary-location TPs converge through the same `HandoffDriver` + commit-tick protocol.

## Non-Goals

- **Chat command rework.** `chat.broadcast` is being deleted, not rewritten — chat-as-a-service is a separate spec. Operators who need a server-wide announcement during the gap can still use the existing event-stream tools.
- **HTTP/gRPC client SDK** beyond what `/commands` JSON-schema already exposes.
- **Authentication/authorization changes.** Existing capability strings (`player.tp`, `entity.spawn`, etc.) keep gating; no new RBAC model.
- **Game-specific entity damage commands.** Damage/heal/kill stay player-targeted (online-only) for this spec. If the game later wants to damage non-player entities, that lives in a game-specific namespace (e.g. `combat.damage`) and is added separately.

## Scope

**Move to engine** (`pkg/universe/builtins_*.go`, surfaced via mmokit facade):

- `entity.spawn`, `entity.despawn`, `entity.list`, `entity.tp`
- `player.tp`, `player.tpto`, `player.list`, `player.info`, `player.kick`

**Stay in space-game** (`internal/game/commands/`), rewritten on top of the new resolver helper:

- `player.heal`, `player.kill`, `player.damage` — online-only (touch live `Health`/`Shield` components)
- `player.give`, `player.currency` — online + offline (touch persisted `PlayerData.Cargo` / `PlayerData.Currencies`)

**Delete** (subsumed or deferred to other work):

- `chat.broadcast` — chat-as-a-service redesign coming
- `debug.npcs` — subsumed by `entity.list --kind=npc`
- `debug.spawn_npcs` — subsumed by `entity.spawn npc <count> <x> <y>`

## Design

### Architecture overview

Three layers change:

1. **Engine primitive** (`Stage.MoveEntityTo`) — single entry point that mutates an entity's location, handling same-cell / cross-cell / cross-host transparently. Subsumes today's TP path and unifies it with boundary handoff.
2. **Routing layer** (`RoutePlayerHomeOrOwner` + `ResolvePlayerTarget`) — one new route kind that resolves online players to their owner host and offline players to a stable DB-bearing host. One helper that hands handlers exactly one of `{Online live session, Offline persisted data, NotFound}`.
3. **Command surface** (`builtins_entity.go`, `builtins_player.go`) — engine-registered commands that compose the primitive and routing layer; surfaced through the mmokit facade so any game gets them by registering builtins.

### Unified entity-move primitive

#### `Stage.MoveEntityTo`

```go
package mmokit // facade re-export from pkg/universe

type MoveOpt func(*moveOpts)

// MoveBypassCooldown skips the HandoffCooldownTicks anti-thrash window.
// Set on explicit teleport calls (admin TP, scripted plot moves) so the
// admin can rapid-fire TP a player without rate-limiting. Boundary
// crossings keep the cooldown.
func MoveBypassCooldown() MoveOpt { ... }

// MoveEntityTo schedules a move of the given entity to absolute world
// coordinates. Same-cell → in-place position update. Different cell on
// same host → loopback handoff. Different host → MeshControl handoff.
// In every case the destination converges at a cluster-tick commit, so
// netIDIndex / replication / session-route invariants stay intact.
//
// Must be called on the cell's loop goroutine (or from a cmdsys
// handler that already runs on-loop via OnLoop).
func (s *Stage) MoveEntityTo(e ecs.Entity, worldX, worldY float32, opts ...MoveOpt) error
```

**Same-cell branch.** Computes `destCellID := coords.CellAtPosition(worldX, worldY)`. If `destCellID == s.CellID()`, the call updates `Position`, `CellCoord`, zeros `Velocity` and `MoveTarget.Active`, and returns. No handoff machinery is involved. This is the fast path for short hops within a cell.

**Cross-cell branch.** Stamps the entity with the new coordinates (so the destination receives the correct world position, not the source's pre-move position), then enqueues a synthetic crossing event onto the same queue the boundary detector feeds. `HandoffDriver.Tick()` drains it next tick like any other crossing.

#### Generalized `HandoffDriver`

The existing driver assumes (a) the destination is a Moore-neighbor of the source and (b) a replica already exists at the destination from prior border AoI. We relax both assumptions:

1. **Neighbor precondition removed.** The crossing-event queue accepts any `destCellID` resolvable via `Coordinator.HostForCellID`. Long-distance TPs land on the queue identically to neighbor crossings.
2. **Replica-pre-existence becomes optional.** The destination side already has a "spawn from `transfer_blob` if no replica yet" path used by `Cell.drainPendingPromotes` (see `cell.go:412`). We make that the default; the replica-already-present optimization stays as a fast path when applicable.
3. **Anti-thrash cooldown made opt-out.** `HandoffCooldownTicks = 20` keeps applying to boundary-crossing events. Explicit TPs go in with `MoveBypassCooldown` and skip the cooldown lookup. The cooldown map (`HandoffDriver.lastHandoff`) is unchanged in shape; just the lookup branches on the bypass flag.

The hard-cut commit protocol is unchanged: `commitTick = currentClusterTick + HandoffLeadTicks` (≈ 100 ms at 20 Hz). Source demotes `Live → Replica` at end-of-tick `commitTick − 1`; destination promotes `Replica → Live` (or spawns from `transfer_blob`) at start-of-tick `commitTick`. The existing `MERGE drain freeze` (`Stage.drainingForMerge`) and other in-flight-transfer protections continue to apply.

#### Cross-host transport

No protocol change. The existing `meshpb.Handoff` message already carries the full transfer blob and `commitTick`. The bridge selects loopback (same host) or MeshControl gRPC (cross-host) at send time. Long-distance TPs route through whichever bridge owns the destination cell — the same code path the existing handoff already uses.

#### Player session follow

When the moved entity has an attached `PlayerSession`, the existing handoff machinery already remaps the session at commit:

- The source notifies the coordinator via `HostMessage.PlayerMigrated`.
- The coordinator atomically bumps the session's epoch in `sessionRoutes`.
- The coordinator dispatches a targeted `CoordMessage.UpstreamSwitch` to the gateway holding that session.
- The gateway updates its local session record; subsequent client input routes to the new authoritative host.

TP gets all of this for free.

#### Failure modes

- **Destination cell unowned.** `Coordinator.HostForCellID(destCellID)` returns "". `MoveEntityTo` returns an explicit error; for partition-monitor edge cases (cell mid-split), it retries up to 100 ms before giving up.
- **Source entity removed between submit and commit.** The existing demote drain handles entity-removed-during-handoff: it drops the pending demote when the source entity no longer exists. No new logic needed.
- **Target host crashes during handoff.** Existing crash-reassignment covers this — the entity respawns on a surviving host's cell during the next reassignment cycle, not during the failed TP. The TP returns success at submit time but the player ends up at the post-reassignment cell.
- **Source and destination cells held by the same `MERGE` operation.** `Stage.drainingForMerge` is set on the donor; new crossings are dropped, including TPs. The TP returns an error with `cell mid-merge` detail.

### Routing layer

#### `RoutePlayerHomeOrOwner`

New constant added to `pkg/cmdsys/command.go`:

```go
const (
    RouteLocal RouteKind = iota
    RouteCoordinator
    RouteAllHosts
    RoutePlayerOwner
    RouteEntityOwner
    RouteAllGateways
    RouteSpecificHost
    RouteSpecificCell
    RoutePlayerHomeOrOwner // NEW: online → owner host; offline → DB-bearing host
)
```

The `meshRouteResolver` (in `pkg/universe/cmdsys_resolver.go`) handles it as follows:

1. Read the `Username` field from args via the existing reflection helper.
2. `hostID := r.coord.ActiveUserHost(username)` — if non-empty, return it. Online → owner host.
3. Otherwise fall back to `r.coord.PickDBHost()` (new method below). If non-empty, return that host. Offline → stable DB host.
4. If both fail, return `ErrRouteNoOwner`.

`Coordinator.PickDBHost()` returns the lexicographically first live host whose process advertises a `PlayerRepository`-bearing role. The advertisement comes from the existing `RegisterHost` handshake — we add a `HasPlayerDB bool` field to the registration message (currently unused). Coordinator-only processes never advertise it; host and `all`-mode processes do when they actually opened a Postgres connection.

The lex-first choice is deterministic, so the same offline command run twice lands on the same host — important for audit clarity and for the rare case where a write conflict could matter. If the chosen host crashes mid-command, the dispatcher's existing transport timeout fires and the operator retries; the next pick is whoever remains.

#### `ResolvePlayerTarget` helper

```go
package mmokit

type PlayerTarget struct {
    Username  string
    GameWorld *GameWorld   // nil if offline
    Online    *PlayerSession // nil if offline
    Offline   *PlayerData    // nil if online (or unknown)
    DirtyMark func()         // call after mutating Offline; no-op if Online
}

// ResolvePlayerTarget gives a handler exactly one branch.
// Caller must check (Online != nil) vs (Offline != nil) vs neither.
func ResolvePlayerTarget(env *cmdsys.Env, username string) PlayerTarget
```

The helper reads the local process handle from `env.Local`. Because `pkg/cmdsys/` is a leaf package and cannot import `pkg/universe/` without a cycle, we add a small interface to `LocalContext` and let the resolver type-assert it back at the universe layer:

```go
// pkg/cmdsys/command.go
type LocalContext struct {
    Process LocalProcess // nil for unit tests
}

// LocalProcess is the minimal surface cmdsys needs from the running process.
// Implemented by *universe.Process at the universe layer.
type LocalProcess interface{ isLocalProcess() }
```

`*universe.Process` adds the unexported marker method, and `mmokit.ResolvePlayerTarget` type-asserts `env.Local.Process.(*universe.Process)`. The dispatcher populates `env.Local.Process` from a value passed at `NewDispatcher` config time. Unit tests construct dispatchers with `nil` and pass mock targets directly, bypassing the resolver.

The helper iterates local cells looking for an active session and falls back to the local `PlayerRepository` if present. Handlers reduce to:

```go
target := mmokit.ResolvePlayerTarget(env, args.Username)
if target.Online != nil {
    return mmokit.OnLoop(ctx, target.GameWorld.Engine, func() (Result, error) {
        // mutate ECS components
    })
}
if target.Offline != nil {
    target.Offline.Cargo[itemID] += qty
    target.DirtyMark()
    return Result{...}, nil
}
return nil, fmt.Errorf("player %q not found", args.Username)
```

### Engine command surface

#### `entity.*` commands (`pkg/universe/builtins_entity.go`)

```text
entity.spawn <kind> <count> <x> <y> [--radius=R]
```

Route: `RouteSpecificCell` resolved from `(x,y)` via `coords.CellAtPosition`. Spawns N entities of the given registered kind at world `(x,y)`; with `--radius`, randomizes uniformly within a disk. `<kind>` must be a name registered via `mmokit.RegisterKind[T]`. The kind's default-init is responsible for producing a valid bundle — generic spawn does not accept a custom init hook (games that need custom init build a thin wrapper command, like 4node-basic's `bot.spawn`).

```text
entity.despawn <netid>
```

Route: `RouteEntityOwner`. Marks the entity for removal on its current cell. Returns `{kind, last_world_pos, cellID, hostID}`.

```text
entity.list [--kind=X]
```

Route: `RouteAllHosts`. Each host returns its local entities (filtered by kind name if specified) as `[{netID, kind, world_x, world_y, cellID, hostID}]`. The dispatcher aggregates per-host lists into one table; the result is rendered as a `cmd:"table"` for console output.

```text
entity.tp <netid> <x> <y>
```

Route: `RouteEntityOwner`. Handler unwraps the entity from the cell's local index and calls `Stage.MoveEntityTo(e, x, y, MoveBypassCooldown())`. Returns `{netID, prev_world_pos, new_world_pos, prev_cellID, new_cellID, hostID}`.

#### `player.*` commands (`pkg/universe/builtins_player.go`)

```text
player.tp <username> <x> <y>
```

Route: `RoutePlayerHomeOrOwner`.

- Online: `target.Online.Entity` → `Stage.MoveEntityTo(e, x, y, MoveBypassCooldown())`.
- Offline: `target.Offline.CellX = floor(x / CellSize)`, `target.Offline.X = x − CellX*CellSize` (likewise for Y), `target.DirtyMark()`. Next login spawn picks the new location up via the existing `SpawnResolver`.

```text
player.tpto <username> <target_username>
```

Route: `RoutePlayerHomeOrOwner` on `username`. Handler resolves the target's world position by `InvokeInternal`-ing `player.info <target_username>` and reading its `world_pos` field. That single call covers both online (live ECS Position) and offline (persisted `PlayerData` location) targets — no separate code path needed. The result is then passed to `Stage.MoveEntityTo` with a small random offset to avoid same-tile overlap.

NetID targets are intentionally not supported — operators dealing with raw netIDs can chain `entity.list --kind=` or `player.info` to read coords, then call `player.tp <user> <x> <y>` directly. Keeps the `tpto` surface narrow.

```text
player.list [--all]
```

Route: `RouteCoordinator`.

- No flag: returns `coord.ActiveUsers()` map (online players + their host + cellID).
- `--all`: coordinator does `InvokeInternal` to a stable DB-bearing host (chosen via `PickDBHost()`) for the offline list, merges with online. Single TraceID for the whole call. The merge reconciles "online" users that appear in both lists by preferring the online row.

```text
player.info <username>
```

Route: `RoutePlayerHomeOrOwner`. Returns `{username, status, host, cell, world_pos, currency_total, cargo, bank, last_login, created_at}`. When online, world_pos comes from the live ECS Position; when offline, from `PlayerData.{CellX,CellY,X,Y}` reconstructed to world coords.

```text
player.kick <username>
```

Route: `RoutePlayerOwner` (online only by definition). Removes the session and closes the WebSocket via the gateway-side ConnMgr. Same logic as today, just relocated to engine.

### Game-side commands (`internal/game/commands/`)

Five commands remain after the cleanup. Each is rewritten on top of `mmokit.ResolvePlayerTarget`.

```text
player.damage <username> <amount>     # RoutePlayerOwner, online only
player.heal <username>                # RoutePlayerOwner, online only
player.kill <username>                # RoutePlayerOwner, online only
player.give <username> <item> <qty>   # RoutePlayerHomeOrOwner
player.currency <user> <amt> [--currency-id=X]  # RoutePlayerHomeOrOwner
```

`player.damage`'s netID branch (currently broken) is removed entirely — game-specific damage is username-only. If the space game later wants to damage non-player entities (NPCs, asteroids), that becomes a separate game-specific command in a game-specific namespace, out of scope for this spec.

`player.give` and `player.currency` gain real offline branches (today's `currency` command sort-of has one but doesn't go through a proper resolver; today's `give` doesn't have one at all).

The `Resolver` type in `internal/game/commands/resolver.go` is deleted — `ResolvePlayerTarget` replaces it. `ExecOnLoop` is also deleted; handlers use `mmokit.OnLoop` directly.

### File layout

**New engine files** (`pkg/universe/`):

```text
builtins_entity.go          entity.spawn, entity.despawn, entity.list, entity.tp
builtins_player.go          player.tp, player.tpto, player.list, player.info, player.kick
cmdsys_route_player.go      RoutePlayerHomeOrOwner branch in meshRouteResolver
player_target.go            ResolvePlayerTarget helper + PlayerTarget type
stage_move_entity.go        Stage.MoveEntityTo primitive + MoveOpt funcs
db_host_picker.go           Coordinator.PickDBHost + HasPlayerDB advertisement
```

**Modified engine files**:

- `pkg/cmdsys/command.go` — adds `RoutePlayerHomeOrOwner` to `RouteKind` enum and `String()`.
- `pkg/cmdsys/command.go` — extends `LocalContext` with the `LocalProcess` interface marker.
- `pkg/universe/cmdsys_resolver.go` — `RoutePlayerHomeOrOwner` resolution case.
- `pkg/universe/handoff_driver.go` — non-neighbor support, optional cooldown bypass.
- `pkg/universe/coordinator.go` — `PickDBHost()` + `HasPlayerDB` registration field.
- `pkg/mmokit/mmokit.go` — facade re-exports `MoveEntityTo`, `MoveBypassCooldown`, `ResolvePlayerTarget`, `PlayerTarget`, `RoutePlayerHomeOrOwner`.

**Game-side cleanup** (`internal/game/commands/`):

- **Delete:** `tp.go`, `tpto.go`, `kick.go`, `players.go`, `say.go`, `npcs.go`, `spawnnpcs.go`, `resolver.go`.
- **Rewrite:** `damage.go`, `heal.go`, `kill.go`, `give.go`, `currency.go` — each becomes ~30–50 lines using `ResolvePlayerTarget`.
- **`registry.go`** trims to 5 registrations.

**Bootstrap wiring:** the new engine builtins are registered by `RegisterBuiltins` in mmokit alongside `cell.*`, `perf.*`, `cluster.overview`, etc. They become available on every coordinator-bearing process automatically.

### Testing

**Unit tests:**

- `Stage.MoveEntityTo` — same-cell, neighbor cell, non-neighbor cell, missing destination cell, mid-merge donor cell.
- Generalized `HandoffDriver` — non-neighbor destination spawns from `transfer_blob`; cooldown bypass on explicit TP; bypass does not affect organic boundary-crossing cooldowns.
- `meshRouteResolver` — `RoutePlayerHomeOrOwner` for online (returns owner host), offline-with-DB (lex-first DB host), no-DB cluster (returns `ErrRouteNoOwner`).
- `ResolvePlayerTarget` — online, offline, not-found, and the precedence rule (online wins over offline if a player exists in both lookups during a transient session-active state).
- `Coordinator.PickDBHost` — deterministic lex-first; skips coord-only hosts; skips dead hosts.

**Integration tests** (existing harness in `pkg/universe/`):

- `TestPlayerTP_CrossCell_SameHost` — TP a player from cell `0_0` to cell `1_1` on the same host. Assert: session follows, replication continues to flow, no duplicate netID in `netIDIndex`, `commit.log` shows one `Handoff` event for the entity.
- `TestPlayerTP_CrossHost` — TP a player from a cell on host A to a cell on host B. Assert: `PlayerMigrated` fires, gateway gets `UpstreamSwitch`, client-side WebSocket stays connected through the move, `sessionRoutes` epoch bumps once.
- `TestPlayerTP_OfflineUpdatesDB` — TP an offline user. Assert: `PlayerData.{CellX,CellY,X,Y}` updated, `PlayerFlusher` flushes within 15s window or on shutdown, next login spawn lands at the new location.
- `TestEntitySpawnDespawn_RouteSpecificCell` — `entity.spawn npc 5 100 100` from coord pane lands on the correct host's cell (verified via `entity.list --kind=npc`).
- `TestPlayerList_FromCoordPane_AllFlag` — `--all` from coord pane returns merged online+offline list; assert single TraceID in audit log.
- `TestPlayerTpto_CrossHost` — source on host A, target online player on host B. Assert source ends up adjacent to target's last known position via the `player.info` round-trip.
- `TestPlayerTpto_OfflineTarget` — source online, target offline. Assert `tpto` resolves the target's persisted location via `player.info` and TPs the source there.

**Manual smoke** (new justfile target `just smoke-commands`):

1. `just distributed` (4-process tmux: coordinator + 2 hosts + gateway).
2. Spawn 50 bots with `entity.spawn bot 50 0 0 --radius=200`.
3. Connect a player; TP across cells via `player.tp <user> <world_x> <world_y>`.
4. TP the player cross-host (verify by `player.info` showing different host).
5. Kill the destination host with `host.kill <id>`; wait for reassignment.
6. Re-TP after reassignment; verify final state is consistent.

State Integrity invariants in `pkg/universe/integrity.go` continue to run at every commit step. The dev fixture's `InvariantPanic` mode (per `examples/4node-basic/main.go`) catches any latent inconsistency introduced by the changes.

### Audit & observability

Every new command routes through the existing `cmdsys.Dispatcher`, so:

- The audit log picks them up with `start` / `done` records and TraceID correlation.
- The `commit.log` ring records any handoff events triggered by `Stage.MoveEntityTo` under the existing `events:*` log categories.
- The `/commands` and `/commands/{verb}` JSON-schema HTTP endpoints expose the new commands automatically — no manual schema wiring.
- The MeshControl gRPC transport carries the new commands the same way it carries `cell.split` etc. — same byte path, same flow control.

### Capabilities

Each new verb's capability string equals its verb (e.g. capability `player.tp` gates verb `player.tp`). Operator-only by default. Existing wildcard grants in operator identities continue to work.

## Migration Plan

The implementation order minimizes "broken in flight" windows by getting the engine plumbing in first, then commands, then deleting stale code.

1. **Engine plumbing** — `RoutePlayerHomeOrOwner` route kind, `ResolvePlayerTarget` helper, `Stage.MoveEntityTo` primitive, generalized `HandoffDriver`, `Coordinator.PickDBHost`, `LocalContext.Process` extension. Unit tests cover each piece in isolation. No commands wired yet; existing space-game commands keep working unmodified.
2. **Engine commands** — register `entity.*` and `player.*` in `RegisterBuiltins`. Integration tests for cross-cell TP, cross-host TP, offline DB updates, distributed list/info.
3. **Space-game cleanup** — delete subsumed commands (`tp.go`, `tpto.go`, `kick.go`, `players.go`, `say.go`, `npcs.go`, `spawnnpcs.go`, `resolver.go`); rewrite the remaining 5 (`damage.go`, `heal.go`, `kill.go`, `give.go`, `currency.go`) on top of `ResolvePlayerTarget`. Tests cover offline `give`/`currency`.
4. **4node-basic verification** — `bot.spawn`/`bot.list`/`bot.clear` keep working unchanged. Manual smoke run.

Each step is mergeable independently — after step 1 the engine has the new primitives but nothing uses them; after step 2 the new commands exist alongside the old; step 3 deletes the duplicates.

## Open Questions

None at design time. The scope/route/primitive choices have been confirmed; implementation details for `PickDBHost` will be settled during plan-writing.
