# Events & Operations Channel Redesign

**Status:** Plan 1 (events channel + chat decomm) **landed** 2026-05-06; Plan 2 (operations channel + login → operation) pending.
**Date:** 2026-05-06
**Author:** brainstormed via session
**Supersedes parts of:** none — net-new framing for the post-Plan-G wire stack

## Plan 1 outcomes (events channel)

Branch `feat/mmokit-events-channel`, 30 commits, 114 files changed (+5109/-2877). All Go tests pass; both web-pixi and 4node-basic typecheck clean; `just build` produces a clean binary.

What landed:
- Channel `0x00` is now the unified typed-message channel — both directions, consume-to-end frames of `[typeID:u32 LE][bodyLen:u32 LE][body]`. Wire format mirrors Plan G's `0x02` typed-input shape exactly.
- 13 server-pushed messages migrated off the protobuf `ServerEvent` envelope onto typed reflection-codec frames: PlayerSpawned, PlayerDied, DockingState, Docked, CurrencyUpdate, BankContents, EquipResult, TransferResult, MapData, PlayerOwnState, CellTopology→DebugInfo, Pong, LoginRejected.
- WorldDelta (the per-tick entity-state delta frame) sheds its protobuf wrapper. Body bytes pass through verbatim via a new `bytes` fast-path codec on `[]byte` fields (avoids the generic-slice `[u16 len][per-elem]` overhead).
- Channel `0x02` (`ChannelClientInput`) **retired**. All typed inputs now flow on `0x00`; the gateway's `forwardChannel` no longer drains 0x02; `pkg/net/conn.go` declares only `ChannelEvent (0x00)` and `ChannelOperation (0x01)` constants.
- Server-side chat **decommissioned**: HandleClient[Chat] handler, pending-chat queue, beforeSend/afterTick send loops, `RelayChatToOtherCells` bridge, `enginepb.ChatMsg`, `CatPlayerChat` log category — all deleted. Client-side chat HUD `<div>` and input box DOM remain as UI shells; receive handler + send call deleted.
- All migrated proto messages deleted: `gamepb.WorldUpdateMsg`, `TypedEvent`, `PlayerSpawnedMsg`, `PlayerDiedMsg`, `DockingStateMsg`, `DockedMsg`, `BankContentsMsg`, `EquipResultMsg`, `TransferResultMsg`, `MapDataMsg`, `PlayerOwnStateMsg`, `EntityState`+sub-messages, `CurrencyUpdateMsg`. Migrated `SE_*`/`GSE_*` enum entries deleted; surviving entries renumbered from 1.
- `pkg/mmokit/server_events.go` `Build` and `Send` methods deleted (no callers); `MakeEvent` deleted.
- `cmd/sdkgen/typedShadowedServerEvents` Phase-3-transition filter deleted.
- New foundation pieces: `mmokit.RegisterEvent[T]()`, `mmokit.SendEvent(stage, connID, msg)`, `pkguniverse.SendEventTyped`, `pkguniverse.BuildTypedEventFrameRaw`, `pkguniverse.DispatchInboundEventFrame`, slice + nested-struct support in the reflect codec, `bytes` fast-path codec for `[]byte` fields.

Deferred to a follow-up branch (out of scope for Plan 1):
- **Login (`CE_LOGIN` / `LoginMsg`)** — Plan 2 Phase 4 (2026-05-06) cleaned up the dead Plan-1-deferred Login artifacts: deleted `enginepb.LoginMsg` + `CE_LOGIN`, the `mmokit.LoginRejected` typed event + registration, the `RegisterClientEvent[enginepb.LoginMsg]` schema-export entry in `cmd/server/main.go`, the bot's dead `b.sendEvent(CE_LOGIN, ...)` call (and now-orphaned `sendEvent` helper). Login already lives as the `AUTH_OPCODE_LOGIN` typed op in `pkg/auth/service.go`; migrating that and the other 4 auth ops from the legacy `ops.Register` proto-shape to the typed `mmokit.RegisterOp[Req, Res]` shape is Phase 5 work.
- **`CE_PING` inbound** — handled inline by the gateway's `EventInterceptor` on the read goroutine; migrating requires either a parallel `ClientInputInterceptor` or extending the hook to disambiguate by typeID. Outbound `Pong` is already typed. Marked with TODO at `cmd/server/main.go`.
- **Bot client (`internal/bot/`)** — recv loop is a no-op shell after Phase 7 (TODO carried forward); Connect now also times out at the spawn wait because login was deleted. Bot rewire to typed events + auth-op login is a separate task; bot is a load-test tool, not on Plan 1's critical path.

Surviving framework events on the legacy `ServerEvent` envelope: `SE_PLAYER_SPAWNED` (engine default, used by `examples/simple` non-overriding games), `SE_CELL_CHANGE`, `SE_SERVER_CONFIG`. The dual decode path in client.ts (peek payload[0] === 0x08) is retained for these. Plan 2 may retire them.



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
- **Operations** = req/res, transactional, or cross-service. Login (auth service), marketplace orders, **bank operations** (transactional financial state — race/exploit-sensitive, plausibly migrates off the eventloop to a dedicated service later), future GM commands, future shop interactions, future chat-service interactions. Each has a request_id and a typed response.

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
[0x01]
  ┌── one entry per WebSocket message ──────────┐
  │  [typeID:     u32 BE] FNV-1a hash             │
  │  [request_id: u64 BE] correlation token       │
  │  [body_len:   u32 BE]                         │
  │  [body:       N bytes] reflect-codec          │
  └─────────────────────────────────────────────┘
```

Same wire shape in both directions. Direction (client→server vs server→client) is implicit from the side parsing the frame. The receiver determines request-vs-response from the typeID — each operation registers a `Request → Response` type pair, so a known typeID is unambiguously one or the other.

**Status / errors.** No wire-level status field. A response body type may carry success/error fields per its domain (e.g., `MarketOrderResultResponse` has its own `success` + `error` fields today and that survives migration). For framework-level errors (unknown typeID, handler returned err, deserialization failure), a generic typed response `OperationError{code: u32, message: string}` is sent — the client framework intercepts this typeID and rejects the matching pending promise by `request_id`.

**Routing kinds.** Each registered operation declares a `RouteKind`:

- `RouteGatewayLocal` — handler runs on the gateway, no cell forwarding. Used by Login (no player cell yet at handshake time).
- `RoutePlayerCell` — handler runs on the player's authoritative cell via `RunOnLoop`. Used by marketplace, bank, future GM commands.

Same model as `cmdsys.Command.RouteKind` — uniform routing taxonomy across the codebase.

**Out: today's `protobuf(OpRequest{op_code, request_id, body})` envelope.** No more `OpRequest` / `OpResponse` proto types, no more enum-based op codes. typeID + reflection codec everywhere.

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

### Operations on 0x01 — full migration to typed bodies

Every existing operation migrates from protobuf to typed reflection-codec. No protobuf envelope, no enum-based op codes, no `OpRequest`/`OpResponse` proto types. Each op registers `Request → Response` typed Go struct pairs.

**Login → `RouteGatewayLocal`:**

- `gamepb.LoginMsg` → typed `LoginRequest`. Body fields: `username string` (fallback only — the cookie is the primary credential; username on the wire is ignored when a valid cookie is present, kept for the legacy no-cookie flow). Once cookie-auth in `examples/4node-basic/web/src/auth.ts` is universal and the no-cookie flow is removed, `username` can drop.
- `gamepb.LoginRejectedMsg` → carried inside typed `LoginResponse`. Body: `accepted bool`, `user_id string`, `username string`, `spawn_x f32`, `spawn_y f32`, `error string` (populated on rejection).
- Gateway routes typeID = `LoginRequest` to the existing `Config.LoginHandler` callback, builds a `LoginResponse`, and writes back on 0x01.
- `gamepb.PlayerSpawnedMsg` is a follow-up *event* on 0x00 once the cell has spawned the entity — fired regardless, same as today (op returns OK, then state-push events follow).

**Marketplace → `RoutePlayerCell`:**

- `MarketBrowseRequest` → `MarketOrderBookResponse`
- `MarketCreateOrderRequest` → `MarketOrderResultResponse`
- `MarketCancelOrderRequest` → `MarketOrderResultResponse`
- `MarketMyOrdersRequest` → `MarketMyOrdersResponse`
- `MarketInstantTradeRequest` → `MarketOrderResultResponse`

Each becomes a typed Go struct. The handler signature stays the same shape as today (`pkg/ops/router.go:197`):

```go
mmokit.RegisterOp[MarketBrowseRequest, MarketOrderBookResponse](
    coord.Ops, mmokit.RoutePlayerCell, handler)
```

(verb name TBD-but-committed: `RegisterOp[Req, Res]`. The existing `ops.Register[Req, Res, Code, ReqP, ResP]` retires; the new shape drops the `code` arg — typeIDs are computed — and drops the `ProtoMessage` constraint.)

**Bank → `RoutePlayerCell` operation:**

- `BankRequest` (today: 0x02 typed input with a `kind` discriminator for deposit/withdraw/query) → typed `BankRequest` op on 0x01. Same Go-struct shape, just registered as an op rather than an input handler.
- `BankResponse` — new typed response carrying `contents: BankContents`, `error: string` (empty on success). Returned synchronously from the op handler.
- `BankContents` event push remains on 0x00 as a typed event for **out-of-band** state changes only (admin/GM commands, future automated payouts, cross-process bank service notifications). The vast majority of bank state arrives via op responses, not events.

**Why operation, not event:** bank operations are transactional financial state mutations. Race/exploit hardening matters: the framework needs an unambiguous serialization point per request, with a guaranteed response that carries the post-mutation state. Modeling as event in + event out leaves the client without a per-request anchor (which response goes with which request?), and any future migration of bank to a dedicated non-eventloop backend (mirroring the auth-service pattern) requires a real request/response framing. Better to land that framing now than refactor later. Same body type (`BankContents`) reused as both the op response payload and the event push payload — the framework handles framing per channel, the underlying typeID and reflect-codec bytes are identical.

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

- `RegisterServerEvent[T](e *ServerEvents, code uint32)` — verb stays, semantics change: the `code uint32` arg is dropped (typeID is computed from `T` via FNV-1a). Becomes `RegisterServerEvent[T](e *ServerEvents)`. The protocol registry continues to back `--dump-schema` for codegen.
- `ServerEvents.Build/Send` — `Build` is deleted (no envelope to build). `Send` becomes a thin wrapper around the typed-event encoder; eventually deleted in favour of direct `Stage.SendEvent[T](connID, *T)` typed sends.
- `Stage.SendEvent(connID, code, msg)` — current shape is `(connID uint32, code uint32, msg proto.Message)`. New shape is `Stage.SendEvent[T](connID uint32, msg *T)`. Code arg goes away.
- **New: `RegisterOp[Req, Res any](r *OpRouter, kind RouteKind, handler func(*OpContext, *Req) (*Res, error))`** — replaces today's `ops.Register[Req, Res, Code, ReqP, ResP]`. typeID is computed from `Req` and `Res` types; no `code` arg, no proto-message constraint. `RouteKind` is one of `RouteGatewayLocal` or `RoutePlayerCell`.
- **`OpRouter` internals** — switches from a `map[uint32]handler` keyed by op code to a `map[uint32]handler` keyed by request typeID. Response typeIDs are emitted on the wire; the client framework correlates them back to pending requests by `request_id`. Server side is symmetric.

`pkg/ops/`:

- `Register` is renamed/restructured to drop the proto constraint; or kept as a deprecated proto-only alias and a new `RegisterTyped` is introduced — decision for the plan author. The simpler endpoint is to delete `Register` and inline the typed shape.

`pkg/universe/`:

- `gateway.go` read-loop dispatches on channel byte: 0x00 → typed-event reflect codec, 0x01 → OpRequest router with Login/marketplace/bank op_code switch.
- `client_input_dispatch.go` becomes the universal typed-event dispatcher for both directions on 0x00.
- `client_input_hooks.go`, `broadcast_hooks.go`, `broadcast_queue.go` retain their game-side hook surface but write through the unified codec path.

## Client-side API impact

`web-pixi/sdk/` (auto-generated by `cmd/sdkgen/`):

- `client.ts` — `onWorldUpdate` deleted. Per-event `onPlayerSpawned`, `onPlayerDied`, `onDockingState`, etc. handlers generated for every server-side typed event. `client.typedEvents.on(Damage, ...)` API surface unchanged for broadcasts.
- `transport.ts` — channel constants reduced to `CH_EVENT = 0x00`, `CH_OPERATION = 0x01`. `CH_CLIENT_INPUT` removed. Single read-loop dispatcher per channel.
- `inputs.ts` (client→server, 0x00) and `broadcasts.ts` (server→client, 0x00) — kept separate by direction. Both use identical encode/decode primitives. `broadcasts.ts` gains the migrated server-event classes (`PlayerSpawned`, `PlayerDied`, `DockingState`, etc.) alongside the existing AoI broadcast classes (`Damage`, `MineExtract`, `Status`, `Killed`).
- `operations.ts` (new module, 0x01) — typed wrappers for every registered op: `client.login(req: LoginRequest): Promise<LoginResponse>`, `client.marketBrowse(req): Promise<MarketOrderBookResponse>`, `client.marketCreateOrder(req): Promise<MarketOrderResultResponse>`, etc. Each returns a Promise resolved when the matching `request_id` response arrives, or rejected on `OperationError`. Replaces the existing op-router client code.
- A new `operations.ts` generated module exposes `client.login(req: LoginRequest): Promise<LoginResponse>` and the existing marketplace/bank op call sites continue working via the existing OpRouter client.

## Codegen impact

`cmd/sdkgen/`:

- `inputs.go` and `broadcasts.go` — body-encoding logic stays. Channel byte changes from 0x02 to 0x00 in the emitted `client.send(...)` wrapper. Per-event `client.on*` handlers added for server-side events (today only `onWorldUpdate` exists).
- A new generator section emits typed-event server-push handlers — symmetric to client-input emission, just opposite direction.
- **New `operations.go` generator** — emits `operations.ts`. For each registered op, emits: a `Request` class (with `encode()`), a `Response` class (with `decode()`), and a `client.<opName>(req): Promise<Response>` wrapper. The wrapper allocates a request_id, encodes the request to 0x01, registers a pending-promise entry keyed by request_id, awaits the matching response or `OperationError`, decodes, resolves/rejects.
- `--dump-schema` adds two new sections: server-side typed events (parallel to client-input), and operations (request typeID + response typeID + RouteKind). The protocol registry in `mmokit.NewProtocol(...)` remains the source of truth.

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
6. **Operations foundation** — new typed-ops codec (the 0x01 wire format above), `RegisterOp[Req, Res]` API, `RouteKind` enum, `OperationError` framework type, request_id correlator on the client framework. Old protobuf `OpRequest`/`OpResponse` path coexists.
7. **Marketplace + bank ops migration** — port the 5 marketplace ops from `pkg/ops/Register` to `mmokit.RegisterOp` with `RoutePlayerCell`. Migrate `BankRequest` from typed input (0x02) to a `RoutePlayerCell` op with a typed `BankResponse` carrying the post-mutation `BankContents`. Convert all proto `Market*Request`/`Market*Response` and `BankRequest` types to typed Go structs. SDK regenerates `operations.ts`. The `BankContents` event push on 0x00 stays for out-of-band state changes.
8. **Login → operation** — define typed `LoginRequest` / `LoginResponse`; register with `RouteGatewayLocal`; gateway routes Login typeID to `LoginHandler` and writes the typed response. Retire `gamepb.LoginMsg` / `gamepb.LoginRejectedMsg`.
9. **Chat decomm** — delete server-side chat code, `enginepb.ChatMsg`, related plumbing. Client UI shells stay.
10. **Cleanup** — delete `WorldUpdateMsg`, `TypedEvent`, `OpRequest`, `OpResponse` protos entirely. Retire `ChannelClientInput` (0x02). Retire `enginepb.ServerEventCode_SE_*` enums and op-code enums. Retire the deprecated `pkg/ops/Register` shape if a deprecation alias was kept during phasing.

## Out of scope

- **Unity client.** The Unity client codegen is dormant (no active use). When it returns, codegen emits typed-event + typed-op handlers parallel to TS — out of scope here.
- **Client-side input prediction / reconciliation.** Today every input is server-confirmed. Adding prediction is a feature, not a wire-format change.
- **Cached events** (Photon-style — events that late-joiners receive on connect). Not needed today; revisit if a use case appears.
- **Chat service.** Decommissioning the existing chat is in scope; building the new chat service is its own design + plan. The chat service will use 0x01 operations (for sending) and 0x00 events (for receiving) — both primitives are now in place after this branch lands.
- **Server-initiated operations (push-style RPC).** This branch keeps operations as client-initiated only. Server-initiated would require a separate "server pushes a request, client responds" flow — no current use case.

## Open questions

- **TypeID collision risk** — FNV-1a 32-bit on Go type names. With ~30 message types we're well below birthday-paradox concern (probability of collision is < 1e-7 at this scale). If a collision is ever detected at registry-build time, panic with a clear message and the user renames. Decision: **don't pre-empt.**
- **`MaxFrameSize`** — the 0x00 dispatcher should refuse frames over some sane upper bound (defensive against malformed clients sending u32-max body_len). Suggest 64KB per individual event body, no batch-frame ceiling beyond what WebSocket already enforces. Decision: **plan-time**.

## File-level change summary

Server (Go):

- Modify: `pkg/universe/{gateway,client_input_dispatch,broadcast_hooks,broadcast_queue,client_input_hooks,virtual_conn_manager,service_runtime}.go`
- Modify: `pkg/mmokit/{protocol,server_events,handle_client,handle_internal,broadcast,messaging*,init,mmokit}.go`
- Modify: `pkg/ops/router.go` (`Register` retired or aliased; new typed `RegisterOp` lives in mmokit and delegates here)
- Modify: `pkg/net/conn.go` (channel constants)
- Modify: `cmd/server/main.go` (RegisterServerEvent calls; op registrations)
- Modify: `internal/game/{system_network,input_handlers,input_messages,hooks,gameworld}.go`
- Modify: `internal/marketplace/handler.go` (5 op registrations migrate to typed `RegisterOp`)
- Modify: `internal/game/input_handlers.go:223` (bank `HandleClient` → `RegisterOp`), `internal/game/system_economy.go:198-280` (processBankRequests + SendBankContents — collapsed into the op handler returning `BankResponse` synchronously, with the queue removed; sell/buy follow-on bank pushes still call into a typed event push for out-of-band changes)
- Delete: `proto/gamepb/game.proto` entries — `WorldUpdateMsg`, `TypedEvent`, `LoginMsg`, `LoginRejectedMsg`, `PlayerSpawnedMsg`, `PlayerDiedMsg`, `DockingStateMsg`, `DockedMsg`, `CurrencyUpdateMsg`, `BankContentsMsg`, `EquipResultMsg`, `TransferResultMsg`, `MapDataMsg`, `PlayerOwnStateMsg`, `MarketBrowseRequest`, `MarketCreateOrderRequest`, `MarketCancelOrderRequest`, `MarketMyOrdersRequest`, `MarketInstantTradeRequest`, `MarketOrderBookResponse`, `MarketOrderResultResponse`, `MarketMyOrdersResponse`, `BankRequestMsg`
- Delete: `proto/enginepb/engine.proto` — `ChatMsg`, `OpRequest`, `OpResponse`, all `SE_*` enum values, all op-code enum values
- Modify (regen): `gen/go/gamepb/`, `gen/go/enginepb/`, `gen/es/gamepb/`, `gen/es/enginepb/`

Client (TS):

- Modify: `web-pixi/sdk/{client,transport,inputs,broadcasts,index}.ts`
- Modify: `web-pixi/src/{network,state}.ts` and `web-pixi/src/ui/{bank,market,hud,loot-popup}.ts` (per-event handler wiring; market UI rewires from old op-router to typed `client.market*` calls)
- Add: `web-pixi/sdk/operations.ts` (typed op wrappers)
- Delete: existing op-router client code (replaced by `operations.ts`)
- Mirror in: `examples/4node-basic/web/sdk/`

Codegen (Go):

- Modify: `cmd/sdkgen/{generate,broadcasts,inputs,schema,main}.go` — add server-event emission; merge channel-byte logic.
- Add: `cmd/sdkgen/operations.go` — emits `operations.ts` from the protocol's op registry.

Tests:

- Modify: `pkg/universe/typed_message_codec_test.go`, `pkg/mmokit/broadcast_test.go`, `pkg/mmokit/client_input_test.go`, `pkg/mmokit/messaging*_test.go`
- Add: cross-direction round-trip tests for every migrated server-event type.

## Acceptance criteria

- `just build` produces a clean binary; `go vet ./...` clean; web-pixi `tsc --noEmit` clean.
- `just dev` smoke flow passes for: connect, spawn, click-to-move, mining, ability cast, status effects (DoT visualization), dock, undock, respawn, marketplace browse/place/cancel/instant-trade, equip, loot, inventory transfer, jettison, login (cookie-resume + register + login-existing + duplicate-rejection).
- `just distributed` smoke flow passes for: cross-host login, cross-host handoff (walk a player across cell boundary), cross-host operation (place a market order on a remote cell).
- **Zero protobuf bytes on the wire** for any 0x00 or 0x01 frame. `WorldUpdateMsg`, `TypedEvent`, `LoginMsg`, `LoginRejectedMsg`, `ChatMsg`, `OpRequest`, `OpResponse`, all market request/response protos, and all migrated `SE_*` / op-code enum values deleted from proto.
- The only remaining `gen/go/*pb/` code lives under `meshpb/` (server-internal mesh data plane — never reaches clients). All client-facing wire format is typed reflection codec.
- Two channels in `pkg/net/conn.go`: `ChannelEvent = 0x00`, `ChannelOperation = 0x01`. `ChannelClientInput` retired.
