# Runtime per-player debug gating

**Date:** 2026-04-28
**Status:** Design

## Motivation

Today, the server unconditionally ships debug data to every connected client:

- Topology updates broadcast to every active player (`topologyBroadcaster`).
- Per-entity Presence/OwnerHost/AoIRadius bytes ride on every replicated entity (added by the recent `mmokit.DebugInfo` refactor).

Both flows assume "debug visualization is for everyone." That's wrong for a production deployment, where:

1. Most players don't need debug overlays — shipping the bytes is wasted bandwidth.
2. Operators need to selectively enable debug for *specific* players (testing a bug report, validating a deploy) without flipping it on for the whole cluster.
3. There's no runtime control surface — debug is a compile-time architectural assumption.

This spec replaces the unconditional flow with a **per-player runtime gate**: debug data is delivered through a single event channel, and the channel is silent unless the operator has granted debug access to that player (or to the cluster as a whole).

## Design

### Single delivery channel

All debug data flows through one event:

```proto
message DebugInfoMsg {
  optional CellTopologyMsg topology = 1;
  optional float aoi_radius = 2;
  // future debug data slots in as new optional fields without
  // breaking the gate or the client decoder
}
```

Code: `enginepb.ServerEventCode_SE_DEBUG_INFO`.

A player's debug-enabled status is the sole gate. Non-debug players never receive `SE_DEBUG_INFO`; debug-enabled players receive it on login (full state) and on change (whichever fields changed).

### Per-entity debug data is derived client-side

Per-entity Presence (LOCAL vs REPLICA) and OwnerHost are computable from data the debug-enabled client already has:

```ts
// In the renderer, only runs while debug overlays are being drawn
function presenceOf(entity, topology, myHost) {
    const cell = topologyCellAt(topology, entity.x, entity.y);
    return cell.hostID === myHost ? "LOCAL" : "REPLICA";
}
```

GHOST is a 1-tick server-side handoff marker; clients don't render it differently from LOCAL/REPLICA, so the wire never carries it. If a future need arises, we add a `presence_changes` field to `DebugInfoMsg` and push from the handoff state machine — but that's out of scope here.

This decision lets us delete the per-entity `mmokit.DebugInfo` component and its associated writer system entirely. Production wire frames carry zero debug bytes; only debug-enabled clients receive any debug data, and only via the event channel.

### Per-player debug flag set

Debug is a *set of capabilities*, not a single boolean — different debug features can be enabled independently per user. Modeled as a `uint32` bitmask on the session, with each bit assigned to a registered flag name:

```go
type DebugFlag uint32

const (
    DebugTopology DebugFlag = 1 << iota  // cell boundaries + AoI radius overlay
    // future flags allocate sequentially: DebugPerf, DebugNetwork, DebugECS, …
)
```

The session carries:

```go
type PlayerSession struct {
    // ...existing fields...
    DebugFlags DebugFlag // bitmask of enabled debug capabilities
}

func (s *PlayerSession) HasDebug(f DebugFlag) bool { return s.DebugFlags&f != 0 }
```

**Engine reserves bits 0–15; games reserve bits 16–31.** Engine flags live in `pkg/engine` (or a sub-package); games register their own via a `mmokit.RegisterDebugFlag(name, bit)` call so the console can list them.

There is no cluster-wide override. Operators who want a flag enabled for many users grant individually (or scripted bulk-update against the DB).

## Components

### Server-side: `debugBroadcaster` system

Replaces the existing `topologyBroadcaster`. Same shape (per-tick, per-cell):

1. Build current debug snapshot: `{topology: stage.Topology(), aoi_radius: stage.GetAoIRadius()}`.
2. Hash the snapshot via FNV.
3. Iterate `Engine.Players.ForEach(StateActive, ...)`:
   - If `sess.DebugFlags == 0`, skip — this player has no debug enabled, no payload to build.
   - Build a per-player `DebugInfoMsg` populating only fields whose flag the player has:
     - `topology`: only if `sess.HasDebug(DebugTopology)`
     - `aoi_radius`: only if `sess.HasDebug(DebugTopology)` (paired with topology in v1)
     - future fields: each gated by their own flag check
   - Hash the per-player payload. If `sentHash[connID] == hash`, skip.
   - Otherwise send `DebugInfoMsg` via `gw.SendEvent(connID, SE_DEBUG_INFO, msg)` and update `sentHash[connID]`.
4. GC `sentHash` entries for disconnected players.

The per-player payload construction means flag flips on a session naturally trigger a fresh send the next tick (the new payload's hash differs from the prior).

The system is registered like `topologyBroadcaster` was — auto-prepended to `c.systemDefs` in `Build()`, runs on every cell.

### `PlayerSession.DebugFlags`

Added to `pkg/engine/PlayerSession`. Populated from the `players.debug_flags` DB column when the session is created at login. Mutated at runtime by console commands. Travels across cell handoffs via `TransferFrame.DebugFlags` (see Cross-cell propagation).

### Debug flag registry

```go
// pkg/engine (or sub-package): a thread-safe registry mapping flag
// names to bit positions. Engine reserves bits 0–15; games reserve
// bits 16–31. Registration is one-shot at process startup.
RegisterDebugFlag(name string, bit DebugFlag)
DebugFlagByName(name string) (DebugFlag, bool)
DebugFlagName(f DebugFlag) (string, bool)
ListDebugFlags() []string  // for `debug features` console command
```

Engine pre-registers `topology` (bit 0). Games can register their own flags during init.

### DB schema

Migration adds:

```sql
ALTER TABLE players ADD COLUMN debug_flags JSONB NOT NULL DEFAULT '[]';
```

JSONB stores the flag set as a string array — e.g. `["topology"]` or `["topology", "perf"]` — for human-readability in DB queries and forward-compat with new flag names. On session creation, names are looked up in the registry and OR'd into the in-memory `DebugFlag` bitmask. Unknown names (a flag renamed since the grant was written, or not yet registered on this server version) are silently dropped from the load.

### Console commands

Four commands, all under `cmdsys`:

| Verb | Args | Effect |
|---|---|---|
| `debug grant <username> <flag\|all>` | `username string`, `flag string` | Add `flag` to player's set (or all registered flags if `all`). DB write + push to active session if online. Triggers an immediate `DebugInfoMsg` send if anything changed. |
| `debug revoke <username> <flag\|all>` | `username string`, `flag string` | Remove `flag` from player's set (or clear if `all`). DB write + push to active session. If the player ends up with `DebugFlags == 0` and is online, send a sentinel empty `DebugInfoMsg{}` to clear client overlays. |
| `debug list` | — | Query DB for users with non-empty `debug_flags`, mark which are currently online (cross-reference `coord.ActiveUsers()`), show the per-user flag set. |
| `debug features` | — | List all registered debug flags with their description (read from registry). |

`grant` and `revoke` route via `cmdsys.RouteKind=PlayerOwner` so they reach whichever cell is currently hosting the user.

## Data flow

### Login

1. Player connects, `LoginHandler` validates credentials.
2. Coordinator's session-create path reads `players.debug_flags` (JSONB array), maps names → bits via the registry, populates `sess.DebugFlags`.
3. Player becomes `StateActive` on their assigned cell.
4. Next tick: `debugBroadcaster.Update` sees `sess.DebugFlags != 0`, builds payload from per-flag fields, sends initial `DebugInfoMsg`.
5. Client receives it, stores topology + aoi_radius (and any future field) in renderer state, debug overlays start drawing.

If `sess.DebugFlags == 0`: step 4's check returns early; client never gets `SE_DEBUG_INFO`. No debug bytes anywhere on the wire.

### Topology change (split / merge / migrate / host join-leave)

1. Coordinator commits the topology change (existing path).
2. Next tick: `debugBroadcaster` hash differs from `sentHash[connID]` for all debug-enabled players.
3. Each gets a fresh `DebugInfoMsg`.
4. Non-debug players: nothing happens.

### Console grant mid-session

1. Operator runs `debug grant alice topology`.
2. `cmdsys` dispatches to the cell hosting alice.
3. Handler:
   - Looks up `topology` in registry → `DebugFlag(0x01)`
   - Reads alice's current DB `debug_flags`, adds `"topology"`, writes back
   - `sess.DebugFlags |= DebugTopology` on the in-memory session
4. Next tick: `debugBroadcaster` builds alice's payload (now includes topology + aoi_radius), hash differs from `sentHash[connID]`, sends fresh `DebugInfoMsg`.
5. Alice's client renders overlays from this point forward.

`debug grant alice all` is the same flow but adds every registered flag.

### Console revoke mid-session

1. Operator runs `debug revoke alice topology`.
2. DB write removes `"topology"` from alice's array.
3. `sess.DebugFlags &= ^DebugTopology`.
4. If `sess.DebugFlags == 0` after the clear, handler sends a sentinel empty `DebugInfoMsg{}` to clear client overlays.
5. Subsequent ticks: broadcaster builds alice's payload (now empty); if zero flags, skip entirely. If some flags remain (multi-flag grant, only one revoked), build a smaller payload with just the still-enabled fields and re-send.

## What gets reverted from the previous refactor

The 11-commit "engine debug component and bindings cleanup" refactor partially unwinds:

| Commit pattern | Action |
|---|---|
| Task 1 (`mmokit.DebugInfo` component) | Delete |
| Task 3 (`Process.HostIndex`) | Delete |
| Task 4 (`DebugInfoWriter` system + auto-register) | Delete |
| Task 6 (`*mmokit.DebugInfo` field on bundles) | Delete from all 6 bundles |
| Task 2 (`Config.VelQuantScale`/`SizeQuantScale`, `Process.Cfg()`) | **Keep** |
| Task 5 (drop `EngineBindingsConfig`, simplify `RegisterKind`) | **Keep** |
| Task 7 (delete 4node-basic's hand-rolled `DebugInfo`) | **Keep** (was a cleanup; new design has no equivalent need) |

Net result: the bindings/config cleanup stays; the per-entity component pipeline goes away; we add the event-based debug channel in its place.

Web client cleanup:
- Remove `entity.presence`, `entity.ownerHost`, `entity.aoIRadius` consumers
- Add `SE_DEBUG_INFO` handler that stores topology + aoi_radius for the renderer
- Add a `presenceOf(entity, topology, myHost)` helper for R/G marker rendering
- Renderer guards all debug-overlay drawing on "do I have topology yet?"

## Wire format

`SE_DEBUG_INFO` rides the existing reliable event channel (channel byte `0x00`, protobuf-encoded). No new framing; same envelope as `SE_CELL_TOPOLOGY` today.

`DebugInfoMsg` uses optional fields rather than oneof. This lets a single message bundle multiple changes (e.g., a cluster reconfigure that changes both topology and AoI radius simultaneously).

## Cross-cell propagation

**Sessions are per-cell, not synchronized across hosts.** When a player crosses a cell boundary, the destination cell's `PlayerManager.RegisterTransferSession(connID, username)` creates a fresh `PlayerSession` carrying only the username and connID — the source session's fields don't travel automatically.

To make `DebugFlags` survive cell handoffs without a DB query on the hot path, ship the bitmask in `TransferFrame`:

```go
type TransferFrame struct {
    // ...existing fields...
    DebugFlags uint32  // 4 bytes; copied from source sess.DebugFlags
}
```

Wire format:

- Source cell, when constructing the TransferFrame: `f.DebugFlags = uint32(sess.DebugFlags)`
- Destination cell, in the transfer-receive hook (`onPlayerTransferReceived` or equivalent inside `RegisterTransferSession`): `sess.DebugFlags = DebugFlag(frame.DebugFlags)`

The destination cell's `debugBroadcaster` has its own `sentHash` (empty for this connID after register), so the next tick fires a fresh `DebugInfoMsg` and the client's overlays continue seamlessly across the handoff.

Cost: 4 bytes permanent addition to `TransferFrame`. Acceptable — handoffs are rare relative to per-tick replication, and we're avoiding a DB query per handoff.

DB is consulted only on initial login. Mid-session console mutations update both DB and the in-memory session, and the in-memory state is what travels through subsequent transfers.

## Persistence guarantees

- `players.debug_flags` JSONB survives restart.
- `sess.DebugFlags` is in-memory only; rebuilt from DB on next login. Travels via `TransferFrame.DebugFlags` across cell handoffs (no DB query needed).
- DB writes from console commands are synchronous (use the existing `MarketRepository`-style sync write pattern, not the async `PlayerFlusher` path) so an operator running `debug grant alice; <crash>; <restart>` sees the grant survive.

## Testing

Each component testable independently:

1. **Flag registry**: register/lookup-by-name/list-all round-trips correctly; double-registration of the same bit panics.
2. **`PlayerSession.DebugFlags` populated from DB**: integration test with Postgres, JSONB array `["topology"]` round-trips to `DebugTopology` bit, unknown names silently dropped.
3. **`debugBroadcaster.Update` gates correctly**: unit test with mock players, vary `DebugFlags`, verify event sends/skips per-flag (e.g. only-DebugPerf player gets a payload without topology field).
4. **Hash-diff send semantics**: same player on subsequent ticks doesn't double-send when nothing changed.
5. **Console commands**:
   - `debug grant <user> <flag>` updates DB JSONB + pushes flag bit to active session
   - `debug grant <user> all` enables every registered flag
   - `debug revoke <user> <flag>` removes flag from set; sentinel empty msg fires only when set becomes zero
   - `debug list` returns correct intersection of DB rows and active users with their flag sets
   - `debug features` lists registered flags with descriptions
6. **Cross-cell handoff preserves DebugFlags**: integration test with player handoff; verify destination session's `DebugFlags` matches source's via `TransferFrame.DebugFlags`.
7. **Web client**: vitest verifies overlay-renderer is no-op until `SE_DEBUG_INFO` is received; verifies presence derivation produces correct R/G markers given topology + entity position.

## Non-goals

- **Per-entity debug streams** (perf timings, ECS component IDs, frame-time histograms): future work. Slot in as new optional fields on `DebugInfoMsg` when the need arises.
- **GHOST state on the wire**: clients don't render it; if a future need arises, add a `presence_changes` field and push from the handoff state machine.
- **Self-grant by players**: only operators can flip the flag (privilege escalation surface). Console-only.
- **Audit log of grants**: not in v1.
- **Graceful degradation if `players.debug_flags` column is missing**: the migration is mandatory; no fallback path.
- **Per-flag fine-tuning of v1 features**: `topology` covers cell-boundary overlay AND AoI radius circle as a single unit. If you ever want them as separate toggles, split `DebugTopology` into two registered flags later — the registry mechanism handles versioning.

## Open questions resolved

- **Why not gate per-entity bytes via per-viewer zero-out or per-viewer omission?** Considered both. Zero-out still ships 6 bytes per entity-visibility-enter to non-debug users — not "needless" in absolute terms but nonzero. Per-viewer omission requires schema-vs-runtime divergence (different layouts per viewer), which violates the project's standing wire-format invariant. Moving the data off the per-entity path entirely is the only design that ships truly zero debug bytes to non-debug clients.

- **Why not keep `*mmokit.DebugInfo` server-side as a queryable component, even if not on the wire?** It would be architectural cruft — the only reason for that component to exist was wire delivery. Server systems that need to know "is this entity a replica?" already have direct access to `Replica`/`Ghost` markers.

- **Why not derive AoIRadius client-side from a hardcoded constant?** AoI radius is configurable at runtime (`config set AoIRadius 1500`). Hardcoding it would silently drift. It belongs in `DebugInfoMsg` so debug clients see the live value.
