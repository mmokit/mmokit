# AoI Auto-Broadcast Design

**Status:** Approved 2026-05-05.
**Predecessor specs:** `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` (the foundation; this design realizes its §4.5 auto-anchor broadcast).
**Predecessor plans:** A+B (mmokit foundation), C (Damage + Mining + Process-level wrappers), D (StatusEffect + legacy surface removal), E (Death/Currency composition + ECS-access sweep). All on `feat/mmokit-entity-message-api`.

## 1. Summary

Implement spec §4.5's auto-broadcast model so game code stops manually enqueueing per-verb visual events and the framework owns AoI fan-out. Default behavior: every typed message Sent to an Entity broadcasts to viewers near any Entity-typed field on the message, post-handler. Server-internal messages opt out via the existing `ServerOnly()` marker.

The wire format for game events stops using `gamepb.AbilityCastResultMsg` (the one-size-fits-all proto every verb funneled through). Instead, each typed Go message IS the wire schema — the existing reflection-based codec (`pkg/universe/reflect_marshal.go`) extends to encode `mmokit.Entity` fields as NetIDs, sdkgen extends to emit a TS class per broadcast-eligible message, and the client subscribes by Go-struct identity. No new `.proto` files for game events. The protobuf envelope (`WorldUpdateMsg`) gains a generic `events []TypedEvent` carrier and the existing `ability_events` field retires.

## 2. Goals

- **Zero per-verb broadcast plumbing.** Game code declares a typed message, registers a handler via `mmokit.HandleAll`, and sends it. The framework auto-broadcasts to AoI viewers without explicit registration calls.
- **Implicit by default.** Most MMO events should reach nearby players. No "register as broadcastable" friction. `ServerOnly()` is the explicit opt-out.
- **No new `.proto` files for game events.** The Go struct IS the wire schema. Reflection-based encoding for the body, sdkgen for the TS class.
- **Result fields in the broadcast.** Handlers may mutate `*msg`; the broadcast carries the post-handler value (so `Damage.Dealt` reaches viewers, not `Damage.Amount`).
- **Cross-cell visual continuity.** Source-cell pre-handler broadcast + dest-cell post-handler broadcast for cross-cell sends, so the caster sees their cast fire on the same tick they pressed the button.
- **Retire `gamepb.AbilityCastResultMsg`.** The hand-rolled `pendingAbilityEvents` slice + per-verb `mmokit.Enqueue` calls + the per-viewer `if visible[CasterId] || visible[TargetId]` filter all delete.

## 3. Non-goals

- Replacing `WorldUpdateMsg` itself (the protobuf envelope) with a binary frame — separate plan.
- Removing protobufs from server↔client engine envelopes broadly (login, chat, etc.) — separate plan.
- Input handler migration (`OnInput*` → typed `Handle`) — separate plan.
- Updating the Unity client SDK (`gen/csharp/`) — defer until that client is back in active use.
- Handler-less broadcast (pure visual events with no state mutation) — YAGNI; all current verbs have handlers. Add `mmokit.RegisterBroadcast[T](p)` later if needed.

## 4. The five primitives in code

### 4.1 Send + auto-broadcast

`Entity.Send(msg)` already exists (Plan A+B). It routes to the authoritative cell's handler. Plan F extends the post-handler step with auto-broadcast:

```go
target.Send(&Damage{Amount: 25, Source: caster, Slot: 0, AbilityType: 1})
```

Framework path:

1. Route to the authoritative cell. (existing)
2. Run the registered handler. Handler mutates `*msg`. (existing)
3. **NEW:** if T is broadcast-eligible (does NOT implement `ServerOnly`), enqueue into the per-stage broadcast queue with the post-handler msg + computed anchors.
4. (Cross-cell only) On the source cell, also enqueue a pre-handler broadcast for the same msg, with anchors that resolve on this stage. The dest cell will run its own post-handler broadcast when it receives.
5. End-of-tick: framework drains the per-stage queue, AoI-filters per viewer, packs into `WorldUpdateMsg.events`, sends.

### 4.2 Anchors

Computed from the message + the receiver:

- Every `mmokit.Entity`-typed field on the struct (recursive into sub-structs).
- The `target` (receiver of `target.Send(&msg)`) — implicit anchor.
- Deduped by NetID (a message with `Caster` and `Target` referring to the same entity contributes one anchor).

Anchor positions resolve at broadcast time on the local stage. If an Entity's authoritative cell is across a boundary but it has a border replica on this cell, the replica's position is the anchor. If an Entity has no presence on this stage at all, that anchor is dropped.

### 4.3 ServerOnly opt-out

```go
type KillCredit struct {
    Currency uint32
    Amount   int64
}
func (KillCredit) serverOnly() {}
```

Already in use for `KillCredit` post-Plan E. The marker is reflective at registration time — `mmokit.HandleAll[T]` inspects T and skips broadcast registration if T implements `ServerOnly`.

### 4.4 Reflection codec extension

`pkg/universe/reflect_marshal.go` today supports primitives + structs + arrays + bools, and explicitly skips `ecs.Entity` fields. Plan F extends:

- `mmokit.Entity` fields encode as `uint32 NetID`.
- Decode (server-side, cross-cell receive) reads `uint32` and constructs `mmokit.EntityByNetID(stage, netID)`. Stage context threaded through decode (new signature).
- `ecs.Entity` continues to be skipped (still only used by transfer codec).

The codec produces the wire format used by both:
- Cross-cell typed-message routing (existing path; gains Entity-field encoding).
- AoI auto-broadcast body (new; same encoding, sent over the gamepb envelope).

### 4.5 TypeID assignment

Each broadcast-eligible message gets a stable `uint32` wire identifier:

```go
typeID = fnv32(reflect.TypeOf((*T)(nil)).Elem().String())
// e.g. "game.Damage" → 0xa1b2c3d4
```

Stable as long as the Go package path + type name don't change. Renames are a deliberate wire-break, in lockstep with SDK regeneration. Collision risk for ~50 message types is effectively zero.

The framework computes typeIDs at registration time. sdkgen reads them from the runtime registry via `--dump-schema` (existing flow). Server and client agree on IDs because they're derived from the same Go type names.

### 4.6 sdkgen + TS client

`cmd/sdkgen` extends to read the broadcast registry. For each registered type, it emits:

- A TS class with the same field shape (primitives mapped natively; `mmokit.Entity` → `number`; sub-structs as TS class composition).
- A binary deserializer keyed by typeID, mirroring the reflection codec on the server.
- A typeID constant for client-side dispatch.

The generated client gets a typed `on(MessageClass, handler)` API:

```ts
client.on(Damage, (msg) => {
    renderDamageNumber(msg.target, msg.dealt);
    fireWeaponAnimation(msg.source, msg.target, msg.slot);
});
client.on(MineExtract, (msg) => { ... });
client.on(Killed, (msg) => { ... });
```

When a `WorldUpdateMsg` arrives, the client iterates `events`, looks up the class by typeID, deserializes, dispatches.

### 4.7 Wire-format envelope

`WorldUpdateMsg` (still protobuf — the envelope migration is a separate plan) gains a generic typed-event carrier and retires `ability_events`:

```protobuf
message WorldUpdateMsg {
    uint32 tick = 1;
    uint32 ack_input_seq = 2;
    repeated EntityState entities = 3;
    repeated uint32 removed_ids = 4;
    repeated enginepb.ChatMsg chat_messages = 5;
    repeated uint32 killed_ids = 6;
    repeated TypedEvent events = 7;          // replaces ability_events
}
message TypedEvent {
    uint32 type_id = 1;
    bytes  body    = 2;  // reflection-codec-encoded
}
```

`ability_events` is field 7 today (last field). Per `feedback_proto_field_cleanup`, removing the trailing field doesn't require renumbering — just delete and replace at field number 7 with the new `events` repeated field. Wire-break for any pinned-to-old-format clients, but we regenerate the client SDK in lockstep.

## 5. Cross-cell broadcast semantics

| Send type | Broadcasts |
|---|---|
| Same-cell (`target.Local() == true`) | One — post-handler on the local stage |
| Cross-cell (`target.Local() == false`) | Two — source-cell **pre-handler** + dest-cell **post-handler** |

**Why two for cross-cell:** the source-cell broadcast covers "caster sees the laser fire on the same tick they pressed the button." The dest-cell post-handler broadcast covers "viewers near the victim see the damage number with the actual `Dealt` value." Each broadcast resolves anchors on its local stage; viewers near both cells *might* receive both events. Client-side renderers should be idempotent (animation plays once even if the message arrives twice). Double-receipt only happens at the cell boundary.

**Same-cell-only optimization:** the framework detects at Send-time that the target is local and suppresses the source-cell pre-broadcast. The post-handler broadcast covers everyone. Zero redundancy in the common case.

## 6. What gets deleted

### 6.1 Game side

| File / symbol | Disposition |
|---|---|
| `internal/game/verb_damage.go` `mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{...})` (×2 sites: handler + cross-cell pre-broadcast) | Delete. Framework owns it. |
| `internal/game/verb_mining.go` (currently no manual enqueue, but `MineExtract` becomes broadcast-eligible — same handler, framework adds broadcast) | No deletions. Behavior change: now broadcasts. |
| `internal/game/verb_status.go` `mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{...})` (×2 sites) | Delete. |
| `internal/game/verb_death.go` `Killed` handler doesn't manually enqueue (loot drop is internal); `KillCredit` is `serverOnly` | No deletions. `Killed` becomes broadcast-eligible (broadcasts to viewers near the dying entity). |
| `internal/game/system_ability.go` `mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{...})` (×1 site, around line 338) | Delete IF it represents an event covered by an existing typed message; otherwise migrate to a typed message. |

### 6.2 NetworkSystem cleanup (`internal/game/system_network.go`)

| Symbol | Disposition |
|---|---|
| `pendingAbilityEvents []*gamepb.AbilityCastResultMsg` field | Delete |
| `pendingAbilityEvents = mmokit.Peek[*gamepb.AbilityCastResultMsg](gw.Queue)` in `beforeTick` | Delete |
| `mmokit.Drain[*gamepb.AbilityCastResultMsg](gw.Queue)` in `afterTick` | Delete |
| `if visible[evt.CasterId] || visible[evt.TargetId]` filter in `afterSend` | Delete (framework owns visibility filtering) |
| `WorldUpdateMsg{AbilityEvents: ...}` build in `afterSend` | Replaced by framework-owned bundling into `WorldUpdateMsg.events` |

### 6.3 Proto cleanup (`proto/gamepb/game.proto`)

| Symbol | Disposition |
|---|---|
| `message AbilityCastResultMsg` (around line 308) | Delete |
| `WorldUpdateMsg.ability_events` field 7 | Delete; replace at field 7 with `repeated TypedEvent events` |
| `message TypedEvent { uint32 type_id; bytes body; }` | Add |

After Plan F, `gamepb` no longer carries any per-verb visual event proto. Future visual messages declare a Go struct, register a handler, and broadcast — no proto change required.

## 7. Implementation phases

1. **Reflection codec extension** (`pkg/universe/reflect_marshal.go`) — `mmokit.Entity` fields encode as uint32, decode resolves via stage NetID index. Tests for round-trip.
2. **Broadcast registry + framework dispatcher** (`pkg/mmokit`) — `mmokit.HandleAll[T]` extended; typeID assignment via fnv32; per-stage per-tick queue; anchor extraction; AoI filter; per-viewer bundle packing. Tests for ServerOnly opt-out, anchor dedup, AoI overlap, dedup of redundant broadcasts.
3. **Wire format + envelope change** (`proto/gamepb/`) — `TypedEvent` message, `WorldUpdateMsg.events`, regenerate proto.
4. **sdkgen extension** (`cmd/sdkgen`, `web-pixi/sdk/`) — read broadcast registry, emit TS classes + typed dispatcher; regenerate the example SDK.
5. **Verb handler cleanup** (`internal/game/verb_*.go`) — delete the four `mmokit.Enqueue(...)` calls. Migrate any `system_ability.go` enqueue site to a typed message (or delete if redundant).
6. **NetworkSystem cleanup** (`internal/game/system_network.go`) — delete `pendingAbilityEvents` field + plumbing; drop the per-viewer ability filter; the framework now owns the bundle.
7. **Proto cleanup** — delete `AbilityCastResultMsg` proto + `WorldUpdateMsg.ability_events`. Regenerate.
8. **Closeout** — full vet + test; smoke-build the example; smoke-test the web client (verify damage numbers + mining beam + DoT cast still render); update spec §10; final report.

## 8. Testing strategy

- **Reflection codec:** unit tests for round-trip of structs containing `mmokit.Entity` fields, including stage-context resolution on decode.
- **Broadcast registry:** unit tests for typeID stability, ServerOnly detection, anchor extraction (Entity fields + receiver, deduped).
- **Framework dispatcher:** unit tests for AoI overlap (viewer in/out of anchor radius), per-tick batching, post-handler timing.
- **Cross-cell broadcast:** integration test using `pkg/mmokit/integration_*.go` style — kill an entity on cell A whose killer is a replica on cell B; both cells broadcast; verify the `Killed` event reaches viewers in either cell's AoI exactly once per cell (idempotence under render).
- **ServerOnly:** integration test verifies `KillCredit` does NOT broadcast (no events reach AoI viewers — only the killer's own `GSE_CURRENCY_UPDATE` reaches the killer's connection).
- **Smoke / regression:** existing 4node-basic example + the death observer / KillCredit cross-cell tests stay green.
- **Web client:** manual smoke test — connect to the running example, fire abilities, verify damage numbers + cast animations still render. Mining beam visual still renders. DoT (IonBurn) cast animation still renders.

## 9. Open risks

- **TypeID stability across rebuilds.** fnv32 on the Go type name is deterministic. Renaming a struct or moving its package is a wire-break — clients must regenerate. Acceptable; matches how renames work everywhere else in the codebase.
- **TS client reflection-decoder correctness.** sdkgen has been generating component decoders for entity replication for a while; broadcast events are a strict subset of the same shape (same field types, same encoding rules). Risk is low but new code paths warrant care.
- **`WorldUpdateMsg` wire break.** Replacing `ability_events` (field 7) with `events` (also field 7) is a hard wire break. Any client running pre-Plan-F code mid-deploy will fail to decode `WorldUpdateMsg`. Acceptable; this is solo-developer dev cycle, the SDK regenerates and the client redeploys in lockstep.
- **Cross-cell double-broadcast at the cell boundary.** Render renderers must be idempotent. Today's `AbilityCastResultMsg` path has the same behavior; nothing new.

## 10. Success criteria

- Spec §4.5's auto-anchor broadcast realized in code.
- Game code declares a typed message + handler and broadcasts to AoI without writing any `mmokit.Enqueue(gw.Queue, ...)` call.
- `gamepb.AbilityCastResultMsg` proto and `WorldUpdateMsg.ability_events` field deleted.
- `pendingAbilityEvents` slice + the per-viewer `if visible[CasterId] || visible[TargetId]` filter deleted from `system_network.go`.
- Adding a new visual event requires zero proto edits — declare a Go struct, register a handler, subscribe on the client. Framework handles wire format, AoI, dispatch.
- Existing 4node-basic example continues to run; damage numbers + cast animations + mining beam visuals all still render.
- Full `go vet ./...` clean; `go test ./pkg/... ./internal/...` green.

## 11. Out of scope (carried forward)

- **Replace `WorldUpdateMsg` envelope with a binary frame** — orthogonal; separate plan.
- **Remove protobufs from server↔client engine envelopes broadly** — orthogonal; separate plan after the envelope migration.
- **Input handler migration** (`OnInput*` → typed `Handle` with from-client-trust marker) — Plan H.
- **Handler-less broadcast** (pure visual events with no state mutation) — YAGNI; add `mmokit.RegisterBroadcast[T](p)` later if a use case appears.
- **Unity client SDK update** (`gen/csharp/`) — defer.
