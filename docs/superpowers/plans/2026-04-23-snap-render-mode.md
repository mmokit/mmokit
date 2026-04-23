# Snap Render Mode (No Interp, No Prediction) — Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a server-declared `ClientRenderMode` config that lets a game choose between two client rendering models. `Interpolated` keeps today's behavior (per-entity `producedAtMs` ring + render-delay interpolation + client-side prediction). `Snap` disables both: every entity renders at the latest server-confirmed position, with no prediction for the local player. The game trades perceived input lag for a complete absence of rubber-band/hitch/seam artifacts — the League-of-Legends "click and wait" model.

**Architecture:** The mode is a single value declared in `mmokit.Config`, included in the protocol schema dump (`--dump-schema`), and emitted as a constant in the generated TypeScript SDK. The client-side render/prediction/interp pipeline reads the constant at bundle time and picks one of two paths. Wire format is unchanged — `producedAtMs` stays on the wire and is simply ignored in Snap mode. No server-side behavioral change; this is a client contract only.

**Tech Stack:** Go (server-side config + schema + sdkgen), TypeScript/bun (client render paths).

**Source motivation:** Replication Timeline Redesign (`2026-04-23-replication-timeline-redesign.md`) landed per-entity `producedAtMs` + client-side interpolation as the unified smoothness layer. In practice, prediction + interp interact in subtle ways at direction-changes, cell handoffs, and arrival boundaries — each fix risks introducing a new seam. Some games (MOBAs, turn-based, grid-movement) don't need per-frame smoothness and prefer the simpler authoritative model. Give them an opt-out.

**Rollout order (strict):** A → B → C → D → E. Each phase ends with a reviewable commit, `go vet ./...` clean between every phase, and TypeScript `bun test` + `bunx tsc --noEmit` clean between every phase.

---

## Design principles

### P1 — Mode is a server-declared contract, not a client toggle

The server's `mmokit.Config` declares the rendering mode for the whole game. The client inherits it via the schema dump + SDK codegen so a fresh clone + `just client-sdk` puts every client on the same contract automatically. Clients never guess; games never get a mixed fleet of clients on different modes.

### P2 — Wire format is mode-agnostic

Keep per-entity `producedAtMs` in the binary frames regardless of mode. Snap-mode clients ignore the stamps; they're an 8 byte/entity cost that's trivial and keeps the schema stable. This means games can flip the mode in config without regenerating the wire — only the generated SDK's render paths differ.

### P3 — `Snap` means "no prediction, no interp, no render-delay"

`ClientRenderMode = Snap` means the client:
- Ignores `producedAtMs` stamps.
- Does NOT maintain a sample ring.
- Does NOT call `updatePrediction`.
- Does NOT compute a `renderTime = serverNow − RENDER_DELAY` cursor.
- Sets `entity.renderX/Y = entity.worldX/Y` on every inbound frame. Between frames, position is held at whatever the last frame said.
- For the local player: no client-side prediction. Clicks send `MOVE_TARGET` and wait for the server's next confirming frame to update position. Input latency is visible but no rubber-band is possible.

The server is unchanged. Tick rate, AoI, replication, handoff, cluster clock — all keep their existing behavior. Only client rendering differs.

### P4 — Game code is rendering-mode-neutral where possible

Game-code (input handlers, spawn hooks, admin commands) shouldn't branch on render mode. The split lives in two places: the generated SDK's render helpers, and the per-game `renderer.ts` / `interpolation.ts` modules. A future game that wants custom rendering can still own both paths via the SDK's exported primitives.

---

## Scope check

This is one feature in one place with two code paths (Go server config + schema + TS client consumer). No subsystem decomposition needed. Ships as one plan.

---

## File structure

### New files

- `docs/superpowers/plans/2026-04-23-snap-render-mode.md` — this plan.
- `examples/4node-basic/web/src/__tests__/snap-render-mode.test.ts` — unit tests for the Snap path (pending clientState → on-frame → snapped position).

### Modified files (Go)

- `pkg/universe/coordinator.go` — add `Config.ClientRenderMode` enum + default.
- `pkg/mmokit/protocol.go` — add `ClientRenderMode` to `ProtocolSchema`.
- `pkg/mmokit/mmokit.go` — expose the enum type + helpers for game code.
- `cmd/sdkgen/generate.go` — emit `CLIENT_RENDER_MODE` constant + any mode-switch scaffolding into the generated TS SDK.

### Modified files (TS)

- `pkg/quantize/ts/delta-decoder-core.ts` — (possibly) skip the producedAtMs BigInt assembly in Snap mode. Wire reads stay identical; only the parsed field is zero-filled. Optional optimization.
- `examples/4node-basic/web/src/constants.ts` — already has `RENDER_DELAY`, `MAX_EXTRAPOLATE_MS`, `RING_SIZE`. Add `CLIENT_RENDER_MODE` import wiring.
- `examples/4node-basic/web/src/interpolation.ts` — `interpolateEntities` + `updatePrediction` become no-ops in Snap mode. `updateEntityFromServer` sets renderX = worldX directly.
- `examples/4node-basic/web/src/renderer.ts` — skip the `bodyDisplayX` lerp logic in Snap mode; body renders at worldX.
- `examples/4node-basic/web/src/input.ts` — skip the predictedX/Y seeding on click in Snap mode (state.predictionActive stays false).
- `examples/4node-basic/web/src/network.ts` — skip the `observeFrameStamps` call in Snap mode (clock sync not needed).

### Generated (via `just client-sdk examples/4node-basic`)

- `examples/4node-basic/web/sdk/entities.ts` — adds `CLIENT_RENDER_MODE` const export (driven by sdkgen).

### Deleted

None.

---

## Lessons carried forward

1. **No backward compat** — flip the config; every existing caller stays on the default (`Interpolated`). No shims.
2. **Wire format schema = runtime bytes** — `producedAtMs` stays on the wire unconditionally. Skipping decode in Snap mode is a client optimization; bytes produced by the server are always there.
3. **`bun`, not `npm`** — all TS test runs via `bun`.
4. **`go vet`, not `go build ./...`** — CLAUDE.md default.
5. **Test both paths** — `bun test` covers Interpolated (existing tests) and Snap (new tests) in the same suite.

---

## Phase A — Go-side config and schema

### Task A1: Add `ClientRenderMode` enum + Config field

**Files:**
- Modify: `pkg/universe/coordinator.go`
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Define the enum in `pkg/universe/coordinator.go`**

Add near the other Config-related types (top of file):

```go
// ClientRenderMode declares how generated clients should render
// incoming replication frames. Exposed on the protocol schema so
// client SDK codegen can emit a matching constant — games don't
// need to duplicate the choice in their client config.
type ClientRenderMode string

const (
	// ClientRenderInterpolated is the default: clients buffer samples
	// in a per-entity ring, interpolate between them using ClusterClock-
	// stamped producedAtMs, render with RENDER_DELAY lag, and run
	// client-side prediction for the local player. Smooth motion at
	// 60fps, at the cost of ~100ms render-lag + complex reconciliation
	// at direction changes / cell handoffs.
	ClientRenderInterpolated ClientRenderMode = "interpolated"

	// ClientRenderSnap disables both interpolation and prediction.
	// Clients render every entity at the latest received server
	// worldX/worldY; positions step at the server tick cadence
	// (typically 20 Hz). No rubber-band, no seams, no hitches — input
	// latency is visible but authoritative by construction.
	//
	// Suits MOBA / RTS / grid-movement / turn-based games where
	// per-frame smoothness matters less than deterministic behavior.
	ClientRenderSnap ClientRenderMode = "snap"
)
```

Add to the `Config` struct (alphabetical or grouped with other client-facing fields):

```go
	// ClientRenderMode declares how generated clients should render
	// replication frames. Default is ClientRenderInterpolated
	// (smooth, render-lagged, predicted). Games that prefer the
	// League-of-Legends server-authoritative model set this to
	// ClientRenderSnap.
	ClientRenderMode ClientRenderMode
```

In `applyDefaults` (or wherever `Config` zero-values get populated):

```go
	if cfg.ClientRenderMode == "" {
		cfg.ClientRenderMode = ClientRenderInterpolated
	}
```

- [ ] **Step 2: Re-export via `pkg/mmokit`**

In `pkg/mmokit/mmokit.go`, add to the type-alias + const-alias blocks (wherever `Config` and related types are re-exported):

```go
	// ClientRenderMode declares how the client SDK should render
	// replication frames. See universe.ClientRenderMode.
	ClientRenderMode = universe.ClientRenderMode

	// Client render mode constants.
	ClientRenderInterpolated = universe.ClientRenderInterpolated
	ClientRenderSnap         = universe.ClientRenderSnap
```

- [ ] **Step 3: go vet + unit test**

Write a tiny unit test in `pkg/universe/coordinator_test.go` (or wherever config-default tests live):

```go
func TestConfig_DefaultClientRenderMode(t *testing.T) {
	var cfg Config
	applyConfigDefaults(&cfg) // or whatever the default-application helper is called
	if cfg.ClientRenderMode != ClientRenderInterpolated {
		t.Errorf("default ClientRenderMode = %q, want %q", cfg.ClientRenderMode, ClientRenderInterpolated)
	}
}

func TestConfig_ClientRenderSnap_Preserved(t *testing.T) {
	cfg := Config{ClientRenderMode: ClientRenderSnap}
	applyConfigDefaults(&cfg)
	if cfg.ClientRenderMode != ClientRenderSnap {
		t.Errorf("explicit Snap mode overwritten: got %q", cfg.ClientRenderMode)
	}
}
```

Run: `go vet ./... && go test ./pkg/universe/ -run TestConfig_ -v`

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go pkg/mmokit/mmokit.go pkg/universe/coordinator_test.go
git commit -m "feat(universe): add ClientRenderMode config + Interpolated/Snap enum

Games declare their preferred client render model via
Config.ClientRenderMode. Default is Interpolated (current behavior:
sample ring + render-lag interp + prediction). Snap disables both and
renders at the latest-received worldX/worldY — no rubber-band, no
hitch, visible input lag. Clients read the mode via the protocol
schema dump in a subsequent task."
```

---

### Task A2: Include `ClientRenderMode` in `ProtocolSchema`

**Files:**
- Modify: `pkg/mmokit/protocol.go`
- Modify: any schema-export test fixtures

- [ ] **Step 1: Add to `ProtocolSchema`**

Find `type ProtocolSchema struct { ... }` in `pkg/mmokit/protocol.go`. Add a top-level field:

```go
	// ClientRenderMode tells client SDK generators which rendering
	// path to emit. Mirrors Config.ClientRenderMode. "interpolated"
	// or "snap".
	ClientRenderMode ClientRenderMode `json:"clientRenderMode"`
```

- [ ] **Step 2: Populate from Config at schema-export time**

Find where `ProtocolSchema` is built from the running process / config — typically the `--dump-schema` path. Ensure it reads `Config.ClientRenderMode` and copies into the schema.

- [ ] **Step 3: Verify via dump-schema**

Run the dump schema path from 4node-basic:

```bash
go run ./examples/4node-basic --dump-schema | jq .clientRenderMode
```

Expected: `"interpolated"` (default).

Then override in the game's config (edit `main.go` temporarily to set `ClientRenderMode: mmokit.ClientRenderSnap`), re-run, expect `"snap"`. Revert the temp edit.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/protocol.go
git commit -m "feat(schema): export ClientRenderMode in ProtocolSchema

The --dump-schema output now includes clientRenderMode so SDK
codegen can emit a matching client-side constant. Games declare the
mode once on the server; clients inherit."
```

---

## Phase B — SDK codegen emits the mode

### Task B1: Emit `CLIENT_RENDER_MODE` constant + mode-switch helpers

**Files:**
- Modify: `cmd/sdkgen/generate.go`

- [ ] **Step 1: Read the mode from the schema JSON**

In `cmd/sdkgen/generate.go`, wherever the schema is parsed, add:

```go
type schema struct {
	// ...existing fields...
	ClientRenderMode string `json:"clientRenderMode"`
}
```

Default to `"interpolated"` if the field is missing (for backward compatibility with stale schemas — though per no-backcompat, we could require it; keep simple default for now).

- [ ] **Step 2: Emit a TS constant in the generated SDK**

Find the entities.ts emission path. At the top of the file (or a new `_core/mode.ts`), emit:

```typescript
/**
 * Client render mode declared by the server's protocol schema.
 *
 * - "interpolated": use per-entity producedAtMs to build a sample
 *   ring; interpolate with RENDER_DELAY lag; run client-side
 *   prediction for the local player. Smooth motion, ~100ms
 *   visual lag, complex reconciliation.
 *
 * - "snap": render at the latest-received worldX/worldY. No sample
 *   ring, no lerp, no prediction. Input latency is visible but
 *   motion is always authoritative. League-of-Legends model.
 *
 * Game code should branch on this constant to enable/disable the
 * prediction and interp paths.
 */
export const CLIENT_RENDER_MODE = "{{ .ClientRenderMode }}" as const;
export type ClientRenderMode = typeof CLIENT_RENDER_MODE;
```

Where `{{ .ClientRenderMode }}` is substituted with the schema value (use text/template).

- [ ] **Step 3: Emit a helper the client can use**

In the same file (or alongside the constant), emit:

```typescript
export function isSnapMode(): boolean {
	return CLIENT_RENDER_MODE === "snap";
}

export function isInterpolatedMode(): boolean {
	return CLIENT_RENDER_MODE === "interpolated";
}
```

These shave the mode-check syntax down to one call at every branch point.

- [ ] **Step 4: Regenerate the 4node-basic SDK**

```bash
just client-sdk examples/4node-basic
```

Verify the output includes `export const CLIENT_RENDER_MODE = "interpolated"`.

- [ ] **Step 5: Commit**

```bash
git add cmd/sdkgen/generate.go examples/4node-basic/web/sdk/
git commit -m "feat(sdkgen): emit CLIENT_RENDER_MODE + mode-check helpers

Generated SDK exports a type-safe constant inherited from the server's
protocol schema. Game-side render modules branch on isSnapMode() /
isInterpolatedMode()."
```

---

## Phase C — Client Snap render path

### Task C1: Short-circuit `updatePrediction` in Snap mode

**Files:**
- Modify: `examples/4node-basic/web/src/interpolation.ts`
- Modify: `examples/4node-basic/web/src/input.ts`

- [ ] **Step 1: Gate prediction on mode**

In `interpolation.ts`, top of `updatePrediction`:

```typescript
import { isSnapMode } from "../sdk/entities.js";

export function updatePrediction(now: number): void {
	if (isSnapMode()) return;
	// ... existing body unchanged ...
}
```

- [ ] **Step 2: Skip prediction seeding on click**

In `input.ts`:

```typescript
import { isSnapMode } from "../sdk/entities.js";

function setMoveTarget(e: MouseEvent): void {
	const [wx, wy] = worldCoords(e);
	state.moveTargetX = wx;
	state.moveTargetY = wy;
	state.moveTargetActive = true;
	if (!isSnapMode()) {
		const player = state.entities.get(state.playerNetID);
		if (player && !state.predictionActive) {
			state.predictedX = player.worldX;
			state.predictedY = player.worldY;
			state.predictionActive = true;
			state.predictionStartTime = performance.now();
		}
	}
	sendMoveTarget();
}
```

`state.predictionActive` stays false in Snap mode — downstream renderer checks will see that and never enter the predicted path.

- [ ] **Step 3: `bunx tsc --noEmit` + `bun test`**

Existing tests should still pass (Snap mode isn't the default). Run:

```bash
cd examples/4node-basic/web && bunx tsc --noEmit && bun test
```

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/web/src/interpolation.ts examples/4node-basic/web/src/input.ts
git commit -m "feat(4node-basic/web): gate prediction on CLIENT_RENDER_MODE

updatePrediction and setMoveTarget's predictedX seeding no-op in Snap
mode. Interpolated mode (default) behaves exactly as before."
```

---

### Task C2: Short-circuit interpolation in Snap mode

**Files:**
- Modify: `examples/4node-basic/web/src/interpolation.ts`

- [ ] **Step 1: Gate `interpolateEntities` on mode**

Top of the function:

```typescript
export function interpolateEntities(
	entities: Map<number, ClientEntity>,
	clock: ClockSync,
	clientNowMs: number,
): void {
	if (isSnapMode()) {
		// In Snap mode the ring is empty and renderX is set directly
		// by updateEntityFromServer; nothing to interpolate.
		return;
	}
	// ... existing body unchanged ...
}
```

- [ ] **Step 2: Set `renderX/Y = worldX/Y` directly in `updateEntityFromServer` when in Snap mode**

```typescript
export function updateEntityFromServer(
	entities: Map<number, ClientEntity>,
	serverState: AnyEntity,
	producedAtMs: number,
): void {
	const id = serverState.netID;
	const existing = entities.get(id);
	const snap = isSnapMode();

	if (!existing) {
		const rot = entityRotation(serverState, 0);
		const first = sampleFrom(serverState, producedAtMs, rot);
		const ent: ClientEntity = {
			...serverState,
			prevX: serverState.worldX,
			prevY: serverState.worldY,
			isReplica: false,
			isGhost: false,
			samples: snap ? [] : [first],   // no ring in snap mode
			renderX: first.worldX,
			renderY: first.worldY,
			renderRot: first.rotation,
		};
		entities.set(id, ent);
		return;
	}

	const prevRot = existing.renderRot;
	Object.assign(existing, serverState);
	existing.prevX = existing.renderX;
	existing.prevY = existing.renderY;

	if (snap) {
		// No ring; render position updates directly from the frame.
		existing.renderX = serverState.worldX;
		existing.renderY = serverState.worldY;
		existing.renderRot = entityRotation(serverState, prevRot);
	} else {
		pushSample(existing, sampleFrom(serverState, producedAtMs, prevRot));
	}
}
```

- [ ] **Step 3: Unit tests**

Create `examples/4node-basic/web/src/__tests__/snap-render-mode.test.ts`:

```typescript
import { describe, it, expect, mock } from "bun:test";
import { updateEntityFromServer } from "../interpolation.js";
import type { AnyEntity } from "../../sdk/entities.js";
import type { ClientEntity } from "../state.js";

// Helper to mock the sdk's isSnapMode — we override per-test via
// dynamic import / module mocking depending on what bun provides.
// Assume a simple flag-override for this test.

// ...adapt to the test infra we already use...

describe("Snap render mode", () => {
	it("updateEntityFromServer sets renderX/Y to latest worldX/Y", () => {
		// Arrange a mock isSnapMode to return true.
		// ...
		const entities = new Map<number, ClientEntity>();
		const serverState = {
			netID: 7, entityType: 1, producedAtMs: 1000,
			worldX: 100, worldY: 200, velX: 10, velY: 0,
			radius: 4, width: 0, height: 0,
			meshState: 0, ownerNode: 0,
			aoIRadius: 500, name: "",
		} as AnyEntity;
		updateEntityFromServer(entities, serverState, 1000);
		const ent = entities.get(7)!;
		expect(ent.renderX).toBe(100);
		expect(ent.renderY).toBe(200);
		expect(ent.samples).toEqual([]);   // no ring
	});

	it("updateEntityFromServer snaps renderX to new worldX on second frame", () => {
		// ...
	});
});
```

The mocking of `isSnapMode` may require some care — if `bun test` can dynamic-mock modules easily, use that. Otherwise, expose a test-only override (e.g. `setClientRenderModeForTest("snap")`).

Run:

```bash
cd examples/4node-basic/web && bun test
```

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/web/src/interpolation.ts examples/4node-basic/web/src/__tests__/
git commit -m "feat(4node-basic/web): snap-mode interpolateEntities + updateEntityFromServer

Snap-mode pipeline: sample ring stays empty; renderX/Y updates
directly from serverState.worldX/Y on every inbound frame."
```

---

### Task C3: Short-circuit `bodyDisplay` handoff + camera in Snap mode

**Files:**
- Modify: `examples/4node-basic/web/src/renderer.ts`

- [ ] **Step 1: Gate `bodyDisplayX` computation on mode**

Near the top of `loop` (or wherever the bodyDisplay block sits):

```typescript
import { isSnapMode } from "../sdk/entities.js";

// ... inside loop, after interpolateEntities + updatePrediction ...

if (isSnapMode()) {
	// Render position IS the latest server position. No lerp, no
	// prediction, no render-delay. Every entity's renderX has been
	// set to worldX by updateEntityFromServer already.
	state.bodyDisplayX = player.renderX;
	state.bodyDisplayY = player.renderY;
} else {
	// ... existing interpolated-mode bodyDisplay logic ...
}

state.camX = state.bodyDisplayX;
state.camY = state.bodyDisplayY;
```

- [ ] **Step 2: Skip clockSync usage in Snap mode**

In `network.ts` (or wherever `observeFrameStamps` is called):

```typescript
import { isSnapMode } from "../sdk/entities.js";

// ...handle decoded frame...
if (!isSnapMode()) {
	observeFrameStamps(state.clockSync, fresh, performance.now());
}
```

clockSync stays uninitialized in Snap mode. interpolateEntities checks `if (!clock.initialized) return;` as an existing guard, so nothing runs.

- [ ] **Step 3: `bunx tsc --noEmit` + `bun test` + full Go test run**

```bash
cd examples/4node-basic/web && bunx tsc --noEmit && bun test
cd ../.. && go vet ./... && go test ./... -count=1 -timeout 300s
```

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/web/src/renderer.ts examples/4node-basic/web/src/network.ts
git commit -m "feat(4node-basic/web): snap-mode renderer + skip clockSync

Body-display, AoI, camera, and entity draw all read renderX/Y in Snap
mode — which is always the latest server-confirmed position.
clockSync stays uninitialized since nothing uses it."
```

---

## Phase D — Manual verification + bot-load smoke

### Task D1: Manual Interpolated-mode regression pass

- [ ] **Step 1: Build + run with defaults**

```bash
cd examples/4node-basic && just run
```

Open `http://localhost:8080`, log in, click around, cross a cell boundary, reverse direction 180°. Confirm behavior is unchanged from before this plan landed.

- [ ] **Step 2: Optional 4-process distributed run**

```bash
just distributed
```

Same checks across a real multi-process handoff.

### Task D2: Snap mode — flip the switch + rerun

- [ ] **Step 1: Edit `main.go` to set Snap**

In `examples/4node-basic/main.go`, add `ClientRenderMode: mmokit.ClientRenderSnap,` to the Config literal.

- [ ] **Step 2: Regenerate SDK + rebuild**

```bash
just client-sdk examples/4node-basic
cd examples/4node-basic && just build
```

- [ ] **Step 3: Run + verify**

```bash
just run
```

Open the client, log in. Click to move. Expected:
- Input feels laggy (~50-100ms to first visible motion — that's the server round-trip plus a tick).
- Motion is *stepped* at the server tick rate (20Hz). Looks choppy compared to Interpolated mode.
- **No rubber-band, no hitch, no seam artifacts.** Direction reversals are sharp. Cell boundaries are invisible.
- AoI ring is glued to the player (single position source — always worldX).

Then `bot spawn 30 cell_0_0` and verify bots render stepped but without glitches.

### Task D3: Confirm Interpolated mode still the default for games that don't set it

- [ ] **Step 1: Revert the main.go Snap override**

```bash
cd examples/4node-basic
git checkout main.go   # revert the Snap override
```

- [ ] **Step 2: Regenerate SDK + rebuild + run**

```bash
just client-sdk examples/4node-basic
just build
just run
```

Verify Interpolated-mode behavior returns.

---

## Phase E — Docs + CLAUDE.md

### Task E1: CLAUDE.md — document the two modes

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add a "Client render modes" subsection under "Networking & Replication"**

```markdown
### Client render modes

Games declare a client rendering model via `Config.ClientRenderMode`.
The schema dump exports it; sdkgen emits a matching TypeScript
constant; client render / prediction / interpolation code paths branch
on `isSnapMode()` / `isInterpolatedMode()`.

- `ClientRenderInterpolated` (default): per-entity `producedAtMs` ring,
  RENDER_DELAY-lagged interpolation, client-side prediction for the
  local player. Smooth 60fps motion; input feels instant. Complex
  reconciliation at direction changes and cell boundaries — chooses
  smoothness over strict authority.

- `ClientRenderSnap`: disables both interpolation and prediction.
  Entities render at the latest received `worldX/worldY`. Motion
  steps at the server tick rate (~20Hz). Input latency is visible
  (click, wait ~50-100ms, see motion). No rubber-band, no hitch, no
  seam artifacts — authoritative by construction. Suits MOBA / RTS /
  grid-movement / turn-based games.

The wire format is identical in both modes — `producedAtMs` stamps
ride along either way. Switching modes only affects the client render
contract; no server behavior changes.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(CLAUDE.md): describe Interpolated vs Snap client render modes"
```

---

## Self-review

**Spec coverage:**
- Config enum + default: Task A1 ✅
- Schema export: Task A2 ✅
- SDK codegen: Task B1 ✅
- Client gate on prediction: Task C1 ✅
- Client gate on interpolation: Task C2 ✅
- Client gate on renderer/camera/clockSync: Task C3 ✅
- Manual verify both modes: Tasks D1-D3 ✅
- Docs: Task E1 ✅

**Wire format unchanged:** yes — `producedAtMs` stays on the wire; Snap mode just ignores it.

**Default behavior preserved:** yes — games that don't set `ClientRenderMode` get `ClientRenderInterpolated`, which is what they have today.

**Game-code impact:** zero for game logic (input handlers, spawn, admin). Only the `interpolation.ts` / `renderer.ts` / `input.ts` / `network.ts` modules in the example client see the `isSnapMode()` branches.

**Testing:**
- Go unit test for default config value ✅
- Go schema round-trip via --dump-schema ✅
- TS unit tests for updateEntityFromServer snap path ✅
- Manual both-mode playthrough ✅

**Tradeoffs flagged for the user:**
- Snap mode has visible input lag — users who click expect immediate motion; Snap makes them wait one round-trip. This is the design point: some games want that.
- Snap mode has choppier visible motion (20Hz steps vs 60fps interpolated). Could mitigate by bumping TickRate, but at CPU cost. Leave as a follow-up if needed.
- Snap mode makes `producedAtMs` dead weight on the wire (8 bytes/entity). For 4node-basic this is negligible; for a bot-heavy game it's ~80 bytes per 10-entity frame. Could add a schema flag to elide it, but wire-format simplicity wins for now.

**Rough effort estimate:** Small plan. ~200 Go LoC + ~150 TS LoC + tests. Less than a day's implementation.

---

## Open questions (for the user before implementation)

1. **Scope of "disable interp":** Plan currently disables both interp AND prediction in Snap mode. Is that what you want, or would you prefer Snap mode to still predict the local player (clicks feel instant) but not interp other entities? LoL is closer to the latter — local predicted, others interp'd. If you want the local player to still feel instant, say the word and I'll split this into `ClientRenderSnap` (full no-interp) vs `ClientRenderSnapWithPrediction` (predicted self, snapped others).

2. **Tick rate default:** Snap mode at 20Hz is noticeably steppy. Do you want this plan to also bump the default TickRate to 30 or 60Hz for Snap mode? My inclination: ship at 20Hz first, tune later per game.

3. **Schema field naming:** `clientRenderMode` in the schema JSON. Alternatives: `renderMode`, `clientMode`, `rendering`. Picking `clientRenderMode` for explicit clarity; let me know if you'd prefer shorter.

4. **Scope: 4node-basic only, or slither + space game too?** Plan only touches 4node-basic's client. If the space game client (web-pixi/) and slither should also respect the mode, add a Phase F to mirror Tasks C1-C3 there.

Let me know answers or "ship it as-is" and we'll execute.
