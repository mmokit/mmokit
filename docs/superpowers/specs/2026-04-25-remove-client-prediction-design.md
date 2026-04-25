# Remove Client-Side Prediction — Design

**Date:** 2026-04-25
**Status:** Approved, ready to plan

## Summary

Delete the client-side prediction code path entirely. The engine has supported two
client render modes — `ClientRenderSnap` (server-authoritative, render-lag
interpolation only) and `ClientRenderInterpolated` (Snap + local-player prediction
with reconciliation). Snap has been the default and the only mode in active use;
the prediction code in 4node-basic is dead-but-present. This change deletes the
mode-discrimination surface from server, schema, and SDK, and rips out the
prediction implementation in the 4node-basic web client.

Render-lag interpolation between server samples (`producedAtMs`-driven) is **kept
unchanged in behavior** — it is what makes 60fps motion possible from 20Hz server
ticks and is unrelated to client-side prediction. As part of the same change, the
render-lag interpolation algorithm is consolidated into the generated SDK
(`_core/interpolation-core.ts`), eliminating duplication between
`web-pixi/src/interpolation.ts` and `examples/4node-basic/web/src/interpolation.ts`
and aligning two divergent implementations (the 4node-basic copy has two bug fixes
the web-pixi copy lacks; consolidation lifts both into the SDK so every consumer
gets them).

## Motivation

- Snap mode is stable and is the desired authority model for current and planned
  games (MOBA / RTS / grid-movement / turn-based).
- Maintaining two client render models doubles the surface area of every
  rendering / state / input change with no current consumer of the second model.
- The mode discriminator (`Config.ClientRenderMode`, `CLIENT_RENDER_MODE`,
  `isSnapMode()`, `isInterpolatedMode()`) is exactly the kind of one-value
  configuration knob the project's "no backward compat / refactor over stopgaps"
  policy targets for removal.

If client-side prediction is ever needed for a future twitch game, it can be
re-introduced as a fresh, focused implementation rather than carried forward as
unused infrastructure.

## Non-goals

- Changing the wire format. Replication frames keep `producedAtMs` per entity and
  the 20-byte frame header.
- Changing the rendering of remote entities. Render-lag interpolation between
  server samples remains the smoothing mechanism for all entities including the
  local player.
- Updating historical plan documents under `docs/superpowers/plans/`. They are
  point-in-time records and stay as-is.
- Touching the slither example (no prediction code, hand-coded replicators
  predate the AutoReplicator/SDK pipeline).
- Changing the rendering algorithm itself. The two render-lag bug fixes that
  exist only in `examples/4node-basic/web/src/interpolation.ts` (stale-sample
  drop, effective-s0 cap) are lifted to the SDK as-is so every client gains
  them; behavior on a single client is unchanged.

## Design

### 1. Server-side Go (source of truth)

Delete the `ClientRenderMode` configuration surface so the schema and protocol
no longer carry a render-mode field.

**Files & changes:**

- `pkg/universe/coordinator.go`
  - Delete `ClientRenderMode` type definition and the `ClientRenderSnap` /
    `ClientRenderInterpolated` constants.
  - Delete the `ClientRenderMode` field from `Config`.
  - Delete the default-assignment in `Config` initialization.
  - Delete the `Process.ClientRenderMode()` getter method.
- `pkg/mmokit/mmokit.go`
  - Delete the type alias and constant re-exports for the render mode.
- `pkg/mmokit/protocol.go`
  - Delete the `ClientRenderMode` field from the schema struct.
  - Delete the `SetClientRenderMode` method and its internal field tracking.
  - Delete the schema serialization references to the field.
  - Delete the auto-sync that copies `proc.ClientRenderMode()` into the schema
    in `AssembleFromProcess`.
- `pkg/universe/coordinator_test.go`
  - Delete `TestConfig_DefaultClientRenderMode` and
    `TestConfig_ClientRenderInterpolated_Preserved`.
- `pkg/mmokit/protocol_test.go`
  - Delete the three tests covering `SetClientRenderMode` (default, explicit
    interpolated, empty-string fallback).

### 2. SDK generator

Stop emitting the render-mode constant and helper functions.

- `cmd/sdkgen/schema.go`
  - Delete the `ClientRenderMode string` field from the schema-deserialization
    struct.
- `cmd/sdkgen/generate.go`
  - Delete the entire mode-emission block: the JSDoc, the
    `export const CLIENT_RENDER_MODE = "..." as const;` line, and the
    `isSnapMode()` / `isInterpolatedMode()` helper emissions.

After this regenerates, `examples/4node-basic/web/sdk/entities.ts` and
`web-pixi/sdk/entities.ts` no longer carry these symbols. That breakage drives
the client-side cleanup in section 3.

### 3. Client-side TypeScript (4node-basic)

Delete the prediction implementation. Keep render-lag interpolation. After this,
the local player follows the same code path as every other entity: server
samples in, interpolated render position out.

**Files & changes:**

- `examples/4node-basic/web/src/state.ts`
  - Delete the `predictedX`, `predictedY`, `predictionActive`,
    `predictionStartTime` fields from the local-player state record.
  - Delete their initializers.
- `examples/4node-basic/web/src/input.ts`
  - Delete the `isSnapMode` import.
  - Delete the click-handler block that seeds `predicted*` state and flips
    `predictionActive = true`.
  - Delete the prediction-stop on player arrival.
- `examples/4node-basic/web/src/interpolation.ts`
  - Delete the `isSnapMode` import.
  - Delete the entire `updatePrediction()` function and remove its caller from
    the per-frame render loop.
  - The remaining render-lag interpolation logic is replaced wholesale by the
    SDK-provided core (see Section 5). The file shrinks to per-game glue:
    a `entityRotation` callback that derives rotation from velocity, and the
    thin `updateEntityFromServer` / `interpolateEntities` wrappers that adapt
    the SDK primitives to the game's `ClientEntity` type.
- `examples/4node-basic/web/src/renderer.ts`
  - Delete the `isSnapMode` import.
  - Collapse the snap-mode branch and the interpolated-mode branch into a
    single render path that displays the interpolated server-confirmed
    position. Drop the `predictedX/Y` mirroring, the `HANDOFF_LERP`
    discontinuity smoothing, and the unified-body-position comment that
    documents the dual rendering paths.
- `examples/4node-basic/web/src/constants.ts`
  - Delete `PREDICTION_TIMEOUT_MS`.
- `web-pixi/src/constants.ts`
  - Delete the stale "the prediction stays bounded…" comment.
- `web-pixi/src/interpolation.ts`
  - Replace its entire algorithm with calls to the SDK-provided core (see
    Section 5). The file shrinks to a per-client glue that supplies an
    `entityRotation` callback (preferring the entity's `angle` field if
    present, falling back to velocity-derived rotation) and the thin
    `updateEntityFromServer` / `interpolateEntities` wrappers.
- `examples/4node-basic/web/src/__tests__/snap-render-mode.test.ts`
  - Delete the entire file. The test exists only to assert behavioral
    differences between the two modes.
- Generated SDK files at `examples/4node-basic/web/sdk/entities.ts` and
  `web-pixi/sdk/entities.ts` regenerate automatically via `just build` after
  section 2 lands.

### 4. Documentation

- `CLAUDE.md`
  - Replace the "Client render modes" section (the dual-mode block) with one
    short paragraph describing the rendering model: server-authoritative
    motion with render-lag interpolation between 20Hz server samples driven by
    `producedAtMs`. Drop the mode-discriminator framing entirely.
  - Remove residual references to `Config.ClientRenderMode` elsewhere in the
    doc.
- `docs/superpowers/plans/2026-04-23-snap-render-mode.md`
  - Leave as historical record. Past plans are not edited.

### 5. Consolidate render-lag interpolation into the SDK

**Goal:** the render-lag interpolation algorithm lives in one place — the
generated SDK — so all current and future SDK consumers share one
implementation. Per-client `interpolation.ts` files keep only game-specific
glue.

**New file:** `pkg/quantize/ts/interpolation-core.ts`. Co-located with
`pkg/quantize/ts/delta-decoder-core.ts`, the existing canonical TypeScript
reference file copied into each SDK's `_core/` directory by the generator. The
two files form a coherent client-runtime pair: decode (binary frame →
typed entities) and interpolate (typed entities → render position).

**Public surface of `interpolation-core.ts`:**

- `interface Sample { worldX, worldY, velX, velY, rotation, producedAtMs }`
- `interface SampleRing { samples: Sample[] }`
- `interface InterpolationResult { renderX, renderY, renderRot }`
- `function pushSample(ring, sample, ringSize): void` — appends, drops
  out-of-order samples (the cross-host-handoff race fix), evicts oldest on
  overflow.
- `function interpolateRing(ring, renderTimeMs, maxExtrapolateMs, renderDelayMs): InterpolationResult | null`
  — finds the bracketing sample pair, lerps, applies the effective-s0 cap
  (the long-idle first-frame-jump fix), extrapolates past the newest sample
  with a velocity cap.
- `function lerp(a, b, t)`, `function lerpAngle(a, b, t)` — utility exports.

The core operates on the generic `Sample`/`SampleRing` interfaces. Per-game
code provides:

- The wrapping entity type (e.g. 4node-basic's `ClientEntity` extends sample
  ring fields with `prevX`, `prevY`, `isReplica`, `isGhost`, plus the spread
  current server state).
- An `entityRotation(entity, fallbackPrev) → number` callback that decides
  per-game whether to read an explicit `angle` field, derive from velocity,
  or both.

**SDK generator:** `cmd/sdkgen/main.go`

- Add a second `flag.String` for the interpolation core path (default
  `pkg/quantize/ts/interpolation-core.ts`).
- After the existing `copyFile` for `delta-decoder-core.ts`, add a parallel
  `copyFile` for `interpolation-core.ts` into the same `_core/` directory.
- No changes to `cmd/sdkgen/generate.go` are required for this section — the
  core file is plain copy-through, no template emission.

**Per-client cleanups (already covered in Section 3):**

- `examples/4node-basic/web/src/interpolation.ts` shrinks from ~170 lines (post
  prediction removal) to ~30 lines: the `entityRotation` callback, plus
  `updateEntityFromServer` and `interpolateEntities` thin wrappers that adapt
  SDK primitives to the game's `ClientEntity` map.
- `web-pixi/src/interpolation.ts` shrinks from 127 lines to ~30 lines on the
  same pattern, with its own `entityRotation` (prefers `angle` field).

**Justfile / build pipeline:** `just client-sdk` and `just space-sdk` recipes
already invoke `cmd/sdkgen` with the existing `--core` flag; the new flag has
a default that points at the canonical source, so no recipe change is
required unless we want to make the default explicit.

**Behavior:** identical to the 4node-basic interpolation post prediction
removal — same algorithm, same constants (`RENDER_DELAY`, `MAX_EXTRAPOLATE_MS`,
`RING_SIZE`) supplied by per-client `constants.ts` and passed in as
parameters, both bug fixes intact. `web-pixi`'s rendering visibly improves
because it now inherits the two fixes it currently lacks (no first-move snap
on long idles, no past-handoff one-frame jump).

## Order of operations

The change is one cohesive commit; intermediate states need not compile.

1. Apply server-side Go edits (section 1) and sdkgen edits (section 2).
2. Add the new `pkg/quantize/ts/interpolation-core.ts` source file and the
   sdkgen `main.go` copy (section 5). Land the two together so the regen step
   below produces both `_core/delta-decoder-core.ts` and
   `_core/interpolation-core.ts`.
3. Run `just build` to regenerate SDKs. The new `entities.ts` files no longer
   carry the render-mode symbols and the SDKs now expose
   `_core/interpolation-core.ts`; the TS error stream identifies the per-client
   call sites that need cleanup.
4. Apply client-side TypeScript edits (section 3) guided by the TS errors.
   Includes both the prediction deletion and the rewrite of
   `interpolation.ts` files to call SDK core primitives.
5. Re-run `just build` end-to-end.
6. Update `CLAUDE.md` (section 4).
7. Verify (see below).
8. Commit.

## Verification

1. `just build` succeeds end-to-end.
2. `just test-pg` passes.
3. `cd examples/4node-basic/web && bun test` passes.
4. `cd examples/4node-basic && just dev`, then manually verify:
   - Click-to-move on the local player works; motion is server-confirmed and
     smooth (no input-latency-driven local snap).
   - Cross-cell handoff: walking the player into a neighboring cell shows no
     visual blackout and no rubber-band.
   - Multi-client smoke: open a second browser tab; both clients see the same
     authoritative motion for both players.
   - Long-idle first-move smoke: park the player for >1 s, then click far
     away. First frame should not snap forward (effective-s0 cap engaged).
   - Cell-cross handoff (20 Hz tick boundary): no one-frame jump just past
     the boundary (stale-sample drop engaged on any racing in-flight frame
     from the ex-authority).
5. `examples/4node-basic/web/sdk/_core/interpolation-core.ts` exists and is
   byte-identical to `pkg/quantize/ts/interpolation-core.ts`. Same for
   `web-pixi/sdk/_core/interpolation-core.ts`.
6. Final grep:

   ```sh
   grep -r 'predict\|isSnapMode\|isInterpolatedMode\|CLIENT_RENDER_MODE\|ClientRenderMode\|ClientRenderInterpolated\|PREDICTION_TIMEOUT' \
     --include='*.go' --include='*.ts' .
   ```

   Should return zero matches outside `docs/superpowers/plans/` (historical
   plan docs).

## Open questions

None.

## Out-of-scope follow-ups

- If a future game needs prediction, implement it as a focused per-game module
  rather than a shared engine mode.
