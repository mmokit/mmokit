# Events & Operations Channel Redesign

**Status:** design
**Date:** 2026-05-06
**Author:** brainstormed via session
**Supersedes parts of:** none — net-new framing for the post-Plan-G wire stack

## Background

The branch `feat/mmokit-entity-message-api` (merged to `main` 2026-05-06) landed Plans A→G of the mmokit redesign. Plan G migrated all client→server gameplay inputs off protobuf onto a typed reflection-codec frame on a new wire channel `0x02`. Plan F migrated server→client AoI-filtered broadcast events (Damage, MineExtract, Status, Killed) into the same reflection codec, but those still ride on the legacy protobuf `WorldUpdateMsg.events` envelope on channel `0x00`.

The result is a half-finished story:

- `0x00` (events) is two unrelated things glued together: an engine-message envelope (`protobuf(ServerEvent{code, data})` wrapping ~13 typed proto messages — login, spawn, death, docking, currency, bank contents, equip results, transfer results, map data, own-state, cell topology, world-update, delta-world-update) AND the carrier of broadcast events.
- `0x02` (client-input) is the typed reflection-codec channel that should be the future shape, but only carries inputs.
- A proposed `0x03` for batched broadcasts would add a third near-identical channel with no real conceptual reason to be separate from `0x02`.

This spec fixes that by reframing the wire stack along the **events / operations** split that Photon, EVE, and most production realtime frameworks use as a first-class architectural primitive.

## Conceptual model — events vs operations

Photon's framing ([forum.photonengine.com](https://forum.photonengine.com/discussion/1613/operations-vs-events)):

- **Operations** are RPCs. Client → server, with a response. Have a request_id for correlation, an opcode identifying which operation, and a return code on the response.
- **Events** are one-way messages. Server → client (or client → server with no response expected). No correlation, no return code.

Photon's guidance: *if the server is meant to do anything but forwarding the message, it is an operation; if it's only a packet relay or a fire-and-forget notification, it's an event.*

Mapping to this codebase:

- **Events** = anything the game loop emits or consumes. Server-pushed state changes (spawn, death, docking transitions, currency changes, equipment results), per-tick broadcasts (Damage, MineExtract, Status, Killed), per-tick own-state pushes, the entity-state delta frame, and client-side game-loop inputs (move, cast, mine, dock, undock, respawn, equip, loot, jettison, chat-input). All fire-and-forget.
- **Operations** = req/res or cross-service. Login (auth service), marketplace orders, bank operations, future GM commands, future shop interactions, future chat-service interactions. Each has a request_id and a typed response.

Two channels, two concepts. End state.

## Wire format

### Channel 0x00 — Events

```
[0x00]
  ┌── repeated until end-of-WebSocket-message ──┐
  │  [typeID:   u32 BE] FNV-1a hash of Go type   │
  │  [body_len: u32 BE]                          │
  │  [body:     N bytes] reflect-codec marshalled │
  └─────────────────────────────────────────────┘
```

- No event-count prefix. WebSocket gives you the message length; consume to end. Same parse loop in either direction.
- A frame can carry one event (single push, single input) or many (batched broadcasts per viewer per tick).
- typeID is the FNV-1a 32-bit hash of the fully-qualified Go type name. Identical to today's 0x02 typeID — same registry, same reflect codec, same `broadcasts.ts` codegen output.
- `body_len` is `u32` to match Plan G's existing 0x02 wire format. Bodies don't realistically exceed 64KB, but consistency beats one byte saved.

### Channel 0x01 — Operations

```
[0x01] + protobuf(OpRequest{op_code, request_id, body})       client → server
[0x01] + protobuf(OpResponse{op_code, request_id, status, body})   server → client
```

**Unchanged from today's marketplace/bank format.** This branch does not redesign operations. It only adds Login as a new operation alongside existing marketplace/bank ones.

A future plan migrates 0x01 bodies to typed reflection-codec (same FNV-1a typeID + body_len + body shape, plus a request_id field), but that's out of scope here.

### Retired channels

- **0x02 `ChannelClientInput`** — subsumed into 0x00. The shape was already consume-to-end with a single message; it just becomes one valid configuration of an event frame instead of its own channel.
- **0x03** never created.

### Why batched broadcasts go on 0x00

Research on production realtime frameworks (Mirror, Source, Quake 3, Glenn Fiedler, Roblox/Colyseus) is unanimous: **batch per-tick per-viewer over reliable transport.** WebSocket framing tax is 2-14 bytes/frame; with 10-30 byte event bodies that's >50% overhead per frame if unbatched. TCP Nagle/delayed-ACK pathology compounds the problem ([Marc Brooker — It's always TCP_NODELAY](https://brooker.co.za/blog/2024/05/09/nagle.html)). Userspace per-tick batching with `TCP_NODELAY` on is the standard answer.

Operationally:

- Verify `TCP_NODELAY` is set on the WebSocket-underlying TCP conn (Go's `net/http` server enables it by default; one-line `SetNoDelay(true)` after upgrade is cheap insurance).
- Per-viewer per-tick `afterSend` hook builds one 0x00 frame containing all AoI-passed events for that viewer and writes once. Empty frames are skipped (don't emit a 1-byte channel-only frame).

## In-scope migrations

### Server → client engine messages (move from `protobuf(ServerEvent)` envelope on 0x00 to typed events on 0x00)

| Today | Becomes | Notes |
|---|---|---|
| `gamepb.PlayerSpawnedMsg` | typed event `PlayerSpawned` | game-loop entry |
| `gamepb.PlayerDiedMsg` | typed event `PlayerDied` | already broadcast as `Killed` event for visibility — `PlayerDied` is the owning-player private notification |
| `gamepb.DockingStateMsg` / `DockedMsg` | typed events `DockingState` / `Docked` | |
| `gamepb.CurrencyUpdateMsg` | typed event `CurrencyUpdate` | |
| `gamepb.BankContentsMsg` | typed event `BankContents` | pushed after a bank op completes |
| `gamepb.EquipResultMsg` | typed event `EquipResult` | |
| `gamepb.TransferResultMsg` | typed event `TransferResult` | inventory transfer feedback |
| `gamepb.MapDataMsg` | typed event `MapData` | one-shot after spawn |
| `gamepb.PlayerOwnStateMsg` | typed event `PlayerOwnState` | per-tick own-entity push |
| `enginepb.CellTopologyMsg` | typed event `CellTopology` | pushed on topology change |
| `gamepb.WorldUpdateMsg` | **deleted entirely** | broadcasts → typed events; chat decomm'd; `tick`/`ack_input_seq` were already dead |
| `SE_DELTA_WORLD_UPDATE` (binary frame in `protobuf(ServerEvent)` wrapper) | typed event `WorldDelta` carrying the existing binary body unchanged | strips the protobuf envelope; body bytes (20-byte header + per-entity FullEntry/DeltaEntry/Removed/Exited) stay as-is |

The auto-broadcast pipeline (`pkg/universe/broadcast_queue.go`, `pkg/mmokit/broadcast.go`) continues to drive AoI-filtered per-viewer event sets per tick. The `afterSend` hook (`internal/game/system_network.go:159`) now writes one 0x00 frame containing all AoI-passed events instead of building a `WorldUpdateMsg` and sending via `ServerEvents().Build/Send`.

### Client → server inputs (already on 0x02, just channel byte changes)

`MoveTo`, `CastAbility`, `Mine`, `Dock`, `Undock`, `Respawn`, `BankRequest`, `Equip`, `LootItem`, `LootAll`, `JettisonItem`, `Chat` — wire shape is identical to Plan G's 0x02 format. Only the channel byte changes from 0x02 to 0x00. SDK codegen and reflection codec are reused unchanged.

`BankRequest` is a borderline case — it's a request that produces a `BankContents` push as the result. By Photon's strict reading it's an operation. Pragmatically, today it's modelled as input + later push (the decoupled pattern Photon explicitly recommends), and that pattern survives the migration unchanged. Keep as event for this branch; revisit when the typed-ops migration lands.

### Login → operation on 0x01

- `gamepb.LoginMsg` (input) → `LoginRequest` op on 0x01. Body fields: `username string` (used as a fallback only — the cookie is the primary credential; username on the wire is ignored when a valid cookie is present, kept for the legacy no-cookie flow). Once the auth-cookie integration in `examples/4node-basic/web/src/auth.ts` is treated as universal and the no-cookie flow is removed, `username` can drop too.
- `gamepb.LoginRejectedMsg` (push on 0x00) → carried inside the `LoginResponse` op-response on 0x01: response has `status` indicating accepted/rejected and `body` carrying either accepted-payload (user_id, username, spawn target) or rejected-reason.
- `gamepb.PlayerSpawnedMsg` is a follow-up event on 0x00 once the cell has spawned the entity — fired regardless, same as today; this is the decoupled pattern (op returns OK, then state push events follow).

**Routing:** the gateway inspects the inbound `OpRequest.op_code`. If it's `Login`, dispatch to the gateway-local `LoginHandler` callback (no cell forwarding) and return an `OpResponse` immediately. If it's marketplace/bank, dispatch to `OpRouter` (forwards to the player's authoritative cell). One switch in the gateway's read path.

The gateway's existing `Config.LoginHandler` keeps its current signature: `func(payload []byte) (username string, sessionData any, err error)`. Only the wire framing changes — what hits the handler is the deserialized `LoginRequest` body bytes instead of the raw 0x00 frame body.

## Server-side chat decommission

User intent: chat moves to its own service (modeled like the auth service); decomm the in-engine implementation entirely; leave the client-side chat HUD/input UI in place so the future service can wire up to existing DOM.

Deletions:

- `proto/enginepb/engine.proto` — remove `ChatMsg`.
- `internal/game/input_messages.go` — remove the `Chat` typed input struct.
- `internal/game/input_handlers.go:121-153` — remove the Chat HandleClient registration.
- `internal/game/system_network.go:131,143-148,189-202` — remove chat-queue peek/drain, beforeSend chat send, afterTick docked-player chat send.
- `internal/game/logcat.go` — remove `CatPlayerChat` (or keep if it has other producers — verify).
- Bridge plumbing — remove `RelayChatToOtherCells` from `pkg/universe/bridge.go` and `pkg/universe/cell_bridge_impl.go`.
- `web-pixi/src/network.ts:298-316` — remove `client.onWorldUpdate` chat-display handler.
- `web-pixi/src/input.ts` — remove the chat-input-box `client.send(Chat, ...)` call site (input box DOM stays, but its submit handler is unwired).
- `examples/4node-basic/web/sdk/inputs.ts` and `web-pixi/sdk/inputs.ts` — `Chat` class generated by sdkgen disappears once its server-side type is deleted.
- The chat HUD `<div>` and the input box DOM elements stay in HTML so the future chat service has somewhere to wire up.

End state: client UI shells exist; nothing sends or receives chat. When the chat service ships, its design will define new send/receive primitives (HTTP+SSE, dedicated WS, or operations on 0x01) — that's a separate spec.

## Server-side API impact

`mmokit` facade (`pkg/mmokit/`):

- `RegisterServerEvent[T](e *ServerEvents, code uint32)` — verb stays, semantics change: the `code uint32` arg is dropped (the typeID is computed from `T` via the same FNV-1a hash already used by client-input registrations). Become `RegisterServerEvent[T](e *ServerEvents)`. The protocol registry continues to back `--dump-schema` for codegen.
- `ServerEvents.Build/Send` — `Build` is deleted (no envelope to build). `Send` becomes a thin wrapper around the typed-event encoder; eventually deleted in favour of direct `Stage.SendEvent[T](connID, *T)` typed sends.
- `Stage.SendEvent(connID, code, msg)` — current shape is `(connID uint32, code uint32, msg proto.Message)`. New shape is `Stage.SendEvent[T](connID uint32, msg *T)`. Code arg goes away; typeID is implicit in the type.

`pkg/universe/`:

- `gateway.go` read-loop dispatches on channel byte: 0x00 → typed-event reflect codec, 0x01 → OpRequest router with Login/marketplace/bank op_code switch.
- `client_input_dispatch.go` becomes the universal typed-event dispatcher for both directions on 0x00.
- `client_input_hooks.go`, `broadcast_hooks.go`, `broadcast_queue.go` retain their game-side hook surface but write through the unified codec path.

## Client-side API impact

`web-pixi/sdk/` (auto-generated by `cmd/sdkgen/`):

- `client.ts` — `onWorldUpdate` deleted. Per-event `onPlayerSpawned`, `onPlayerDied`, `onDockingState`, etc. handlers generated for every server-side typed event. `client.typedEvents.on(Damage, ...)` API surface unchanged for broadcasts.
- `transport.ts` — channel constants reduced to `CH_EVENT = 0x00`, `CH_OPERATION = 0x01`. `CH_CLIENT_INPUT` removed. Single read-loop dispatcher per channel.
- `inputs.ts` (client→server) and `broadcasts.ts` (server→client) — kept separate by direction for readability. Both use identical encode/decode primitives. `broadcasts.ts` gains the migrated server-event classes (`PlayerSpawned`, `PlayerDied`, `DockingState`, etc.) alongside the existing AoI broadcast classes (`Damage`, `MineExtract`, `Status`, `Killed`).
- A new `operations.ts` generated module exposes `client.login(req: LoginRequest): Promise<LoginResponse>` and the existing marketplace/bank op call sites continue working via the existing OpRouter client.

## Codegen impact

`cmd/sdkgen/`:

- `inputs.go` and `broadcasts.go` — body-encoding logic stays. Channel byte changes from 0x02 to 0x00 in the emitted `client.send(...)` wrapper. Per-event `client.on*` handlers added for server-side events (today only `onWorldUpdate` exists).
- A new generator section emits typed-event server-push handlers — symmetric to client-input emission, just opposite direction.
- Login operation generator — could be deferred; for this branch hand-write the `client.login()` wrapper since it's one operation. The future typed-ops plan introduces formal codegen.
- `--dump-schema` adds a new section listing server-side typed events with their typeIDs (parallel to the existing client-input section). The protocol registry in `mmokit.NewProtocol(...)` remains the source of truth.

## Testing strategy

- **Unit:** new tests for the unified 0x00 dispatcher in `pkg/universe/` covering: single-event frames in either direction; multi-event batched frames (server→client only); mixed-direction smoke (one client-input frame interleaved with one server-push frame).
- **Migration regressions:** every retired protobuf message gets a "still works in the new shape" test — port the existing `*_test.go` cases for spawn, death, docking, currency, etc. to the typed-event shape.
- **End-to-end:** the existing `examples/4node-basic` smoke flow (browser test) is the integration check. After the migration, run `just dev` and verify: connect, spawn, click-to-move, mining, ability cast, dock, undock, respawn, marketplace browse/place/cancel, equip, loot, transfer, login rejection on duplicate username. Distributed-mode (`just distributed`) verifies the gateway↔host MeshData forwarding still works after the channel-byte change in `gateway.go:forwardChannel` (already routes by channel, so the change is one-line: 0x02 entries route the same as 0x00 entries do today).
- **Wire-format invariants:** `pkg/universe/typed_message_codec_test.go` extended to cover round-trips for every migrated message type (spawn, death, docking, …).

## Migration order

The migration is one logical change touching many files but with low coupling between message types. Proposed phasing **inside** the branch (each phase ends in a clean compile + pass).

**Coexistence note:** during phases 2-4, channel 0x00 is dual-purpose (legacy `protobuf(ServerEvent)` envelope alongside new typed-event frames). The dispatcher distinguishes by the first byte of the frame body: a typed-event frame starts with a u32 typeID (high bytes typically non-zero given FNV-1a), whereas a legacy ServerEvent's first byte is the protobuf field tag (always `0x08`, varint marker for field 1 = `code`). This is enough to disambiguate during the transition. Phase 8 deletes the legacy path and the disambiguation along with it. During phases 2-5 inputs are still on 0x02 — both channels are live; phase 5 collapses them.

1. **Foundation** — extend the typed-event registry and codec to support server→client direction. Add the unified 0x00 dispatcher in `pkg/universe/`. Add the `Stage.SendEvent[T]` typed primitive. No callers migrate yet; old paths still work alongside.
2. **Broadcasts** — migrate the auto-broadcast pipeline to write 0x00 batched frames. Retire `WorldUpdateMsg.events`. WorldUpdateMsg still exists carrying chat for now.
3. **Server→client engine events** — migrate ~13 messages off the protobuf-envelope path. Most are mechanical: replace `mmokit.RegisterServerEvent[T]` + `Build/Send` with the typed-event primitive. Each migration ships green.
4. **WorldDelta** — strip the protobuf envelope from `SE_DELTA_WORLD_UPDATE`; ship body as typed-event payload.
5. **Inputs channel-byte change** — flip 0x02 → 0x00 in client SDK and gateway. Single-commit, runnable end-to-end.
6. **Login → operation** — define `LoginRequest` / `LoginResponse` op protos; gateway routes Login op_code to `LoginHandler`; SDK exposes `client.login(...)`. Retire `gamepb.LoginMsg` / `gamepb.LoginRejectedMsg`.
7. **Chat decomm** — delete server-side chat code, `enginepb.ChatMsg`, related plumbing. Client UI shells stay.
8. **Cleanup** — delete `WorldUpdateMsg` proto entirely, retire `0x02` channel constants, retire `gamepb.TypedEvent`, retire `enginepb.ServerEventCode_SE_*` enums for migrated messages.

## Out of scope

- **Operations channel typed-bodies migration.** Marketplace/bank ops stay on 0x01 protobuf for this branch. A follow-up plan migrates op bodies to the typed reflection codec and adds a formal request/response correlation primitive that retires the OpRouter's bespoke shape.
- **Unity client.** The Unity client codegen is dormant (no active use). When it returns, codegen emits typed-event handlers parallel to TS — out of scope here.
- **Client-side input prediction / reconciliation.** Today every input is server-confirmed. Adding prediction is a feature, not a wire-format change.
- **Cached events** (Photon-style — events that late-joiners receive on connect). Not needed today; revisit if a use case appears.
- **Chat service.** Decommissioning the existing chat is in scope; building the new chat service is its own design + plan.

## Open questions

- **`BankRequest` placement** — strict-Photon says it's an operation (req/res with `BankContents` as the response). Today it's modelled as event in + event out, which is the explicit decoupled pattern Photon recommends. Keep as event for this branch; revisit when typed-ops lands. Decision: **as-is**.
- **TypeID collision risk** — FNV-1a 32-bit on Go type names. With ~30 message types we're well below birthday-paradox concern (probability of collision is < 1e-7 at this scale). If a collision is ever detected at registry-build time, panic with a clear message and the user renames. Decision: **don't pre-empt.**
- **`MaxFrameSize`** — the 0x00 dispatcher should refuse frames over some sane upper bound (defensive against malformed clients sending u32-max body_len). Suggest 64KB per individual event body, no batch-frame ceiling beyond what WebSocket already enforces. Decision: **plan-time**.

## File-level change summary

Server (Go):

- Modify: `pkg/universe/{gateway,client_input_dispatch,broadcast_hooks,broadcast_queue,client_input_hooks,virtual_conn_manager}.go`
- Modify: `pkg/mmokit/{protocol,server_events,handle_client,handle_internal,broadcast,messaging*,init,mmokit}.go`
- Modify: `pkg/net/conn.go` (channel constants)
- Modify: `cmd/server/main.go` (RegisterServerEvent calls)
- Modify: `internal/game/{system_network,input_handlers,input_messages,hooks,gameworld}.go`
- Delete: `proto/gamepb/game.proto` entries — `WorldUpdateMsg`, `TypedEvent`, `LoginMsg`, `LoginRejectedMsg`, `PlayerSpawnedMsg`, `PlayerDiedMsg`, `DockingStateMsg`, `DockedMsg`, `CurrencyUpdateMsg`, `BankContentsMsg`, `EquipResultMsg`, `TransferResultMsg`, `MapDataMsg`, `PlayerOwnStateMsg`
- Delete: `proto/enginepb/engine.proto` — `ChatMsg`, all `SE_*` enum values made obsolete
- Modify (regen): `gen/go/gamepb/`, `gen/go/enginepb/`, `gen/es/gamepb/`, `gen/es/enginepb/`

Client (TS):

- Modify: `web-pixi/sdk/{client,transport,inputs,broadcasts,index}.ts`
- Modify: `web-pixi/src/{network,state}.ts` (per-event handler wiring)
- Add: `web-pixi/sdk/operations.ts` (login wrapper)
- Mirror in: `examples/4node-basic/web/sdk/`

Codegen (Go):

- Modify: `cmd/sdkgen/{generate,broadcasts,inputs,schema,main}.go` — add server-event emission; merge channel-byte logic.

Tests:

- Modify: `pkg/universe/typed_message_codec_test.go`, `pkg/mmokit/broadcast_test.go`, `pkg/mmokit/client_input_test.go`, `pkg/mmokit/messaging*_test.go`
- Add: cross-direction round-trip tests for every migrated server-event type.

## Acceptance criteria

- `just build` produces a clean binary; `go vet ./...` clean; web-pixi `tsc --noEmit` clean.
- `just dev` smoke flow passes for: connect, spawn, click-to-move, mining, ability cast, status effects (DoT visualization), dock, undock, respawn, marketplace browse/place/cancel/instant-trade, equip, loot, inventory transfer, jettison, login (cookie-resume + register + login-existing + duplicate-rejection).
- `just distributed` smoke flow passes for: cross-host login, cross-host handoff (walk a player across cell boundary), cross-host operation (place a market order on a remote cell).
- No protobuf wrapper on any 0x00 frame. `WorldUpdateMsg`, `TypedEvent`, `LoginMsg`, `LoginRejectedMsg`, `ChatMsg`, all migrated `SE_*` codes deleted from proto. Channel 0x01 retains its protobuf `OpRequest`/`OpResponse` envelope unchanged.
- Two channels in `pkg/net/conn.go`: `ChannelEvent = 0x00`, `ChannelOperation = 0x01`. `ChannelClientInput` retired.
