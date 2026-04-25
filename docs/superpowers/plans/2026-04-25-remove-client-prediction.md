# Remove Client-Side Prediction — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete the `ClientRenderInterpolated` mode, all client-side prediction code, and consolidate render-lag interpolation into the generated SDK as a copy-through core file.

**Architecture:** Solo, cohesive change. Server first (delete `ClientRenderMode` config + protocol field + sdkgen emission). Then add a new `pkg/quantize/ts/interpolation-core.ts` reference file and copy it through `cmd/sdkgen/main.go` like `delta-decoder-core.ts`. Then rewrite the per-client `interpolation.ts` files in `examples/4node-basic/web/` and `web-pixi/` as thin glue over the SDK core; delete prediction state, input seeding, prediction loop, and renderer prediction-blend. Finally update `CLAUDE.md` and verify.

**Tech Stack:** Go (server, sdkgen), TypeScript (clients, generated SDK), Bun (TS test runner), `just` build.

**Spec:** [docs/superpowers/specs/2026-04-25-remove-client-prediction-design.md](docs/superpowers/specs/2026-04-25-remove-client-prediction-design.md)

---

## Task 1: Delete `ClientRenderMode` Go type and Config field

**Files:**
- Modify: [pkg/universe/coordinator.go](pkg/universe/coordinator.go) — delete type/constants, Config field, default assignment, getter
- Modify: [pkg/mmokit/mmokit.go](pkg/mmokit/mmokit.go) — delete re-exports
- Modify: [pkg/universe/coordinator_test.go](pkg/universe/coordinator_test.go) — delete the two render-mode tests

- [ ] **Step 1: Delete `ClientRenderMode` type and constants in coordinator.go**

Delete lines 38-64 (the `// ClientRenderMode declares...` doc block, the `type ClientRenderMode string` declaration, and the `ClientRenderSnap` / `ClientRenderInterpolated` constants).

- [ ] **Step 2: Delete `Config.ClientRenderMode` field**

Delete lines 222-228 in coordinator.go (the field plus its leading comment).

- [ ] **Step 3: Delete the default-assignment in Config init**

Delete lines 468-469 in coordinator.go (the `if cfg.ClientRenderMode == "" { cfg.ClientRenderMode = ClientRenderSnap }` block).

- [ ] **Step 4: Delete the `Process.ClientRenderMode()` getter**

Delete lines 752-756 in coordinator.go.

- [ ] **Step 5: Delete the mmokit re-exports**

Delete lines 291-298 in `pkg/mmokit/mmokit.go` (the `type ClientRenderMode = ...` alias and the two constant re-exports).

- [ ] **Step 6: Delete the two coordinator tests**

Delete `TestConfig_DefaultClientRenderMode` and `TestConfig_ClientRenderInterpolated_Preserved` from `pkg/universe/coordinator_test.go` (lines 5-25). If the file becomes empty (only `package universe` and imports), keep it as-is — Go is fine with that.

- [ ] **Step 7: Verify Go compiles**

Run: `just build` — expected to FAIL at `pkg/mmokit/protocol.go` references to `ClientRenderMode` (those are deleted in Task 2). Skip `just build` and use:

Run: `go vet ./pkg/universe/... ./pkg/mmokit/... 2>&1 | head -30`

Expected: errors about `ClientRenderMode` in `pkg/mmokit/protocol.go` only — confirms `pkg/universe` is clean.

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/coordinator_test.go pkg/mmokit/mmokit.go
git commit -m "$(cat <<'EOF'
refactor(universe): delete ClientRenderMode type and Config field

Snap is the only rendering model. The mode discriminator is removed
from the Config surface, the mmokit re-exports are dropped, and the
two tests covering default + override are deleted.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Delete `ClientRenderMode` from protocol schema

**Files:**
- Modify: [pkg/mmokit/protocol.go](pkg/mmokit/protocol.go) — delete schema field, internal tracking, setter, schema serialization, AssembleFromProcess auto-sync
- Modify: [pkg/mmokit/protocol_test.go](pkg/mmokit/protocol_test.go) — delete the three SetClientRenderMode tests

- [ ] **Step 1: Delete the `ClientRenderMode` field from the schema struct**

In `pkg/mmokit/protocol.go`, delete the doc comment block at lines 44-46 and the `ClientRenderMode ClientRenderMode` field at line 47.

- [ ] **Step 2: Delete internal field tracking**

Delete the `clientRenderMode` field on the `Protocol` builder struct (around line 59) and its initialization (around line 85).

- [ ] **Step 3: Delete the `SetClientRenderMode` method**

Delete the entire `func (p *Protocol) SetClientRenderMode(...)` method (lines 115-120).

- [ ] **Step 4: Delete schema-serialization references**

Delete the lines that copy the field into the assembled schema (around lines 209 and 223). Search `protocol.go` for `ClientRenderMode` and `clientRenderMode` to ensure all references are gone.

- [ ] **Step 5: Delete the auto-sync in `AssembleFromProcess`**

Delete the `proto.SetClientRenderMode(proc.ClientRenderMode())` call (around lines 279-281).

- [ ] **Step 6: Delete protocol tests**

Delete `TestProtocolSchema_SetClientRenderMode_Interpolated`, `TestProtocolSchema_SetClientRenderMode_EmptyFallsBackToSnap`, and the default-fallback test from `pkg/mmokit/protocol_test.go` (lines 12-59). Adjust file imports if they become unused.

- [ ] **Step 7: Verify Go compiles cleanly**

Run: `go vet ./...`

Expected: zero errors. (`pkg/mmokit/protocol.go` no longer references `ClientRenderMode`, and `cmd/sdkgen/schema.go` still does — that error is fixed in Task 3. Run with package filter:)

Run: `go vet ./pkg/... 2>&1`

Expected: zero errors.

- [ ] **Step 8: Commit**

```bash
git add pkg/mmokit/protocol.go pkg/mmokit/protocol_test.go
git commit -m "$(cat <<'EOF'
refactor(mmokit): drop ClientRenderMode from protocol schema

The protocol no longer carries a render-mode field; assembly skips
the auto-sync and the setter is gone. All three tests covering
the mode-switch are deleted.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Stop sdkgen emitting render-mode constants

**Files:**
- Modify: [cmd/sdkgen/schema.go](cmd/sdkgen/schema.go) — delete `ClientRenderMode string` field from schema struct
- Modify: [cmd/sdkgen/generate.go](cmd/sdkgen/generate.go) — delete the entire mode-emission block (JSDoc + constant + helpers)

- [ ] **Step 1: Delete the schema field**

In `cmd/sdkgen/schema.go`, delete the `ClientRenderMode string` field at line 58 (and any trailing tag).

- [ ] **Step 2: Delete the TS emission block in generate.go**

Delete lines 101-138 in `cmd/sdkgen/generate.go`: the mode read, the default fallback, the multi-line JSDoc comment template, the `CLIENT_RENDER_MODE` constant emission, and the `isSnapMode()` / `isInterpolatedMode()` helper emissions. Make sure no dangling references to the deleted local variable remain in the surrounding function. Re-read the surrounding 20 lines on either side of the deletion to confirm the function still flows correctly.

- [ ] **Step 3: Verify Go compiles**

Run: `go vet ./...`

Expected: zero errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/sdkgen/schema.go cmd/sdkgen/generate.go
git commit -m "$(cat <<'EOF'
refactor(sdkgen): stop emitting CLIENT_RENDER_MODE and mode helpers

The generated SDK no longer carries the render-mode constant or
isSnapMode()/isInterpolatedMode() helpers. Per-client glue that
imported them will be cleaned up in follow-up tasks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `pkg/quantize/ts/interpolation-core.ts`

**Files:**
- Create: `pkg/quantize/ts/interpolation-core.ts`

- [ ] **Step 1: Create the file**

Write `pkg/quantize/ts/interpolation-core.ts` with the following contents (verbatim):

```typescript
/**
 * Reference render-lag interpolation core for mmokit clients.
 *
 * Smooths 20Hz authoritative server samples into 60fps client motion
 * by interpolating on a per-entity ring keyed by producedAtMs (the
 * producer-side ClusterClock-aligned stamp). Games layer their
 * entity-type-specific glue on top.
 *
 * Wire format: see pkg/quantize/wireformat.go for producedAtMs
 * semantics.
 */

/**
 * One snapshot of an entity's authoritative state at a moment in
 * producer cluster-clock time. The interpolation core only reads the
 * fields below; games may store a richer per-entity record so long
 * as it carries a `samples: Sample[]` ring.
 */
export interface Sample {
  worldX: number;
  worldY: number;
  velX: number;
  velY: number;
  rotation: number;
  producedAtMs: number;
}

/** A ring of samples ordered ascending by producedAtMs. */
export interface SampleRing {
  samples: Sample[];
}

/** Interpolated render position computed by interpolateRing. */
export interface InterpolationResult {
  renderX: number;
  renderY: number;
  renderRot: number;
}

/** Linear interpolation between a and b at fraction t in [0, 1]. */
export function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

/**
 * Linear interpolation between two angles in radians, taking the
 * shortest path around the unit circle.
 */
export function lerpAngle(a: number, b: number, t: number): number {
  let diff = b - a;
  while (diff > Math.PI) diff -= Math.PI * 2;
  while (diff < -Math.PI) diff += Math.PI * 2;
  return a + diff * t;
}

/**
 * Append a sample to the ring.
 *
 * Drops samples whose stamp predates the ring tip — when authority
 * transfers across cells (or hosts under EMA-drifted ClusterClocks),
 * the ex-authority's final in-flight frame can race the new
 * authority's first frame and arrive last; without this drop the
 * ring becomes non-monotonic and interpolateRing's pair-finder picks
 * the wrong bracket — visible as a one-frame jump just past a cell
 * crossing.
 *
 * Evicts the oldest sample when the ring would exceed ringSize.
 */
export function pushSample(ring: SampleRing, s: Sample, ringSize: number): void {
  const samples = ring.samples;
  const tip = samples.length > 0 ? samples[samples.length - 1] : null;
  if (tip && s.producedAtMs < tip.producedAtMs) {
    return;
  }
  samples.push(s);
  if (samples.length > ringSize) {
    samples.shift();
  }
}

/**
 * Compute the interpolated render position for one sample ring at
 * the given render time.
 *
 * - Empty ring → returns null (caller should leave previous render
 *   state untouched).
 * - One sample → static at that sample.
 * - Two or more samples → finds the newest pair that brackets
 *   renderTimeMs and lerps. Past the newest sample, extrapolates
 *   with that sample's velocity, capped to maxExtrapolateMs.
 *
 * Effective-s0 cap: when s0 is much older than (s1 - renderDelayMs)
 * (entity was idle, then moved), tighten the lerp window to the most
 * recent renderDelayMs. Without this cap the first new sample after
 * a long idle gap snaps the render position to s1 in one frame.
 */
export function interpolateRing(
  ring: SampleRing,
  renderTimeMs: number,
  maxExtrapolateMs: number,
  renderDelayMs: number,
): InterpolationResult | null {
  const samples = ring.samples;
  const n = samples.length;
  if (n === 0) return null;
  if (n === 1) {
    const s = samples[0];
    return { renderX: s.worldX, renderY: s.worldY, renderRot: s.rotation };
  }

  let s0 = samples[0];
  let s1 = samples[1];
  for (let i = 1; i < n - 1; i++) {
    if (samples[i].producedAtMs <= renderTimeMs) {
      s0 = samples[i];
      s1 = samples[i + 1];
    }
  }

  const effS0Stamp = Math.max(s0.producedAtMs, s1.producedAtMs - renderDelayMs);

  if (renderTimeMs <= effS0Stamp) {
    return { renderX: s0.worldX, renderY: s0.worldY, renderRot: s0.rotation };
  }
  if (renderTimeMs >= s1.producedAtMs) {
    const extMs = Math.min(renderTimeMs - s1.producedAtMs, maxExtrapolateMs);
    const extS = extMs / 1000;
    return {
      renderX: s1.worldX + s1.velX * extS,
      renderY: s1.worldY + s1.velY * extS,
      renderRot: s1.rotation,
    };
  }
  const t = (renderTimeMs - effS0Stamp) / (s1.producedAtMs - effS0Stamp);
  return {
    renderX: lerp(s0.worldX, s1.worldX, t),
    renderY: lerp(s0.worldY, s1.worldY, t),
    renderRot: lerpAngle(s0.rotation, s1.rotation, t),
  };
}
```

- [ ] **Step 2: Commit (no build needed yet — the file is only referenced after sdkgen wires it through in Task 5)**

```bash
git add pkg/quantize/ts/interpolation-core.ts
git commit -m "$(cat <<'EOF'
feat(quantize): add interpolation-core.ts reference implementation

Generic render-lag primitives (Sample, SampleRing, pushSample,
interpolateRing, lerp, lerpAngle) lifted from the per-client
interpolation.ts files. Carries the stale-sample drop and
effective-s0 cap fixes so every consumer gets them.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Wire `interpolation-core.ts` through sdkgen

**Files:**
- Modify: [cmd/sdkgen/main.go](cmd/sdkgen/main.go) — add a flag for the new core file path; copy it into `_core/`

- [ ] **Step 1: Add the flag declaration**

Find the `coreTS := flag.String("core", "pkg/quantize/ts/delta-decoder-core.ts", ...)` line at `cmd/sdkgen/main.go:25` and add a sibling flag immediately after:

```go
interpTS := flag.String("interp", "pkg/quantize/ts/interpolation-core.ts", "Path to interpolation-core.ts to copy")
```

- [ ] **Step 2: Add the copy step**

Below the existing `copyFile(*coreTS, ...)` call (around `main.go:62`), add a parallel call:

```go
if err := copyFile(*interpTS, filepath.Join(*outDir, "_core", "interpolation-core.ts")); err != nil {
    log.Fatalf("copy interp core: %v", err)
}
```

- [ ] **Step 3: Verify Go compiles**

Run: `go build -o /tmp/sdkgen-check ./cmd/sdkgen && rm /tmp/sdkgen-check`

Expected: builds clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/sdkgen/main.go
git commit -m "$(cat <<'EOF'
feat(sdkgen): copy interpolation-core.ts into generated SDK _core/

Mirrors the existing delta-decoder-core.ts pattern so every SDK
consumer gets the render-lag interpolation primitives.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Regenerate SDKs

**Files:**
- (No edits — verifies the previous five tasks combine correctly)

- [ ] **Step 1: Run the full build**

Run: `just build`

Expected: Go compiles clean. SDK regeneration runs. The TypeScript build step (vite/tsc inside the example or web-pixi) MAY fail because clients still reference `isSnapMode` / `CLIENT_RENDER_MODE` — that's fine, those are fixed in Tasks 7-11. If `just build` aborts on TS errors before regenerating files, run sdkgen directly:

Run: `just client-sdk examples/4node-basic && just space-sdk`

(Substitute the actual recipe names if they differ — check `justfile` for the exact target if needed.)

- [ ] **Step 2: Confirm regenerated artifacts**

Run: `ls examples/4node-basic/web/sdk/_core/ web-pixi/sdk/_core/`

Expected: each directory contains `delta-decoder-core.ts` AND `interpolation-core.ts`.

Run: `grep -c CLIENT_RENDER_MODE examples/4node-basic/web/sdk/entities.ts web-pixi/sdk/entities.ts`

Expected: `0` for both files (confirms regeneration dropped the constants).

- [ ] **Step 3: Confirm interpolation-core was copied byte-identical**

Run: `diff -q pkg/quantize/ts/interpolation-core.ts examples/4node-basic/web/sdk/_core/interpolation-core.ts && diff -q pkg/quantize/ts/interpolation-core.ts web-pixi/sdk/_core/interpolation-core.ts`

Expected: zero output (files match).

- [ ] **Step 4: Commit the regenerated SDKs**

```bash
git add examples/4node-basic/web/sdk/ web-pixi/sdk/
git commit -m "$(cat <<'EOF'
chore(sdk): regenerate SDKs without render-mode constants

Regenerates entities.ts (drops CLIENT_RENDER_MODE / isSnapMode /
isInterpolatedMode) and adds _core/interpolation-core.ts to both
generated SDKs.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Rewrite `examples/4node-basic/web/src/interpolation.ts`

**Files:**
- Modify: `examples/4node-basic/web/src/interpolation.ts` — replace whole file

- [ ] **Step 1: Replace the file with the SDK-glue version**

Overwrite `examples/4node-basic/web/src/interpolation.ts` with:

```typescript
import type { AnyEntity } from "../sdk/entities.js";
import {
  type Sample,
  pushSample as coreSPush,
  interpolateRing,
  lerp,
  lerpAngle,
} from "../sdk/_core/interpolation-core.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants.js";
import type { ClientEntity, EntitySample } from "./state.js";
import { type ClockSync, estimatedServerNow } from "./clockSync.js";

function entityRotation(e: AnyEntity, fallbackPrev: number): number {
  const moving = e.velX !== 0 || e.velY !== 0;
  return moving ? Math.atan2(e.velY, e.velX) : fallbackPrev;
}

function sampleFrom(e: AnyEntity, producedAtMs: number, prevRot: number): EntitySample {
  return {
    worldX: e.worldX,
    worldY: e.worldY,
    velX: e.velX,
    velY: e.velY,
    rotation: entityRotation(e, prevRot),
    producedAtMs,
  };
}

export function pushSample(ent: ClientEntity, s: EntitySample): void {
  coreSPush(ent, s, RING_SIZE);
}

/**
 * updateEntityFromServer pushes one new authoritative snapshot into
 * the entity's ring (creating the ClientEntity if it doesn't exist
 * yet). The per-entity producedAtMs stamp lets the render loop
 * interpolate on true ClusterClock-aligned server-time deltas,
 * immune to network jitter and cell-tick phase drift.
 */
export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);

  if (!existing) {
    const rot = entityRotation(serverState, 0);
    const first = sampleFrom(serverState, producedAtMs, rot);
    const ent: ClientEntity = {
      ...serverState,
      prevX: serverState.worldX,
      prevY: serverState.worldY,
      isReplica: false,
      isGhost: false,
      samples: [first],
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
  pushSample(existing, sampleFrom(serverState, producedAtMs, prevRot));
}

/**
 * interpolateEntities sets renderX/Y/Rot on every entity by
 * interpolating between the two ring samples that bracket
 * (estimatedServerNow - RENDER_DELAY). Packet loss / phase drift
 * are absorbed naturally; extrapolation past the newest sample is
 * capped.
 */
export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  clock: ClockSync,
  clientNowMs: number,
): void {
  if (!clock.initialized) return;
  const renderTime = estimatedServerNow(clock, clientNowMs) - RENDER_DELAY;

  for (const ent of entities.values()) {
    const r = interpolateRing(ent, renderTime, MAX_EXTRAPOLATE_MS, RENDER_DELAY);
    if (r) {
      ent.renderX = r.renderX;
      ent.renderY = r.renderY;
      ent.renderRot = r.renderRot;
    }
  }
}

export { lerp, lerpAngle };
```

- [ ] **Step 2: Verify the file typechecks**

Run: `cd examples/4node-basic/web && bunx tsc --noEmit 2>&1 | head -40`

Expected: errors limited to `state.ts` (predicted/bodyDisplay fields still defined but unused), `input.ts` (still imports `isSnapMode`), `renderer.ts` (still calls `updatePrediction` and uses `isSnapMode`/`predictionActive`/`bodyDisplay`), `__tests__/snap-render-mode.test.ts` (mocks deleted symbols). All fixed in Tasks 8-9.

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/web/src/interpolation.ts
git commit -m "$(cat <<'EOF'
refactor(4node-basic): rewrite interpolation.ts over SDK core

Reduces interpolation.ts to per-game glue — entityRotation callback
and thin wrappers over pushSample / interpolateRing. Deletes
updatePrediction (~50 lines) and the MOVE_SPEED / DECEL_DIST /
MIN_SPEED prediction tunables. Algorithm sits in
sdk/_core/interpolation-core.ts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Delete prediction state from `state.ts` and remove the dead test

**Files:**
- Modify: `examples/4node-basic/web/src/state.ts` — delete `predicted*`, `bodyDisplay*`, `lastFrameTime` fields and initializers
- Modify: `examples/4node-basic/web/src/constants.ts` — delete `PREDICTION_TIMEOUT_MS`
- Delete: `examples/4node-basic/web/src/__tests__/snap-render-mode.test.ts`

- [ ] **Step 1: Delete prediction-related fields in `GameState`**

In `state.ts`, delete:

- Lines 67-71: the `// Client prediction.` block — `predictedX`, `predictedY`, `predictionActive`, `predictionStartTime`.
- Lines 73-79: the `// Rendered player-body position` doc + `bodyDisplayX`, `bodyDisplayY` fields.
- Line 82: the `lastFrameTime: number;` field plus its leading comment if it has one (the `// FPS counter.` comment stays since `fps`, `frameCount`, `lastFpsTime` remain).

- [ ] **Step 2: Delete matching initializers**

In `state.ts`, delete from the `state` literal:

- Lines 115-118: `predictedX: 0, predictedY: 0, predictionActive: false, predictionStartTime: 0,`.
- Lines 119-120: `bodyDisplayX: 0, bodyDisplayY: 0,`.
- Line 121: `lastFrameTime: 0,`.

- [ ] **Step 3: Delete `PREDICTION_TIMEOUT_MS` from constants.ts**

In `examples/4node-basic/web/src/constants.ts`, delete the `PREDICTION_TIMEOUT_MS = 3000` constant and its leading comment (lines 4-5).

- [ ] **Step 4: Delete the snap-render-mode test**

Run: `rm examples/4node-basic/web/src/__tests__/snap-render-mode.test.ts`

- [ ] **Step 5: Verify**

Run: `cd examples/4node-basic/web && bunx tsc --noEmit 2>&1 | head -30`

Expected: errors now limited to `input.ts` (still imports `isSnapMode`, references `state.predictedX/Y/predictionActive/predictionStartTime`) and `renderer.ts` (still imports `updatePrediction`, `isSnapMode`, references `state.predicted*`, `state.bodyDisplay*`, `state.lastFrameTime`). All fixed in Task 9.

- [ ] **Step 6: Commit**

```bash
git add examples/4node-basic/web/src/state.ts examples/4node-basic/web/src/constants.ts examples/4node-basic/web/src/__tests__/snap-render-mode.test.ts
git commit -m "$(cat <<'EOF'
refactor(4node-basic): drop prediction state fields and stale test

Removes predicted*, bodyDisplay*, lastFrameTime from GameState, the
PREDICTION_TIMEOUT_MS constant, and the snap-render-mode test that
existed only to assert mode-switch behavior.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Delete prediction code from `input.ts` and simplify `renderer.ts`

**Files:**
- Modify: `examples/4node-basic/web/src/input.ts` — delete prediction seeding
- Modify: `examples/4node-basic/web/src/renderer.ts` — delete `updatePrediction` call, replace bodyDisplay logic with direct `player.renderX/Y` reads

- [ ] **Step 1: Clean input.ts**

In `examples/4node-basic/web/src/input.ts`:

- Delete the `import { isSnapMode } from "../sdk/entities.js";` line (line 4).
- In `setMoveTarget`, delete lines 26-34 (the entire `if (!isSnapMode()) { ... }` block that seeds prediction state).

The function should reduce to:

```typescript
function setMoveTarget(e: MouseEvent): void {
  const [wx, wy] = worldCoords(e);
  state.moveTargetX = wx;
  state.moveTargetY = wy;
  state.moveTargetActive = true;
  sendMoveTarget();
}
```

- [ ] **Step 2: Clean renderer.ts imports**

In `examples/4node-basic/web/src/renderer.ts`, change line 2 from

```typescript
import { updatePrediction, interpolateEntities } from "./interpolation.js";
```

to

```typescript
import { interpolateEntities } from "./interpolation.js";
```

Delete line 4: `import { isSnapMode } from "../sdk/entities.js";`.

- [ ] **Step 3: Drop the `updatePrediction` call and `lastFrameTime` set**

Delete line 56 (`updatePrediction(now);`) and line 57 (`state.lastFrameTime = now;`).

- [ ] **Step 4: Replace the bodyDisplay branching with direct renderX/Y**

In `renderer.ts`, replace the entire block from line 64 through line 118 (the `// Compute the unified player-body display position.` comment, the `if (isSnapMode()) { ... } else { ... }` body-display logic, and the `state.camX = state.bodyDisplayX; state.camY = state.bodyDisplayY;` assignments) with:

```typescript
  // Camera follows the player.
  state.camX = player.renderX;
  state.camY = player.renderY;
```

- [ ] **Step 5: Update the AoI radius ring to use renderX/Y**

Find the AoI ring block (was at lines 173-184, now shifted up after step 4's deletion). Change

```typescript
const [px, py] = worldToScreen(state.bodyDisplayX, state.bodyDisplayY);
```

to

```typescript
const [px, py] = worldToScreen(player.renderX, player.renderY);
```

Also rewrite the surrounding comment ("Center on the unified body-display position …") to a one-liner: `// Centered on the player's interpolated render position.` (or delete it — the code is self-explanatory).

- [ ] **Step 6: Simplify drawEntity for the local player**

In `drawEntity`, find the block (was at lines 209-217):

```typescript
const isPlayer = netID === state.playerNetID;
if (isPlayer) {
  // Unified body-display position: …
  rx = state.bodyDisplayX;
  ry = state.bodyDisplayY;
}
```

Reduce it to:

```typescript
const isPlayer = netID === state.playerNetID;
```

The default `rx = ent.renderX; ry = ent.renderY;` from a few lines above already does the right thing for both players and bots.

- [ ] **Step 7: Verify TypeScript builds**

Run: `cd examples/4node-basic/web && bunx tsc --noEmit`

Expected: zero errors.

- [ ] **Step 8: Commit**

```bash
git add examples/4node-basic/web/src/input.ts examples/4node-basic/web/src/renderer.ts
git commit -m "$(cat <<'EOF'
refactor(4node-basic): remove prediction call sites in input + renderer

Click handler stops seeding predicted state; render loop drops the
updatePrediction call and the bodyDisplay branching. Local player
renders from player.renderX/Y like every other entity.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Add interpolation-core unit tests

**Files:**
- Create: `examples/4node-basic/web/src/__tests__/interpolation-core.test.ts`

- [ ] **Step 1: Write the test file**

Create `examples/4node-basic/web/src/__tests__/interpolation-core.test.ts` with:

```typescript
import { describe, expect, test } from "bun:test";
import {
  type Sample,
  type SampleRing,
  pushSample,
  interpolateRing,
  lerp,
  lerpAngle,
} from "../../sdk/_core/interpolation-core.js";

function s(t: number, x: number, y: number, vx = 0, vy = 0, rot = 0): Sample {
  return { producedAtMs: t, worldX: x, worldY: y, velX: vx, velY: vy, rotation: rot };
}

function ring(...samples: Sample[]): SampleRing {
  return { samples };
}

describe("lerp", () => {
  test("midpoint", () => {
    expect(lerp(0, 10, 0.5)).toBe(5);
  });
  test("endpoints", () => {
    expect(lerp(2, 8, 0)).toBe(2);
    expect(lerp(2, 8, 1)).toBe(8);
  });
});

describe("lerpAngle", () => {
  test("takes shortest path across PI boundary", () => {
    // From -3 rad to +3 rad: shortest path goes through ±PI, not through 0.
    const result = lerpAngle(-3, 3, 0.5);
    // Halfway across the short path (~0.28 rad span) should be near ±PI.
    expect(Math.abs(Math.abs(result) - Math.PI)).toBeLessThan(0.2);
  });
});

describe("pushSample", () => {
  test("appends and evicts oldest at ring overflow", () => {
    const r = ring();
    pushSample(r, s(10, 0, 0), 3);
    pushSample(r, s(20, 1, 0), 3);
    pushSample(r, s(30, 2, 0), 3);
    pushSample(r, s(40, 3, 0), 3);
    expect(r.samples.length).toBe(3);
    expect(r.samples[0].producedAtMs).toBe(20);
    expect(r.samples[2].producedAtMs).toBe(40);
  });
  test("drops out-of-order samples (handoff race)", () => {
    const r = ring(s(100, 0, 0), s(150, 1, 0));
    pushSample(r, s(120, 9, 9), 8); // stale
    expect(r.samples.length).toBe(2);
    expect(r.samples[1].worldX).toBe(1);
  });
  test("accepts sample with stamp equal to tip", () => {
    const r = ring(s(100, 0, 0));
    pushSample(r, s(100, 1, 0), 8);
    expect(r.samples.length).toBe(2);
  });
});

describe("interpolateRing", () => {
  const RD = 100;
  const EXT = 100;

  test("empty ring returns null", () => {
    expect(interpolateRing(ring(), 0, EXT, RD)).toBeNull();
  });

  test("single sample returns static position", () => {
    const r = ring(s(10, 5, 5, 0, 0, 0.5));
    const out = interpolateRing(r, 9999, EXT, RD);
    expect(out).toEqual({ renderX: 5, renderY: 5, renderRot: 0.5 });
  });

  test("renderTime between two samples lerps linearly", () => {
    const r = ring(s(0, 0, 0), s(100, 100, 0));
    // Without effective-s0 cap (renderDelayMs = 100, gap = 100 → no clamp)
    // renderTime=50 should be at midpoint.
    const out = interpolateRing(r, 50, EXT, RD)!;
    expect(out.renderX).toBeCloseTo(50, 5);
    expect(out.renderY).toBeCloseTo(0, 5);
  });

  test("past newest sample extrapolates with velocity", () => {
    const r = ring(s(0, 0, 0), s(100, 100, 0, 1000, 0)); // 1000 px/s
    const out = interpolateRing(r, 150, EXT, RD)!; // 50ms past
    expect(out.renderX).toBeCloseTo(150, 5);
  });

  test("extrapolation capped at maxExtrapolateMs", () => {
    const r = ring(s(0, 0, 0), s(100, 100, 0, 1000, 0));
    const out = interpolateRing(r, 1000, EXT, RD)!; // 900ms past, cap=100ms
    // At cap: 100 + 1000 px/s * 0.1s = 200
    expect(out.renderX).toBeCloseTo(200, 5);
  });

  test("effective-s0 cap clamps long-idle first move", () => {
    // Entity idle from t=0 to t=10000, then a fresh sample at t=10100.
    // Without cap, renderTime≈10001 would yield t=(10001-0)/(10100-0)≈0.99,
    // snapping renderX to ~99 in one frame.
    // With cap, effective s0 = max(0, 10100-100) = 10000, so
    // t=(10001-10000)/(10100-10000) = 0.01 → renderX≈1.
    const r = ring(s(0, 0, 0), s(10100, 100, 0));
    const out = interpolateRing(r, 10001, EXT, RD)!;
    expect(out.renderX).toBeLessThan(5); // not snapped to ~99
  });
});
```

- [ ] **Step 2: Run the tests**

Run: `cd examples/4node-basic/web && bun test`

Expected: all interpolation-core tests pass. (Other test files may also run; they should also pass.)

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/web/src/__tests__/interpolation-core.test.ts
git commit -m "$(cat <<'EOF'
test(4node-basic): cover interpolation-core algorithm

Locks in lerp/lerpAngle, pushSample's stale-sample drop and ring
eviction, and interpolateRing's bracket-lerp / extrapolation-cap /
effective-s0 cap behaviors.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Rewrite `web-pixi/src/interpolation.ts` and clean stale comment

**Files:**
- Modify: `web-pixi/src/interpolation.ts` — rewrite as SDK glue (with `angle`-aware `entityRotation`)
- Modify: `web-pixi/src/constants.ts` — delete the stale prediction comment

- [ ] **Step 1: Replace web-pixi's interpolation.ts**

Overwrite `web-pixi/src/interpolation.ts` with:

```typescript
import type { AnyEntity } from "../sdk/index.js";
import {
  pushSample as coreSPush,
  interpolateRing,
  lerp,
  lerpAngle,
} from "../sdk/_core/interpolation-core.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants";
import type { ClientEntity, EntitySample } from "./types";
import { type ClockSync, estimatedServerNow } from "./clockSync";

function entityRotation(e: AnyEntity, fallbackPrev: number): number {
  if ("angle" in e) return e.angle as number;
  const moving = e.velX !== 0 || e.velY !== 0;
  return moving ? Math.atan2(e.velY, e.velX) : fallbackPrev;
}

function sampleFrom(e: AnyEntity, producedAtMs: number, prevRot: number): EntitySample {
  return {
    worldX: e.worldX,
    worldY: e.worldY,
    velX: e.velX,
    velY: e.velY,
    rotation: entityRotation(e, prevRot),
    producedAtMs,
  };
}

export function pushSample(ent: ClientEntity, s: EntitySample): void {
  coreSPush(ent, s, RING_SIZE);
}

export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  if (!existing) {
    const rot = entityRotation(serverState, 0);
    const first: EntitySample = sampleFrom(serverState, producedAtMs, rot);
    entities.set(id, {
      current: serverState,
      samples: [first],
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    });
    return;
  }
  pushSample(existing, sampleFrom(serverState, producedAtMs, existing.renderRot));
  existing.current = serverState;
}

export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  clock: ClockSync,
  clientNowMs: number,
): void {
  if (!clock.initialized) return;
  const renderTime = estimatedServerNow(clock, clientNowMs) - RENDER_DELAY;
  for (const ent of entities.values()) {
    const r = interpolateRing(ent, renderTime, MAX_EXTRAPOLATE_MS, RENDER_DELAY);
    if (r) {
      ent.renderX = r.renderX;
      ent.renderY = r.renderY;
      ent.renderRot = r.renderRot;
    }
  }
}

export { lerp, lerpAngle };
```

- [ ] **Step 2: Delete the stale comment in web-pixi/src/constants.ts**

Open `web-pixi/src/constants.ts`, find line 80 (the `// the prediction stays bounded and visibly pauses rather than diverging` comment fragment), and delete it. Read the surrounding 5-10 lines to ensure the comment isn't part of a larger block that needs reformatting; trim if needed.

- [ ] **Step 3: Verify TypeScript builds**

Run: `cd web-pixi && bunx tsc --noEmit 2>&1 | head -20`

Expected: zero errors. (If `bunx tsc` isn't configured for web-pixi, use whichever build command web-pixi normally runs — check `web-pixi/package.json` for the typecheck script.)

- [ ] **Step 4: Commit**

```bash
git add web-pixi/src/interpolation.ts web-pixi/src/constants.ts
git commit -m "$(cat <<'EOF'
refactor(web-pixi): rewrite interpolation.ts over SDK core

Reduces interpolation.ts to per-game glue. Inherits the
stale-sample drop and effective-s0 cap fixes from the SDK core
(web-pixi previously lacked both). Drops the stale prediction
comment from constants.ts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Update `CLAUDE.md`

**Files:**
- Modify: [CLAUDE.md](CLAUDE.md) — replace the "Client render modes" section; remove residual `Config.ClientRenderMode` references

- [ ] **Step 1: Read the current rendering section**

Read `CLAUDE.md` lines 270-295 to confirm the exact current shape of the "Client render modes" section.

- [ ] **Step 2: Replace the dual-mode section**

Find the block starting with `**Client render modes** — games declare …` (around line 276) and ending with the bullets describing `ClientRenderSnap` and `ClientRenderInterpolated` (around line 289). Replace it with:

```markdown
**Client rendering:** server-authoritative. Clients receive 20Hz authoritative samples and render at 60fps by interpolating between samples on a per-entity ring keyed by `producedAtMs` (the producer-side `ClusterClock` stamp). Render-lag of `RENDER_DELAY` ms keeps the bracketing pair available; extrapolation past the newest sample is capped at `MAX_EXTRAPOLATE_MS`. There is no client-side prediction — clicks send to the server and the player waits for server confirmation before moving. The interpolation primitives live in the generated SDK at `sdk/_core/interpolation-core.ts` (copied from `pkg/quantize/ts/interpolation-core.ts`); per-game code provides a thin glue layer with an `entityRotation` callback.
```

- [ ] **Step 3: Remove residual Config.ClientRenderMode references**

Search for `ClientRenderMode` in `CLAUDE.md`:

Run: `grep -n ClientRenderMode CLAUDE.md`

Expected after this step: zero matches. Delete any remaining sentence that mentions `Config.ClientRenderMode` (likely in the Config section around lines 222-228 in the prior tree shape; line numbers shift as edits land — search before deleting).

- [ ] **Step 4: Final scan for stale prediction language**

Run: `grep -n -E 'predict|render mode|isSnapMode|isInterpolatedMode|CLIENT_RENDER_MODE' CLAUDE.md`

Expected: zero matches outside of `pkg/quantize/wireformat.go` references or any incidental mentions in unrelated sections (review each match — anything tied to the deleted feature gets pruned).

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(claude): rewrite client-rendering section as single model

Replaces the dual-mode framing with one paragraph describing the
server-authoritative + render-lag-interpolation model. Drops all
residual ClientRenderMode references.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: End-to-end verification

**Files:** none — verification only.

- [ ] **Step 1: Full build**

Run: `just build`

Expected: clean build end-to-end. Both Go and the regenerated TS clients compile.

- [ ] **Step 2: Go tests**

Run: `just test-pg` (Postgres-backed tests)

Expected: all pass.

- [ ] **Step 3: TS tests**

Run: `cd examples/4node-basic/web && bun test`

Expected: all pass — including the new `interpolation-core.test.ts`.

- [ ] **Step 4: Manual smoke test**

Run: `cd examples/4node-basic && just dev`

Open `http://localhost:8080` in two browser tabs (or one tab + one incognito for separate sessions). Verify:

1. Click-to-move: the local player moves smoothly to the click target after a brief server-confirm delay (no instant local snap, no rubber-band).
2. Cross-cell handoff: walk into a neighboring cell — no visible blackout, no one-frame jump just past the boundary.
3. Multi-client: both tabs see each other's motion smoothly at 60fps.
4. Long-idle first move: park the player for 2+ seconds. Click far away. The first frame should not snap forward — motion ramps up smoothly. (Effective-s0 cap engaged.)
5. Direction reversal: click rapidly between two distant points. No rubber-band artifact (there's nothing to reconcile — server is authoritative).

Stop the dev server with Ctrl-C.

- [ ] **Step 5: Final symbol grep**

Run:

```sh
grep -r 'predict\|isSnapMode\|isInterpolatedMode\|CLIENT_RENDER_MODE\|ClientRenderMode\|ClientRenderInterpolated\|PREDICTION_TIMEOUT\|bodyDisplay\|updatePrediction' \
  --include='*.go' --include='*.ts' .
```

Expected: zero matches outside `docs/superpowers/plans/` (the historical plan files retain mentions and stay frozen).

- [ ] **Step 6: SDK consistency**

Run: `diff -q pkg/quantize/ts/interpolation-core.ts examples/4node-basic/web/sdk/_core/interpolation-core.ts && diff -q pkg/quantize/ts/interpolation-core.ts web-pixi/sdk/_core/interpolation-core.ts`

Expected: zero output.

- [ ] **Step 7: If verification surfaced issues, fix them, then commit and re-run**

Otherwise the work is complete. The commits from Tasks 1-12 collectively encode the full change.

---

## Notes for the executor

- Order matters: server changes (Tasks 1-3) must precede the SDK regen (Task 6) which must precede the per-client TS cleanup (Tasks 7-11).
- Per the spec, intermediate states need not compile end-to-end. After Task 3 the SDK still emits the old constants until Task 6 regenerates; after Task 6 the TS clients reference deleted symbols until Task 7-11 clean them up. That's expected.
- If Task 6's `just build` fails before regen runs because the TS step blows up first, run `cmd/sdkgen` directly via `just client-sdk examples/4node-basic` and `just space-sdk` (or whatever recipe maps to web-pixi).
- Don't squash commits at the end. The phase-level history makes the change easier to bisect if a regression surfaces later.
