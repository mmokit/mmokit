# Protobuf Residue Cleanup + Loose Ends Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining loose ends from Plans 1+2 (events/operations channel redesign): migrate the 4 surviving framework messages off the legacy `protobuf(ServerEvent)` envelope, retire the envelope itself, rewire the bot client (broken since Plan 1), and rebuild the `service` console command (deleted in Plan 2 Phase 5). After this lands, **no protobuf envelope wraps any client-facing wire frame**.

**Architecture:** Three independent workstreams sharing a branch:

1. **Wire-format completion** — migrate `SE_PLAYER_SPAWNED` / `SE_CELL_CHANGE` / `SE_SERVER_CONFIG` (server→client) and `CE_PING` (client→server) from the legacy proto envelope on `0x00` to typed reflection-codec frames. Then delete the `enginepb.ClientEvent` / `ServerEvent` envelope types, the `ClientEventCode` / `ServerEventCode` enums, and the gateway's `EventInterceptor` first-byte-`0x08` disambiguation (no more legacy path to disambiguate against).
2. **Bot client rewire** — `internal/bot/` recv loop has been a no-op shell since Plan 1 Phase 7; `Connect` times out on `spawnCh`. Bot needs cookie-auth (mirroring the web client's `/auth/register` + `/auth/login` HTTP flow) to obtain a session cookie, then a typed-event consumer for `PlayerSpawned` / `WorldDelta` / `PlayerOwnState` / `PlayerDied`.
3. **`service` console command rebuild** — Plan 2 Phase 5 deleted the legacy admin tool that introspected `Router.Schema()` to invoke ops via console. Rebuild against `mmokit.RegisteredTypedOps()` with JSON input for typed Go struct construction.

**Tech Stack:** Go 1.21+ (generics), existing `pkg/universe/reflect_marshal.go` reflection codec, FNV-1a typeIDs via `mmokit.TypeIDOf`, `coder/websocket`, sdkgen for TypeScript, Go's `encoding/json` for the `service` command's runtime struct construction.

**Plans this builds on:**
- Plan 1: [docs/superpowers/plans/2026-05-06-mmokit-events-channel-redesign.md](2026-05-06-mmokit-events-channel-redesign.md) — events channel typed; `ChannelClientInput` (0x02) retired
- Plan 2: [docs/superpowers/plans/2026-05-07-mmokit-operations-channel-redesign.md](2026-05-07-mmokit-operations-channel-redesign.md) — ops channel typed; auth+marketplace+bank ops migrated; `pkg/ops.Register` retired

**Plans this is on top of (commits on main):**
- `de3ba6a` — Plan 2 merge

**Out of scope:**
- Cross-host typed-op routing in distributed mode (flagged in Plan 2 Phase 3.7 as a real gap; needs its own design)
- New auth flows (registration UX, password reset, etc.)
- Cached events / late-joiner replay
- Any new game features

---

## File Structure

**Workstream 1 — Wire-format completion:**

- Modify: `pkg/mmokit/event_messages.go` — define typed `PlayerEntityAssigned` (replaces `SpawnedMsg`), `CellChange`, `ServerConfig`, `Ping`, `Pong` Go structs. Note: typed `Pong` already landed in Plan 1; verify and skip if present.
- Modify: `pkg/mmokit/protocol.go` — replace `RegisterServerEvent[enginepb.SpawnedMsg]` / `CellChangeMsg` / `ServerConfigMsg` with `RegisterEvent[T]` for the new typed structs. Remove `RegisterClientEvent[enginepb.PingMsg]` (event-code registration is dead — typed inputs go through `RegisterClientInputType`).
- Modify: `pkg/engine/player_manager.go:351` — the `enginepb.ServerConfigMsg` send site. Replace with `mmokit.SendEvent(stage, connID, &mmokit.ServerConfig{...})`.
- Modify: `pkg/universe/coordinator.go` (and adjacent) — find every `Stage.SendSpawnedMsg` / `SendCellChangeMsg` / `SendServerConfigMsg` and migrate to `mmokit.SendEvent[T]`.
- Modify: `pkg/universe/gateway.go` (the `EventInterceptor`) — Ping handler currently lives there for inline pong response. Migrate to a typed-input handler via `mmokit.HandleClient[Ping]`.
- Modify: `cmd/server/main.go:101` — remove the Phase 5 TODO; the migration is done.
- Delete: `proto/enginepb/engine.proto::ClientEvent`, `ServerEvent`, `ClientEventCode`, `ServerEventCode`, `PingMsg`, `SpawnedMsg`, `CellChangeMsg`, `ServerConfigMsg`. Run `just proto`.
- Delete: client SDK envelope-dispatch path. The disambiguation peek for first-byte `0x08` is dead; remove from `web-pixi/sdk/transport.ts`/`client.ts` and the corresponding sdkgen template in `cmd/sdkgen/generate.go`.
- Modify: `pkg/mmokit/server_events.go` — if `RegisterServerEvent` (proto-keyed by event code) has zero remaining callers after migration, delete the whole file. Same for any `enginepb.ServerEvent` / `MakeEvent` helper code.
- Modify: `pkg/universe/event_dispatch.go` — the legacy-vs-typed disambiguation drops; only the typed path remains.

**Workstream 2 — Bot rewire:**

- Modify: `internal/bot/bot.go` — add HTTP-cookie auth flow before WebSocket connect. Replace the no-op recv loop with typed-event decoder.
- Create: `internal/bot/typed_decoder.go` — hand-rolled typed-event decoder for the 4 event types the bot consumes.
- Modify: `internal/bot/actions.go` — already has `sendTypedOp` from Plan 2 Phase 3.6; no changes unless tests reveal gaps.
- Modify: `internal/bot/world.go` — entity-state derivation from the new `PlayerOwnState` + `WorldDelta` shapes (the `WorldDelta` body decode already exists in Plan 1 Phase 4).

**Workstream 3 — `service` console command:**

- Create: `pkg/universe/builtins_service.go` — the rebuilt console command. Iterates `mmokit.RegisteredTypedOps()` for `service list/info`. JSON input for typed-arg construction (`service call <opName> '{"itemID": 42}'`). Dispatches via `pkguniverse.DispatchTypedOpInbound` directly with a synthetic `OpContext`.
- Modify: `pkg/universe/coordinator.go` — register the command back into the builtins slice.

---

## Phase 0 — Setup

### Task 0.1: Create branch from main

**Files:** none (git only)

- [ ] **Step 1: Verify clean tree on main**

```bash
git checkout main && git status
```

Expected: `On branch main`, `nothing to commit, working tree clean`. Plan 2's merge commit `de3ba6a` should be the most recent commit.

- [ ] **Step 2: Create branch**

```bash
git checkout -b feat/mmokit-protobuf-residue-cleanup
```

- [ ] **Step 3: Verify build is clean**

```bash
go vet ./... && go test ./... 2>&1 | grep -E "FAIL|^ok" | tail -25
(cd examples/4node-basic/web && bunx tsc --noEmit) && (cd web-pixi && bunx tsc --noEmit)
```

Expected: 21 packages pass, both TS clean.

---

## Phase 1 — Migrate Surviving Framework Events

The 3 server→client events still on the legacy envelope (`SE_PLAYER_SPAWNED` / `SE_CELL_CHANGE` / `SE_SERVER_CONFIG`) follow Plan 1's Phase 3 mechanical migration pattern. Apply once per message.

### The pattern (apply 3 times)

1. Define typed Go struct in `pkg/mmokit/event_messages.go` mirroring the proto field set.
2. Replace `RegisterServerEvent[enginepb.XxxMsg]` in `pkg/mmokit/protocol.go` with `RegisterEvent[NewName]()`.
3. Replace every `gw.ServerEvents().Send(code, &enginepb.XxxMsg{...})` call site with `mmokit.SendEvent(stage, connID, &mmokit.NewName{...})`.
4. Update web-pixi consumer (if any) — handlers go from `client.onPlayerEntityAssigned(msg => ...)` style to the same name (codegen continues to emit `client.on<EventName>` for typed events).
5. Run `go vet ./...` + `go test ./pkg/mmokit/ ./pkg/engine/ ./pkg/universe/`.
6. Regenerate SDKs (`just client-sdk examples/4node-basic && just space-sdk`) + verify TS typecheck.
7. Commit per migration.

### Task 1.1: `SpawnedMsg` → `PlayerEntityAssigned`

**Files:**
- Modify: `pkg/mmokit/event_messages.go`
- Modify: `pkg/mmokit/protocol.go:116` (registration)
- Modify: `pkg/universe/stage.go::SendSpawnedMsg` (or wherever the producer lives)
- Modify: any other consumers found via `grep -rn "enginepb\.SpawnedMsg\|SendSpawnedMsg" --include="*.go"`
- Modify: `web-pixi/src/network.ts` and `examples/4node-basic/web/src/network.ts` consumer call sites (handler name unchanged; field-name access may need updates)

- [ ] **Step 1: Read the existing proto**

```bash
grep -A 15 "message SpawnedMsg" proto/enginepb/engine.proto
```

Note the field set: likely `your_entity_id`, `world_x`, `world_y`, plus possibly `cell_x`/`cell_y` for non-game-specific reuse.

- [ ] **Step 2: Find every send site**

```bash
grep -rn "enginepb\.SpawnedMsg\|SendSpawnedMsg\|GameServerEventCode_SE_PLAYER_SPAWNED\|enginepb\.ServerEventCode_SE_PLAYER_SPAWNED" --include="*.go" .
```

Expected hits: `pkg/mmokit/protocol.go` (registration), `pkg/universe/stage.go` (the `SendSpawnedMsg` method), one or two send sites in `pkg/universe/coordinator.go` or `gateway.go`.

- [ ] **Step 3: Define typed struct**

Append to `pkg/mmokit/event_messages.go`:

```go
// PlayerEntityAssigned — engine-default server→client message announcing a
// player's entity ID and spawn position. Replaces enginepb.SpawnedMsg. Games
// with their own spawn flow (e.g., the space game's PlayerSpawned typed event)
// override via Process.OnPlayerJoin and don't use this default; examples like
// `examples/simple` rely on it.
type PlayerEntityAssigned struct {
    EntityNetID uint32
    WorldX      float32
    WorldY      float32
    OriginCellX int32
    OriginCellY int32
}
```

(Exact field set: mirror `enginepb.SpawnedMsg` from `proto/enginepb/engine.proto`.)

- [ ] **Step 4: Register typed event**

In `pkg/mmokit/protocol.go`, replace:

```go
// OLD:
RegisterServerEvent[enginepb.SpawnedMsg](p.serverEventsRegistry, enginepb.ServerEventCode_SE_PLAYER_SPAWNED, WithEventName("playerEntityAssigned"))

// NEW:
RegisterEvent[PlayerEntityAssigned]()
```

- [ ] **Step 5: Migrate every send site**

For each site found in Step 2, replace:

```go
// OLD:
gw.ServerEvents().Send(connMgr, connID,
    uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED),
    &enginepb.SpawnedMsg{YourEntityId: nid, WorldX: x, WorldY: y, ...})

// NEW:
mmokit.SendEvent(stage, connID, &mmokit.PlayerEntityAssigned{
    EntityNetID: nid, WorldX: x, WorldY: y, OriginCellX: cx, OriginCellY: cy,
})
```

The `Stage.SendSpawnedMsg` method in `pkg/universe/stage.go` (if it exists as a wrapper) gets either deleted or rewritten to call the typed send.

- [ ] **Step 6: Regenerate SDKs**

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Verify the regenerated `web-pixi/sdk/broadcasts.ts` has a `PlayerEntityAssigned` class. The codegen rename from `SpawnedMsg` → `PlayerEntityAssigned` may break TS callers if they relied on the old proto name; check `web-pixi/src/network.ts` for `SpawnedMsg` references.

- [ ] **Step 7: Run tests + typechecks**

```bash
go vet ./... && go test ./pkg/mmokit/ ./pkg/engine/ ./pkg/universe/ ./internal/game/
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add pkg/mmokit/event_messages.go pkg/mmokit/protocol.go pkg/universe/stage.go pkg/engine/player_manager.go web-pixi/src/ examples/4node-basic/web/src/ web-pixi/sdk/ examples/4node-basic/web/sdk/
git add -f gen/  # if needed
git commit -m "feat(mmokit): migrate SpawnedMsg → typed PlayerEntityAssigned event

Plan 1 Phase 7 left this engine-default event on the legacy
protobuf(ServerEvent) envelope (used by examples/simple and any
non-overriding game). Migrating to typed reflection-codec on 0x00
closes the residue.

enginepb.SpawnedMsg + SE_PLAYER_SPAWNED enum entry stay until the
final cleanup pass deletes the envelope itself."
```

### Task 1.2: `CellChangeMsg` → `CellChange`

Apply the same pattern. Source: `enginepb.CellChangeMsg` (proto fields likely `new_cell_x`, `new_cell_y`, `entity_net_id`). The send site is in `pkg/universe/` — the framework emits this when a player's cell ownership transfers cross-host.

```bash
grep -rn "enginepb\.CellChangeMsg\|SE_CELL_CHANGE" --include="*.go" .
```

Migration commit:

```
feat(mmokit): migrate CellChangeMsg → typed CellChange event
```

### Task 1.3: `ServerConfigMsg` → `ServerConfig`

Apply the pattern. Source: `enginepb.ServerConfigMsg`. The producer is at `pkg/engine/player_manager.go:351`. Sent on connect with engine-config metadata (tick rate, etc.).

```bash
grep -rn "enginepb\.ServerConfigMsg\|SE_SERVER_CONFIG" --include="*.go" .
```

Migration commit:

```
feat(engine): migrate ServerConfigMsg → typed ServerConfig event
```

### Task 1.4: Phase 1 verification

```bash
go vet ./... && go test ./...
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
just build
```

Expected: all clean.

---

## Phase 2 — Migrate `CE_PING` Inbound to Typed

The `CE_PING` client→server input is currently handled inline by the gateway's `EventInterceptor` (a hook on the read goroutine that responds with `Pong` immediately, before the frame reaches the cell input dispatch). Plan 1 Phase 3.13a deferred this because the EventInterceptor only sees protobuf-envelope frames on `0x00`.

After Phase 1 lands, `0x00` carries ONLY typed reflection-codec frames (no more legacy envelope). The `EventInterceptor` is now obsolete — typed inputs go through `mmokit.HandleClient[T]` and route to the cell. So:

### Task 2.1: Define typed `Ping` input

**Files:**
- Modify: `pkg/mmokit/event_messages.go` — add `Ping` typed struct (verify `Pong` already exists from Plan 1 Phase 3.13b)

```go
// Ping — client→server liveness check. Server responds with Pong (typed event,
// already migrated in Plan 1 Phase 3.13b).
type Ping struct {
    Timestamp int64 // millisecond epoch from client clock; server echoes in Pong
}
```

Match the field set to `enginepb.PingMsg`.

### Task 2.2: Register Ping handler

**Files:**
- Modify: `pkg/mmokit/protocol.go:122` — remove `RegisterClientEvent[enginepb.PingMsg]`
- Modify: a wiring point that handles inbound Ping (likely `pkg/universe/coordinator.go` or wherever the framework's universal handlers register)

Add a `HandleClient[Ping]` registration that responds with a typed `Pong`. Where does this live? It's framework-level — the response should not require a `*Stage` (Ping arrives before a player has a cell, in some configurations). Two options:

**Option A**: Register `HandleClient[Ping]` per-stage, responding via the stage's connection manager. Simple; matches the existing `HandleClient[T]` pattern. Downside: requires the Ping to reach a cell, which it now does (post-Phase-1 the EventInterceptor is gone).

**Option B**: Special-case Ping in the typed-event dispatcher, responding inline before reaching the registry. Mirrors the legacy `EventInterceptor` semantics.

**Pick Option A** — uniform with every other typed input. The latency added (1 cell hop) is negligible for liveness checks.

- [ ] **Step 1: Wire the Ping handler**

In whatever file owns framework-level handler registration (search for `HandleClient[*Pong]` or `RegisterServerEvent`-adjacent code; likely `pkg/mmokit/protocol.go::registerEngineDefaults` or similar):

```go
HandleClient[Ping](world, func(player Entity, msg *Ping) {
    conn := Get[PlayerConn](player)
    if conn == nil {
        return
    }
    // Respond with Pong (typed event already registered in Plan 1).
    SendEvent(player.Stage(), conn.ConnID, &Pong{
        Timestamp:       msg.Timestamp,
        ServerTimestamp: time.Now().UnixMilli(),
    })
})
```

(Adjust signatures to match the existing `HandleClient[T]` pattern. The `Pong` struct's field set should mirror `enginepb.PongMsg`.)

- [ ] **Step 2: Delete the legacy EventInterceptor Ping path**

In `pkg/universe/gateway.go` (or wherever the `EventInterceptor` lives) find the inline Ping handling. Delete it. The interceptor function may now be entirely unused — if so, delete the parameter / removal the corresponding `pkg/net/conn.go` `EventInterceptor` field. Verify with grep:

```bash
grep -rn "EventInterceptor\|eventInterceptor" --include="*.go" .
```

Zero hits after the deletion is the goal.

- [ ] **Step 3: Update the bot's Ping send (if it has one)**

Search:

```bash
grep -rn "PingMsg\|CE_PING" --include="*.go" .
```

If `internal/bot/bot.go` sends `enginepb.PingMsg`, migrate to `b.sendTypedInput(&mmokit.Ping{Timestamp: ...})` using the existing typed-input helper.

- [ ] **Step 4: Remove the Plan 5 TODO**

In `cmd/server/main.go:101` (approx — find via `grep -n "TODO.*CE_PING\|TODO.*Phase 5"`), delete the now-resolved TODO comment.

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test ./...
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

- [ ] **Step 6: Commit**

```bash
git add -u
git commit -m "feat(mmokit): migrate CE_PING → typed Ping handler

Plan 1 Phase 3.13a deferred Ping inbound migration because the
EventInterceptor only saw protobuf-envelope frames. After this branch's
Phase 1 retired the envelope, the interceptor is obsolete; Ping
becomes a normal HandleClient[Ping] handler that responds with Pong.

EventInterceptor field on pkg/net/conn.go retired (zero callers)."
```

---

## Phase 3 — Retire the Legacy Envelope

After Phases 1+2, `0x00` carries only typed reflection-codec frames in both directions. The `enginepb.ClientEvent` / `ServerEvent` proto envelopes have zero callers. Time to delete them and the disambiguation infrastructure.

### Task 3.1: Delete envelope decode/encode infrastructure

**Files:**
- Modify: `cmd/sdkgen/generate.go` — find the `transport.ts` template's first-byte-`0x08` disambiguation; delete the legacy branch (typed-event-only now).
- Modify: `web-pixi/sdk/transport.ts` and `examples/4node-basic/web/sdk/transport.ts` — same deletion (regenerated automatically after the codegen change).
- Modify: `pkg/mmokit/server_events.go` — if `RegisterServerEvent` (the proto-event-code re-export) has zero callers, delete it. The file itself may be deletable.
- Modify: `pkg/mmokit/protocol.go` — drop `clientEventsRegistry` and `serverEventsRegistry` fields if zero callers remain.
- Modify: `pkg/mmokit/mmokit.go::MakeEvent` — verify zero callers; delete.
- Modify: `pkg/universe/event_dispatch.go` — the disambiguation code path becomes unreachable; simplify to just the typed-event consumption.

- [ ] **Step 1: Inventory remaining proto-envelope consumers**

```bash
grep -rn "ServerEvent{Code\|ClientEvent{Code\|MakeEvent\|RegisterServerEvent\b\|RegisterClientEvent\b" --include="*.go" .
```

Expected hits: only test files and possibly `pkg/mmokit/protocol.go::registerEngineDefaults` references that were deleted in Phase 1's migrations. Verify zero production callers.

- [ ] **Step 2: Delete the disambiguation in the SDK template**

In `cmd/sdkgen/generate.go`, find the `transport.ts` template (search for `payload[0] === 0x08`). Delete the legacy branch:

```typescript
// OLD:
case CH_EVENT: {
  const payload = data.subarray(1);
  if (payload.length > 0 && payload[0] === 0x08) {
    handleServerEventLegacy(payload);
  } else {
    // typed-event parse loop
  }
  break;
}

// NEW:
case CH_EVENT: {
  const payload = data.subarray(1);
  // Typed-event frames only; legacy ServerEvent envelope retired.
  let off = 0;
  while (off + 8 <= payload.length) {
    const view = new DataView(payload.buffer, payload.byteOffset + off, 8);
    const typeID = view.getUint32(0, true);
    const bodyLen = view.getUint32(4, true);
    off += 8;
    if (off + bodyLen > payload.length) {
      console.warn(`typed-event frame: truncated body for typeID=${typeID.toString(16)}`);
      break;
    }
    const body = payload.subarray(off, off + bodyLen);
    off += bodyLen;
    client.typedEvents.dispatch(typeID, body);
  }
  break;
}
```

Delete the `handleServerEventLegacy` function it referenced (no callers).

- [ ] **Step 3: Regenerate SDKs**

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Verify the regenerated `transport.ts` no longer has the legacy branch.

- [ ] **Step 4: Delete the server-side disambiguation**

In `pkg/universe/event_dispatch.go`, find the parallel disambiguation:

```go
// OLD (the gateway's inbound 0x00 read pump):
if len(payload) > 0 && payload[0] == 0x08 {
    handleLegacyServerEvent(payload)
} else {
    DispatchInboundEventFrame(stage, playerNetID, payload)
}

// NEW:
DispatchInboundEventFrame(stage, playerNetID, payload)
```

Delete `handleLegacyServerEvent` and any helpers it used.

- [ ] **Step 5: Delete `pkg/mmokit/server_events.go`**

If `RegisterServerEvent` and friends have zero callers (verify via Step 1's grep), delete the entire file. Update `pkg/mmokit/protocol.go` to remove the `serverEventsRegistry` field initialization.

- [ ] **Step 6: Delete `MakeEvent` helper**

In `pkg/mmokit/mmokit.go` (or wherever `MakeEvent` lives), delete the function. Zero callers after Phase 1.

- [ ] **Step 7: Verify build green**

```bash
go vet ./... && go test ./...
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

- [ ] **Step 8: Commit**

```bash
git add -u
git commit -m "chore(mmokit,sdk): retire legacy 0x00 ServerEvent envelope path

After Phase 1+2, channel 0x00 carries only typed reflection-codec
frames in both directions. The first-byte-0x08 disambiguation
(legacy proto vs typed) is dead code. Delete:
- cmd/sdkgen/generate.go: transport.ts legacy branch
- pkg/universe/event_dispatch.go: handleLegacyServerEvent
- pkg/mmokit/server_events.go: RegisterServerEvent/Build/Send (entire file)
- pkg/mmokit/mmokit.go: MakeEvent helper
- web-pixi/sdk/, examples/4node-basic/web/sdk/: regenerated"
```

### Task 3.2: Delete the proto envelope types

**Files:**
- Delete: `proto/enginepb/engine.proto::ClientEvent`, `ServerEvent`, `ClientEventCode`, `ServerEventCode`, `PingMsg`, `PongMsg`, `SpawnedMsg`, `CellChangeMsg`, `ServerConfigMsg`. Run `just proto`.

Per codebase memory `feedback_proto_field_cleanup`: never reserve, renumber from 1 (no concrete renumbering needed if everything goes — verify only `EntityMeshState` enum (used in entity replication body) survives in `engine.proto`).

- [ ] **Step 1: Inventory remaining enginepb proto references**

```bash
grep -rn "enginepb\." --include="*.go" . | grep -v "/gen/" | head -20
```

Expected hits: `enginepb.EntityMeshState` (still used in entity replication) and possibly nothing else. If anything else references the deleted types, fix the consumer first.

- [ ] **Step 2: Edit the proto**

Delete from `proto/enginepb/engine.proto`:
- `message ClientEvent { ... }`
- `message ServerEvent { ... }`
- `enum ClientEventCode { ... }`
- `enum ServerEventCode { ... }`
- `message PingMsg { ... }`
- `message PongMsg { ... }`
- `message SpawnedMsg { ... }`
- `message CellChangeMsg { ... }`
- `message ServerConfigMsg { ... }`

Keep `enum EntityMeshState`.

- [ ] **Step 3: Regen + verify**

```bash
just proto
go vet ./...
go test ./...
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

- [ ] **Step 4: Commit**

```bash
git add proto/enginepb/engine.proto
git add -f gen/go/enginepb/engine.pb.go gen/es/enginepb/
git commit -m "chore(proto): retire enginepb envelope types

ClientEvent / ServerEvent envelopes, ClientEventCode / ServerEventCode
enums, and the framework messages PingMsg / PongMsg / SpawnedMsg /
CellChangeMsg / ServerConfigMsg deleted — all migrated to typed
reflection-codec in this branch's Phase 1+2.

enum EntityMeshState retained (still used by entity replication body)."
```

### Task 3.3: Phase 3 verification — zero protobuf bytes on the wire

```bash
# Server-side: every proto.Marshal/Unmarshal call should be in mesh-internal code only.
grep -rn "proto\.Marshal\|proto\.Unmarshal" --include="*.go" . | grep -v "/gen/" | head -20
```

Expected: only `pkg/universe/grpc_bridge.go` / `pkg/universe/host_network.go` / `pkg/universe/coordinator.go` for `meshpb` (server-internal mesh data plane). Zero hits in `pkg/mmokit/`, `pkg/auth/`, `internal/game/`, `internal/marketplace/`, etc.

```bash
# Client-side: every fromBinary/toBinary call should be in test or mesh-internal code only.
grep -rn "fromBinary\|toBinary" --include="*.ts" .
```

Expected: zero hits (or only in test scaffolding). The web client no longer touches protobuf.

```bash
# gen/go/ directory listing.
ls gen/go/
```

Expected: `meshpb/` (server-internal). `enginepb/` may persist with just `EntityMeshState` enum (if so, that's fine — the enum is not on the wire, just compiled into Go for entity-state field types). `gamepb/` may persist for `EntityType` / `GameClientEventCode` / `GameServerEventCode` (similar — enum-only, not on the wire).

If `enginepb/` and `gamepb/` are reduced to enum-only files with no wire-format usage, optionally consolidate them into a non-proto Go file (e.g., `pkg/universe/entity_mesh_state.go`). Skip if it adds noise; the proto enums work fine.

---

## Phase 4 — Bot Client Rewire

The bot has been broken since Plan 1 Phase 7 retired the proto envelope. The recv loop is documented as a no-op shell at `internal/bot/bot.go:172`. Plan 2 Phase 4 also removed the dead login send.

The bot needs:
1. **Auth flow** — obtain a session cookie via HTTP `/auth/register` then `/auth/login` (mirroring `examples/4node-basic/web/src/auth.ts`).
2. **WebSocket connect with cookie** — the gateway validates the cookie at upgrade time, no `LoginRequest` needed on the wire.
3. **Typed-event consumer** — decode the typed-event frames on `0x00` for the 4 event types the bot needs: `PlayerSpawned`, `PlayerDied`, `PlayerOwnState`, `WorldDelta`.

### Task 4.1: Add HTTP cookie-auth flow

**Files:**
- Create: `internal/bot/auth.go` — `Authenticate(serverURL, username, password) (cookie http.Cookie, err error)` helper

- [ ] **Step 1: Define the auth flow**

`internal/bot/auth.go`:

```go
package bot

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "strings"
)

const devPassword = "bot-test-password" // bots use a fixed password for simplicity

// Authenticate performs the same /auth/register or /auth/login flow the web
// client uses. Returns the session cookie that the gateway validates at WS
// upgrade. On 409 (username taken) it falls through to login.
func Authenticate(serverURL, username string) (*http.Cookie, error) {
    base := strings.TrimSuffix(serverURL, "/")

    // 1. Try register first (idempotent on first-time bots).
    cookie, err := postAuth(base+"/auth/register", username, devPassword)
    if err == nil {
        return cookie, nil
    }
    if !errors.Is(err, errUserExists) {
        return nil, fmt.Errorf("register: %w", err)
    }

    // 2. Username taken → log in.
    cookie, err = postAuth(base+"/auth/login", username, devPassword)
    if err != nil {
        return nil, fmt.Errorf("login: %w", err)
    }
    return cookie, nil
}

var errUserExists = errors.New("username already exists")

func postAuth(url, username, password string) (*http.Cookie, error) {
    body, _ := json.Marshal(map[string]string{
        "username": username,
        "password": password,
    })
    req, err := http.NewRequest("POST", url, bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode == 409 {
        return nil, errUserExists
    }
    if resp.StatusCode != 200 {
        msg, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
    }
    for _, c := range resp.Cookies() {
        if strings.HasPrefix(c.Name, "session") || c.Name == "auth" {
            return c, nil
        }
    }
    return nil, fmt.Errorf("auth response had no session cookie")
}
```

(Adjust the cookie name to whatever `pkg/auth/http.go` actually sets — search there before finalizing.)

- [ ] **Step 2: Test the auth flow against a live server**

The test server needs to be running (`just dev`). The bot's auth helper test should:

1. Spin up the server in the test (or skip if the test server is already running)
2. Call `Authenticate("http://localhost:8080", "bot-test-1")`
3. Assert a non-nil cookie is returned

For unit testability, abstract the HTTP client behind an interface so a fake-server test can validate without a live server.

- [ ] **Step 3: Commit**

```bash
git add internal/bot/auth.go
git commit -m "feat(bot): add HTTP cookie-auth helper

Mirrors the web client's /auth/register → /auth/login flow. Returns
the session cookie the gateway validates at WebSocket upgrade. Bots
register once with a fixed dev password; subsequent runs log in."
```

### Task 4.2: Connect WebSocket with cookie

**Files:**
- Modify: `internal/bot/bot.go::Connect`

Today `Connect` opens a UDP/WS connection without auth. Add the cookie to the WS upgrade request.

- [ ] **Step 1: Investigate the existing WS dial path**

```bash
grep -n "websocket\.Dial\|udpclient\.Dial\|http.NewRequest" internal/bot/bot.go pkg/net/udpclient/*.go
```

The bot currently uses `pkg/net/udpclient.Dial` (UDP, despite the package name suggesting WebSocket). Check whether the bot connects via WS or UDP.

If WS: use `websocket.Dial(ctx, url, opts)` from `coder/websocket` with `opts.HTTPHeader.Set("Cookie", cookie.String())`.

If UDP: the auth model is different — UDP doesn't have HTTP cookies. Either the bot needs a different auth flow (server validates UDP packets via session token in the first packet) or the bot's transport switches to WebSocket. Check the existing UDP-bot infrastructure for clues.

- [ ] **Step 2: Decide auth-on-UDP vs switch-to-WS**

If the bot uses UDP today, the simplest path is to switch to WebSocket — the auth path is HTTP-cookie-based and WS upgrade carries the cookie naturally. UDP would require a parallel session-token-in-first-packet flow.

**Recommendation: switch the bot to WebSocket.** Less code, mirrors what real clients do.

- [ ] **Step 3: Implement WS connect with cookie**

```go
import "github.com/coder/websocket"

func (b *Bot) Connect(addr string) error {
    cookie, err := Authenticate(toHTTPURL(addr), b.name)
    if err != nil {
        return fmt.Errorf("auth: %w", err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
    defer cancel()

    opts := &websocket.DialOptions{
        HTTPHeader: http.Header{},
    }
    opts.HTTPHeader.Set("Cookie", cookie.String())

    ws, _, err := websocket.Dial(ctx, toWSURL(addr), opts)
    if err != nil {
        return fmt.Errorf("ws dial: %w", err)
    }
    b.ws = ws

    b.ctx, b.cancel = context.WithCancel(context.Background())
    go b.recvLoop()
    go b.inputLoop()

    // Wait for spawn confirmation (sent by the server after the cookie
    // validates and the player is assigned to a cell).
    select {
    case <-b.spawnCh:
        log.Printf("[bot:%s] connected, entityID=%d", b.name, b.myEntityID)
    case <-time.After(connectTimeout):
        ws.Close(websocket.StatusNormalClosure, "")
        return errors.New("connect timeout waiting for spawn")
    }

    return nil
}

func toHTTPURL(addr string) string {
    if strings.HasPrefix(addr, "ws://") {
        return "http://" + strings.TrimPrefix(addr, "ws://")
    }
    if strings.HasPrefix(addr, "wss://") {
        return "https://" + strings.TrimPrefix(addr, "wss://")
    }
    return "http://" + addr
}

func toWSURL(addr string) string {
    if strings.HasPrefix(addr, "ws") {
        return addr
    }
    return "ws://" + addr + "/ws"
}
```

(Adjust the WS path `/ws` to whatever the gateway listens on — check `pkg/universe/gateway.go`.)

- [ ] **Step 4: Commit**

```bash
git add internal/bot/bot.go
git commit -m "feat(bot): connect via WebSocket with auth cookie

Replaces the UDP dial with a WebSocket dial that carries the auth
cookie obtained via Authenticate(). Mirrors what real clients do —
the gateway validates the cookie at upgrade and dispatches the
PlayerAssignment automatically."
```

### Task 4.3: Replace recv loop with typed-event decoder

**Files:**
- Create: `internal/bot/typed_decoder.go`
- Modify: `internal/bot/bot.go::recvLoop`

The bot consumes 4 typed events:
- `PlayerSpawned` (game-specific) or `PlayerEntityAssigned` (engine-default) — sets `b.myEntityID`, fires `b.spawnCh`
- `PlayerDied` — sets `b.alive = false`, fires `b.deathCh`
- `PlayerOwnState` — updates `b.ownState`
- `WorldDelta` — feeds the existing `BasicDeltaDecoder` to update `b.state.entities`

The typeIDs are FNV-1a hashes of the Go type names. The bot needs to compute them at runtime from the registered types.

- [ ] **Step 1: Define the decoder**

`internal/bot/typed_decoder.go`:

```go
package bot

import (
    "encoding/binary"
    "log"
    "reflect"

    "github.com/zenion/mmokit/pkg/mmokit"
    "github.com/zenion/mmokit/pkg/universe"
)

// typedEventTypeID returns the FNV-1a typeID the server uses to identify the
// given Go type on the wire. Mirrors mmokit.TypeIDOf for runtime use.
func typedEventTypeID(t reflect.Type) uint32 {
    return mmokit.TypeIDOf(t)
}

// decodeTypedEventFrame parses a 0x00 channel payload (channel byte already
// stripped). The frame may contain N entries; each is dispatched.
func (b *Bot) decodeTypedEventFrame(payload []byte) {
    off := 0
    for off+8 <= len(payload) {
        typeID := binary.LittleEndian.Uint32(payload[off : off+4])
        bodyLen := binary.LittleEndian.Uint32(payload[off+4 : off+8])
        off += 8
        if int(bodyLen) > len(payload)-off {
            log.Printf("[bot:%s] typed-event frame truncated for typeID %#x", b.name, typeID)
            return
        }
        body := payload[off : off+int(bodyLen)]
        off += int(bodyLen)

        b.dispatchTypedEvent(typeID, body)
    }
}

// dispatchTypedEvent decodes the body for known typeIDs. Unknown typeIDs are
// silently skipped (the bot only cares about a small subset).
func (b *Bot) dispatchTypedEvent(typeID uint32, body []byte) {
    switch typeID {
    case typedEventTypeID(reflect.TypeFor[mmokit.PlayerEntityAssigned]()):
        var msg mmokit.PlayerEntityAssigned
        universe.ReflectUnmarshal(body, &msg)
        b.handleSpawned(msg.EntityNetID, msg.WorldX, msg.WorldY)
    case typedEventTypeID(reflect.TypeFor[mmokit.WorldDelta]()):
        var msg mmokit.WorldDelta
        universe.ReflectUnmarshal(body, &msg)
        b.applyDelta(msg.Body)
    // Add more typeIDs as needed: PlayerOwnState, PlayerDied, etc.
    }
}

// handleSpawned fires the spawn channel and sets the entity ID.
func (b *Bot) handleSpawned(netID uint32, x, y float32) {
    b.mu.Lock()
    b.myEntityID = netID
    b.alive = true
    b.mu.Unlock()
    select {
    case b.spawnCh <- struct{}{}:
    default:
    }
    if b.onSpawn != nil {
        b.onSpawn()
    }
}

// applyDelta runs the existing BasicDeltaDecoder against the WorldDelta body.
// (Reuses Plan 1's bytes-fast-path body which is the 20-byte-header binary
// frame the existing decoder consumes.)
func (b *Bot) applyDelta(body []byte) {
    // Implementation: reuse the existing b.decoders.applyDelta(body) call.
    // Ensure the decoder knows about all entity kinds it needs.
}
```

(Verify the actual game-side `PlayerSpawned` typed event vs the engine default `PlayerEntityAssigned` — the bot may need to handle both depending on which game it connects to. Check `internal/game/event_messages.go` for the game's typed PlayerSpawned shape.)

- [ ] **Step 2: Replace the recv loop**

In `internal/bot/bot.go::recvLoop`, delete the no-op stub. Replace with:

```go
func (b *Bot) recvLoop() {
    for {
        select {
        case <-b.ctx.Done():
            return
        default:
        }

        msgType, data, err := b.ws.Read(b.ctx)
        if err != nil {
            select {
            case <-b.ctx.Done():
            default:
                log.Printf("[bot:%s] recv error: %v", b.name, err)
            }
            return
        }
        if msgType != websocket.MessageBinary {
            continue
        }

        if len(data) < 1 {
            continue
        }
        channel := data[0]
        payload := data[1:]

        switch channel {
        case 0x00: // ChannelEvent — typed events
            b.decodeTypedEventFrame(payload)
        case 0x01: // ChannelOperation — typed-op responses (handled by sendTypedOp's correlator)
            b.dispatchTypedOpResponse(payload)
        default:
            // Unknown channel; skip
        }
    }
}
```

The `dispatchTypedOpResponse` helper handles op-response correlation. If the bot's Phase 3.6 `sendTypedOp` already has correlation logic, integrate it; otherwise add a `pendingTypedOps map[uint64]chan typedOpResponse` correlator on the `Bot` struct.

- [ ] **Step 3: Wire `sendTypedInput` for Ping** (if Phase 2 hasn't already)

The bot's existing `sendTypedInput` helper from Plan 1's Phase G work should handle typed inputs. Verify Ping is wired:

```go
go b.pingLoop()  // in Connect()

func (b *Bot) pingLoop() {
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-b.ctx.Done():
            return
        case <-ticker.C:
            b.sendTypedInput(&mmokit.Ping{Timestamp: time.Now().UnixMilli()})
        }
    }
}
```

- [ ] **Step 4: Test against a live server**

```bash
just dev  # in one terminal
go run ./cmd/botclient/duel  # in another (or whichever bot demo binary exists)
```

Expected: bot connects, spawns, fires actions, doesn't time out.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/typed_decoder.go internal/bot/bot.go
git commit -m "feat(bot): wire typed-event recv loop + Ping send

Replaces the no-op shell with a typed-event decoder for the 4 events
the bot consumes (PlayerEntityAssigned, WorldDelta, PlayerOwnState,
PlayerDied). Per-tick Ping send keeps the connection liveness signal
flowing through the typed-input path.

The bot now mirrors what a real client does: HTTP cookie auth →
WebSocket connect → typed events on 0x00 + typed ops on 0x01."
```

### Task 4.4: Verify cmd/botclient demos work

```bash
go build ./cmd/botclient/...
just dev &
sleep 3
go run ./cmd/botclient/duel
```

If `cmd/botclient/duel.go` and `cmd/botclient/miners.go` had references to the old proto-decode bot internals, fix them. Most should "just work" after the bot's external API stays the same.

Commit any fixes:

```
fix(botclient): update demos for typed-event bot
```

---

## Phase 5 — `service` Console Command Rebuild

Plan 2 Phase 5 deleted `pkg/universe/builtins_service.go` because it depended on the legacy `Router.Schema()` introspection. Rebuild against `mmokit.RegisteredTypedOps()` with JSON input for typed Go struct construction.

### Task 5.1: Design — JSON input for typed-arg construction

Console syntax:

```
service list                              # list all registered ops
service info <opName>                     # show request/response field schema
service call <opName> '<json-args>'       # invoke the op synchronously
```

Example:

```
> service call marketBrowse '{"itemID": 42}'
MarketOrderBookResponse:
  itemID: 42
  sellLevels:
    - { price: 10.0, quantity: 5, orderCount: 1 }
  buyLevels: []
```

The JSON input is unmarshaled into a fresh `*Req` instance via `json.Unmarshal`. The op is dispatched via `pkguniverse.DispatchTypedOpInbound` with a synthetic `OpContext` (using a `service-cli` username and a synthetic ConnID).

For RoutePlayerCell ops that require an active session, the console command surfaces an error "this op requires an active player session — invoke from console while a player is logged in".

For RouteGatewayLocal ops, the synthetic context works fine.

### Task 5.2: Implement the rebuilt command

**Files:**
- Create: `pkg/universe/builtins_service.go` (fresh — deleted file is being recreated)
- Modify: `pkg/universe/coordinator.go` — add `registerServiceBuiltins(c, c.cfg.Console.Add)` to the builtins slice.

- [ ] **Step 1: Investigate the existing console-builtin pattern**

```bash
grep -rn "registerCellBuiltins\|registerPerfBuiltins\|registerLogBuiltins\|console\.Add\b" --include="*.go" pkg/universe/
```

Find one of the existing builtin registrations. Mirror its shape.

- [ ] **Step 2: Implement `service list`**

```go
package universe

import (
    "encoding/json"
    "fmt"
    "reflect"
    "sort"
    "strings"

    "github.com/zenion/mmokit/pkg/mmokit"
)

func registerServiceBuiltins(c *Process, add func(name, help string, fn ConsoleCommand)) {
    add("service", "service list/info/call — manage typed operations", func(args []string, out io.Writer) {
        if len(args) == 0 {
            fmt.Fprintln(out, "usage: service list | service info <op> | service call <op> '<json>'")
            return
        }
        switch args[0] {
        case "list":
            serviceList(out)
        case "info":
            if len(args) < 2 {
                fmt.Fprintln(out, "usage: service info <op>")
                return
            }
            serviceInfo(out, args[1])
        case "call":
            if len(args) < 3 {
                fmt.Fprintln(out, "usage: service call <op> '<json>'")
                return
            }
            serviceCall(c, out, args[1], strings.Join(args[2:], " "))
        default:
            fmt.Fprintf(out, "unknown subcommand: %s\n", args[0])
        }
    })
}

func serviceList(out io.Writer) {
    entries := mmokit.RegisteredTypedOps()
    sort.Slice(entries, func(i, j int) bool {
        return entries[i].RequestType.String() < entries[j].RequestType.String()
    })
    for _, e := range entries {
        fmt.Fprintf(out, "%-40s [%s] -> %s\n",
            opName(e.RequestType), e.Kind, e.ResponseType.String())
    }
}

func serviceInfo(out io.Writer, name string) {
    e := findOpByName(name)
    if e == nil {
        fmt.Fprintf(out, "unknown op: %s\n", name)
        return
    }
    fmt.Fprintf(out, "Op: %s\n", opName(e.RequestType))
    fmt.Fprintf(out, "Kind: %s\n", e.Kind)
    fmt.Fprintf(out, "Request fields:\n")
    printFields(out, e.RequestType, "  ")
    fmt.Fprintf(out, "Response fields:\n")
    printFields(out, e.ResponseType, "  ")
}

func serviceCall(c *Process, out io.Writer, name, jsonArgs string) {
    e := findOpByName(name)
    if e == nil {
        fmt.Fprintf(out, "unknown op: %s\n", name)
        return
    }

    // Allocate fresh *Req and JSON-unmarshal the input.
    reqPtr := reflect.New(e.RequestType)
    if err := json.Unmarshal([]byte(jsonArgs), reqPtr.Interface()); err != nil {
        fmt.Fprintf(out, "json parse: %v\n", err)
        return
    }

    // For RoutePlayerCell ops without an active player, return an error.
    if e.Kind == mmokit.RoutePlayerCell {
        fmt.Fprintf(out, "service call: %s requires RoutePlayerCell — use console while a player is logged in (not yet supported)\n", name)
        return
    }

    // Dispatch synchronously via the framework.
    body := ReflectMarshal(reqPtr.Interface())
    payload := encodeTypedOpRequestPayload(mmokit.TypeIDOf(e.RequestType), 0 /*requestID*/, body)

    ctx := &mmokit.OpContext{
        Username: "service-cli",
        ConnID:   0,
    }
    respFrame := DispatchTypedOpInbound(payload, ctx)
    if respFrame == nil {
        fmt.Fprintf(out, "service call: dispatcher returned nil (RoutePlayerCell async path?)\n")
        return
    }

    // Decode response: skip channel byte + typeID + request_id + body_len.
    if len(respFrame) < 17 {
        fmt.Fprintf(out, "service call: response frame too short\n")
        return
    }
    resTypeID := binary.LittleEndian.Uint32(respFrame[1:5])
    bodyLen := binary.LittleEndian.Uint32(respFrame[13:17])
    resBody := respFrame[17 : 17+bodyLen]

    if resTypeID == mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]()) {
        opErr := &mmokit.OperationError{}
        ReflectUnmarshal(resBody, opErr)
        fmt.Fprintf(out, "OperationError: code=%d %s\n", opErr.Code, opErr.Message)
        return
    }

    resPtr := reflect.New(e.ResponseType)
    ReflectUnmarshal(resBody, resPtr.Interface())
    out2, _ := json.MarshalIndent(resPtr.Interface(), "", "  ")
    fmt.Fprintln(out, string(out2))
}

func opName(t reflect.Type) string {
    name := t.String()
    name = strings.TrimSuffix(name, "Request")
    name = strings.TrimPrefix(name, "mmokit.")
    name = strings.TrimPrefix(name, "marketplace.")
    name = strings.TrimPrefix(name, "auth.")
    return strings.ToLower(name[:1]) + name[1:]
}

func findOpByName(name string) *mmokit.TypedOpEntry {
    for _, e := range mmokit.RegisteredTypedOps() {
        if opName(e.RequestType) == name {
            return e
        }
    }
    return nil
}

func printFields(out io.Writer, t reflect.Type, prefix string) {
    if t.Kind() != reflect.Struct {
        fmt.Fprintf(out, "%s(non-struct: %s)\n", prefix, t.String())
        return
    }
    for i := 0; i < t.NumField(); i++ {
        f := t.Field(i)
        fmt.Fprintf(out, "%s%s: %s\n", prefix, f.Name, f.Type.String())
    }
}

// encodeTypedOpRequestPayload builds the body of a 0x01 typed-op request
// (without the channel byte — DispatchTypedOpInbound expects channel-stripped).
func encodeTypedOpRequestPayload(typeID uint32, requestID uint64, body []byte) []byte {
    out := make([]byte, 4+8+4+len(body))
    binary.LittleEndian.PutUint32(out[0:4], typeID)
    binary.LittleEndian.PutUint64(out[4:12], requestID)
    binary.LittleEndian.PutUint32(out[12:16], uint32(len(body)))
    copy(out[16:], body)
    return out
}
```

- [ ] **Step 3: Wire the registration**

In `pkg/universe/coordinator.go`, find the builtins-registration block (search for `registerCellBuiltins\|registerPerfBuiltins`). Add `registerServiceBuiltins(c, ...)` to the list.

- [ ] **Step 4: Test from the running server**

```bash
just dev
# In the server console:
> service list
> service info marketBrowse
> service call marketBrowse '{"itemID": 42}'
```

Expected: list shows all registered ops; info shows fields; call returns a typed JSON response.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_service.go pkg/universe/coordinator.go
git commit -m "feat(universe): rebuild service console command

Replaces the deleted Plan 2 Phase 5 admin tool that depended on the
legacy Router.Schema(). New implementation iterates
mmokit.RegisteredTypedOps() and accepts JSON input for typed-arg
construction:

  service list
  service info <opName>
  service call <opName> '<json-args>'

RouteGatewayLocal ops dispatch synchronously through
DispatchTypedOpInbound. RoutePlayerCell ops require an active player
session and are deferred to a future enhancement (the console
goroutine has no player context to thread through engine.RunOnLoop)."
```

---

## Phase 6 — Final Acceptance + Merge

### Task 6.1: Full validation

- [ ] **Step 1: Full test suite**

```bash
go vet ./... && go test ./...
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
just build
```

- [ ] **Step 2: Browser smoke**

```bash
just dev
```

Run through the full feature matrix from Plan 2:
- Connect (cookie-auth → typed AuthLogin)
- Spawn confirmation (typed `PlayerEntityAssigned` or game-specific `PlayerSpawned`)
- Click-to-move
- Mining + ability cast
- Dock + undock
- Marketplace: browse, place sell/buy, cancel, my-orders, instant-trade
- Bank: deposit, withdraw, query
- Equip + loot + transfer
- Cell topology (CellChange typed event when crossing boundaries)

- [ ] **Step 3: Bot demo smoke**

```bash
go run ./cmd/botclient/duel
go run ./cmd/botclient/miners
```

Verify they connect, spawn, and exhibit their expected behavior.

- [ ] **Step 4: Distributed-mode smoke**

```bash
just distributed
```

Connect via gateway. Verify auth + marketplace + bank work cross-host.

- [ ] **Step 5: `service` console smoke**

In the server console:
```
service list
service info marketBrowse
service call authValidateToken '{"sessionToken": "..."}'
```

Verify each subcommand produces sensible output.

### Task 6.2: Update spec

```bash
# Find the spec status line
grep -n "Status\|Plan 2" docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md | head -5
```

Update:

```markdown
**Status:** Plan 1 (events channel + chat decomm) **landed** 2026-05-06; Plan 2 (operations channel + login → operation) **landed** 2026-05-07; Plan 3 (protobuf residue cleanup + bot rewire + service console) **landed** YYYY-MM-DD.
```

Add a brief Plan 3 outcomes section near the bottom of the file.

```bash
git add docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md
git commit -m "docs(spec): mark Plan 3 (protobuf residue cleanup) landed"
```

### Task 6.3: Merge to main

```bash
git checkout main
git merge --no-ff feat/mmokit-protobuf-residue-cleanup -m "Merge branch 'feat/mmokit-protobuf-residue-cleanup'

Plan 3 closes the residue from Plans 1+2:
- Migrated SE_PLAYER_SPAWNED, SE_CELL_CHANGE, SE_SERVER_CONFIG, CE_PING
  to typed reflection-codec; retired enginepb envelope types
- Rewired the bot client (broken since Plan 1 Phase 7) with HTTP
  cookie-auth + typed-event recv loop
- Rebuilt the 'service' console command against the typed-op registry

End state: zero protobuf bytes on any client-facing wire frame.
gen/go/ reduced to meshpb (server-internal). Plan 1+2's
out-of-scope items are all resolved."
```

---

## Acceptance Criteria

- `just build` clean; `go vet ./...` clean
- All Go tests pass
- Both TS typechecks clean
- `just dev` smoke flow passes the full feature matrix
- `just distributed` smoke passes login + cross-host marketplace op
- Bot demos (`go run ./cmd/botclient/duel`) connect and spawn successfully
- `service` console command returns typed responses for at least one op (e.g., `marketBrowse`)
- **Zero `proto.Marshal` / `proto.Unmarshal` calls outside of mesh-internal code paths** (`pkg/universe/grpc_bridge.go`, `pkg/universe/host_network.go`, `pkg/universe/coordinator.go` only)
- **`gen/go/` reduced** — `enginepb/` retains only `EntityMeshState` enum (or is deleted entirely if the enum migrates to a Go-native const block); `gamepb/` retains only enum-only types not on the wire (or is deleted); `meshpb/` is server-internal and unchanged
- Spec marked Plan 3 landed
- The 4 specific TODOs in Plan 1+2 are all resolved:
  - `cmd/server/main.go:101` Plan 5 TODO for CE_PING — gone
  - Plan 1 Phase 7 bot stale-recv-loop comment — gone
  - Plan 2 Phase 4 deferred-Login comment — gone (replaced by Plan 3 acknowledgment)
  - Plan 2 Phase 5 deleted-service-command note — gone (replaced by rebuild)

## Open design questions for the executor

If you hit surprises:

1. **`pkg/auth` cookie name** — The bot's cookie-auth flow needs the actual cookie name `pkg/auth/http.go` sets. If it differs from the assumption in Task 4.1, update `Authenticate` accordingly.
2. **Bot's transport: WS vs UDP** — Task 4.2 recommends switching to WebSocket. If `pkg/net/udpclient` has features the bot relies on (e.g., specific buffer semantics), the WS switch may need to preserve those. Investigate before implementing.
3. **`service call` for RoutePlayerCell ops** — The proposed implementation defers these. If a critical use case needs RoutePlayerCell support from console, the synthetic context could be enriched with a real player's ConnID + cell — but that requires console-to-cell threading work that's its own design exercise.
4. **`enginepb` reduction** — If `EntityMeshState` is the only surviving enum and the migration cost is small, consolidate to a Go-native const block (`pkg/universe/entity_mesh_state.go`) and delete `enginepb/` entirely. Skip if it adds friction.

If escalation is needed, surface BLOCKED with specifics.
