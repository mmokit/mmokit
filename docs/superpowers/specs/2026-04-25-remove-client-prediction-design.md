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
unchanged** — it is what makes 60fps motion possible from 20Hz server ticks and is
unrelated to client-side prediction.

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
- Touching the slither example (no prediction code).
- Touching `web-pixi/src/` rendering code — its `interpolation.ts` is already
  pure render-lag with no prediction (only a stale comment to clean).

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
  - Adjust the existing "interpolation runs in BOTH modes" comment to describe
    only render-lag interpolation.
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

## Order of operations

The change is one cohesive commit; intermediate states need not compile.

1. Apply server-side Go edits (section 1) and sdkgen edits (section 2). Build
   will fail at TypeScript-typecheck after SDK regen — expected.
2. Run `just build` to regenerate SDKs. The new `entities.ts` files no longer
   carry the render-mode symbols; the TS error stream identifies the call sites
   that need cleanup.
3. Apply client-side TypeScript edits (section 3) guided by the TS errors.
4. Re-run `just build` end-to-end.
5. Update `CLAUDE.md` (section 4).
6. Verify (see below).
7. Commit.

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
5. Final grep:
   ```
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
