# Spec 1 — Time & Transparency

**Date:** 2026-04-20
**Status:** Design

## Purpose

Finish the topology-transparent client-server contract so that cell splits, merges, migrations, and ordinary cross-cell handoffs produce **zero visible artifacts** on the client, regardless of whether the involved cells live on the same host or across gRPC. The client should never be able to perceive — through any visible rendering glitch — that cells exist, let alone that their ownership changes at runtime.

## Context and lessons learned

This spec is the direct continuation of a multi-session investigation that surfaced and fixed a cascade of bugs in the mesh handoff path. The relevant lessons, all validated against industry references (Valve Source, Star Citizen, SpatialOS, Gaffer On Games):

1. **Client-server topology coupling is an anti-pattern.** Messages like `SE_CELL_CHANGE` and `SE_PLAYER_SPAWNED` on cell-bounds change leaked server-internal topology to the client; every such event caused state resets and visible artifacts. Removed in prior work; the client now consumes only a delta stream.

2. **A per-snapshot baseline-reset flag is the canonical mechanism for baseline invalidation.** `FRAME_FLAG_FRESH_SNAPSHOT` (added earlier) is the one-and-only signal the client needs when it should drop decoder baselines. Maps directly to Gaffer's "encoded relative to initial state" flag and Valve's `cl_fullupdate`.

3. **Farewell `Removed` frames race with destination fresh frames.** When a cell-transfer source sent a farewell listing every netID in its view, the frame raced the destination's fresh snapshot on the wire. If farewell arrived second, the client lost baselines for entities still visible from the destination, producing 1.5-second blackouts. Removed in prior work.

4. **Independent per-cell goroutines with independent tick phases cause visible client artifacts on handoff.** The server simulates 100ms of motion (two 50ms physics ticks on two cells) over as little as 25ms of wall-clock when two cells' tick phases are offset by 25ms. The client renders that compression as a visible speed-up. Attempts to fix this server-side (phase alignment across cells) violate the local-equivalent-remote architecture tenant because cross-host clock synchronization is brittle. **This spec fixes it client-side via snapshot interpolation, following Gaffer and Valve's canonical pattern.**

5. **Client interpolation must use a stable time-base that is immune to network jitter.** Deriving timestamps from client-side frame arrival times couples the interpolation rate to wall-clock network delivery. A jitter spike compresses the visible interpolation; a stall stretches it. The industry solution is server-stamped timestamps: every frame carries a server wall-clock moment, and the client interpolates in server-time space.

## Architectural principles (enforced by this spec)

- **Local and remote behave identically.** Any mechanism we introduce must work the same whether cells share a process or are separated by gRPC. No branch in client or server that says "if cells are local, do X; otherwise Y."
- **The client is topology-transparent.** It consumes a single delta stream with per-frame flags. It never learns about cells, hosts, authority, or server boundaries.
- **Server-side synchronization between simulation nodes is rejected as a strategy.** Cross-host clock coordination is brittle and adds a second-order coordination problem. The client buffers and interpolates — this is how every serious meshed-simulation engine solves it.

## Architecture overview

Two deltas, one per side.

**Server:** every replication frame gets stamped with `server_time_ms = time.Now().UnixMilli()` at the moment the binary encoder runs. A new 8-byte header field carries it to the wire. That is the entire server-side change.

**Client:** each `ClientEntity` gains a fixed-size ring of 3 samples `(worldX, worldY, velX, velY, rotation, serverTimeMs)`. A lightweight exponentially-smoothed clock-offset estimator derives "current server time" from every frame's timestamp. The render loop interpolates each entity at `estimatedServerNow() - RENDER_DELAY` by finding the two ring samples that bracket that time.

The protocol is purely additive-from-the-client's-perspective: every frame carries one new field. The behavior change is entirely in client rendering; server simulation code is unchanged.

## Wire format changes

Breaking change to the delta-world-update header (per project no-backward-compat norm, server and client are rebuilt together). Header grows from 20 → 28 bytes.

```
Header (28 bytes):
  [4] tick            uint32 big-endian
  [4] seq             uint32 big-endian
  [4] flags           uint32 big-endian
  [8] server_time_ms  uint64 big-endian     ← NEW
  [2] fullCount       uint16 big-endian
  [2] deltaCount      uint16 big-endian
  [2] removedCount    uint16 big-endian
  [2] exitedCount     uint16 big-endian
```

`server_time_ms` is Unix milliseconds as observed on the host that produced the frame. Cross-host NTP skew is typically ≤5ms on a LAN and well under our 50ms tick interval. Same-host cells share one `time.Now()` source and are exact.

The client treats the stamp as an opaque monotonic value — it never reasons about *which* host emitted it. Per-entity interpolation uses these stamps to find bracketing samples; the clock-offset estimator uses them to estimate "current server time" client-side.

## Server-side implementation

### A. Encoder signature

`pkg/quantize/wireformat.go`:

```go
type FrameHeader struct {
    Tick         uint32
    Seq          uint32
    Flags        uint32
    ServerTimeMs uint64  // NEW
    FullCount    uint16
    DeltaCount   uint16
    RemovedCount uint16
    ExitedCount  uint16
}

const frameHeaderSize = 28  // was 20

func (e *FrameEncoder) Encode(
    tick, seq, flags uint32,
    serverTimeMs uint64,       // NEW
    full []FullEntry,
    deltas []DeltaEntry,
    removed []uint32,
    exited []uint32,
) []byte {
    // ... existing header encoding with 8 extra bytes for serverTimeMs ...
}
```

The decoder reads the same field. Both tested by a round-trip unit test.

### B. Writer stamping

`pkg/system/frame_writer.go`:

```go
func (w *BinaryFrameWriter) WriteFrame(frame *ReplicationFrame) {
    // ... existing setup unchanged ...
    serverTimeMs := uint64(time.Now().UnixMilli())
    binData := w.encoder.Encode(
        frame.Tick, frame.Seq, frame.Flags, serverTimeMs,
        full, deltas, frame.Removed, frame.Exited,
    )
    // ... existing send unchanged ...
}
```

Stamping at the wire layer (not in `ReplicationSystem.Update`) keeps the claim honest: the stamped time represents the moment bytes leave the process, minimizing the gap between "what the stamp says" and "when the client actually receives it."

The `ReplicationFrame` struct is unchanged. The timestamp exists only at the encoding boundary — game logic has no reason to care about it.

### C. Other encoder callsites

The merge-path special-case encoder call in `examples/slither/replication.go:228` gets the same signature change (`serverTimeMs` param). Set to `time.Now().UnixMilli()` there too.

## Client-side implementation

### A. Per-entity sample ring

`web-pixi/src/types.ts`:

```ts
export interface EntitySample {
  worldX: number;
  worldY: number;
  velX: number;
  velY: number;
  rotation: number;
  serverTimeMs: number;
}

export interface ClientEntity {
  current: AnyEntity;            // latest decoded state (for HUD / game logic)
  samples: EntitySample[];       // ring, [0] = oldest, capped at RING_SIZE
  renderX: number;               // interpolated — renderer reads these
  renderY: number;
  renderRot: number;
}
```

The `prev*` fields from the current `ClientEntity` are removed — the ring replaces them.

Ring push on every received frame (delta or fresh):

```ts
function pushSample(ent: ClientEntity, s: EntitySample): void {
  ent.samples.push(s);
  if (ent.samples.length > RING_SIZE) {
    ent.samples.shift();
  }
}
```

Constants:

- `RING_SIZE = 3` — one past-sample, one current-sample, one lookahead cushion for late arrivals.
- `RENDER_DELAY = 100` (ms) — two tick intervals. Matches Source's `cl_interp 0.1` default.
- `MAX_EXTRAPOLATE_MS = 50` — one tick's worth, bounded so sustained packet loss doesn't produce wild prediction.

Memory: 3 samples × ~48 bytes per sample ≈ 144 bytes per entity. 200 visible entities = 29KB. Trivial.

### B. Clock sync

`web-pixi/src/clockSync.ts` (new file):

```ts
export interface ClockSync {
  offsetMs: number;       // smoothed server_ms - client_ms
  initialized: boolean;
}

export function newClockSync(): ClockSync {
  return { offsetMs: 0, initialized: false };
}

// Called every received frame with that frame's server_time_ms field.
export function observeServerTime(c: ClockSync, serverTimeMs: number): void {
  const instant = serverTimeMs - performance.now();
  if (!c.initialized) {
    c.offsetMs = instant;
    c.initialized = true;
  } else {
    c.offsetMs = c.offsetMs * 0.9 + instant * 0.1;
  }
}

// Called every render frame.
export function estimatedServerNow(c: ClockSync): number {
  return performance.now() + c.offsetMs;
}
```

EMA smoothing constant `α = 0.1` stabilizes within ~20 frames (~1s at 20Hz observation rate) and rejects short network hiccups. RTT is not compensated — the estimate runs RTT/2 behind true server-now, which is absorbed by `RENDER_DELAY`.

The `ClockSync` instance is held on `GameState`, initialized at client start, fed from `network.ts` on each incoming frame.

### C. Interpolation

`web-pixi/src/interpolation.ts`:

```ts
export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  clock: ClockSync,
): void {
  if (!clock.initialized) return;  // no time-base yet; leave renderX at spawn position
  const renderTime = estimatedServerNow(clock) - RENDER_DELAY;

  for (const ent of entities.values()) {
    const n = ent.samples.length;
    if (n === 0) continue;

    if (n === 1) {
      applyStaticSample(ent, ent.samples[0]);
      continue;
    }

    // Find bracketing pair among available samples.
    let s0 = ent.samples[0], s1 = ent.samples[1];
    for (let i = 1; i < n - 1; i++) {
      if (ent.samples[i].serverTimeMs <= renderTime) {
        s0 = ent.samples[i];
        s1 = ent.samples[i + 1];
      }
    }

    if (renderTime <= s0.serverTimeMs) {
      applyStaticSample(ent, s0);                // hold at oldest
    } else if (renderTime >= s1.serverTimeMs) {
      const ext = Math.min(renderTime - s1.serverTimeMs, MAX_EXTRAPOLATE_MS) / 1000;
      applyExtrapolated(ent, s1, ext);           // predict with velocity
    } else {
      const t = (renderTime - s0.serverTimeMs) / (s1.serverTimeMs - s0.serverTimeMs);
      applyInterpolated(ent, s0, s1, t);         // normal case
    }
  }
}
```

`applyInterpolated` uses `lerp` for position and `lerpAngle` for rotation. `applyExtrapolated` uses `pos + vel * dt`. Both are straight-line arithmetic on fields the ring already holds.

`main.ts` render loop changes from the current `t = (now - lastTickTime) / TICK_INTERVAL` approach to a straight `interpolateEntities(state.entities, state.clockSync)` call. The `lastTickTime` field on `GameState` is removed.

### D. Wiring on frame arrival

`web-pixi/src/network.ts`:

```ts
function applyDeltaUpdate(state: GameState, update: DeltaWorldUpdate): void {
  state.tickCount = update.tick;
  observeServerTime(state.clockSync, update.serverTimeMs);   // NEW

  // Fresh-snapshot set-wise reconciliation unchanged from prior work.

  for (const e of [...update.entered, ...update.updated]) {
    const existing = state.entities.get(e.netID);
    if (!existing) {
      state.entities.set(e.netID, newClientEntity(e, update.serverTimeMs));
    } else {
      pushSample(existing, sampleFrom(e, update.serverTimeMs));
      existing.current = e;
    }
  }

  // Removed/exited handling unchanged.
}
```

The generated SDK adds `serverTimeMs: number` to `DeltaWorldUpdate`, read from the new wire-format field (`sdkgen` generator change).

## Edge cases

**Cell handoff with tick phase drift.** Source stamps frame at `T`; destination's fresh frame stamped at `T+25`. Both samples land in the ring with correct timestamps. Interp computes `t = (renderTime - T) / ((T+25) - T)` and advances at true velocity. No speed-up.

**Merge rename.** Survivor cell continues its own tick-time sequence. Adopted donor entities receive their first post-transfer sample stamped on the survivor's clock. No rendering disruption.

**Teleport** (respawn, `tp` command). Server sends a fresh snapshot with discontinuous position. Sample stamps are sequential (gap ≈ one tick) but positions jump. Client interpolates over ~50ms from old to new — fast slide rather than instant snap, matching Source's default behavior. If harder snaps are needed later, a per-frame `teleport` flag can clear the ring before pushing the new sample. Out of scope for this spec.

**Packet loss.** One dropped frame means the next sample has a stamp >50ms past the previous. Interp still works; the wider interval is interpolated at correct real-time velocity. Sustained loss (>100ms) triggers extrapolation for up to 50ms, then holds at newest. Frames resuming cause normal interp on the next bracketing pair.

**Newly entered entity.** Ring has 1 sample; `applyStaticSample` renders at that position. Second sample ~50ms later; interp begins. No visual pop — `newClientEntity` initializes `renderX/Y` to the sample's position.

**Entity exits AoI then re-enters.** `update.exited` removes the entity entirely. On re-enter (next `entered`), a fresh `ClientEntity` is created with a new 1-sample ring. No stale history.

**First frame (clock uninitialized).** `interpolateEntities` early-returns when `!clock.initialized`; renderers hold at the static position they were spawned at. First frame initializes both the clock and the first entity samples; second frame onward, interpolation works normally.

**Browser tab goes idle.** `requestAnimationFrame` pauses; `performance.now()` keeps advancing. On wake, the EMA estimate briefly shows a large offset. During the correction window (~1s), the extrapolation-cap + hold-at-newest behavior keeps entities frozen rather than lurching — the desired degraded mode.

**Cross-host NTP skew.** Host A's clock is 3ms ahead of host B's. Entity X handed off from A to B: last A-stamp and first B-stamp appear ~3ms different from what pure physics would predict. Interp still smooth; the "error" is below perceptual threshold (60fps = 16ms/frame). If skew ever exceeds 10ms and becomes visible, a future spec can add a coord-broadcast "cluster epoch" — for now, operational NTP is sufficient.

## Testing strategy

### Go side

One unit test, `pkg/quantize/wireformat_test.go`:

- Round-trip: encode a header with non-zero `server_time_ms`, decode, assert field matches.

All existing tests that call `Encode` get `0` passed for the new parameter. Already handled mechanically (see `sed` precedent in prior session).

### TypeScript side

Three new test suites:

1. `web-pixi/src/__tests__/ringBuffer.test.ts` — `pushSample` maintains ordering; length cap at 3; oldest evicted on 4th push.

2. `web-pixi/src/__tests__/interpolation.test.ts` — synthetic sample inputs at known times, assert correct `renderX/Y/rot` at specified render times. Coverage:
   - Bracketing pair selection for 2, 3 samples.
   - Extrapolation cap applied when `renderTime > newest + 50ms`.
   - Single-sample hold.
   - `renderTime < oldest` hold.
   - Normal interpolation yields linear progress at correct rate.

3. `web-pixi/src/__tests__/clockSync.test.ts` — feed sequences of `(serverTimeMs, performance.now())` pairs, assert EMA convergence and that the estimator initializes correctly on first observation.

### Integration test (Go)

Extend the existing S7 fixture with a simulated client that records decoded sample timestamps across a **split → cross-border → merge** sequence. Assertions:

- Per-entity timestamps are monotonically increasing.
- Effective interpolation rate (diffed from renderX traces across simulated render frames) stays within ±10% of true entity velocity throughout each scenario.
- No frame is ever discarded due to out-of-order timestamps.

One regression guard per scenario (split, merge, migrate). Runs in CI.

### Manual verification

The split → cross → merge → cross → split playthrough we've been doing manually. Success criterion: **no visible hitch, speed-up, or blink on any crossing, regardless of how many splits/merges happened first in the session.**

## Non-goals (explicitly out of scope)

- **Client-side prediction with server reconciliation.** Separate feature addressing a different problem (input latency on the local player). Tracked as future Spec 1b.
- **Server-side lag compensation / rewind for hit detection.** Requires client timestamps on input messages; not present today. Future work.
- **Server tick-phase alignment across hosts.** Explicitly rejected as an architectural strategy. See "Architectural principles."
- **Cross-host cluster epoch broadcast.** Deferred until observed NTP skew becomes visible.
- **Backward compatibility with pre-spec wire format.** Per project norm, server and client are rebuilt together; no version negotiation.

## Files changed (summary)

**Server:**
- `pkg/quantize/wireformat.go` — header size, `ServerTimeMs` field, encode/decode.
- `pkg/quantize/wireformat_test.go` — round-trip test addition.
- `pkg/system/replication.go` — `FrameEncoder.Encode` callsite.
- `pkg/system/frame_writer.go` — `time.Now().UnixMilli()` stamping.
- `examples/slither/replication.go` — callsite.
- `cmd/sdkgen/generate.go` — emit `serverTimeMs: number` in `DeltaWorldUpdate`, wire into decode.
- `pkg/quantize/ts/delta-decoder-core.ts` — 28-byte header, `flags` + `serverTimeMs` fields.

**Client (web-pixi):**
- `web-pixi/src/types.ts` — `EntitySample` type, `ClientEntity` fields.
- `web-pixi/src/clockSync.ts` — new file.
- `web-pixi/src/interpolation.ts` — replace `updateEntityFromServer` + `interpolateEntities` with ring-based versions.
- `web-pixi/src/network.ts` — call `observeServerTime` + `pushSample`; drop `lastTickTime` bookkeeping.
- `web-pixi/src/main.ts` — change `interpolateEntities` call signature.
- `web-pixi/src/state.ts` — add `clockSync: ClockSync`, remove `lastTickTime`.
- `web-pixi/src/__tests__/ringBuffer.test.ts` — new file.
- `web-pixi/src/__tests__/interpolation.test.ts` — new file.
- `web-pixi/src/__tests__/clockSync.test.ts` — new file.

**SDK regeneration:**
- Run `just space-sdk` and `just client-sdk examples/4node-basic` after server-side changes land. Generated `delta-decoder.ts` and `entities.ts` update automatically.

## Rollout

Single atomic change: all server and client files updated in one PR. Delete any prior render-anchor hack on the client (`anchorToRender` parameter on `updateEntityFromServer`). Deploy server, rebuild + redeploy client bundle. Existing running clients with the old wire format will fail to decode the new header and disconnect — acceptable per project norms, and matches the no-backcompat stance.
