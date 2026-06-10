# SDK Interpolation Playback Primitives — ClockSync + InterpolationBuffer

**Date:** 2026-06-10
**Status:** Approved (pending spec review)

## Problem

Snapshot-interpolation playback logic is duplicated and footgun-prone:

- **Two TS copies** of the same code: `web-pixi/src/clockSync.ts` + `web-pixi/src/interpolation.ts`, and `examples/4node-basic/web/src/clockSync.ts` + `interpolation.ts`. They re-derive the server-clock offset estimator and the per-entity ring/playback orchestration.
- **C# has neither.** `csharp/Mmokit.Sdk.Core/InterpolationCore.cs` ships only the low-level ring/lerp primitives; there is no clock-sync, and an earlier ad-hoc C# clock "stepped at 20 Hz" (wrong). The web client's sliding-window-max estimator is the correct reference.

These are reusable *playback primitives*, not world state — they belong in the shared SDK core that sdkgen emits into every SDK (`_core/`), so nobody re-derives them and TS/C# stay in lockstep.

## Non-Goals

- No wire-format change.
- No change to the existing `interpolation-core` primitives (`Sample`, `lerp`, `lerpAngle`, `pushSample`, `isStaleSample`, `interpolateRing`) — the new code *bundles* them.
- The InterpolationBuffer is **optional**: headless/bot consumers ignore it and use the raw core (or nothing). This does not violate the stateless-SDK principle, which governs *world/message state* (the consumer owns the world), not a reusable playback helper.

## Architecture

Two new portable primitives per language, beside `interpolation-core`, emitted verbatim by sdkgen into each SDK's `_core/`:

| Concern | TS source | C# source |
|---|---|---|
| Server-clock offset estimator | `pkg/quantize/ts/clock-sync.ts` | `csharp/Mmokit.Sdk.Core/ClockSync.cs` |
| Per-entity playback buffer | `pkg/quantize/ts/interpolation-buffer.ts` | `csharp/Mmokit.Sdk.Core/InterpolationBuffer.cs` |

Both are pure (clock injected as a scalar parameter — no DOM, no Unity, no wall-clock dependency), matching how the codec is already portable.

### Component 1 — ClockSync (moved ≈verbatim from web-pixi; new in C#)

Pure scalar estimator of `server_ms − client_ms`. Algorithm: **sliding-window max** of `instant = serverMs − clientNowMs` over a fixed window (`INSTANT_WINDOW = 40` ≈ 2 s @ 20 Hz). Max (least-negative) instant tracks the least-delayed sample; window scope lets the offset drift if base latency genuinely shifts. Max (not EWMA) is deliberate: bursty delivery clusters frames with a fixed `clientNow` and advancing `serverStamp`, which an EWMA mis-weights.

- **TS** (interface + free functions, verbatim move): `ClockSync` interface, `newClockSync()`, `observeServerTime(c, serverMs, clientNowMs)`, `observeFrameStamps(c, entities, clientNowMs)`, `estimatedServerNow(c, clientNowMs)`.
- **C#** (class, same algorithm): `ClockSync` with `double OffsetMs`, `bool Initialized`, `ObserveServerTime(double serverMs, double clientNowMs)`, `ObserveFrameStamps(IEnumerable<ulong> producedAtMs, double clientNowMs)`, `EstimatedServerNow(double clientNowMs)`. All math on `double` to match TS `number`.

The consumer drives it (stateless SDK): it feeds `producedAtMs` from decoded entities each frame.

### Component 2 — InterpolationBuffer (Sample-based, new in both langs)

A stateful per-entity wrapper over a `Sample` ring that bundles the two orchestration footguns the apps currently re-derive:

- `push(s: Sample)` — stale-gates via `isStaleSample` (drops frames older than the newest held — prevents the cell-boundary backward-snap of non-interpolated fields), then `pushSample` into the ring.
- `newest(): Sample | null` — exposes the latest sample so the app can resolve a prev-rotation fallback for stationary entities.
- `sampleAt(renderTimeMs): InterpolationResult | null` — `interpolateRing` with the buffer's configured `maxExtrapolateMs` / `renderDelay`.
- Constructor config `{ ringSize, renderDelayMs, maxExtrapolateMs }`, defaulting to the promoted shared constants.

The buffer operates **only on `Sample`** — it carries no game semantics. The app keeps the genuinely game-specific glue: entity→`Sample` mapping (including its rotation rule, e.g. space's `angle` field vs 4node's velocity-derived heading, using `buf.newest()` for the stationary fallback), the `netID → buffer` map, and applying `renderX/Y/Rot` to sprites/GameObjects.

### Promoted constants

`RING_SIZE`, `RENDER_DELAY`, `MAX_EXTRAPOLATE_MS`, `INSTANT_WINDOW` move into the core as exported defaults (both langs), overridable via the buffer ctor / explicit args. Apps stop defining their own copies.

## Data Flow (consumer, per frame)

```
on WorldDelta decode:
  for each decoded entity e:
    clock.observeFrameStamps([e...], clientNow)          // feed the offset estimator
    buf[e.netID].push(appEntityToSample(e, buf.newest))  // app glue: entity -> Sample (+rotation)

on render frame:
  renderTime = estimatedServerNow(clock, clientNow) - RENDER_DELAY
  for each entity buffer:
    r = buf.sampleAt(renderTime)
    if r: apply r.renderX / r.renderY / r.renderRot to the view
```

Identical shape in TS and C#; the only per-engine code is `appEntityToSample` and the view application.

## sdkgen Emission

`CoreFiles()` copies `_core` sources verbatim per backend. Add the two TS files to `tsBackend.CoreFiles()` and the two C# files to `csharpBackend.CoreFiles()`. The TS backend currently takes explicit per-file source paths (`CoreTS`, `InterpTS`); extend `backendOpts` + the justfile codegen flags to carry the two new TS paths (or consolidate to a TS core-dir, mirroring `CSharpCoreDir`). After this, `just client-sdk` / `just space-sdk` / `just csharp-sdk` emit the primitives into every SDK automatically.

## Web Client Migration (both clients)

For **both** `web-pixi` and `examples/4node-basic/web`:

1. Delete the local `clockSync.ts`; import `ClockSync` + funcs from `sdk/_core/clock-sync.js`.
2. Refactor `interpolation.ts` to drive `InterpolationBuffer` from `sdk/_core/interpolation-buffer.js` — keep only `entityRotation` / `sampleFrom` glue and view application; drop the hand-rolled ring/stale-gate/renderTime code.
3. Remove the now-duplicated constants in favor of the core defaults (or pass app overrides to the ctor where a client intentionally differs).
4. Repoint/preserve existing web tests (`web-pixi/src/__tests__/clockSync.test.ts`, `interpolation.test.ts`, and the 4node-web equivalents). Behavior must be unchanged — the bursty-delivery max-offset semantics and the stale-gate are load-bearing (see prior interpolation incidents).

## Testing — Cross-Language Golden Parity

Extend `cmd/csharp-golden` to emit a **clock-sync golden section**: a Go-authored sequence of `{ serverMs, clientNowMs }` observations plus the expected `offsetMs` after each, including a bursty cluster (fixed `clientNow`, advancing `serverStamp`) that pins max-vs-EWMA behavior, plus a window-rollover case (> `INSTANT_WINDOW` samples) that exercises aging-out.

- A new **C# test** (`csharp/Mmokit.Sdk.Core.Tests`) reads the golden and asserts `ClockSync` reproduces each `offsetMs`.
- A new **TS test** reads the same golden JSON and asserts `clock-sync.ts` reproduces it. (Confirm the TS runner — vitest/bun — during planning; both web clients already run `*.test.ts`.)

The shared golden makes the "I got the clock wrong" TS/C# divergence un-reintroducible. An optional small InterpolationBuffer unit test (push/stale-gate/sampleAt) is per-language, not golden.

## Validation

- `just space-sdk` + `just csharp-sdk` regenerate cleanly; both SDKs expose the new `_core` files.
- C# compile gate (`just csharp-compile-test`) + `dotnet test` (golden) green.
- Both web clients build (`bun run build`) and their interpolation tests pass.
- Manual smoke (existing): web client renders smooth 60fps interpolation over 20 Hz ticks with no boundary jitter.

## Risks

1. **Algorithm parity** (Float64Array vs `double[]`, window max, `INSTANT_WINDOW`) — mitigated by the golden.
2. **Web regression** — the max-offset and stale-gate are subtle and previously bug-prone; preserve behavior exactly and lean on the existing web tests + golden.
3. **TS sdkgen wiring** for the two new files (flags/opts) — mechanical; verified by a clean regen + a `diff` that TS output for existing files is unchanged.
4. **C# ClockSync is net-new** — surface it in the Unity example design doc so the consumer opts in (`clock.ObserveFrameStamps(...)` per decoded WorldDelta).
