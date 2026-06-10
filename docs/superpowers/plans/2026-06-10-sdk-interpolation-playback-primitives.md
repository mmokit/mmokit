# SDK Interpolation Playback Primitives Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Promote the snapshot-interpolation playback primitives (ClockSync offset estimator + a new Sample-based InterpolationBuffer) into the shared SDK core so they are emitted into every TS and C# SDK, de-duplicating the two web copies and adding the missing C# implementation, locked by a cross-language golden test.

**Architecture:** Two new pure primitives per language live beside `interpolation-core` in `pkg/quantize/ts/` (TS) and `csharp/Mmokit.Sdk.Core/` (C#). sdkgen copies them verbatim into each SDK's `_core/`. A Go-authored golden sequence pins TS≡C# ClockSync behavior. Both web clients then import the shared versions and delete their local copies.

**Tech Stack:** Go (sdkgen + golden generator), TypeScript (`bun:test`), C# (xUnit, netstandard2.1).

---

## File Structure

**New core sources (emitted into `_core/`):**
- `pkg/quantize/ts/clock-sync.ts` — ClockSync (moved from web-pixi, verbatim)
- `pkg/quantize/ts/interpolation-buffer.ts` — InterpolationBuffer (new)
- `csharp/Mmokit.Sdk.Core/ClockSync.cs` — ClockSync (new, port)
- `csharp/Mmokit.Sdk.Core/InterpolationBuffer.cs` — InterpolationBuffer (new)

**Tests:**
- `pkg/quantize/ts/clock-sync.test.ts`, `pkg/quantize/ts/interpolation-buffer.test.ts` (bun)
- `csharp/Mmokit.Sdk.Core.Tests/ClockSyncGoldenTests.cs`, `InterpolationBufferTests.cs`

**Golden + sdkgen wiring (modified):**
- `cmd/csharp-golden/main.go` — add a `clockSync` section
- `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs` — add the DTO
- `cmd/sdkgen/main.go`, `cmd/sdkgen/backend.go`, `cmd/sdkgen/backend_ts.go`, `cmd/sdkgen/backend_csharp.go` — emit the new core files
- `justfile` — add a `ts-core-test` recipe

**Web migration (modified / deleted):**
- `web-pixi/src/interpolation.ts`, `web-pixi/src/types.ts`, `web-pixi/src/state.ts`; delete `web-pixi/src/clockSync.ts`; repoint `web-pixi/src/__tests__/clockSync.test.ts`
- `examples/4node-basic/web/src/interpolation.ts`, `state.ts`, `network.ts`; delete `examples/4node-basic/web/src/clockSync.ts`

---

## Task 1: ClockSync golden section (Go generator + C# DTO)

**Files:**
- Modify: `cmd/csharp-golden/main.go`
- Modify: `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs`

- [ ] **Step 1: Add the Go golden types + reference algorithm + sequence**

In `cmd/csharp-golden/main.go`, add the `ClockSync` field to `Manifest` (after `Reflect ReflectCase`):

```go
	Reflect    ReflectCase    `json:"reflect"`
	ClockSync  ClockSyncCase  `json:"clockSync"`
```

Add these types near the other `*Case` types:

```go
// ClockSyncCase pins the sliding-window-max offset estimator across langs.
type ClockSyncCase struct {
	Window       int            `json:"window"`
	Observations []ClockSyncObs `json:"observations"`
}

// ClockSyncObs is one fed observation plus the offset the estimator must
// report after consuming it. expectedOffsetMs is computed by the Go reference
// below — the single source of truth both TS and C# must reproduce.
type ClockSyncObs struct {
	ServerMs         float64 `json:"serverMs"`
	ClientNowMs      float64 `json:"clientNowMs"`
	ExpectedOffsetMs float64 `json:"expectedOffsetMs"`
}

// clockSyncRef mirrors clock-sync.ts / ClockSync.cs exactly: offset = max
// instant (serverMs-clientNowMs) over the last `window` observations.
type clockSyncRef struct {
	window      int
	instants    []float64
	idx, count  int
	offset      float64
	initialized bool
}

func (r *clockSyncRef) observe(serverMs, clientNowMs float64) {
	if r.instants == nil {
		r.instants = make([]float64, r.window)
	}
	instant := serverMs - clientNowMs
	r.instants[r.idx] = instant
	r.idx = (r.idx + 1) % len(r.instants)
	if r.count < len(r.instants) {
		r.count++
	}
	if !r.initialized {
		r.offset = instant
		r.initialized = true
		return
	}
	max := math.Inf(-1)
	for i := 0; i < r.count; i++ {
		if r.instants[i] > max {
			max = r.instants[i]
		}
	}
	r.offset = max
}

// buildClockSyncCase exercises: first-obs init, max-tracking (a less-delayed
// later sample raises the offset), a bursty cluster (clientNow fixed while
// serverMs advances), and window rollover (old max ages out after `window`).
func buildClockSyncCase() ClockSyncCase {
	const window = 40
	type in struct{ server, client float64 }
	seq := []in{
		{1000, 0},     // init -> 1000
		{1100, 150},   // instant 950, max stays 1000
		{1200, 180},   // instant 1020, max -> 1020
		{1300, 300},   // burst start (client fixed at 300): instant 1000
		{1350, 300},   // instant 1050
		{1400, 300},   // instant 1100, max -> 1100
	}
	// Window rollover: 45 samples at a steady instant of 500 (server advances
	// 50/step, client advances 50/step). After >window of these, the old 1100
	// ages out and the offset settles to 500.
	for i := 0; i < 45; i++ {
		server := 2000 + float64(i)*50
		seq = append(seq, in{server, server - 500})
	}
	ref := &clockSyncRef{window: window}
	obs := make([]ClockSyncObs, 0, len(seq))
	for _, s := range seq {
		ref.observe(s.server, s.client)
		obs = append(obs, ClockSyncObs{ServerMs: s.server, ClientNowMs: s.client, ExpectedOffsetMs: ref.offset})
	}
	return ClockSyncCase{Window: window, Observations: obs}
}
```

Ensure `"math"` is in the import block. In `main()`, set the field on the manifest before it is marshalled (locate where `m := Manifest{...}` / the fields are assigned and add):

```go
	m.ClockSync = buildClockSyncCase()
```

- [ ] **Step 2: Add the C# DTO**

In `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs`, add to the `Manifest` class:

```csharp
        public ClockSyncCase ClockSync { get; set; } = new();
```

And add the DTO classes (near the other `*Case` classes):

```csharp
    public class ClockSyncCase
    {
        public int Window { get; set; }
        public ClockSyncObs[] Observations { get; set; } = Array.Empty<ClockSyncObs>();
    }

    public class ClockSyncObs
    {
        public double ServerMs { get; set; }
        public double ClientNowMs { get; set; }
        public double ExpectedOffsetMs { get; set; }
    }
```

- [ ] **Step 3: Regenerate the golden + verify the section exists**

Run: `just csharp-golden && python3 -c "import json; d=json.load(open('csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json')); print('window', d['clockSync']['window'], 'obs', len(d['clockSync']['observations']), 'first', d['clockSync']['observations'][0], 'last', d['clockSync']['observations'][-1])"`
Expected: `window 40 obs 51 first {'serverMs': 1000.0, 'clientNowMs': 0.0, 'expectedOffsetMs': 1000.0} last {... 'expectedOffsetMs': 500.0}`

- [ ] **Step 4: Verify the C# test project still compiles (DTO only, no test yet)**

Run: `cd csharp && dotnet build Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj -v q -nologo`
Expected: `Build succeeded. 0 Error(s)`

- [ ] **Step 5: Commit**

```bash
git add cmd/csharp-golden/main.go csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json
git commit -m "test(golden): add cross-language ClockSync offset-estimator golden section"
```

---

## Task 2: ClockSync (C#) + golden test

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/ClockSync.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/ClockSyncGoldenTests.cs`

- [ ] **Step 1: Write the failing golden test**

Create `csharp/Mmokit.Sdk.Core.Tests/ClockSyncGoldenTests.cs`:

```csharp
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class ClockSyncGoldenTests
    {
        [Fact]
        public void ReproducesGoldenOffsets()
        {
            var golden = GoldenModel.Load();
            Assert.Equal(ClockSync.InstantWindow, golden.ClockSync.Window);

            var c = new ClockSync();
            foreach (var o in golden.ClockSync.Observations)
            {
                c.ObserveServerTime(o.ServerMs, o.ClientNowMs);
                Assert.True(c.Initialized);
                Assert.Equal(o.ExpectedOffsetMs, c.OffsetMs, 6); // 6 dp tolerance
                Assert.Equal(o.ClientNowMs + o.ExpectedOffsetMs, c.EstimatedServerNow(o.ClientNowMs), 6);
            }
        }
    }
}
```

- [ ] **Step 2: Run to verify it fails (ClockSync undefined)**

Run: `cd csharp && dotnet test Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj --filter ClockSyncGoldenTests -v q 2>&1 | tail -15`
Expected: build error — `ClockSync` does not exist.

- [ ] **Step 3: Implement ClockSync.cs**

Create `csharp/Mmokit.Sdk.Core/ClockSync.cs`:

```csharp
using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// Server-to-client wall-clock offset estimator. Faithful port of
    /// pkg/quantize/ts/clock-sync.ts: offset = sliding-window MAX of
    /// instant = serverMs - clientNowMs over the last InstantWindow samples.
    /// Max (not EWMA) because bursty delivery clusters frames with a fixed
    /// clientNow and advancing serverStamp; the max is the least-delayed
    /// (truest) reading, and window scope lets it drift if base latency shifts.
    ///
    /// Pure + engine-agnostic (clock injected as a scalar). The CONSUMER drives
    /// it from decoded producedAtMs stamps — consistent with the stateless SDK.
    public sealed class ClockSync
    {
        public const int InstantWindow = 40;

        public double OffsetMs { get; private set; }
        public bool Initialized { get; private set; }

        readonly double[] _instants = new double[InstantWindow];
        int _idx;
        int _count;

        /// Feed one (serverTimeMs, clientNowMs) observation; recompute offset.
        public void ObserveServerTime(double serverTimeMs, double clientNowMs)
        {
            double instant = serverTimeMs - clientNowMs;
            _instants[_idx] = instant;
            _idx = (_idx + 1) % _instants.Length;
            if (_count < _instants.Length) _count++;

            if (!Initialized)
            {
                OffsetMs = instant;
                Initialized = true;
                return;
            }
            double max = double.NegativeInfinity;
            for (int i = 0; i < _count; i++)
                if (_instants[i] > max) max = _instants[i];
            OffsetMs = max;
        }

        /// Feed the freshest producedAtMs across a frame's decoded entities.
        public void ObserveFrameStamps(IEnumerable<ulong> producedAtMs, double clientNowMs)
        {
            ulong maxStamp = 0;
            foreach (var p in producedAtMs)
                if (p > maxStamp) maxStamp = p;
            if (maxStamp > 0) ObserveServerTime(maxStamp, clientNowMs);
        }

        /// Estimated current server wall-clock ms, given a client clock reading.
        public double EstimatedServerNow(double clientNowMs) => clientNowMs + OffsetMs;
    }
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd csharp && dotnet test Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj --filter ClockSyncGoldenTests -v q 2>&1 | tail -5`
Expected: `Passed!  - Failed: 0`

- [ ] **Step 5: Commit**

```bash
git add csharp/Mmokit.Sdk.Core/ClockSync.cs csharp/Mmokit.Sdk.Core.Tests/ClockSyncGoldenTests.cs
git commit -m "feat(csharp-sdk): ClockSync offset estimator (golden-verified port of clock-sync.ts)"
```

---

## Task 3: ClockSync (TS) — move to shared core + golden test

**Files:**
- Create: `pkg/quantize/ts/clock-sync.ts` (moved from `web-pixi/src/clockSync.ts`)
- Create: `pkg/quantize/ts/clock-sync.test.ts`
- Modify: `justfile` (add `ts-core-test`)

- [ ] **Step 1: Move ClockSync into the shared core**

The source is already pure (no imports, engine-agnostic). Copy it verbatim:

Run: `cp web-pixi/src/clockSync.ts pkg/quantize/ts/clock-sync.ts`

(The web-pixi copy is not deleted yet — Task 7 repoints + deletes it. The 4node copy is identical; Task 8 handles it.)

- [ ] **Step 2: Write the failing golden test**

Create `pkg/quantize/ts/clock-sync.test.ts`:

```ts
import { describe, test, expect } from "bun:test";
import { newClockSync, observeServerTime, estimatedServerNow } from "./clock-sync";

// The golden manifest is authored by cmd/csharp-golden (Go reference).
const golden = require("../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json");

describe("ClockSync golden parity (TS === Go === C#)", () => {
  test("window matches", () => {
    // INSTANT_WINDOW is a module-private const; assert via observation count.
    expect(golden.clockSync.window).toBe(40);
  });

  test("reproduces every golden offset", () => {
    const c = newClockSync();
    for (const o of golden.clockSync.observations) {
      observeServerTime(c, o.serverMs, o.clientNowMs);
      expect(c.initialized).toBe(true);
      expect(c.offsetMs).toBeCloseTo(o.expectedOffsetMs, 6);
      expect(estimatedServerNow(c, o.clientNowMs)).toBeCloseTo(o.clientNowMs + o.expectedOffsetMs, 6);
    }
  });
});
```

- [ ] **Step 3: Run to verify it passes (source is the proven web copy)**

Run: `bun test pkg/quantize/ts/clock-sync.test.ts`
Expected: `2 pass`. (This passes immediately — the TS source is the reference the Go golden was modeled on. The test's job is to PREVENT future drift; run it now to confirm the golden + import path resolve.)

- [ ] **Step 4: Add a justfile recipe to run TS core tests**

In `justfile`, after the `csharp-golden` recipe, add:

```make
# run the shared TS core unit/golden tests (bun)
ts-core-test:
    bun test pkg/quantize/ts/
```

Run: `just ts-core-test`
Expected: passes (includes `clock-sync.test.ts`).

- [ ] **Step 5: Commit**

```bash
git add pkg/quantize/ts/clock-sync.ts pkg/quantize/ts/clock-sync.test.ts justfile
git commit -m "feat(sdk): promote ClockSync into pkg/quantize/ts shared core + golden parity test"
```

---

## Task 4: InterpolationBuffer (C#) + unit test

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/InterpolationBuffer.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/InterpolationBufferTests.cs`

- [ ] **Step 1: Write the failing unit test**

Create `csharp/Mmokit.Sdk.Core.Tests/InterpolationBufferTests.cs`:

```csharp
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class InterpolationBufferTests
    {
        static Sample S(double x, double t) => new Sample { WorldX = x, WorldY = 0, VelX = 0, VelY = 0, Rotation = 0, ProducedAtMs = t };

        [Fact]
        public void EmptyBufferSamplesNull()
        {
            var b = new InterpolationBuffer();
            Assert.False(b.SampleAt(123, out _));
            Assert.False(b.TryNewest(out _));
        }

        [Fact]
        public void StaleGateDropsOutOfOrderFrames()
        {
            var b = new InterpolationBuffer();
            b.Push(S(0, 1000));
            b.Push(S(10, 1050));
            Assert.True(b.IsStale(1000));        // older than tip 1050
            b.Push(S(99, 1000));                 // dropped by Push
            Assert.True(b.TryNewest(out var tip));
            Assert.Equal(10, tip.WorldX);        // still the 1050 sample
        }

        [Fact]
        public void InterpolatesMidpoint()
        {
            var b = new InterpolationBuffer(renderDelayMs: 0);
            b.Push(S(0, 1000));
            b.Push(S(100, 1100));
            Assert.True(b.SampleAt(1050, out var r));
            Assert.Equal(50, r.RenderX, 6);      // halfway between the two
        }

        [Fact]
        public void RingEvictsBeyondSize()
        {
            var b = new InterpolationBuffer(ringSize: 2);
            b.Push(S(1, 1000));
            b.Push(S(2, 1100));
            b.Push(S(3, 1200));                  // evicts the 1000 sample
            Assert.True(b.SampleAt(900, out var r)); // before oldest -> clamps to oldest held (1100)
            Assert.Equal(2, r.RenderX, 6);
        }
    }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd csharp && dotnet test Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj --filter InterpolationBufferTests -v q 2>&1 | tail -15`
Expected: build error — `InterpolationBuffer` does not exist.

- [ ] **Step 3: Implement InterpolationBuffer.cs**

Create `csharp/Mmokit.Sdk.Core/InterpolationBuffer.cs`:

```csharp
using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// Optional stateful per-entity playback buffer: a Sample ring + the two
    /// orchestration footguns bundled (stale-gated push, interpolate-at-render-
    /// time). Operates ONLY on Sample — carries no game semantics. The consumer
    /// owns one buffer per entity, converts its entity -> Sample (incl. rotation
    /// rule via TryNewest), and applies the InterpolationResult to its view.
    /// Headless/bot consumers ignore this and use InterpolationCore directly.
    public sealed class InterpolationBuffer
    {
        public const int DefaultRingSize = 4;
        public const double DefaultRenderDelayMs = 100;
        public const double DefaultMaxExtrapolateMs = 50;

        readonly List<Sample> _ring = new();

        public int RingSize { get; }
        public double RenderDelayMs { get; }
        public double MaxExtrapolateMs { get; }

        public InterpolationBuffer(
            int ringSize = DefaultRingSize,
            double renderDelayMs = DefaultRenderDelayMs,
            double maxExtrapolateMs = DefaultMaxExtrapolateMs)
        {
            RingSize = ringSize;
            RenderDelayMs = renderDelayMs;
            MaxExtrapolateMs = maxExtrapolateMs;
        }

        /// Stale-gated append (drops frames older than the newest held).
        public void Push(Sample s) => InterpolationCore.PushSample(_ring, s, RingSize);

        /// Whether Push(s) would drop s as stale. Gate non-interpolated field
        /// snapshots (size/health/…) on this to match the position ring.
        public bool IsStale(double producedAtMs) => InterpolationCore.IsStaleSample(_ring, producedAtMs);

        /// Newest sample held (for a prev-rotation fallback on stationary entities).
        public bool TryNewest(out Sample s)
        {
            if (_ring.Count > 0) { s = _ring[_ring.Count - 1]; return true; }
            s = default;
            return false;
        }

        /// Interpolated render pose at the given server render time.
        public bool SampleAt(double renderTimeMs, out InterpolationResult result)
            => InterpolationCore.InterpolateRing(_ring, renderTimeMs, MaxExtrapolateMs, RenderDelayMs, out result);
    }
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd csharp && dotnet test Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj --filter InterpolationBufferTests -v q 2>&1 | tail -5`
Expected: `Passed!  - Failed: 0`

- [ ] **Step 5: Commit**

```bash
git add csharp/Mmokit.Sdk.Core/InterpolationBuffer.cs csharp/Mmokit.Sdk.Core.Tests/InterpolationBufferTests.cs
git commit -m "feat(csharp-sdk): Sample-based InterpolationBuffer playback primitive"
```

---

## Task 5: InterpolationBuffer (TS) + unit test

**Files:**
- Create: `pkg/quantize/ts/interpolation-buffer.ts`
- Create: `pkg/quantize/ts/interpolation-buffer.test.ts`

- [ ] **Step 1: Write the failing unit test**

Create `pkg/quantize/ts/interpolation-buffer.test.ts`:

```ts
import { describe, test, expect } from "bun:test";
import { InterpolationBuffer } from "./interpolation-buffer";
import type { Sample } from "./interpolation-core";

const S = (x: number, t: number): Sample => ({ worldX: x, worldY: 0, velX: 0, velY: 0, rotation: 0, producedAtMs: t });

describe("InterpolationBuffer", () => {
  test("empty buffer samples null", () => {
    const b = new InterpolationBuffer();
    expect(b.sampleAt(123)).toBeNull();
    expect(b.newest()).toBeNull();
  });

  test("stale gate drops out-of-order frames", () => {
    const b = new InterpolationBuffer();
    b.push(S(0, 1000));
    b.push(S(10, 1050));
    expect(b.isStale(1000)).toBe(true);
    b.push(S(99, 1000)); // dropped
    expect(b.newest()!.worldX).toBe(10);
  });

  test("interpolates midpoint", () => {
    const b = new InterpolationBuffer({ renderDelayMs: 0 });
    b.push(S(0, 1000));
    b.push(S(100, 1100));
    expect(b.sampleAt(1050)!.renderX).toBeCloseTo(50, 6);
  });

  test("ring evicts beyond size", () => {
    const b = new InterpolationBuffer({ ringSize: 2 });
    b.push(S(1, 1000));
    b.push(S(2, 1100));
    b.push(S(3, 1200)); // evicts 1000
    expect(b.sampleAt(900)!.renderX).toBeCloseTo(2, 6);
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `bun test pkg/quantize/ts/interpolation-buffer.test.ts`
Expected: FAIL — cannot find module `./interpolation-buffer`.

- [ ] **Step 3: Implement interpolation-buffer.ts**

Create `pkg/quantize/ts/interpolation-buffer.ts`:

```ts
/**
 * Optional stateful per-entity playback buffer: a Sample ring + the two
 * orchestration footguns bundled (stale-gated push, interpolate-at-render-
 * time). Operates only on Sample — no game semantics. The consumer owns one
 * buffer per entity, converts its entity -> Sample (incl. rotation rule via
 * newest()), and applies the InterpolationResult to its view. Headless/bot
 * consumers ignore this and use interpolation-core directly.
 */
import {
  type Sample,
  type SampleRing,
  type InterpolationResult,
  pushSample,
  isStaleSample,
  interpolateRing,
} from "./interpolation-core.js";

export const DEFAULT_RING_SIZE = 4;
export const DEFAULT_RENDER_DELAY_MS = 100;
export const DEFAULT_MAX_EXTRAPOLATE_MS = 50;

export interface InterpolationBufferConfig {
  ringSize?: number;
  renderDelayMs?: number;
  maxExtrapolateMs?: number;
}

export class InterpolationBuffer implements SampleRing {
  samples: Sample[] = [];
  readonly ringSize: number;
  readonly renderDelayMs: number;
  readonly maxExtrapolateMs: number;

  constructor(cfg: InterpolationBufferConfig = {}) {
    this.ringSize = cfg.ringSize ?? DEFAULT_RING_SIZE;
    this.renderDelayMs = cfg.renderDelayMs ?? DEFAULT_RENDER_DELAY_MS;
    this.maxExtrapolateMs = cfg.maxExtrapolateMs ?? DEFAULT_MAX_EXTRAPOLATE_MS;
  }

  /** Stale-gated append (drops frames older than the newest held). */
  push(s: Sample): void {
    pushSample(this, s, this.ringSize);
  }

  /** Whether push(s) would drop s as stale — gate non-position field snapshots on this. */
  isStale(producedAtMs: number): boolean {
    return isStaleSample(this, producedAtMs);
  }

  /** Newest sample held, or null when empty (for prev-rotation fallback). */
  newest(): Sample | null {
    return this.samples.length > 0 ? this.samples[this.samples.length - 1] : null;
  }

  /** Interpolated render pose at the given server render time, or null when empty. */
  sampleAt(renderTimeMs: number): InterpolationResult | null {
    return interpolateRing(this, renderTimeMs, this.maxExtrapolateMs, this.renderDelayMs);
  }
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `bun test pkg/quantize/ts/interpolation-buffer.test.ts`
Expected: `4 pass`.

- [ ] **Step 5: Commit**

```bash
git add pkg/quantize/ts/interpolation-buffer.ts pkg/quantize/ts/interpolation-buffer.test.ts
git commit -m "feat(sdk): Sample-based InterpolationBuffer in pkg/quantize/ts shared core"
```

---

## Task 6: sdkgen emission — copy the new core files into every SDK

**Files:**
- Modify: `cmd/sdkgen/main.go`, `cmd/sdkgen/backend.go`, `cmd/sdkgen/backend_ts.go`, `cmd/sdkgen/backend_csharp.go`

- [ ] **Step 1: Add TS source-path flags (defaults point at the shared core)**

In `cmd/sdkgen/main.go`, after the `interpTS` flag (line ~28), add:

```go
	clockSyncTS := flag.String("clock-sync", "pkg/quantize/ts/clock-sync.ts", "Path to clock-sync.ts to copy")
	interpBufferTS := flag.String("interp-buffer", "pkg/quantize/ts/interpolation-buffer.ts", "Path to interpolation-buffer.ts to copy")
```

In the `backendOpts{...}` literal passed to `backendFor`, add:

```go
		ClockSyncTS:    *clockSyncTS,
		InterpBufferTS: *interpBufferTS,
```

- [ ] **Step 2: Extend backendOpts + tsBackend**

In `cmd/sdkgen/backend.go`, add to `backendOpts`:

```go
	ClockSyncTS    string // TS: clock-sync.ts source path
	InterpBufferTS string // TS: interpolation-buffer.ts source path
```

And in `backendFor`'s `"ts"` case:

```go
		return tsBackend{coreTS: o.CoreTS, interpTS: o.InterpTS, clockSyncTS: o.ClockSyncTS, interpBufferTS: o.InterpBufferTS}, nil
```

In `cmd/sdkgen/backend_ts.go`, add the fields + emit them:

```go
type tsBackend struct {
	coreTS         string
	interpTS       string
	clockSyncTS    string
	interpBufferTS string
}
```

```go
func (b tsBackend) CoreFiles() []CoreFile {
	return []CoreFile{
		{Src: b.coreTS, Dst: "delta-decoder-core.ts"},
		{Src: b.interpTS, Dst: "interpolation-core.ts"},
		{Src: b.clockSyncTS, Dst: "clock-sync.ts"},
		{Src: b.interpBufferTS, Dst: "interpolation-buffer.ts"},
	}
}
```

- [ ] **Step 3: Add the C# core files to csharpBackend.CoreFiles()**

In `cmd/sdkgen/backend_csharp.go`, add to the `names` slice in `CoreFiles()`:

```go
		"ClockSync.cs",
		"InterpolationBuffer.cs",
```

- [ ] **Step 4: Verify sdkgen builds**

Run: `go build -o bin/sdkgen ./cmd/sdkgen && echo OK`
Expected: `OK`

- [ ] **Step 5: Regenerate all three SDKs + verify the new files land in _core**

Run:
```bash
just space-sdk && just client-sdk examples/4node-basic && POSTGRES_URL="${POSTGRES_URL:-postgres://mmo:mmo@localhost:5432/mmo_4node?sslmode=disable}" just csharp-sdk
ls web-pixi/sdk/_core/clock-sync.ts web-pixi/sdk/_core/interpolation-buffer.ts \
   examples/4node-basic/web/sdk/_core/clock-sync.ts examples/4node-basic/web/sdk/_core/interpolation-buffer.ts \
   <WINDOWS-HOME>/unitygames/spacemmo-client/Assets/Mmokit/Sdk/_core/ClockSync.cs \
   <WINDOWS-HOME>/unitygames/spacemmo-client/Assets/Mmokit/Sdk/_core/InterpolationBuffer.cs
```
Expected: all six paths exist. (Requires the dev Postgres up for the schema dumps: `just db-up`.)

- [ ] **Step 6: Verify the C# SDK still compiles with the new _core files**

Run: `just csharp-compile-test 2>&1 | tail -3`
Expected: `PASS`.

- [ ] **Step 7: Commit**

```bash
git add cmd/sdkgen/main.go cmd/sdkgen/backend.go cmd/sdkgen/backend_ts.go cmd/sdkgen/backend_csharp.go web-pixi/sdk examples/4node-basic/web/sdk
git commit -m "feat(sdkgen): emit clock-sync + interpolation-buffer into every SDK _core"
```

---

## Task 7: Migrate web-pixi to the shared core

**Files:**
- Modify: `web-pixi/src/types.ts:22-30` (ClientEntity)
- Modify: `web-pixi/src/interpolation.ts` (full rewrite)
- Modify: `web-pixi/src/state.ts:3,61,215`
- Modify: `web-pixi/src/__tests__/clockSync.test.ts:2` (import path)
- Delete: `web-pixi/src/clockSync.ts`

- [ ] **Step 1: Repoint the ClientEntity ring to a buffer**

In `web-pixi/src/types.ts`, replace the `ClientEntity` interface body. Change the `samples` field to a buffer (drop the local `EntitySample[]` ring; the buffer owns it):

```ts
import type { InterpolationBuffer } from "../sdk/_core/interpolation-buffer.js";
// ... (keep existing imports)

export interface ClientEntity {
  current: AnyEntity;            // latest decoded state (for HUD / game logic)
  buffer: InterpolationBuffer;   // per-entity sample ring + playback
  // Interpolated render values (set each frame by interpolateEntities).
  renderX: number;
  renderY: number;
  renderRot: number;
}
```

Keep the `EntitySample` interface (still used as the Sample shape; it is structurally `Sample`).

- [ ] **Step 2: Rewrite interpolation.ts to drive the buffer + shared ClockSync**

Replace `web-pixi/src/interpolation.ts` entirely with:

```ts
import type { AnyEntity } from "../sdk/index.js";
import { lerp, lerpAngle } from "../sdk/_core/interpolation-core.js";
import { InterpolationBuffer } from "../sdk/_core/interpolation-buffer.js";
import { type ClockSync, estimatedServerNow } from "../sdk/_core/clock-sync.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants";
import type { ClientEntity, EntitySample } from "./types";

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

function newBuffer(): InterpolationBuffer {
  return new InterpolationBuffer({
    ringSize: RING_SIZE,
    renderDelayMs: RENDER_DELAY,
    maxExtrapolateMs: MAX_EXTRAPOLATE_MS,
  });
}

export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  if (!existing) {
    const buffer = newBuffer();
    const first = sampleFrom(serverState, producedAtMs, 0);
    buffer.push(first);
    entities.set(id, {
      current: serverState,
      buffer,
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    });
    return;
  }
  // Gate the whole snapshot on the same monotonicity rule the ring uses, so
  // non-interpolated fields (size/health/…) don't snap backward at a cell
  // boundary when the ex-authority's final frame arrives last.
  if (existing.buffer.isStale(producedAtMs)) {
    return;
  }
  existing.buffer.push(sampleFrom(serverState, producedAtMs, existing.renderRot));
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
    const r = ent.buffer.sampleAt(renderTime);
    if (r) {
      ent.renderX = r.renderX;
      ent.renderY = r.renderY;
      ent.renderRot = r.renderRot;
    }
  }
}

export { lerp, lerpAngle };
```

(Note: the standalone `pushSample` export is removed; if anything imports it, repoint to `ent.buffer.push`. Verify in Step 5.)

- [ ] **Step 3: Repoint state.ts to the shared ClockSync**

In `web-pixi/src/state.ts`, change the import (line 3) and construction (line 215):

```ts
import { newClockSync, type ClockSync } from "../sdk/_core/clock-sync.js";
```
```ts
    clockSync: newClockSync(),
```

(The `clockSync: ClockSync` field at line 61 is unchanged.)

- [ ] **Step 4: Repoint the existing clockSync test + delete the local copy**

In `web-pixi/src/__tests__/clockSync.test.ts`, change line 2:

```ts
import { newClockSync, observeServerTime, estimatedServerNow } from "../../sdk/_core/clock-sync.js";
```

Then delete the now-orphaned local module:

Run: `rm web-pixi/src/clockSync.ts`

- [ ] **Step 5: Typecheck, test, build**

Run:
```bash
cd web-pixi && bun test src/__tests__/ 2>&1 | tail -5 && bun run typecheck && bun run build 2>&1 | tail -3
```
Expected: tests pass, `tsc --noEmit` clean, vite build succeeds. If `typecheck` flags a stray `samples`/`pushSample`/`clockSync` import elsewhere (e.g. `main.ts`, a renderer, or `interpolation.test.ts`), repoint it: `ent.samples` → `ent.buffer`, `./clockSync` → `../sdk/_core/clock-sync.js`, `pushSample(ent, s)` → `ent.buffer.push(s)`. Re-run until clean.

- [ ] **Step 6: Commit**

```bash
git add web-pixi/src
git commit -m "refactor(web-pixi): use shared SDK ClockSync + InterpolationBuffer, drop local copies"
```

---

## Task 8: Migrate examples/4node-basic/web to the shared core

**Files:**
- Modify: `examples/4node-basic/web/src/state.ts` (ClientEntity type + clockSync import/construction)
- Modify: `examples/4node-basic/web/src/interpolation.ts` (full rewrite)
- Modify: `examples/4node-basic/web/src/network.ts:7,105` (observeFrameStamps import)
- Delete: `examples/4node-basic/web/src/clockSync.ts`

- [ ] **Step 1: Repoint ClientEntity + ClockSync in state.ts**

`ClientEntity` is declared in `examples/4node-basic/web/src/state.ts` (note: 4node spreads `...serverState` onto the entity and stores `samples`). Replace its `samples: EntitySample[]` field with `buffer: InterpolationBuffer` and add the import. Change the clockSync import (line 2) + construction (line 118):

```ts
import { type ClockSync, newClockSync } from "../sdk/_core/clock-sync.js";
import { InterpolationBuffer } from "../sdk/_core/interpolation-buffer.js";
```
```ts
    clockSync: newClockSync(),
```

In the `ClientEntity` interface, replace `samples: EntitySample[];` with `buffer: InterpolationBuffer;` (keep `EntitySample` for the Sample shape).

- [ ] **Step 2: Rewrite 4node interpolation.ts to drive the buffer**

Replace `examples/4node-basic/web/src/interpolation.ts` entirely with (4node derives rotation from velocity only — no `angle` field; preserves its audit hook):

```ts
import type { AnyEntity } from "../sdk/entities.js";
import { lerp, lerpAngle } from "../sdk/_core/interpolation-core.js";
import { InterpolationBuffer } from "../sdk/_core/interpolation-buffer.js";
import { type ClockSync, estimatedServerNow } from "../sdk/_core/clock-sync.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants.js";
import type { ClientEntity, EntitySample } from "./state.js";
import { recordEntityCreate, type ReplicationAudit } from "./replicationAudit.js";

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

function newBuffer(): InterpolationBuffer {
  return new InterpolationBuffer({
    ringSize: RING_SIZE,
    renderDelayMs: RENDER_DELAY,
    maxExtrapolateMs: MAX_EXTRAPOLATE_MS,
  });
}

export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
  audit?: ReplicationAudit,
  nowMs?: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);

  if (!existing) {
    if (audit && nowMs !== undefined) recordEntityCreate(audit, nowMs, id);
    const buffer = newBuffer();
    const first = sampleFrom(serverState, producedAtMs, 0);
    buffer.push(first);
    const ent: ClientEntity = {
      ...serverState,
      prevX: serverState.worldX,
      prevY: serverState.worldY,
      isReplica: false,
      isGhost: false,
      buffer,
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    };
    entities.set(id, ent);
    return;
  }
  if (existing.buffer.isStale(producedAtMs)) {
    return;
  }
  const prevRot = existing.renderRot;
  Object.assign(existing, serverState);
  existing.prevX = existing.renderX;
  existing.prevY = existing.renderY;
  existing.buffer.push(sampleFrom(serverState, producedAtMs, prevRot));
}

export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  clock: ClockSync,
  clientNowMs: number,
): void {
  if (!clock.initialized) return;
  const renderTime = estimatedServerNow(clock, clientNowMs) - RENDER_DELAY;
  for (const ent of entities.values()) {
    const r = ent.buffer.sampleAt(renderTime);
    if (r) {
      ent.renderX = r.renderX;
      ent.renderY = r.renderY;
      ent.renderRot = r.renderRot;
    }
  }
}

export { lerp, lerpAngle };
```

Note: `Object.assign(existing, serverState)` overwrites scalar fields only; `buffer` (a class instance, not present on `serverState`) is preserved. Confirm `ClientEntity` no longer references `samples` anywhere after this.

- [ ] **Step 3: Repoint network.ts's observeFrameStamps + delete the local clockSync**

In `examples/4node-basic/web/src/network.ts`, change line 7:

```ts
import { observeFrameStamps } from "../sdk/_core/clock-sync.js";
```

Then:

Run: `rm examples/4node-basic/web/src/clockSync.ts`

- [ ] **Step 4: Typecheck + build (+ tests if present)**

Run:
```bash
cd examples/4node-basic/web && (bun test src/__tests__/ 2>&1 | tail -5 || true) && bun run typecheck && bun run build 2>&1 | tail -3
```
Expected: typecheck clean, build succeeds. Repoint any remaining `./clockSync`, `ent.samples`, or `pushSample` references the typechecker flags (e.g. `src/__tests__/interpolation.test.ts`, `renderer.ts`) — `ent.samples` → `ent.buffer`, push via `ent.buffer.push(...)`, clockSync import → `../sdk/_core/clock-sync.js`. Re-run until clean.

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/web/src
git commit -m "refactor(4node-web): use shared SDK ClockSync + InterpolationBuffer, drop local copies"
```

---

## Task 9: Final verification

- [ ] **Step 1: All Go + C# + TS core tests green**

Run:
```bash
go vet ./... && \
cd csharp && dotnet test -v q 2>&1 | tail -5 && cd .. && \
just ts-core-test && just csharp-compile-test 2>&1 | tail -3
```
Expected: vet clean; C# `Passed! Failed: 0` (incl. ClockSync golden + InterpolationBuffer); TS core tests pass; compile gate PASS.

- [ ] **Step 2: Both web clients build**

Run:
```bash
(cd web-pixi && bun run build 2>&1 | tail -2) && (cd examples/4node-basic/web && bun run build 2>&1 | tail -2)
```
Expected: both succeed.

- [ ] **Step 3: Confirm no orphaned local copies remain**

Run: `ls web-pixi/src/clockSync.ts examples/4node-basic/web/src/clockSync.ts 2>&1`
Expected: both `No such file or directory`.

- [ ] **Step 4: Manual smoke (operator-run, not automated)**

Start a dev server + web client (`just dev` for space, or `examples/4node-basic just dev`) and confirm entities render with smooth 60fps interpolation over 20Hz ticks and no jitter at cell boundaries. This is the existing manual interpolation smoke; behavior must be unchanged from before the refactor.

---

## Self-Review Notes

- **Spec coverage:** ClockSync (Tasks 2,3) ✓; InterpolationBuffer Sample-based (Tasks 4,5) ✓; promoted constants as defaults (Tasks 4,5 ctors) ✓; sdkgen emission both langs (Task 6) ✓; both web clients migrated + local copies deleted (Tasks 7,8) ✓; golden cross-language parity with bursty + rollover cases (Tasks 1,2,3) ✓; out-of-scope items untouched (no wire/core-primitive changes). All spec sections map to a task.
- **Type consistency:** TS `InterpolationBuffer` methods `push`/`isStale`/`newest`/`sampleAt` + config `{ringSize,renderDelayMs,maxExtrapolateMs}` used identically in Tasks 5/7/8; C# `Push`/`IsStale`/`TryNewest`/`SampleAt` used identically in Tasks 4 and the (consumer-facing) Unity glue. `ClockSync` API (`observeServerTime`/`estimatedServerNow`/`offsetMs`/`initialized` TS; `ObserveServerTime`/`EstimatedServerNow`/`OffsetMs`/`Initialized`/`ObserveFrameStamps` C#) consistent across Tasks 2/3 and consumers. Golden field names (`serverMs`/`clientNowMs`/`expectedOffsetMs`/`window`) match between Go (Task 1), C# DTO (Task 1), and both tests (Tasks 2,3).
- **Risk note:** Tasks 7/8 may surface stray imports in files not enumerated (renderer/main/test). Each migration task's verify step instructs the exact repoint and to re-run until the typechecker is clean — no silent gaps.
