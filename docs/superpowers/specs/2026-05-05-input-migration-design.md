# Input Migration + Full Cleanup Design

**Status:** Approved 2026-05-05.
**Predecessor specs:** `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` (the foundation; this design realizes step 6 of its §10 migration plan), `docs/superpowers/specs/2026-05-05-aoi-auto-broadcast-design.md` (Plan F's auto-broadcast).
**Predecessor plans:** A+B (mmokit foundation), C (Damage + Mining), D (StatusEffect + legacy surface), E (Death/Currency + ECS sweep), F (AoI auto-broadcast). All on `feat/mmokit-entity-message-api`.

## 1. Summary

Realize spec §6.6 — client input is a typed Send. Convert `OnInput[T]` / `OnInputWith[T, Deps]` to `mmokit.HandleClient[T]` (a registration verb, not a marker interface). Replace the empty-method `ServerOnly()` marker with a paired registration verb `HandleAllInternal[T]`. Split the bundled `PlayerInputMsg` into four discrete typed messages (`SetMoveTarget`, `SetLockTarget`, `CastAbility`, `JettisonItem`) — discrete events should be discrete events. Delete every per-input proto type, the `InputBinding` registry, the `dispatchInput` phase, and the input-keyed event-code enums. Sweep all loose ends from Plans D-F: dead `ActionResult` infrastructure, README staleness, duplicate test, foundation deferrals, missing mining-beam VFX, Unity SDK regen, `reflect.Ptr` → `reflect.Pointer` codebase-wide.

After Plan G lands, the spec §10 migration plan is fully implemented. Adding a new input is: declare a Go struct + register a handler. Two lines. Adding a new server-internal message is: declare a struct + register via `HandleAllInternal`. Two lines. The `*mmokit.Player` parameter on input handlers becomes `mmokit.Entity` — same pattern as every other typed-message handler.

## 2. Goals

- Realize spec §6.6: client input is a typed Send to the player's own Entity, dispatched server-side by the same machinery as Damage/Mining/Status/Killed/KillCredit.
- Replace the marker-interface pattern (`ServerOnly()`) with registration verbs. Trust + broadcast policy live at the wiring site, not on data-type definitions.
- Split `PlayerInputMsg` into four discrete typed messages aligned with their semantic shape (continuous-state vs discrete-event).
- Eliminate every input-related proto type, event-code enum, and parallel registration registry.
- Close every loose end inherited from Plans D-F.
- After Plan G: every primitive in spec §4 is delivered, every example in §5 runs in real code, every legacy surface in §7 is deleted.

## 3. Non-goals

- Replacing the `WorldUpdateMsg` envelope with a binary frame (separate plan; orthogonal).
- Removing protobufs from server↔client engine envelopes broadly (login frame, op request/response, world update — all stay protobuf for now).
- Migrating `OpRouter` (request/response with reply codes) to typed Sends. Different shape; out of scope.
- Adding client-side input prediction or rate limiting in the framework. Per-handler concerns; not a framework feature in this plan.
- Updating the Unity client (`gen/csharp/`) for typed dispatch. Generated proto types are regenerated as a side effect of proto edits; the typed-Send dispatcher is web-pixi-only.

## 4. Registration verbs replace marker interfaces

### 4.1 The three handler categories

Three distinct registration verbs. The verb IS the policy declaration:

```go
// Default: handler runs on Send (entity → entity); framework auto-broadcasts to AoI.
mmokit.HandleAll(mmo, damageHandler)

// Server-internal: handler runs on Send; NO broadcast.
mmokit.HandleAllInternal(mmo, killCreditHandler)

// Client input: handler runs when a connection sends this type;
// framework validates connection → player ownership; routes to player's
// Entity as the handler target. No broadcast.
mmokit.HandleClient(mmo, playerInputHandler)
```

All three handlers have the same signature: `func(target mmokit.Entity, msg *T)`. The framework decides routing + broadcast policy from the registration verb, not from the type.

### 4.2 `ServerOnly()` marker removal

Plans E and F shipped `func (KillCredit) ServerOnly() {}` plus an `IsServerOnly` interface check. Plan G removes both. Empty marker methods are a Go syntactic hack — they pollute the type definition with fake methods and confuse readers. The trust + broadcast policy belongs at the wiring site.

Migration:
- `KillCredit.ServerOnly()` method deleted.
- `mmokit.ServerOnly` interface + `mmokit.IsServerOnly` helper deleted.
- KillCredit registers via `mmokit.HandleAllInternal(mmo, killCreditHandler)` instead of `HandleAll`.
- Test stand-in markers in `pkg/mmokit/integration_killcredit_test.go` and `pkg/mmokit/auto_broadcast_test.go` likewise drop the marker; tests register via the appropriate verb.

The broadcast registry's `brSet` (today: types that registered via `HandleAll` and are not ServerOnly) becomes simply: types that registered via `HandleAll`. `HandleAllInternal` registers in the dispatcher only, not in the broadcast registry.

### 4.3 Why registration verbs win

- Empty marker methods like `func (T) ServerOnly() {}` are pure syntactic ceremony — no behavior, no return value. They look broken to readers.
- Marker scaling: imagine `ServerOnly()`, `FromClient()`, `Persistent()`, `Encrypted()`, `RateLimited()` all on the same struct. Five ghost methods cluttering the type definition.
- Trust + broadcast are wiring decisions, not data properties. The same `MoveTarget` struct could conceivably be sent client→server (via `HandleClient`) OR server-internal (via `HandleAllInternal`). Tagging the type fixes the policy; tagging the registration site keeps the type pure.
- Sdkgen reads registry contents (verb-keyed) to decide what to emit on the TS side: `HandleAll` types get a TS broadcast class; `HandleClient` types get a TS class with a `client.send()` method; `HandleAllInternal` types are not exposed to TS at all.

## 5. PlayerInputMsg split

Today: one bundled `gamepb.PlayerInputMsg` carrying movement target, lock target, ability cast bitmask, jettison ID, and a sequence number. Sent every input tick whether anything changed or not. Continuous-state-snapshot pattern.

The bundle conflates four distinct concerns:

| Today's field | Conceptual shape | Plan G typed message |
|---|---|---|
| `MoveActive` + `MoveX` + `MoveY` | continuous state command | `SetMoveTarget` |
| `LockTargetNetID` | continuous state command (sparse) | `SetLockTarget` |
| `AbilityCast` (bitmask) | discrete event | `CastAbility` |
| `Jettison` (item ID) | discrete event (rare) | `JettisonItem` |

After split:

```go
type SetMoveTarget struct {
    Sequence uint32
    Active   bool         // false = stop / clear move target
    X, Y     float32
}

type SetLockTarget struct {
    Sequence uint32
    TargetNetID uint32   // 0 = clear lock
}

type CastAbility struct {
    Sequence uint32
    Slot     uint8       // which ability slot fired
}

type JettisonItem struct {
    Sequence uint32
    ItemID   uint32
}
```

Each is its own `HandleClient` registration. Client sends them when the corresponding action happens, not every tick.

**Wire-bandwidth implication:** an idle player sends zero input messages. An active player sends sparse messages (one `SetMoveTarget` on click, one `CastAbility` per ability press, etc.) instead of a 14-byte frame every input tick.

**Sequence semantics:** each message carries its own monotonic `Sequence` (per-connection counter that increments on every send regardless of type). Server tracks max sequence seen; world update echoes it as `ack_input_seq`. Client-side reconciliation works the same as today.

**Order independence:** the four handlers each touch one component (or one PlayerInput field). They're commutative within a tick — no ordering hazard from receiving multiple typed messages between input phases.

## 6. Wire format for client inputs

The reflection codec from Plan F (`pkg/universe/reflect_marshal.go` + the `mmokit.Entity` codec registration) handles all wire serialization for typed messages. Plan G reuses it; client → server inputs encode the same way as broadcast events.

**Wire frame for client input:**

```
[byte channel = 0x02][u32 typeID][u32 bodyLen][body bytes]
```

The 0x02 channel byte is new; today's channels are `0x00` events and `0x01` operations. Client-input is a third channel, distinct because it has different validation semantics (typeID lookup goes against the client-input registry, not the broadcast registry).

**Server-side dispatch path:**

1. Gateway receives WebSocket binary frame.
2. Strip channel byte. If `0x02`, this is a client-input frame.
3. Read typeID. Look up handler in the **client-input registry** (separate from the broadcast/Handle registry).
4. If no handler: drop + log (untrusted/unknown type).
5. If registered: framework resolves connection → player Entity (existing session→connID→player mapping; framework rejects mismatch).
6. Decode body via reflection codec.
7. Dispatch to handler with `(playerEntity, *msg)`.

**Three registries, one dispatch primitive:**

| Registry | Source | Target | Verb | Broadcast? |
|---|---|---|---|---|
| Entity-internal (default) | `target.Send(&msg)` | target Entity | `HandleAll` | yes |
| Entity-internal (no-broadcast) | `target.Send(&msg)` | target Entity | `HandleAllInternal` | no |
| Client-input | WebSocket frame from connection | connection's player Entity | `HandleClient` | no |

All three handlers have identical signatures. The framework's per-stage `Stage.Dispatcher().Invoke` is the same primitive across all three registries — only the entry-point validation differs.

## 7. PlayerStateOf helper + Deps migration

State filtering migrates from `.States(...)` builder API into a one-liner inside the handler:

```go
mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *SetMoveTarget) {
    state := mmokit.PlayerStateOf(player)
    if state != mmokit.StateActive && state != StateDocking { return }
    // ...
})
```

`mmokit.PlayerStateOf(entity)` is a thin helper: looks up `gamecomp.PlayerConn` on the entity, finds the session via `Players.ByConnID`, returns `s.State`. Returns the zero-value PlayerState on mismatch.

Deps injection (`OnInputWith[Msg, Deps]`'s component-injection feature) migrates to direct `mmokit.Get[T](player)` calls in the handler body:

```go
input := mmokit.Get[gamecomp.PlayerInput](player)
if input == nil { return }
moveTarget := mmokit.Get[mmokit.MoveTarget](player)  // optional; nil-check inline
```

Three lines instead of a Deps struct + builder method. Same readability; fewer concepts.

## 8. sdkgen extension for client inputs

Today sdkgen reads the broadcast registry from `--dump-schema` and emits TS classes with `decode` static methods. Plan G adds a parallel pass for the client-input registry:

- Per-input TS class with the same field shape (primitives mapped natively; `mmokit.Entity` → `number`).
- Per-input TS class with an `encode` instance method (NEW — clients serialize, broadcast classes only deserialize).
- A `client.send(msg)` method on the generated SpaceClient that takes any registered input class.

```ts
// Generated:
export class SetMoveTarget {
    static readonly typeID = 0xa1b2c3d4;
    sequence: number = 0;
    active: boolean = false;
    x: number = 0;
    y: number = 0;
    encode(): Uint8Array { /* mirror reflection codec field order */ }
}

// Usage:
client.send(new SetMoveTarget({
    sequence: state.inputSeq,
    active: true,
    x: state.moveTargetX,
    y: state.moveTargetY,
}));
```

The TS `client.send` method:
1. Calls the message's `encode()` to produce body bytes.
2. Builds the wire frame: `[0x02][typeID][bodyLen][body]`.
3. Sends via the existing WebSocket binary path.

`Protocol` (server-side) gains `ClientInputTypes []ClientInputTypeSchema` alongside `BroadcastTypes`. Same shape, different registry source. sdkgen iterates both.

## 9. What gets deleted

### 9.1 Server-side (Plan G core)

| File / symbol | Disposition |
|---|---|
| `pkg/mmokit/input.go` (entire file) | DELETE. `OnInput`, `OnInputWith`, `InputBuilder`, `Do`, `States`, `Active`, `Guard`. |
| `pkg/engine/input_dispatcher.go` + `input_dispatcher_test.go` | DELETE. The dispatchInput phase merges into the typed-message dispatcher. |
| `pkg/universe/coordinator.go::Process.AddInputBinding` / `InputBindings` / `inputBindings` field | DELETE. |
| `pkg/universe/coordinator.go::dispatchInput` phase wiring (in cell loop) | DELETE. |
| `internal/game/input_handlers.go` | REWRITE. Each `OnInput*` block converts to `mmokit.HandleClient[T]`. PlayerInput splits to 4 separate handlers. |
| `internal/game/factory.go::RegisterInputs(coord)` call | KEEP (the function name). The body is rewritten. |

### 9.2 Server-side (proto deletions)

| Proto type | Disposition |
|---|---|
| `gamepb.PlayerInputMsg` | DELETE (replaced by 4 Go structs in `internal/game/input_messages.go`) |
| `gamepb.DockRequestMsg` | DELETE |
| `gamepb.UndockRequestMsg` | DELETE |
| `gamepb.RespawnRequestMsg` | DELETE |
| `gamepb.InventoryTransferMsg` | DELETE |
| `gamepb.BankRequestMsg` | DELETE |
| `gamepb.EquipRequestMsg` | DELETE |
| `gamepb.LootItemMsg` | DELETE |
| `gamepb.LootAllMsg` | DELETE |
| `enginepb.ChatMsg` | KEEP — chat is also broadcast (server → other clients) and uses ChatMsg as a transport type. The HandleClient handler accepts a `Chat` typed Go struct; the broadcast continues to use `enginepb.ChatMsg` proto for compat with `WorldUpdateMsg.chat_messages`. (Or migrate too — see Phase planning.) |

### 9.3 Server-side (event code enums)

| Enum value | Disposition |
|---|---|
| `enginepb.ClientEventCode_CE_PLAYER_INPUT` | DELETE |
| `enginepb.ClientEventCode_CE_CHAT` | DELETE if Chat fully migrates; KEEP otherwise. |
| `gamepb.GameClientEventCode_GCE_DOCK` / `GCE_UNDOCK` / `GCE_RESPAWN` / `GCE_INVENTORY_TRANSFER` / `GCE_BANK_REQUEST` / `GCE_EQUIP` / `GCE_LOOT_ITEM` / `GCE_LOOT_ALL` | DELETE |
| Other `enginepb.ClientEventCode_*` values used by the login flow | KEEP (login is special). |

### 9.4 Server-side (`ServerOnly` removal)

| Symbol | Disposition |
|---|---|
| `pkg/mmokit/broadcast.go::ServerOnly` interface + `IsServerOnly` helper | DELETE |
| `internal/game/verb_death.go::func (KillCredit) ServerOnly() {}` | DELETE |
| `pkg/mmokit/integration_killcredit_test.go::killCreditMsg.ServerOnly()` | DELETE |
| `pkg/mmokit/auto_broadcast_test.go::testServerOnlyMsg.ServerOnly()` | DELETE |
| `TestIsServerOnly` in `pkg/mmokit/broadcast_test.go` | REWRITE (tests verb-based registration policy instead of marker detection) |

### 9.5 Client-side

| TS file / symbol | Disposition |
|---|---|
| `web-pixi/sdk/client.ts::sendMoveTarget`, `sendLogin`, etc. (sdkgen-generated typed methods per input proto) | DELETE — replaced by single `client.send(msg)` method. |
| `examples/4node-basic/web/sdk/client.ts::sendMoveTarget` etc. | Same. |
| `web-pixi/src/network.ts` and `examples/4node-basic/web/src/network.ts` | REWRITE to use `client.send(new SetMoveTarget(...))` etc. |
| `web-pixi/sdk/inputs.ts` (or similar) — sdkgen output | NEW. One TS class per `HandleClient[T]` registration. |

## 10. Cleanup punch list (full sweep)

### 10.1 Loose ends inherited from Plans D-F

- **`internal/game/README.md`** — references deleted `MarkPlayerDeath`, `processDeaths`, `PendingDeaths`, `DeadPlayers`. Rewrite to describe the post-Plan-E death observer + `Killed` handler architecture.
- **Tombstone comments in `internal/game/verb_death.go`** (lines ~105, 182) — references to legacy `gw.MarkPlayerDeath` / `MarkNPCDeath`. Remove or rewrite to current intent.
- **Historical comments in `internal/game/cross_cell_kill_credit_test.go:18`** and **`pkg/mmokit/integration_killcredit_test.go:7`** — references to the removed SideEffect path. Trim.
- **`TestDeathObserver_DoesNotRefireWhenDeathFiredTrue`** (in `internal/game/death_observer_test.go`) — duplicate coverage. Delete.
- **`internal/game/commands/kill.go::registerKill(reg, coord)`** — `coord` parameter unused (gopls warning). Drop the parameter; update `commands/registry.go` call site.

### 10.2 Dead `ActionResult` infrastructure

After Plan E removed `gw.SideEffects.Emit`, nothing produces `pkg/universe.ActionResult` anymore. The supporting infrastructure (Bridge method, codec, proto field, MsgActionResult arm, test) is dead but was kept by Plan D's design choice. Plan G deletes it all.

| Symbol | Disposition |
|---|---|
| `pkg/universe/action.go::ActionResult` Go type | DELETE |
| `pkg/universe/bridge.go::Bridge.SendActionResult` interface method | DELETE |
| `pkg/universe/bridge.go::NoopBridge.SendActionResult` | DELETE |
| `pkg/universe/cell_bridge_impl.go::cellBridge.SendActionResult` | DELETE |
| `pkg/universe/grpc_bridge.go::grpcBridge.SendActionResult` | DELETE |
| `pkg/universe/cell.go::MsgActionResult` arm | DELETE (the log-and-drop body) |
| `pkg/universe/message.go::MsgActionResult` constant | DELETE |
| `proto/meshpb/mesh.proto::ActionResult` message | DELETE. Field 11 of `MeshFrame.action_result`; per `feedback_proto_field_cleanup` if mid-numbering, renumber subsequent fields from 1. |
| `pkg/universe/mesh_frame_codec.go` ActionResult encode + decode arms | DELETE |
| `pkg/universe/mesh_frame_codec_test.go` ActionResult roundtrip case | DELETE |
| `pkg/universe/universe_test.go::TestBridge_SendActionResult` | DELETE |

### 10.3 Other cleanup

- **`cmd/sdkgen/main.go::snakeToTitle`** (Plan F leftover, unused). Delete.
- **Foundation deferral (c)** — `Set`-on-dead-entity debug log: add a single log line in `pkg/mmokit/components.go::Set` that fires when the entity is non-zero but `!e.Alive()`. Catches use-after-free patterns in dev.
- **Mining-beam toggle press-pulse VFX** — restored via a tiny typed `BeamToggle` message that `system_ability.go` Sends when the mining beam toggles. `mmokit.HandleAll` dispatches; framework auto-broadcasts. ~20 lines.
- **`gen/csharp/` Unity SDK regen** — re-run `just proto` so Unity's generated proto types reflect Plan F's `AbilityCastResultMsg` deletion + Plan G's per-input proto deletions. Unity won't have the typed-Send dispatcher (out of scope), but its protos should at least compile.
- **Codebase-wide `reflect.Ptr` → `reflect.Pointer` sweep.** Mechanical replacement; ~30 sites by previous grep counts. Single commit at the end.

## 11. Implementation phases

1. **Registration verbs primitive + `ServerOnly` removal.** Add `mmokit.HandleAllInternal[T]`, refactor broadcast registry to be verb-keyed (not marker-keyed), migrate KillCredit. Delete the `ServerOnly` interface + `IsServerOnly` helper. Tests for each verb.
2. **`HandleClient[T]` primitive + dispatch path.** Define the new client-input registry and the wire frame channel byte (0x02). Server-side gateway parses incoming 0x02 frames, validates connection→player ownership, decodes via reflection codec, dispatches to the registered handler.
3. **`PlayerStateOf(entity)` helper.** Small lookup; tested.
4. **Split `PlayerInputMsg` into four typed messages.** Define `SetMoveTarget`, `SetLockTarget`, `CastAbility`, `JettisonItem` Go structs in `internal/game/input_messages.go` (or extend an existing file). Register handlers via `HandleClient`. Existing component-mutation logic preserves; reorganized into 4 small handlers.
5. **Migrate remaining game inputs.** `Dock`, `Undock`, `Respawn`, `InventoryTransfer`, `BankRequest`, `Equip`, `LootItem`, `LootAll`, `Chat` — each becomes a typed Go struct + `HandleClient` registration. Delete the corresponding gamepb / enginepb proto types. Decision per Chat: migrate fully (delete `enginepb.ChatMsg` from input path; the broadcast path uses the typed `Chat` struct via the same broadcast pipeline as Damage/etc.).
6. **sdkgen extension for client inputs.** `--dump-schema` exposes `client_input_types`. sdkgen emits TS class per input + `client.send(MessageClass)` method. Regenerate web-pixi + 4node-basic SDKs.
7. **Migrate web-pixi + 4node-basic clients** to typed `client.send(...)` calls. Delete `client.sendMoveTarget(...)`-shaped methods.
8. **Delete `OnInput` / `OnInputWith` infrastructure.** `pkg/mmokit/input.go`, `pkg/engine/input_dispatcher*`, `Process.AddInputBinding`/`InputBindings`/`dispatchInput`, the input-event-code enums.
9. **Cleanup phase 1: dead `ActionResult` infrastructure.** Delete `Bridge.SendActionResult` + impls, `MsgActionResult` arm + constant, `ActionResult` Go type, `meshpb.ActionResult` message + `MeshFrame.action_result` field (renumber if needed), codec arms, related tests.
10. **Cleanup phase 2: everything else.** README rewrite, tombstone comments, historical comments, duplicate test, `unusedparams: coord`, `snakeToTitle`, foundation deferral (c), mining-beam VFX, Unity SDK regen, `reflect.Ptr` → `reflect.Pointer` sweep.
11. **Closeout.** Spec §10 step 6 marked done. Final report.

## 12. Testing strategy

- **`HandleClient` dispatch test.** Stand-in client-input message registered via `HandleClient`; framework's gateway path (mock) decodes and dispatches; assert handler ran with the expected target Entity.
- **`HandleAllInternal` no-broadcast test.** Register a stand-in via `HandleAllInternal`; Send the message; assert no entry in the broadcast queue.
- **`PlayerStateOf` test.** Spawn a player Entity, transition between states, assert `PlayerStateOf` reflects the current state.
- **PlayerInputMsg-split equivalence test.** Replay a sequence of (move, lock, ability, jettison) actions through the four split handlers; assert the resulting component state matches what the bundled `PlayerInputMsg` handler produced.
- **End-to-end smoke (browser).** Connect, drive movement, fire abilities, mine asteroids, dock, undock, respawn. Verify all input paths work after migration.
- **Existing tests** — Plans D-F regression coverage stays green throughout.

## 13. Open risks

- **Wire-format break for clients.** Replacing `CE_PLAYER_INPUT` proto with four typed Go structs is a hard wire break. Web-pixi + 4node-basic regenerate in lockstep; any external client (Unity, third-party) needs manual update. Solo-dev cycle so this is acceptable; flag it for any future external-client scenarios.
- **Login envelope is special.** Login arrives BEFORE the player Entity exists. It stays a one-off envelope (proto-keyed event code on channel `0x00`). All post-login client → server messages migrate to typed Send on channel `0x02`. The bifurcation is intentional but worth documenting.
- **`OpRouter` stays separate.** Request/response with reply codes (market trades, equip ops). Different shape than typed Send. Out of scope per design; could migrate to typed-Send-with-reply in a future plan.
- **Component-injection ergonomics regression.** Today `OnInputWith[Msg, Deps]` injects components in a struct. Plan G replaces with direct `mmokit.Get[T]` calls in the handler. Slightly more code per handler, but more explicit and consistent with every other typed-message handler in the codebase.

## 14. Success criteria

- `mmokit.HandleAll`, `HandleAllInternal`, `HandleClient` are the three registration verbs. No marker interfaces.
- `pkg/mmokit/input.go` deleted. No `OnInput` / `OnInputWith` / `InputBuilder` / `InputBinding` anywhere.
- `Process.AddInputBinding` / `InputBindings` / `dispatchInput` deleted.
- `gamepb.PlayerInputMsg`, `DockRequestMsg`, `UndockRequestMsg`, `RespawnRequestMsg`, `InventoryTransferMsg`, `BankRequestMsg`, `EquipRequestMsg`, `LootItemMsg`, `LootAllMsg`, `enginepb.ChatMsg` (from input path) all deleted.
- `internal/game/input_handlers.go` is exclusively `HandleClient[T]` registrations with handlers taking `(player Entity, msg *T)`.
- `PlayerInputMsg` split into `SetMoveTarget` / `SetLockTarget` / `CastAbility` / `JettisonItem`.
- `client.send(MessageClass)` is the only client → server path (besides login).
- All Plan D-F loose ends closed: README accurate, no tombstone comments, dead `ActionResult` infrastructure deleted, `snakeToTitle` gone, `unusedparams: coord` fixed, duplicate test deleted, foundation deferral (c) addressed, mining-beam VFX restored, `reflect.Ptr` swept.
- Spec §10 step 6 marked done. Spec §10 migration plan: complete.
- Full `go vet ./...` clean; `go test ./pkg/... ./internal/...` green; web-pixi + 4node-basic build clean; example smoke-tests render correctly.

## 15. Out of scope (carried forward)

- **Replace `WorldUpdateMsg` envelope with binary frame** — separate plan; closes the protobuf-vestigiality story for game events.
- **Remove protobufs from server↔client engine envelopes broadly** — separate plan after the envelope migration.
- **OpRouter migration to typed-Send-with-reply** — different shape; future plan.
- **Client-side input prediction / rate limiting** — per-handler concerns; not framework features.
- **Unity client typed-Send dispatcher** — generated proto types are regenerated as a side effect; the dispatcher is web-pixi-only.
