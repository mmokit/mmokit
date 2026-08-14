# Move PlayerDiedMsg + RespawnRequestMsg to gamepb (+ SDK type re-exports) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two coupled cleanups in a single plan:
1. Move the two combat/lifecycle-specific proto messages (`PlayerDiedMsg`, `RespawnRequestMsg`) and their event codes (`SE_PLAYER_DIED`, `CE_RESPAWN`) from `enginepb` to `gamepb` where they belong.
2. Close an SDK abstraction leak: `sdkgen` currently forces consumers to import proto types directly from `@gen/...` even though those types are the argument/return types of the generated SDK's public methods. Teach `sdkgen` to re-export the proto types it surfaces on its own public API. Then update `web-pixi/src/network.ts` to import SDK-surfaced proto types from `../sdk/` instead of reaching into `@gen/...`.

**Architecture:** Edit both proto files, regenerate bindings (`just proto`), update Go callers, extend `sdkgen`'s `genIndex()` to emit `export type { ... } from "@gen/..."` lines, regenerate all three in-tree SDKs (`just space-sdk` + `just client-sdk` for each example), consolidate `web-pixi/src/network.ts` imports, verify, commit. No backward-compat shims — enum gaps closed by renumbering per project convention.

**Tech Stack:** protobuf (buf), Go, TypeScript (via generated SDK).

**Scope rationale:** Two cleanups bundled because the second one subsumes the last edit of the first. Task 1 would otherwise need to rewrite a `PlayerDiedMsg` import in `network.ts`; Task 2 rewrites that same import block anyway as part of the broader consolidation. Doing them together avoids churning `network.ts` twice. The commits are split (one per refactor) to keep `git log` readable.

**Background — why `PlayerDiedMsg`/`RespawnRequestMsg` belong in gamepb:**
- No code in `pkg/` references `PlayerDiedMsg`, `RespawnRequestMsg`, `CE_RESPAWN`, or `SE_PLAYER_DIED`. The engine's `Bridge.RequestRespawn(connID, username)` is proto-free — just routes sessions.
- `PlayerDiedMsg.killer_id` is combat-semantic; the engine has no concept of "kill."
- `examples/slither/` already defines its own `SCE_RESPAWN = 102` in `slitherpb` — treats respawn as game-specific.
- `examples/4node-basic/` uses neither.
- Only the zenion space game (`cmd/server/main.go`, `internal/game/`, `internal/bot/`, `web-pixi/`) depends on them.

**Background — why SDK type re-exports:**
- `web-pixi/sdk/client.ts` has methods like `onPlayerDied(handler: (msg: PlayerDiedMsg) => void)` — the handler parameter type is a proto message.
- Consumer code (`web-pixi/src/network.ts`) must import `PlayerDiedMsg` from `@gen/enginepb/engine_pb.js` directly to satisfy the type, even though the SDK already depends on it internally.
- The SDK's value proposition is "you don't need to know about proto." The current output defeats that for any handler that wants to name its argument type.
- Fix: `genIndex()` already has all the metadata it needs via `collectTypeImports()`. Emit `export type { ... } from "@gen/..."` in the generated `index.ts` for every proto message that appears in the SDK's public surface (server-event handler types + operation response types — the same set `collectTypeImports` already computes for `client.ts`).

**Enum renumber policy:** Per the project "no backward-compat" rule and the `feedback_proto_field_cleanup` memory ("never reserve old proto fields; renumber from 1 on changes"), removing `CE_RESPAWN = 3` and `SE_PLAYER_DIED = 3` means closing the gap — renumber subsequent values down. The intentional 5-9 gap in `ServerEventCode` (between `SE_LOGIN_REJECTED` and `SE_CHAT`) stays as-is; only the hole at =3 collapses.

---

## File Structure

**Proto files (edited):**
- `proto/enginepb/engine.proto` — delete 2 messages, 2 enum values; renumber 3 neighbouring enum values to close the gap.
- `proto/gamepb/game.proto` — add 2 messages, add 2 enum values.

**sdkgen source (edited):**
- `cmd/sdkgen/generate.go` — extend `genIndex()` to emit `export type { ... }` lines for proto types surfaced by the SDK.

**Generated bindings (fully regenerated via `just proto`):**
- `gen/go/enginepb/engine.pb.go` + `gen/go/gamepb/game.pb.go`
- `gen/csharp/Engine.cs` + `gen/csharp/Game.cs`
- `gen/es/enginepb/engine_pb.ts` + `gen/es/gamepb/game_pb.ts`

**Go callers (updated):**
- `cmd/server/main.go` (2 registrations: lines ~44 and ~57-58)
- `internal/game/lifecycle.go` (line ~62, `SE_PLAYER_DIED` send)
- `internal/game/input_handlers.go` (line ~27, `CE_RESPAWN` handler registration)
- `internal/bot/actions.go` (line ~97, bot respawn send)
- `internal/bot/bot.go` (lines ~213-214, bot death event decode)

**TS callers (updated):**
- `web-pixi/src/network.ts` — consolidate proto-type imports to `../sdk/index.js`; keep only nested-type escape-hatch imports (`MapStationInfo`, `CellInfo`) from `@gen/...` if needed.

**Regenerated SDKs (via `just space-sdk` and `just client-sdk` per example):**
- `web-pixi/sdk/{client.ts,index.ts,...}`
- `examples/4node-basic/web/sdk/{client.ts,index.ts,...}`
- `examples/slither/web/sdk/{client.ts,index.ts,...}`

All three pick up the new `export type` block in `index.ts`. `web-pixi/sdk/client.ts` additionally picks up the PlayerDiedMsg/RespawnRequestMsg import-path change; the example SDKs are unaffected by the proto move but get the new `index.ts` re-exports for their own surfaced types.

**Commit boundaries:**
- Commit 1 (proto move): tasks 1-3 + minimal SDK regen for space game + stop-gap edit to `network.ts`. Rejected — instead, do a single-commit-per-concern split where Commit 1 covers only the proto move, SDK regens stay consistent.
- Actually simpler: two commits, sequential, each atomic.
  - Commit A: proto move + Go caller updates + generated-binding updates + web SDK regens (import paths change for PlayerDiedMsg/RespawnRequestMsg inside SDK, method names stable) + `network.ts` localized edit to move `PlayerDiedMsg` import from enginepb to gamepb. Tasks 1-5, Task 6 step 1 only.
  - Commit B: sdkgen improvement + all three SDK regens (adds `export type { ... }` to all three `index.ts` files) + `network.ts` consolidation (switch SDK-surfaced proto types to `../sdk/` imports, drop the enginepb/gamepb direct imports except for nested-type escape hatches). Tasks 6 remainder + 7.

---

## Task 1: Edit the proto files

**Files:**
- Modify: `proto/enginepb/engine.proto`
- Modify: `proto/gamepb/game.proto`

- [ ] **Step 1: Remove `CE_RESPAWN` and renumber trailing ClientEventCodes in `enginepb/engine.proto`**

Edit `proto/enginepb/engine.proto` lines 43-50. The current enum is:

```proto
enum ClientEventCode {
    CE_PLAYER_INPUT = 0;
    CE_PING = 1;
    CE_LOGIN = 2;
    CE_RESPAWN = 3;
    CE_CHAT = 4;
    CE_ACK_SNAPSHOT = 5;    // client ack: data = uint32 big-endian sequence number
}
```

Replace with:

```proto
enum ClientEventCode {
    CE_PLAYER_INPUT = 0;
    CE_PING = 1;
    CE_LOGIN = 2;
    CE_CHAT = 3;
    CE_ACK_SNAPSHOT = 4;    // client ack: data = uint32 big-endian sequence number
}
```

- [ ] **Step 2: Remove `SE_PLAYER_DIED` and renumber `SE_LOGIN_REJECTED` in `enginepb/engine.proto`**

Edit `proto/enginepb/engine.proto` lines 53-65. The current enum is:

```proto
enum ServerEventCode {
    SE_WORLD_UPDATE = 0;
    SE_PLAYER_SPAWNED = 1;
    SE_PONG = 2;
    SE_PLAYER_DIED = 3;
    SE_LOGIN_REJECTED = 4;
    SE_CHAT = 10;
    SE_PLAYER_OWN_STATE = 11;
    SE_CELL_CHANGE = 12;
    SE_DELTA_WORLD_UPDATE = 13;  // binary delta-compressed world update
    SE_CELL_TOPOLOGY = 14;      // cell topology update (debug/dynamic partitioning)
    SE_SERVER_CONFIG = 15;      // engine config sent on connect (tick rate, etc.)
}
```

Replace with (drop `SE_PLAYER_DIED`; renumber `SE_LOGIN_REJECTED` from 4 to 3; leave the intentional 4-9 reservation gap as-is):

```proto
enum ServerEventCode {
    SE_WORLD_UPDATE = 0;
    SE_PLAYER_SPAWNED = 1;
    SE_PONG = 2;
    SE_LOGIN_REJECTED = 3;
    SE_CHAT = 10;
    SE_PLAYER_OWN_STATE = 11;
    SE_CELL_CHANGE = 12;
    SE_DELTA_WORLD_UPDATE = 13;  // binary delta-compressed world update
    SE_CELL_TOPOLOGY = 14;      // cell topology update (debug/dynamic partitioning)
    SE_SERVER_CONFIG = 15;      // engine config sent on connect (tick rate, etc.)
}
```

- [ ] **Step 3: Delete `RespawnRequestMsg` and `PlayerDiedMsg` from `enginepb/engine.proto`**

Edit `proto/enginepb/engine.proto`. Delete line 101:

```proto
message RespawnRequestMsg {}
```

And delete lines 113-115:

```proto
message PlayerDiedMsg {
    uint32 killer_id = 1; // network ID of who killed you, 0 if unknown
}
```

Leave surrounding whitespace sensible — one blank line between remaining messages.

- [ ] **Step 4: Add `GCE_RESPAWN = 14` to `GameClientEventCode` in `gamepb/game.proto`**

Edit `proto/gamepb/game.proto` lines 13-24. Current enum:

```proto
enum GameClientEventCode {
    GCE_UNKNOWN = 0;
    GCE_INVENTORY_TRANSFER = 5;
    GCE_BANK_REQUEST = 6;
    GCE_SELL_BANK_ITEM = 7;
    GCE_EQUIP = 8;
    GCE_SHOP_BUY = 9;
    GCE_DOCK = 10;
    GCE_UNDOCK = 11;
    GCE_LOOT_ITEM = 12;
    GCE_LOOT_ALL = 13;
}
```

Add `GCE_RESPAWN = 14;` as the last entry:

```proto
enum GameClientEventCode {
    GCE_UNKNOWN = 0;
    GCE_INVENTORY_TRANSFER = 5;
    GCE_BANK_REQUEST = 6;
    GCE_SELL_BANK_ITEM = 7;
    GCE_EQUIP = 8;
    GCE_SHOP_BUY = 9;
    GCE_DOCK = 10;
    GCE_UNDOCK = 11;
    GCE_LOOT_ITEM = 12;
    GCE_LOOT_ALL = 13;
    GCE_RESPAWN = 14;
}
```

- [ ] **Step 5: Add `GSE_PLAYER_DIED = 108` to `GameServerEventCode` in `gamepb/game.proto`**

Edit `proto/gamepb/game.proto` lines 27-37. Current enum:

```proto
enum GameServerEventCode {
    GSE_UNKNOWN = 0;
    GSE_BANK_CONTENTS = 100;
    GSE_TRANSFER_RESULT = 101;
    GSE_EQUIP_RESULT = 102;
    GSE_DOCKING_STATE = 103;
    GSE_DOCKED = 104;
    GSE_MAP_DATA = 105;
    GSE_DEBUG_FLAGS = 106;
    GSE_CURRENCY_UPDATE = 107;
}
```

Add `GSE_PLAYER_DIED = 108;` as the last entry:

```proto
enum GameServerEventCode {
    GSE_UNKNOWN = 0;
    GSE_BANK_CONTENTS = 100;
    GSE_TRANSFER_RESULT = 101;
    GSE_EQUIP_RESULT = 102;
    GSE_DOCKING_STATE = 103;
    GSE_DOCKED = 104;
    GSE_MAP_DATA = 105;
    GSE_DEBUG_FLAGS = 106;
    GSE_CURRENCY_UPDATE = 107;
    GSE_PLAYER_DIED = 108;
}
```

- [ ] **Step 6: Add `RespawnRequestMsg` and `PlayerDiedMsg` message definitions in `gamepb/game.proto`**

Edit `proto/gamepb/game.proto`. The file already has a clear "GAME CLIENT EVENT PAYLOADS" section (`// ════════════════════════...` banner around line 126) and a "GAME SERVER EVENT PAYLOADS" section (around line 167).

Add `RespawnRequestMsg` inside the "GAME CLIENT EVENT PAYLOADS" section, immediately after the existing `UndockRequestMsg {}` (around line 155):

```proto
message DockRequestMsg {}

message UndockRequestMsg {}

message RespawnRequestMsg {}

message LootItemMsg {
    uint32 crate_net_id = 1;
    uint32 item_id = 2;
}
```

Add `PlayerDiedMsg` inside the "GAME SERVER EVENT PAYLOADS" section, right after `DockedMsg {}` (around line 307):

```proto
message DockedMsg {}

message PlayerDiedMsg {
    uint32 killer_id = 1; // network ID of who killed you, 0 if unknown
}

message AbilityCastResultMsg {
    uint32 slot = 1;
    ...
```

---

## Task 2: Regenerate proto bindings

**Files:**
- Regenerate: `gen/go/enginepb/engine.pb.go`, `gen/go/gamepb/game.pb.go`
- Regenerate: `gen/csharp/Engine.cs`, `gen/csharp/Game.cs`
- Regenerate: `gen/es/enginepb/engine_pb.ts`, `gen/es/gamepb/game_pb.ts`

- [ ] **Step 1: Run `buf generate` via just**

```bash
just proto
```

Expected: command exits 0, no stderr errors. `git status` will show modifications in `gen/go/enginepb/`, `gen/go/gamepb/`, `gen/csharp/`, `gen/es/enginepb/`, `gen/es/gamepb/`.

- [ ] **Step 2: Spot-check the Go bindings landed**

```bash
grep -n "PlayerDiedMsg\|RespawnRequestMsg" gen/go/enginepb/engine.pb.go gen/go/gamepb/game.pb.go | head
```

Expected: `PlayerDiedMsg` and `RespawnRequestMsg` appear ONLY in `gen/go/gamepb/game.pb.go` (many matches — type def, schema, etc.), and ZERO matches in `gen/go/enginepb/engine.pb.go`.

- [ ] **Step 3: Spot-check the enum values**

```bash
grep -n "GCE_RESPAWN\|GSE_PLAYER_DIED\|CE_RESPAWN\|SE_PLAYER_DIED" gen/go/enginepb/engine.pb.go gen/go/gamepb/game.pb.go | head
```

Expected: `GCE_RESPAWN` / `GameClientEventCode_GCE_RESPAWN` and `GSE_PLAYER_DIED` / `GameServerEventCode_GSE_PLAYER_DIED` appear in `gen/go/gamepb/game.pb.go`. No hits for `CE_RESPAWN` or `SE_PLAYER_DIED` anywhere under `gen/go/`.

At this point the tree does NOT compile — Go callers still reference the deleted engine symbols. Task 3 fixes that.

---

## Task 3: Update Go callers

**Files:**
- Modify: `cmd/server/main.go:44` (client-event registration for `CE_RESPAWN`)
- Modify: `cmd/server/main.go:57-58` (server-event registration for `SE_PLAYER_DIED`)
- Modify: `internal/game/lifecycle.go:62` (death-event send)
- Modify: `internal/game/input_handlers.go:27` (respawn handler registration)
- Modify: `internal/bot/actions.go:97` (bot respawn send)
- Modify: `internal/bot/bot.go:213-214` (bot death event decode)

- [ ] **Step 1: Update `cmd/server/main.go` respawn registration**

Open `cmd/server/main.go`. At line ~44 the current line is:

```go
mmokit.RegisterClientEvent[enginepb.RespawnRequestMsg](e, enginepb.ClientEventCode_CE_RESPAWN)
```

Replace with:

```go
mmokit.RegisterClientEvent[gamepb.RespawnRequestMsg](e, gamepb.GameClientEventCode_GCE_RESPAWN)
```

- [ ] **Step 2: Update `cmd/server/main.go` player-died registration**

In the same file at lines ~57-58 the current block is:

```go
mmokit.RegisterServerEvent[enginepb.PlayerDiedMsg](e,
    enginepb.ServerEventCode_SE_PLAYER_DIED)
```

Replace with:

```go
mmokit.RegisterServerEvent[gamepb.PlayerDiedMsg](e,
    gamepb.GameServerEventCode_GSE_PLAYER_DIED)
```

(Both `enginepb` and `gamepb` are already imported at the top of the file — no import change needed.)

- [ ] **Step 3: Update `internal/game/lifecycle.go` death send**

Open `internal/game/lifecycle.go`. Line ~62 currently reads:

```go
gw.ServerEvents().Send(gw.eng.ConnMgr, death.ConnID, uint32(enginepb.ServerEventCode_SE_PLAYER_DIED), &enginepb.PlayerDiedMsg{
    KillerId: death.KillerNetID,
})
```

Replace with:

```go
gw.ServerEvents().Send(gw.eng.ConnMgr, death.ConnID, uint32(gamepb.GameServerEventCode_GSE_PLAYER_DIED), &gamepb.PlayerDiedMsg{
    KillerId: death.KillerNetID,
})
```

`gamepb` is already imported at the top of the file (`processDockCompletions` uses `gamepb.GameServerEventCode_GSE_DOCKED`). No import change needed.

- [ ] **Step 4: Update `internal/game/input_handlers.go` respawn router registration**

Open `internal/game/input_handlers.go`. Line ~27 currently reads:

```go
router.Handle(uint32(enginepb.ClientEventCode_CE_RESPAWN), mmokit.States(StateDead), handleRespawn(gw))
```

Replace with:

```go
router.Handle(uint32(gamepb.GameClientEventCode_GCE_RESPAWN), mmokit.States(StateDead), handleRespawn(gw))
```

Also update the comment above `handleRespawn` at line ~109:

```go
// handleRespawn processes CE_RESPAWN. Logs and enqueues a respawn request.
```

Replace with:

```go
// handleRespawn processes GCE_RESPAWN. Logs and enqueues a respawn request.
```

Both `enginepb` and `gamepb` are already imported. No import change needed.

- [ ] **Step 5: Update `internal/bot/actions.go` bot respawn send**

Open `internal/bot/actions.go`. Line ~97 currently reads:

```go
b.sendEvent(uint32(enginepb.ClientEventCode_CE_RESPAWN), &enginepb.RespawnRequestMsg{}, true)
```

Replace with:

```go
b.sendEvent(uint32(gamepb.GameClientEventCode_GCE_RESPAWN), &gamepb.RespawnRequestMsg{}, true)
```

Check imports at the top of `internal/bot/actions.go`. If `gamepb` is not already imported, add:

```go
gamepb "github.com/mmokit/mmokit/gen/go/gamepb"
```

to the import block alongside the existing `enginepb` import.

- [ ] **Step 6: Update `internal/bot/bot.go` death-event decode**

Open `internal/bot/bot.go`. Read the full `switch` block containing the `SE_PLAYER_DIED` case (likely lines ~195-260) before editing — the case's `evt.Code` value is being compared against a typed enum, so mixing `enginepb.ServerEventCode_*` and `gamepb.GameServerEventCode_*` in one switch expression won't compile.

The minimal fix: change the switch expression from `switch enginepb.ServerEventCode(evt.Code)` (or similar cast form) to `switch evt.Code`, and update every `case` label to `uint32(enginepb.ServerEventCode_SE_XXX)` / `uint32(gamepb.GameServerEventCode_GSE_XXX)`.

Apply this transform, then update the PLAYER_DIED case specifically — change from:

```go
case enginepb.ServerEventCode_SE_PLAYER_DIED:
    var died enginepb.PlayerDiedMsg
```

to:

```go
case uint32(gamepb.GameServerEventCode_GSE_PLAYER_DIED):
    var died gamepb.PlayerDiedMsg
```

(The body below — `proto.Unmarshal(evt.Data, &died)`, `b.deathCh <- died.KillerId`, etc. — is type-compatible between the two proto types because both define `KillerId uint32`. No body changes.)

Check imports at the top of `internal/bot/bot.go`. `gamepb` likely is already imported (the bot consumes game events too — `gamepb.PlayerOwnStateMsg` is right next to this case). If not, add:

```go
gamepb "github.com/mmokit/mmokit/gen/go/gamepb"
```

- [ ] **Step 7: Run `go vet ./...`**

```bash
go vet ./...
```

Expected: exits 0, no output. If vet complains about unused `enginepb` imports, confirm the file still references `enginepb` elsewhere (it does for almost every file on this list — `SE_WORLD_UPDATE`, `SE_PONG`, `CE_CHAT`, etc.). If a file genuinely has no remaining `enginepb` references, remove the import.

- [ ] **Step 8: Build the Go binary to verify full compilation**

```bash
just build-go
```

Expected: compiles to `bin/server`, exits 0. Do NOT use `go build ./...` (CLAUDE.md explicitly forbids it — it drops binaries in package directories).

---

## Task 4: Regenerate the web-pixi SDK (pre-sdkgen-change)

**Files:**
- Regenerate: `web-pixi/sdk/client.ts` (primary change — import paths for the two schemas + event-code numerals on send/subscribe calls)
- Regenerate: `web-pixi/sdk/entities.ts`, `web-pixi/sdk/delta-decoder.ts`, `web-pixi/sdk/index.ts`, `web-pixi/sdk/transport.ts` (may be touched depending on sdkgen implementation, but should be unchanged by this refactor on its own)

This task regenerates the SDK after Task 3 so we can commit Commit A in a compilable state before moving on to the sdkgen improvement. It will be regenerated *again* in Task 6 after the sdkgen change; both regenerations are cheap and idempotent.

- [ ] **Step 1: Run `just space-sdk` to regenerate**

```bash
just space-sdk
```

Expected: exits 0. Pipes `go run ./cmd/server --dump-schema` through `cmd/sdkgen` into `web-pixi/sdk/`.

- [ ] **Step 2: Verify client.ts import paths changed**

```bash
grep -n "PlayerDiedMsg\|RespawnRequestMsg" web-pixi/sdk/client.ts
```

Expected: all `PlayerDiedMsgSchema`, `PlayerDiedMsg`, `RespawnRequestMsgSchema` imports now come from `"@gen/gamepb/game_pb.js"` (NOT `"@gen/enginepb/engine_pb.js"`). Method names `sendRespawnRequest(...)` and `onPlayerDied(...)` unchanged (derivation is stable: `RespawnRequestMsg` → strip `Msg` → `RespawnRequest`; `GSE_PLAYER_DIED` → strip `GSE_` → `playerDied`, same as the old `SE_PLAYER_DIED` → `playerDied`).

- [ ] **Step 3: Verify event codes updated in client.ts**

```bash
grep -n "sendRespawnRequest\|onPlayerDied" web-pixi/sdk/client.ts
```

Expected: `sendRespawnRequest` body now calls `this.sendEvent(14, data)` (was `3`); `onPlayerDied` body now calls `this.on(108, ...)` (was `3`).

---

## Task 5: Update `web-pixi/src/network.ts` import (minimal — Commit A)

**Files:**
- Modify: `web-pixi/src/network.ts:20-26` (enginepb import block)

- [ ] **Step 1: Move `PlayerDiedMsg` type import from enginepb to the existing gamepb import block**

Open `web-pixi/src/network.ts`. Lines 8-26 currently read:

```typescript
import type {
  WorldUpdateMsg,
  PlayerSpawnedMsg,
  BankContentsMsg,
  TransferResultMsg,
  EquipResultMsg,
  DockingStateMsg,
  PlayerOwnStateMsg,
  MapDataMsg,
  MapStationInfo,
  CurrencyUpdateMsg,
} from "@gen/gamepb/game_pb.js";
import type {
  PongMsg,
  LoginRejectedMsg,
  PlayerDiedMsg,
  CellTopologyMsg,
  CellInfo as PbCellInfo,
} from "@gen/enginepb/engine_pb.js";
```

Replace with:

```typescript
import type {
  WorldUpdateMsg,
  PlayerSpawnedMsg,
  BankContentsMsg,
  TransferResultMsg,
  EquipResultMsg,
  DockingStateMsg,
  PlayerOwnStateMsg,
  MapDataMsg,
  MapStationInfo,
  CurrencyUpdateMsg,
  PlayerDiedMsg,
} from "@gen/gamepb/game_pb.js";
import type {
  PongMsg,
  LoginRejectedMsg,
  CellTopologyMsg,
  CellInfo as PbCellInfo,
} from "@gen/enginepb/engine_pb.js";
```

No callsite changes — the type name `PlayerDiedMsg` is identical across the two proto packages.

- [ ] **Step 2: Full build to catch any drift**

```bash
just build
```

Expected: exits 0. Both the Go server and the web client compile and bundle.

- [ ] **Step 3: Commit A — the proto move**

```bash
git status
git add proto/enginepb/engine.proto proto/gamepb/game.proto \
        gen/go/enginepb gen/go/gamepb gen/csharp gen/es \
        cmd/server/main.go \
        internal/game/lifecycle.go internal/game/input_handlers.go \
        internal/bot/actions.go internal/bot/bot.go \
        web-pixi/src/network.ts web-pixi/sdk
git commit -m "$(cat <<'EOF'
refactor(proto): move PlayerDiedMsg + RespawnRequestMsg to gamepb

PlayerDiedMsg carries combat semantics (killer_id) and respawn is not a
universal engine concept — examples/slither has its own SCE_RESPAWN,
examples/4node-basic uses neither. Nothing in pkg/ references the
engine-proto versions. Move both to gamepb where the zenion game's other
lifecycle messages already live.

- enginepb: drop CE_RESPAWN, SE_PLAYER_DIED, and their message types;
  close the resulting enum gaps (CE_CHAT 4→3, CE_ACK_SNAPSHOT 5→4,
  SE_LOGIN_REJECTED 4→3).
- gamepb: add GCE_RESPAWN=14, GSE_PLAYER_DIED=108, and the two messages.
- Update all Go and TS callers; regenerate bindings and web SDK.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Expected: commit lands, `git status` is clean.

---

## Task 6: Teach sdkgen to re-export SDK-surfaced proto types

**Files:**
- Modify: `cmd/sdkgen/generate.go` — extend `genIndex()` to emit `export type { ... } from "@gen/..."` lines.

**Design:**

The generator already has `collectTypeImports()` at [cmd/sdkgen/generate.go:849-871](cmd/sdkgen/generate.go#L849-L871) which builds the exact `map[importPath][]typeName` we need — it walks `g.schema.ServerEvents` (skipping binary-payload events where `ProtoName == ""`) and `g.schema.Operations[].ResponseProto`. This is precisely the set of proto types that appear in the SDK's public method signatures (handler args + operation response return types).

`genIndex()` currently emits only entity exports, the client class, and the transport class (see [cmd/sdkgen/generate.go:898-909](cmd/sdkgen/generate.go#L898-L909)).

The fix is additive: call `collectTypeImports()` from `genIndex()` and emit one `export type { ... }` line per import-path group. Sort paths and type names for deterministic output.

- [ ] **Step 1: Extend `genIndex()` to emit proto type re-exports**

Open `cmd/sdkgen/generate.go`. Find `genIndex()` (currently ~line 898):

```go
func (g *Generator) genIndex() string {
    gameName := titleCase(g.schema.Game)
    var b strings.Builder
    b.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
    fmt.Fprintf(&b, "export { %sClient } from \"./client.js\";\n", gameName)
    fmt.Fprintf(&b, "export type { %sClientOptions } from \"./client.js\";\n", gameName)
    b.WriteString("export type { AnyEntity, DeltaWorldUpdate } from \"./entities.js\";\n")
    b.WriteString("export * from \"./entities.js\";\n")
    fmt.Fprintf(&b, "export { %sDeltaDecoder } from \"./delta-decoder.js\";\n", gameName)
    b.WriteString("export { Transport } from \"./transport.js\";\n")
    return b.String()
}
```

Replace with:

```go
func (g *Generator) genIndex() string {
    gameName := titleCase(g.schema.Game)
    var b strings.Builder
    b.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
    fmt.Fprintf(&b, "export { %sClient } from \"./client.js\";\n", gameName)
    fmt.Fprintf(&b, "export type { %sClientOptions } from \"./client.js\";\n", gameName)
    b.WriteString("export type { AnyEntity, DeltaWorldUpdate } from \"./entities.js\";\n")
    b.WriteString("export * from \"./entities.js\";\n")
    fmt.Fprintf(&b, "export { %sDeltaDecoder } from \"./delta-decoder.js\";\n", gameName)
    b.WriteString("export { Transport } from \"./transport.js\";\n")

    // Re-export proto types that appear in the SDK's public method
    // signatures — consumers should not have to reach into @gen/... for
    // types the SDK already uses on its own surface.
    typeImports := g.collectTypeImports()
    paths := make([]string, 0, len(typeImports))
    for p := range typeImports {
        paths = append(paths, p)
    }
    sort.Strings(paths)
    for _, path := range paths {
        names := append([]string(nil), typeImports[path]...)
        sort.Strings(names)
        fmt.Fprintf(&b, "export type { %s } from %q;\n", strings.Join(names, ", "), path)
    }

    return b.String()
}
```

No new imports needed — `sort` and `strings` are already imported at the top of `generate.go`.

- [ ] **Step 2: Build and run `sdkgen` standalone against the space schema to spot-check**

```bash
go run ./cmd/server --dump-schema > /tmp/space-schema.json
go run ./cmd/sdkgen --out /tmp/space-sdk-preview --proto-es gen/es --core pkg/quantize/ts/delta-decoder-core.ts < /tmp/space-schema.json
grep -n "export type" /tmp/space-sdk-preview/index.ts
```

Expected: in addition to the pre-existing `export type { SpaceClientOptions }` and `export type { AnyEntity, DeltaWorldUpdate }` lines, new lines appear at the bottom like:

```typescript
export type { CellChangeMsg, CellTopologyMsg, LoginRejectedMsg, PongMsg, ServerConfigMsg } from "@gen/enginepb/engine_pb.js";
export type { BankContentsMsg, CurrencyUpdateMsg, DockedMsg, DockingStateMsg, EquipResultMsg, MapDataMsg, MarketMyOrdersResponse, MarketOrderBookResponse, MarketOrderResultResponse, PlayerDiedMsg, PlayerOwnStateMsg, PlayerSpawnedMsg, TransferResultMsg, WorldUpdateMsg } from "@gen/gamepb/game_pb.js";
```

Exact lists depend on the schema dump; the key check is that `PlayerDiedMsg` now appears on the `gamepb` line (confirming the Commit A proto move carries through) and that the index re-exports the same set the client.ts imports.

Clean up: `rm -rf /tmp/space-sdk-preview /tmp/space-schema.json`.

- [ ] **Step 3: Regenerate the space-game SDK for real**

```bash
just space-sdk
```

Expected: exits 0. `web-pixi/sdk/index.ts` now contains the `export type` re-export lines.

- [ ] **Step 4: Regenerate the 4node-basic SDK**

```bash
just client-sdk examples/4node-basic
```

Expected: exits 0. `examples/4node-basic/web/sdk/index.ts` gains its own `export type` lines, scoped to whatever types that game's schema dump surfaces (likely a smaller set than the space game — basic events only).

- [ ] **Step 5: Regenerate the slither SDK**

```bash
just client-sdk examples/slither
```

Expected: exits 0. `examples/slither/web/sdk/index.ts` gains `export type` lines for slither's surfaced types.

- [ ] **Step 6: Verify none of the example web clients broke**

```bash
cd examples/4node-basic/web && bun install --frozen-lockfile && bun run build && cd -
cd examples/slither/web && bun install --frozen-lockfile && bun run build && cd -
```

Expected: both exit 0. The new `export type` lines are additive — no existing consumer import should break. If either fails with a naming collision (e.g., a game already exports a type with the same name as a proto message), the collision must be resolved in sdkgen before proceeding. Likelihood is low given the `XxxMsg` / `XxxResponse` proto naming convention.

---

## Task 7: Consolidate `web-pixi/src/network.ts` imports to use SDK re-exports

**Files:**
- Modify: `web-pixi/src/network.ts` — consolidate SDK-surfaced proto-type imports to `../sdk/index.js`.

- [ ] **Step 1: Rewrite the import block**

Open `web-pixi/src/network.ts`. Lines 1-33 currently (after Task 5) read approximately:

```typescript
import {
  SpaceClient,
  type AnyEntity,
  type DeltaWorldUpdate,
  type ShipEntity,
  type NPCEntity,
} from "../sdk/index.js";
import type {
  WorldUpdateMsg,
  PlayerSpawnedMsg,
  BankContentsMsg,
  TransferResultMsg,
  EquipResultMsg,
  DockingStateMsg,
  PlayerOwnStateMsg,
  MapDataMsg,
  MapStationInfo,
  CurrencyUpdateMsg,
  PlayerDiedMsg,
} from "@gen/gamepb/game_pb.js";
import type {
  PongMsg,
  LoginRejectedMsg,
  CellTopologyMsg,
  CellInfo as PbCellInfo,
} from "@gen/enginepb/engine_pb.js";
```

Rewrite to use the SDK for SDK-surfaced types; keep direct `@gen/...` imports ONLY for nested types the SDK doesn't surface (`MapStationInfo` is nested inside `MapDataMsg.stations`; `CellInfo` is nested inside `CellTopologyMsg.cells`):

```typescript
import {
  SpaceClient,
  type AnyEntity,
  type DeltaWorldUpdate,
  type ShipEntity,
  type NPCEntity,
  type WorldUpdateMsg,
  type PlayerSpawnedMsg,
  type BankContentsMsg,
  type TransferResultMsg,
  type EquipResultMsg,
  type DockingStateMsg,
  type PlayerOwnStateMsg,
  type MapDataMsg,
  type CurrencyUpdateMsg,
  type PlayerDiedMsg,
  type PongMsg,
  type LoginRejectedMsg,
  type CellTopologyMsg,
} from "../sdk/index.js";
// Nested proto types used for iterating repeated fields on server-event
// messages — the SDK doesn't re-export nested shapes yet, so import
// these directly from @gen/... as an explicit escape hatch.
import type { MapStationInfo } from "@gen/gamepb/game_pb.js";
import type { CellInfo as PbCellInfo } from "@gen/enginepb/engine_pb.js";
```

- [ ] **Step 2: Build**

```bash
just build
```

Expected: exits 0. If any type is missing from the SDK re-exports, TypeScript will error with a specific `Cannot find name ...` in `network.ts` — if that happens, add the missing type to `collectTypeImports` (probably reachable via `collectReachableTypes` or similar; otherwise scope that as a follow-up and keep the missing type as a direct `@gen/...` import).

- [ ] **Step 3: Commit B — the sdkgen improvement**

```bash
git status
git add cmd/sdkgen/generate.go \
        web-pixi/sdk \
        examples/4node-basic/web/sdk \
        examples/slither/web/sdk \
        web-pixi/src/network.ts
git commit -m "$(cat <<'EOF'
feat(sdkgen): re-export proto types from SDK index

The generated SDK's public method signatures already use proto message
types as handler args and operation response types, but the SDK never
re-exported them — consumers had to reach into @gen/... directly to
name those types, defeating the SDK's "you don't need to know about
proto" abstraction.

Teach genIndex() to reuse collectTypeImports() and emit `export type`
lines grouped by proto package. web-pixi/src/network.ts now imports
SDK-surfaced proto types from ../sdk/ and keeps only nested-type
escape hatches (MapStationInfo, CellInfo) on @gen/... direct imports.

Regenerates all three in-tree SDKs (space, 4node-basic, slither).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Expected: commit lands clean.

---

## Task 8: Full verification and smoke test

**Files:**
- No edits; pure verification.

- [ ] **Step 1: Full build + type-check**

```bash
just build
```

Expected: exits 0.

- [ ] **Step 2: Search the tree for orphaned references to the moved symbols**

```bash
grep -rn "enginepb.PlayerDiedMsg\|enginepb.RespawnRequestMsg\|ClientEventCode_CE_RESPAWN\|ServerEventCode_SE_PLAYER_DIED" \
  --include="*.go" --include="*.ts" --include="*.proto" \
  . 2>/dev/null | grep -v "gen/go\|gen/es\|gen/csharp\|docs/superpowers/plans"
```

Expected: zero matches.

- [ ] **Step 3: Verify no SDK consumer in web-pixi/src still reaches for SDK-surfaced types from @gen/...**

```bash
grep -rn "@gen/enginepb\|@gen/gamepb" web-pixi/src/
```

Expected: results are limited to `MapStationInfo` and `CellInfo` (nested types) in `network.ts`. Anything else should have moved to the SDK import.

- [ ] **Step 4: Smoke-test in single-process mode**

Start Postgres if not already up:

```bash
just db-up
```

Then run the server:

```bash
./bin/server
```

Expected: server logs `postgres connected at ...`, `game config loaded`, interactive console prompt appears. No panic on startup (the `ServerEvents`/`ClientEvents` registries panic on duplicate codes, and `RegisterServerEvent` call-site type validation would catch a mismatch — clean startup proves registrations are consistent).

Open `http://localhost:8080`. Log in. Observe:
- Player spawns, can click-to-move.
- If combat is easy to trigger: kill the player (ram an asteroid at high velocity, or use an admin `kill` console command if available). Death explosion fires, client goes dead.
- Respawn via whatever web input is wired at [web-pixi/src/input.ts:85](web-pixi/src/input.ts#L85) (`state.client.sendRespawnRequest({})`). Player respawns at station.

If death isn't easy to trigger manually, tail log categories from the server console:

```
log events:*
```

Then spawn a few bots via `bot spawn 10 0_0` and let them kill each other. Absence of router "unknown event code" errors + presence of `GSE_PLAYER_DIED` / `GCE_RESPAWN` trace confirms wire-format round-trip.

- [ ] **Step 5: Shutdown cleanly**

Ctrl+C. Expected: `shutdown: flushed N players`, `shutdown complete`, no errors.

---

## Self-Review Notes

**Spec coverage check:**
- Proto move for `PlayerDiedMsg` → Task 1 Step 6, Task 3 Steps 3+6, Task 5 Step 1.
- Proto move for `RespawnRequestMsg` → Task 1 Step 6, Task 3 Steps 1+5.
- Renumber engine codes to close gaps → Task 1 Steps 1+2.
- Add game codes `GCE_RESPAWN=14`, `GSE_PLAYER_DIED=108` → Task 1 Steps 4+5.
- Regenerate proto bindings → Task 2.
- Update Go callers → Task 3 (all 5 files).
- SDK regen after proto move → Task 4.
- Minimal `network.ts` fix (Commit A) → Task 5.
- sdkgen `genIndex()` re-exports → Task 6 (all steps).
- All three SDK regens (space, 4node-basic, slither) → Task 6 Steps 3-5.
- Full `network.ts` consolidation (Commit B) → Task 7.
- Verify + smoke test → Task 8.

**Potential gotchas flagged inline:**
- Task 3 Step 6: `internal/bot/bot.go` switch expression type-mixing — plan instructs switching on `evt.Code` directly with `uint32(...)` casts on labels.
- Task 3 Step 5+6: `gamepb` import may need to be added; plan includes the import line and notes likely-already-present cases.
- Task 6 Step 6: if any example web build breaks because of a name collision, flag it and pause — unlikely but possible.
- Task 7 Step 2: if a proto type used by `network.ts` isn't re-exported by the SDK (because it's nested or otherwise not in `collectTypeImports`'s scope), keep it as a direct `@gen/...` import and document. Only `MapStationInfo` and `CellInfo` are known nested cases today.
- Derived SDK method-name stability across the proto move: `sendRespawnRequest` stays (proto message name unchanged); `onPlayerDied` stays (both `SE_PLAYER_DIED` and `GSE_PLAYER_DIED` derive to `playerDied` via `deriveEventName`'s prefix-strip logic).

**Tests not added:**
- No new Go unit tests — the proto change is a symbol rename + event-code renumber, and `pkg/mmokit/server_events_name_test.go` already covers `SE_`/`GSE_`/`CE_`/`GCE_` prefix stripping.
- No new sdkgen unit tests — the change calls an existing tested helper (`collectTypeImports`) and emits deterministic string output. Spot-check in Task 6 Step 2 is the verification.
- End-to-end validation is the smoke test in Task 8.
