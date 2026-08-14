# C# SDK — Plan 3: C# Core (workspace + delta-decoder + interpolation + golden tests) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish an in-repo C# workspace and port the two pure-function runtime cores — the delta-decoder primitives and the render-lag interpolation — to C#, proven correct against the Go/TS source by a cross-language golden test.

**Architecture:** A self-contained `csharp/` workspace at repo root holds a `netstandard2.1` library (`Mmokit.Sdk.Core`) and a `net10.0` xUnit test project. The two ports (`DeltaDecoderCore.cs`, `InterpolationCore.cs`) mirror `pkg/quantize/ts/delta-decoder-core.ts` and `interpolation-core.ts` line-for-line in behavior. A Go generator program (`cmd/csharp-golden`) emits canonical wire bytes + expected decoded values (authoritative, using `pkg/quantize`) into the test project's `testdata/`; the C# golden test decodes those bytes and asserts a match — the drift guard that fails loudly if the hand-ported C# diverges from the Go source of truth. Unity consumes the `.cs` source directly (copied by sdkgen in a later plan), so the library targets `netstandard2.1` for Unity compatibility; the `.csproj` exists only for in-repo `dotnet test`.

**Tech Stack:** C# (.NET 10 SDK present; library `netstandard2.1`, tests `net10.0` + xUnit), Go (`cmd/csharp-golden` generator), `just`, `dotnet test`.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §C, §F.3. Resolves the spec open-item "exact in-repo home for the C# ports" → a single top-level `csharp/` workspace (NOT scattered into `pkg/*/cs/`).

**Prerequisites:** Plans 1–2 merged. `.NET 10` SDK available (`dotnet --version` → 10.x, verified). No existing C# in repo.

**Scope note:** The stateful **UDP transport** port is explicitly NOT in this plan — it is Plan 4. This plan covers only the two pure cores + workspace + golden infra.

---

## Background facts (verified in current source)

- `pkg/quantize/quantize.go` dequantizers (C# must match exactly): `UnPos = q/65535*cellSize`; `UnAngle = q/65535*2π − π`; `UnNorm = q/255`; `UnVel = q/32767*scale`; `UnRel = q/32767*halfRange`. Quantizers: `Vel(v,scale)=clamp(v/scale,−1,1)*32767` (int16), `Angle` normalizes via atan2 then `*65535` (uint16), `Norm=clamp(v,0,1)*255` (uint8).
- `pkg/quantize/ts/delta-decoder-core.ts` (386 lines) is the reference for `DeltaDecoderCore.cs`. Public surface: big-endian reads `readInt16/readUint16/readUint32/readFloat32`; dequantizers `unAngle/unNorm/unVel/unRel`; `FrameHeader` + `decodeFrameHeader` (20-byte header: tick u32, seq u32, flags u32, fullCount u16, deltaCount u16, removedCount u16, exitedCount u16; const `FRAME_HEADER_SIZE=20`, `FRAME_FLAG_FRESH_SNAPSHOT=1`); `FullEntryHeader`/`decodeFullEntry` (netID u32, epoch u32, entityType u8, producedAtMs u64, snapLen u16, snapshot, initLen u16, [init]); `DeltaEntryHeader`/`decodeDeltaEntry` (… deltaLen u16, delta); `decodeRemovedIDs` (count × u32); `applyDelta(fieldSizes, hasVarTail, baseline, delta)`; `BaselineStore`; `decodeLengthPrefixedStringU8/U16`.
- `pkg/quantize/ts/interpolation-core.ts` (160 lines) is the reference for `InterpolationCore.cs`. Public surface: `Sample{worldX,worldY,velX,velY,rotation,producedAtMs}`, `InterpolationResult{renderX,renderY,renderRot}`, `lerp`, `lerpAngle`, `pushSample(ring,s,ringSize)`, `isStaleSample(ring,producedAtMs)`, `interpolateRing(ring,renderTimeMs,maxExtrapolateMs,renderDelayMs)`.
- All `number`s in the TS are IEEE-754 doubles → the C# ports use `double` for all sample/result/quantize-output fields to preserve parity (no `float`).

---

## File Structure

- **Create:** `csharp/.gitignore` — ignore `bin/`, `obj/`.
- **Create:** `csharp/Mmokit.Sdk.Core/Mmokit.Sdk.Core.csproj` — `netstandard2.1` library.
- **Create:** `csharp/Mmokit.Sdk.Core/InterpolationCore.cs` — interpolation port.
- **Create:** `csharp/Mmokit.Sdk.Core/DeltaDecoderCore.cs` — delta-decoder port.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj` — `net10.0` xUnit test project referencing the library.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/InterpolationCoreTests.cs` — hand-computed unit tests.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs` — JSON DTOs + loader for the golden manifest.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/DeltaDecoderGoldenTests.cs` — golden cross-language tests.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json` — generated golden manifest (committed).
- **Create:** `cmd/csharp-golden/main.go` — Go generator that writes the golden manifest.
- **Modify:** `justfile` — add `csharp-test` and `csharp-golden` recipes.

---

### Task 1: C# workspace scaffold + smoke test

**Files:**
- Create: `csharp/.gitignore`, `csharp/Mmokit.Sdk.Core/Mmokit.Sdk.Core.csproj`, `csharp/Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj`, `csharp/Mmokit.Sdk.Core/Placeholder.cs`, `csharp/Mmokit.Sdk.Core.Tests/SmokeTest.cs`
- Modify: `justfile`

- [ ] **Step 1: Create `csharp/.gitignore`**

```gitignore
bin/
obj/
```

- [ ] **Step 2: Create the library project `csharp/Mmokit.Sdk.Core/Mmokit.Sdk.Core.csproj`**

```xml
<Project Sdk="Microsoft.NET.Sdk">

  <PropertyGroup>
    <!-- netstandard2.1 = the Unity-consumable surface. Unity compiles the
         .cs source (copied into Assets by sdkgen); this csproj exists only
         for in-repo `dotnet test`. -->
    <TargetFramework>netstandard2.1</TargetFramework>
    <LangVersion>9.0</LangVersion>
    <Nullable>enable</Nullable>
    <RootNamespace>Mmokit.Sdk.Core</RootNamespace>
    <AssemblyName>Mmokit.Sdk.Core</AssemblyName>
  </PropertyGroup>

</Project>
```

- [ ] **Step 3: Create a temporary `csharp/Mmokit.Sdk.Core/Placeholder.cs`** (removed in Task 2 once real types land — keeps the library non-empty so the project builds):

```csharp
namespace Mmokit.Sdk.Core
{
    // Placeholder so the library compiles before the real cores land.
    // Removed in Task 2.
    internal static class Placeholder { }
}
```

- [ ] **Step 4: Create the test project `csharp/Mmokit.Sdk.Core.Tests/Mmokit.Sdk.Core.Tests.csproj`**

```xml
<Project Sdk="Microsoft.NET.Sdk">

  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <LangVersion>latest</LangVersion>
    <Nullable>enable</Nullable>
    <IsPackable>false</IsPackable>
  </PropertyGroup>

  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" />
    <PackageReference Include="xunit" Version="2.9.2" />
    <PackageReference Include="xunit.runner.visualstudio" Version="2.8.2" />
  </ItemGroup>

  <ItemGroup>
    <ProjectReference Include="../Mmokit.Sdk.Core/Mmokit.Sdk.Core.csproj" />
  </ItemGroup>

  <!-- Golden manifest is copied next to the test assembly so tests can read it. -->
  <ItemGroup>
    <None Include="testdata/**/*" CopyToOutputDirectory="PreserveNewest" />
  </ItemGroup>

</Project>
```

- [ ] **Step 5: Create `csharp/Mmokit.Sdk.Core.Tests/SmokeTest.cs`**

```csharp
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class SmokeTest
    {
        [Fact]
        public void ToolchainWorks()
        {
            Assert.Equal(4, 2 + 2);
        }
    }
}
```

- [ ] **Step 6: Add justfile recipes** — append to `justfile`:

```just
# run the C# SDK core unit + golden tests
csharp-test:
    cd csharp && dotnet test

# regenerate the C# golden manifest from Go (authoritative wire bytes)
csharp-golden:
    go run ./cmd/csharp-golden
```

- [ ] **Step 7: Verify the toolchain end-to-end**

Run: `cd csharp && dotnet test 2>&1 | tail -15`
Expected: restore + build succeed; `Passed!  - Failed: 0, Passed: 1` (the smoke test). If `dotnet` needs to download the netstandard/net10 targeting packs on first run, that's normal.

- [ ] **Step 8: Commit**

```bash
git add csharp/.gitignore csharp/Mmokit.Sdk.Core csharp/Mmokit.Sdk.Core.Tests justfile
git commit -m "build(csharp): scaffold Mmokit.Sdk.Core workspace + xUnit smoke test

netstandard2.1 library (Unity-consumable) + net10.0 xUnit test project.
just csharp-test / csharp-golden recipes added.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Port InterpolationCore + unit tests

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/InterpolationCore.cs`
- Delete: `csharp/Mmokit.Sdk.Core/Placeholder.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/InterpolationCoreTests.cs`

- [ ] **Step 1: Write the failing tests** — Create `csharp/Mmokit.Sdk.Core.Tests/InterpolationCoreTests.cs`:

```csharp
using System;
using System.Collections.Generic;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class InterpolationCoreTests
    {
        const double Eps = 1e-9;

        [Fact]
        public void Lerp_Midpoint()
        {
            Assert.Equal(5.0, InterpolationCore.Lerp(0, 10, 0.5), 12);
        }

        [Fact]
        public void LerpAngle_TakesShortestPath()
        {
            // From +3.0 rad to -3.0 rad: shortest path is forward across PI (~+0.283),
            // NOT backward (~-6.0). At t=0.5 we should be just past PI / wrapped.
            double a = 3.0, b = -3.0;
            double mid = InterpolationCore.LerpAngle(a, b, 0.5);
            // diff wraps to (-3 - 3) + 2PI = +0.2832; mid = 3 + 0.1416 = 3.1416 (~PI)
            Assert.Equal(3.0 + ((b - a) + 2 * Math.PI) / 2.0, mid, 9);
        }

        [Fact]
        public void PushSample_DropsOutOfOrderAndEvicts()
        {
            var ring = new List<Sample>();
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 100 }, 3);
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 200 }, 3);
            // Out-of-order (older than tip 200) → dropped.
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 150 }, 3);
            Assert.Equal(2, ring.Count);
            // Fill past ringSize → oldest evicted.
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 300 }, 3);
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 400 }, 3);
            Assert.Equal(3, ring.Count);
            Assert.Equal(200, ring[0].ProducedAtMs, Eps);
            Assert.Equal(400, ring[2].ProducedAtMs, Eps);
        }

        [Fact]
        public void IsStaleSample_MatchesPushDrop()
        {
            var ring = new List<Sample> { new Sample { ProducedAtMs = 200 } };
            Assert.True(InterpolationCore.IsStaleSample(ring, 150));
            Assert.False(InterpolationCore.IsStaleSample(ring, 250));
            Assert.False(InterpolationCore.IsStaleSample(new List<Sample>(), 100));
        }

        [Fact]
        public void InterpolateRing_EmptyReturnsFalse()
        {
            Assert.False(InterpolationCore.InterpolateRing(
                new List<Sample>(), 0, 250, 100, out _));
        }

        [Fact]
        public void InterpolateRing_SingleSampleStatic()
        {
            var ring = new List<Sample> { new Sample { WorldX = 7, WorldY = 8, Rotation = 1.2, ProducedAtMs = 500 } };
            Assert.True(InterpolationCore.InterpolateRing(ring, 9999, 250, 100, out var r));
            Assert.Equal(7, r.RenderX, Eps);
            Assert.Equal(8, r.RenderY, Eps);
            Assert.Equal(1.2, r.RenderRot, Eps);
        }

        [Fact]
        public void InterpolateRing_LerpsBetweenPair()
        {
            // Two samples 100ms apart; renderDelay 0 so effS0 = s0 stamp.
            var ring = new List<Sample>
            {
                new Sample { WorldX = 0,   WorldY = 0,  ProducedAtMs = 1000 },
                new Sample { WorldX = 100, WorldY = 50, ProducedAtMs = 1100 },
            };
            // renderTime 1050 → t = 0.5 → (50, 25).
            Assert.True(InterpolationCore.InterpolateRing(ring, 1050, 250, 0, out var r));
            Assert.Equal(50, r.RenderX, 6);
            Assert.Equal(25, r.RenderY, 6);
        }

        [Fact]
        public void InterpolateRing_ExtrapolatesCapped()
        {
            var ring = new List<Sample>
            {
                new Sample { WorldX = 0, WorldY = 0, ProducedAtMs = 1000 },
                new Sample { WorldX = 0, WorldY = 0, VelX = 10, VelY = 0, ProducedAtMs = 1100 },
            };
            // renderTime far past s1; extrapolate capped to 250ms → 0.25s * 10 = 2.5.
            Assert.True(InterpolationCore.InterpolateRing(ring, 5000, 250, 0, out var r));
            Assert.Equal(2.5, r.RenderX, 6);
        }
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd csharp && dotnet test --filter InterpolationCoreTests 2>&1 | tail -15`
Expected: BUILD FAILS — `InterpolationCore`, `Sample`, `InterpolationResult` don't exist yet.

- [ ] **Step 3: Create the port** — `csharp/Mmokit.Sdk.Core/InterpolationCore.cs`:

```csharp
using System;
using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// One snapshot of an entity's authoritative state at a producer
    /// cluster-clock time. Port of interpolation-core.ts Sample.
    public struct Sample
    {
        public double WorldX;
        public double WorldY;
        public double VelX;
        public double VelY;
        public double Rotation;
        public double ProducedAtMs;
    }

    /// Interpolated render position computed by InterpolateRing.
    public struct InterpolationResult
    {
        public double RenderX;
        public double RenderY;
        public double RenderRot;
    }

    /// Render-lag interpolation primitives. Faithful port of
    /// pkg/quantize/ts/interpolation-core.ts. The CONSUMER owns the sample
    /// ring (a List&lt;Sample&gt; ordered ascending by ProducedAtMs); these
    /// helpers are stateless — consistent with the stateless-SDK design.
    public static class InterpolationCore
    {
        /// Linear interpolation between a and b at fraction t in [0,1].
        public static double Lerp(double a, double b, double t) => a + (b - a) * t;

        /// Angle lerp taking the shortest path around the unit circle.
        public static double LerpAngle(double a, double b, double t)
        {
            double diff = b - a;
            while (diff > Math.PI) diff -= Math.PI * 2;
            while (diff < -Math.PI) diff += Math.PI * 2;
            return a + diff * t;
        }

        /// Append a sample to the ring. Drops samples whose stamp predates
        /// the ring tip (out-of-order arrival across cell handoffs). Evicts
        /// the oldest sample when the ring would exceed ringSize.
        public static void PushSample(List<Sample> ring, Sample s, int ringSize)
        {
            if (ring.Count > 0 && s.ProducedAtMs < ring[ring.Count - 1].ProducedAtMs)
                return;
            ring.Add(s);
            if (ring.Count > ringSize)
                ring.RemoveAt(0);
        }

        /// True if producedAtMs is older than the ring tip — i.e. PushSample
        /// would drop it. Glue code gates non-interpolated field snapshots on
        /// this to obey the same monotonicity rule as the position ring.
        public static bool IsStaleSample(List<Sample> ring, double producedAtMs)
        {
            return ring.Count > 0 && producedAtMs < ring[ring.Count - 1].ProducedAtMs;
        }

        /// Compute the interpolated render position for one ring at
        /// renderTimeMs. Returns false (result = default) for an empty ring
        /// (caller leaves previous render state untouched). One sample →
        /// static. Two+ → newest bracketing pair lerped; past the newest
        /// sample, extrapolate with its velocity capped to maxExtrapolateMs.
        public static bool InterpolateRing(
            List<Sample> ring,
            double renderTimeMs,
            double maxExtrapolateMs,
            double renderDelayMs,
            out InterpolationResult result)
        {
            int n = ring.Count;
            result = default;
            if (n == 0) return false;
            if (n == 1)
            {
                var only = ring[0];
                result = new InterpolationResult { RenderX = only.WorldX, RenderY = only.WorldY, RenderRot = only.Rotation };
                return true;
            }

            Sample s0 = ring[0];
            Sample s1 = ring[1];
            for (int i = 1; i < n - 1; i++)
            {
                if (ring[i].ProducedAtMs <= renderTimeMs)
                {
                    s0 = ring[i];
                    s1 = ring[i + 1];
                }
            }

            double effS0Stamp = Math.Max(s0.ProducedAtMs, s1.ProducedAtMs - renderDelayMs);

            if (renderTimeMs <= effS0Stamp)
            {
                result = new InterpolationResult { RenderX = s0.WorldX, RenderY = s0.WorldY, RenderRot = s0.Rotation };
                return true;
            }
            if (renderTimeMs >= s1.ProducedAtMs)
            {
                double extMs = Math.Min(renderTimeMs - s1.ProducedAtMs, maxExtrapolateMs);
                double extS = extMs / 1000.0;
                result = new InterpolationResult
                {
                    RenderX = s1.WorldX + s1.VelX * extS,
                    RenderY = s1.WorldY + s1.VelY * extS,
                    RenderRot = s1.Rotation,
                };
                return true;
            }
            double t = (renderTimeMs - effS0Stamp) / (s1.ProducedAtMs - effS0Stamp);
            result = new InterpolationResult
            {
                RenderX = Lerp(s0.WorldX, s1.WorldX, t),
                RenderY = Lerp(s0.WorldY, s1.WorldY, t),
                RenderRot = LerpAngle(s0.Rotation, s1.Rotation, t),
            };
            return true;
        }
    }
}
```

- [ ] **Step 4: Remove the placeholder**

Run: `git rm csharp/Mmokit.Sdk.Core/Placeholder.cs`

- [ ] **Step 5: Run the tests**

Run: `cd csharp && dotnet test --filter InterpolationCoreTests 2>&1 | tail -15`
Expected: `Passed!  - Failed: 0, Passed: 8`.

- [ ] **Step 6: Commit**

```bash
git add csharp/Mmokit.Sdk.Core/InterpolationCore.cs csharp/Mmokit.Sdk.Core.Tests/InterpolationCoreTests.cs
git rm --cached csharp/Mmokit.Sdk.Core/Placeholder.cs 2>/dev/null || true
git commit -m "feat(csharp): port interpolation-core to InterpolationCore.cs

Faithful port of pkg/quantize/ts/interpolation-core.ts: lerp/lerpAngle,
pushSample (drop out-of-order + evict), isStaleSample, interpolateRing
(bracket-lerp + capped extrapolation). Consumer owns the List<Sample>
ring (stateless SDK). 8 unit tests.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Go golden generator + committed manifest

**Files:**
- Create: `cmd/csharp-golden/main.go`
- Create (generated, committed): `csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json`

The manifest is authoritative wire bytes + expected decoded values, produced by Go using `encoding/binary` (big-endian) and `pkg/quantize`. The C# golden test (Task 4) reads it.

- [ ] **Step 1: Create `cmd/csharp-golden/main.go`**

```go
// Command csharp-golden emits the cross-language golden manifest used by the
// C# Mmokit.Sdk.Core tests. It is the authoritative producer of canonical
// wire bytes (big-endian, matching pkg/quantize/wireformat.go layout) and the
// expected decoded values; the hand-ported C# DeltaDecoderCore must reproduce
// them exactly. Regenerate via `just csharp-golden`.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/zenion/mmokit/pkg/quantize"
)

// Manifest mirrors the C# GoldenModel DTOs (csharp/.../GoldenModel.cs).
type Manifest struct {
	Dequant   []DequantCase `json:"dequant"`
	Frame     FrameCase     `json:"frame"`
	ApplyDelta []ApplyCase  `json:"applyDelta"`
	Strings   []StringCase  `json:"strings"`
}

type DequantCase struct {
	Kind     string  `json:"kind"`     // "unAngle" | "unNorm" | "unVel" | "unRel"
	Q        int64   `json:"q"`        // quantized input (already sign-extended for vel/rel)
	Scale    float64 `json:"scale"`    // for unVel/unRel; ignored otherwise
	Expected float64 `json:"expected"` // Go-computed dequantized value
}

type FrameCase struct {
	HexBytes      string         `json:"hexBytes"`
	Tick          uint32         `json:"tick"`
	Seq           uint32         `json:"seq"`
	Flags         uint32         `json:"flags"`
	FullCount     uint16         `json:"fullCount"`
	DeltaCount    uint16         `json:"deltaCount"`
	RemovedCount  uint16         `json:"removedCount"`
	ExitedCount   uint16         `json:"exitedCount"`
	Full          []FullEntry    `json:"full"`
	Delta         []DeltaEntry   `json:"delta"`
	RemovedIDs    []uint32       `json:"removedIDs"`
}

type FullEntry struct {
	NetID        uint32 `json:"netID"`
	Epoch        uint32 `json:"epoch"`
	EntityType   uint8  `json:"entityType"`
	ProducedAtMs uint64 `json:"producedAtMs"`
	SnapshotHex  string `json:"snapshotHex"`
	InitialHex   string `json:"initialHex"` // "" when none
}

type DeltaEntry struct {
	NetID        uint32 `json:"netID"`
	Epoch        uint32 `json:"epoch"`
	EntityType   uint8  `json:"entityType"`
	ProducedAtMs uint64 `json:"producedAtMs"`
	DeltaHex     string `json:"deltaHex"`
}

type ApplyCase struct {
	FieldSizes  []int  `json:"fieldSizes"`
	HasVarTail  bool   `json:"hasVarTail"`
	BaselineHex string `json:"baselineHex"`
	DeltaHex    string `json:"deltaHex"`
	ExpectedHex string `json:"expectedHex"`
}

type StringCase struct {
	Kind     string `json:"kind"` // "u8" | "u16"
	HexBytes string `json:"hexBytes"`
	Expected string `json:"expected"`
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func main() {
	m := Manifest{}

	// --- Dequant cases: quantize a known float in Go, record q + expected un* ---
	angQ := quantize.Angle(1.0)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unAngle", Q: int64(angQ), Expected: float64(quantize.UnAngle(angQ))})
	normQ := quantize.Norm(0.6)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unNorm", Q: int64(normQ), Expected: float64(quantize.UnNorm(normQ))})
	velQ := quantize.Vel(12.5, 50.0)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unVel", Q: int64(velQ), Scale: 50.0, Expected: float64(quantize.UnVel(velQ, 50.0))})
	relQ := quantize.Rel(-30.0, 100.0)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unRel", Q: int64(relQ), Scale: 100.0, Expected: float64(quantize.UnRel(relQ, 100.0))})

	// --- A representative frame: header + 1 full + 1 delta + 2 removed ---
	snapshot := []byte{0x01, 0x02, 0x03, 0x04}
	initial := []byte("hi")
	delta := []byte{0xFF, 0xAA, 0xBB} // arbitrary delta payload bytes
	full := FullEntry{NetID: 1001, Epoch: 7, EntityType: 3, ProducedAtMs: 1717000000123,
		SnapshotHex: hex.EncodeToString(snapshot), InitialHex: hex.EncodeToString(initial)}
	dlt := DeltaEntry{NetID: 1002, Epoch: 8, EntityType: 4, ProducedAtMs: 1717000000456,
		DeltaHex: hex.EncodeToString(delta)}
	removed := []uint32{555, 666}

	var fr []byte
	fr = append(fr, be32(42)...)   // tick
	fr = append(fr, be32(7)...)    // seq
	fr = append(fr, be32(1)...)    // flags (FRESH_SNAPSHOT)
	fr = append(fr, be16(1)...)    // fullCount
	fr = append(fr, be16(1)...)    // deltaCount
	fr = append(fr, be16(2)...)    // removedCount
	fr = append(fr, be16(0)...)    // exitedCount
	// full entry
	fr = append(fr, be32(full.NetID)...)
	fr = append(fr, be32(full.Epoch)...)
	fr = append(fr, full.EntityType)
	fr = append(fr, be64(full.ProducedAtMs)...)
	fr = append(fr, be16(uint16(len(snapshot)))...)
	fr = append(fr, snapshot...)
	fr = append(fr, be16(uint16(len(initial)))...)
	fr = append(fr, initial...)
	// delta entry
	fr = append(fr, be32(dlt.NetID)...)
	fr = append(fr, be32(dlt.Epoch)...)
	fr = append(fr, dlt.EntityType)
	fr = append(fr, be64(dlt.ProducedAtMs)...)
	fr = append(fr, be16(uint16(len(delta)))...)
	fr = append(fr, delta...)
	// removed IDs
	for _, id := range removed {
		fr = append(fr, be32(id)...)
	}

	m.Frame = FrameCase{
		HexBytes: hex.EncodeToString(fr),
		Tick: 42, Seq: 7, Flags: 1, FullCount: 1, DeltaCount: 1, RemovedCount: 2, ExitedCount: 0,
		Full: []FullEntry{full}, Delta: []DeltaEntry{dlt}, RemovedIDs: removed,
	}

	// --- applyDelta: 2 fixed fields (2 bytes, 2 bytes), no var tail; change field 1 only ---
	// bitmask 1 byte = 0b00000010 (field index 1 set); delta = [bitmask][field1 new bytes]
	baseline := []byte{0x11, 0x22, 0x33, 0x44}
	deltaPayload := []byte{0x02, 0x99, 0x88} // bit for field 1, new 2 bytes for field 1
	expected := []byte{0x11, 0x22, 0x99, 0x88}
	m.ApplyDelta = append(m.ApplyDelta, ApplyCase{
		FieldSizes: []int{2, 2}, HasVarTail: false,
		BaselineHex: hex.EncodeToString(baseline),
		DeltaHex:    hex.EncodeToString(deltaPayload),
		ExpectedHex: hex.EncodeToString(expected),
	})

	// --- strings ---
	su8 := append([]byte{byte(len("alice"))}, []byte("alice")...)
	m.Strings = append(m.Strings, StringCase{Kind: "u8", HexBytes: hex.EncodeToString(su8), Expected: "alice"})
	su16 := append(be16(uint16(len("bobbb"))), []byte("bobbb")...)
	m.Strings = append(m.Strings, StringCase{Kind: "u16", HexBytes: hex.EncodeToString(su16), Expected: "bobbb"})

	out := filepath.Join("csharp", "Mmokit.Sdk.Core.Tests", "testdata", "delta_golden.json")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	log.Printf("wrote %s (%d bytes)", out, len(data))
}
```

- [ ] **Step 2: Generate the manifest and vet**

Run: `go vet ./cmd/csharp-golden/... && go run ./cmd/csharp-golden`
Expected: vet clean; prints `wrote csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json (...)`. Confirm the file exists: `test -f csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json && echo OK`.

- [ ] **Step 3: Commit**

```bash
git add cmd/csharp-golden/main.go csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json
git commit -m "feat(csharp): Go golden generator + committed delta_golden manifest

cmd/csharp-golden emits authoritative big-endian wire bytes + expected
decoded values (via pkg/quantize) for the C# DeltaDecoderCore golden test.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Port DeltaDecoderCore + golden tests

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/DeltaDecoderCore.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/DeltaDecoderGoldenTests.cs`

- [ ] **Step 1: Create the golden DTO loader** — `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs`:

```csharp
using System;
using System.IO;
using System.Text.Json;

namespace Mmokit.Sdk.Core.Tests
{
    // DTOs mirror cmd/csharp-golden/main.go Manifest. Property names match the
    // JSON (camelCase) via JsonSerializerOptions.PropertyNameCaseInsensitive.
    public class Manifest
    {
        public DequantCase[] Dequant { get; set; } = Array.Empty<DequantCase>();
        public FrameCase Frame { get; set; } = new();
        public ApplyCase[] ApplyDelta { get; set; } = Array.Empty<ApplyCase>();
        public StringCase[] Strings { get; set; } = Array.Empty<StringCase>();
    }

    public class DequantCase { public string Kind { get; set; } = ""; public long Q { get; set; } public double Scale { get; set; } public double Expected { get; set; } }
    public class FrameCase
    {
        public string HexBytes { get; set; } = "";
        public uint Tick { get; set; } public uint Seq { get; set; } public uint Flags { get; set; }
        public ushort FullCount { get; set; } public ushort DeltaCount { get; set; }
        public ushort RemovedCount { get; set; } public ushort ExitedCount { get; set; }
        public FullEntry[] Full { get; set; } = Array.Empty<FullEntry>();
        public DeltaEntry[] Delta { get; set; } = Array.Empty<DeltaEntry>();
        public uint[] RemovedIDs { get; set; } = Array.Empty<uint>();
    }
    public class FullEntry { public uint NetID { get; set; } public uint Epoch { get; set; } public byte EntityType { get; set; } public ulong ProducedAtMs { get; set; } public string SnapshotHex { get; set; } = ""; public string InitialHex { get; set; } = ""; }
    public class DeltaEntry { public uint NetID { get; set; } public uint Epoch { get; set; } public byte EntityType { get; set; } public ulong ProducedAtMs { get; set; } public string DeltaHex { get; set; } = ""; }
    public class ApplyCase { public int[] FieldSizes { get; set; } = Array.Empty<int>(); public bool HasVarTail { get; set; } public string BaselineHex { get; set; } = ""; public string DeltaHex { get; set; } = ""; public string ExpectedHex { get; set; } = ""; }
    public class StringCase { public string Kind { get; set; } = ""; public string HexBytes { get; set; } = ""; public string Expected { get; set; } = ""; }

    public static class Golden
    {
        public static Manifest Load()
        {
            // Copied next to the test assembly via the csproj <None> include.
            string path = Path.Combine(AppContext.BaseDirectory, "testdata", "delta_golden.json");
            string json = File.ReadAllText(path);
            var opts = new JsonSerializerOptions { PropertyNameCaseInsensitive = true };
            return JsonSerializer.Deserialize<Manifest>(json, opts)!;
        }

        public static byte[] Hex(string s)
        {
            if (string.IsNullOrEmpty(s)) return Array.Empty<byte>();
            var b = new byte[s.Length / 2];
            for (int i = 0; i < b.Length; i++)
                b[i] = Convert.ToByte(s.Substring(i * 2, 2), 16);
            return b;
        }
    }
}
```

- [ ] **Step 2: Write the failing golden tests** — `csharp/Mmokit.Sdk.Core.Tests/DeltaDecoderGoldenTests.cs`:

```csharp
using System;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class DeltaDecoderGoldenTests
    {
        readonly Manifest g = Golden.Load();

        [Fact]
        public void Dequantizers_MatchGo()
        {
            foreach (var c in g.Dequant)
            {
                double got = c.Kind switch
                {
                    "unAngle" => DeltaDecoderCore.UnAngle((int)c.Q),
                    "unNorm" => DeltaDecoderCore.UnNorm((int)c.Q),
                    "unVel" => DeltaDecoderCore.UnVel((int)c.Q, c.Scale),
                    "unRel" => DeltaDecoderCore.UnRel((int)c.Q, c.Scale),
                    _ => throw new Xunit.Sdk.XunitException($"unknown kind {c.Kind}"),
                };
                Assert.Equal(c.Expected, got, 4); // 4 decimal places — float32→double slack
            }
        }

        [Fact]
        public void BigEndianReads_RoundTripGoldenFrameHeader()
        {
            byte[] frame = Golden.Hex(g.Frame.HexBytes);
            var (h, off) = DeltaDecoderCore.DecodeFrameHeader(frame, 0);
            Assert.Equal(DeltaDecoderCore.FrameHeaderSize, off);
            Assert.Equal(g.Frame.Tick, h.Tick);
            Assert.Equal(g.Frame.Seq, h.Seq);
            Assert.Equal(g.Frame.Flags, h.Flags);
            Assert.Equal(g.Frame.FullCount, h.FullCount);
            Assert.Equal(g.Frame.DeltaCount, h.DeltaCount);
            Assert.Equal(g.Frame.RemovedCount, h.RemovedCount);
            Assert.Equal(g.Frame.ExitedCount, h.ExitedCount);
        }

        [Fact]
        public void DecodeFullAndDeltaAndRemoved_MatchGo()
        {
            byte[] frame = Golden.Hex(g.Frame.HexBytes);
            var (_, pos) = DeltaDecoderCore.DecodeFrameHeader(frame, 0);

            var (full, p2) = DeltaDecoderCore.DecodeFullEntry(frame, pos);
            var ef = g.Frame.Full[0];
            Assert.Equal(ef.NetID, full.NetID);
            Assert.Equal(ef.Epoch, full.Epoch);
            Assert.Equal(ef.EntityType, full.EntityType);
            Assert.Equal(ef.ProducedAtMs, full.ProducedAtMs);
            Assert.Equal(Golden.Hex(ef.SnapshotHex), full.Snapshot);
            Assert.Equal(Golden.Hex(ef.InitialHex), full.InitialData ?? Array.Empty<byte>());

            var (dlt, p3) = DeltaDecoderCore.DecodeDeltaEntry(frame, p2);
            var ed = g.Frame.Delta[0];
            Assert.Equal(ed.NetID, dlt.NetID);
            Assert.Equal(ed.ProducedAtMs, dlt.ProducedAtMs);
            Assert.Equal(Golden.Hex(ed.DeltaHex), dlt.DeltaData);

            var (ids, _) = DeltaDecoderCore.DecodeRemovedIDs(frame, p3, g.Frame.RemovedCount);
            Assert.Equal(g.Frame.RemovedIDs, ids);
        }

        [Fact]
        public void ApplyDelta_MatchesGo()
        {
            foreach (var c in g.ApplyDelta)
            {
                byte[] got = DeltaDecoderCore.ApplyDelta(c.FieldSizes, c.HasVarTail,
                    Golden.Hex(c.BaselineHex), Golden.Hex(c.DeltaHex));
                Assert.Equal(Golden.Hex(c.ExpectedHex), got);
            }
        }

        [Fact]
        public void Strings_MatchGo()
        {
            foreach (var c in g.Strings)
            {
                byte[] data = Golden.Hex(c.HexBytes);
                string got = c.Kind == "u8"
                    ? DeltaDecoderCore.DecodeLengthPrefixedStringU8(data)
                    : DeltaDecoderCore.DecodeLengthPrefixedStringU16(data);
                Assert.Equal(c.Expected, got);
            }
        }
    }
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd csharp && dotnet test --filter DeltaDecoderGoldenTests 2>&1 | tail -15`
Expected: BUILD FAILS — `DeltaDecoderCore` doesn't exist yet.

- [ ] **Step 4: Create the port** — `csharp/Mmokit.Sdk.Core/DeltaDecoderCore.cs`. Faithful port of `pkg/quantize/ts/delta-decoder-core.ts`; all reads are manual big-endian (portable; matches the TS exactly), float via `BitConverter.Int32BitsToSingle` on the assembled big-endian int:

```csharp
using System;
using System.Collections.Generic;
using System.Text;

namespace Mmokit.Sdk.Core
{
    /// Decoded 20-byte SE_DELTA_WORLD_UPDATE frame header.
    public struct FrameHeader
    {
        public uint Tick;
        public uint Seq;
        public uint Flags;
        public ushort FullCount;
        public ushort DeltaCount;
        public ushort RemovedCount;
        public ushort ExitedCount;
    }

    /// A full entity entry. InitialData is null when the entry carries none.
    public struct FullEntryHeader
    {
        public uint NetID;
        public uint Epoch;
        public byte EntityType;
        public ulong ProducedAtMs;
        public byte[] Snapshot;
        public byte[]? InitialData;
    }

    /// A delta entity entry. DeltaData is the bitmask + changed-field payload.
    public struct DeltaEntryHeader
    {
        public uint NetID;
        public uint Epoch;
        public byte EntityType;
        public ulong ProducedAtMs;
        public byte[] DeltaData;
    }

    /// Per-entity baseline store for delta decoding (latest full snapshot per netID).
    public sealed class BaselineStore<T>
    {
        readonly Dictionary<uint, (byte[] Snapshot, T? Meta)> _store = new();
        public void Set(uint netID, byte[] snapshot, T? meta = default) => _store[netID] = (snapshot, meta);
        public bool TryGet(uint netID, out byte[] snapshot, out T? meta)
        {
            if (_store.TryGetValue(netID, out var v)) { snapshot = v.Snapshot; meta = v.Meta; return true; }
            snapshot = Array.Empty<byte>(); meta = default; return false;
        }
        public void Delete(uint netID) => _store.Remove(netID);
        public void Clear() => _store.Clear();
    }

    /// Reusable building blocks for decoding mmokit binary wire frames.
    /// Faithful port of pkg/quantize/ts/delta-decoder-core.ts. All multi-byte
    /// reads are big-endian. Games layer entity-type-specific decode on top.
    public static class DeltaDecoderCore
    {
        public const int FrameHeaderSize = 20;
        public const uint FrameFlagFreshSnapshot = 1u << 0;

        // --- big-endian primitive reads ---
        public static short ReadInt16(byte[] data, int offset)
        {
            int v = (data[offset] << 8) | data[offset + 1];
            return (short)(v > 32767 ? v - 65536 : v);
        }

        public static ushort ReadUint16(byte[] data, int offset)
            => (ushort)((data[offset] << 8) | data[offset + 1]);

        public static uint ReadUint32(byte[] data, int offset)
            => ((uint)data[offset] << 24) | ((uint)data[offset + 1] << 16)
             | ((uint)data[offset + 2] << 8) | data[offset + 3];

        public static ulong ReadUint64(byte[] data, int offset)
        {
            ulong hi = ReadUint32(data, offset);
            ulong lo = ReadUint32(data, offset + 4);
            return (hi << 32) | lo;
        }

        public static float ReadFloat32(byte[] data, int offset)
            => BitConverter.Int32BitsToSingle((int)ReadUint32(data, offset));

        // --- dequantizers (match pkg/quantize/quantize.go) ---
        public static double UnAngle(int q) => (q / 65535.0) * 2.0 * Math.PI - Math.PI;
        public static double UnNorm(int q) => q / 255.0;
        public static double UnVel(int q, double scale) => (q / 32767.0) * scale;
        public static double UnRel(int q, double halfRange) => (q / 32767.0) * halfRange;

        // --- frame header ---
        public static (FrameHeader Header, int Offset) DecodeFrameHeader(byte[] data, int offset)
        {
            int pos = offset;
            var h = new FrameHeader
            {
                Tick = ReadUint32(data, pos),
                Seq = ReadUint32(data, pos + 4),
                Flags = ReadUint32(data, pos + 8),
                FullCount = ReadUint16(data, pos + 12),
                DeltaCount = ReadUint16(data, pos + 14),
                RemovedCount = ReadUint16(data, pos + 16),
                ExitedCount = ReadUint16(data, pos + 18),
            };
            return (h, pos + 20);
        }

        // --- entries ---
        public static (FullEntryHeader Entry, int Offset) DecodeFullEntry(byte[] data, int pos)
        {
            uint netID = ReadUint32(data, pos); pos += 4;
            uint epoch = ReadUint32(data, pos); pos += 4;
            byte entityType = data[pos]; pos += 1;
            ulong producedAtMs = ReadUint64(data, pos); pos += 8;
            ushort snapLen = ReadUint16(data, pos); pos += 2;
            byte[] snapshot = Slice(data, pos, snapLen); pos += snapLen;
            ushort initLen = ReadUint16(data, pos); pos += 2;
            byte[]? initialData = null;
            if (initLen > 0) { initialData = Slice(data, pos, initLen); pos += initLen; }
            return (new FullEntryHeader { NetID = netID, Epoch = epoch, EntityType = entityType, ProducedAtMs = producedAtMs, Snapshot = snapshot, InitialData = initialData }, pos);
        }

        public static (DeltaEntryHeader Entry, int Offset) DecodeDeltaEntry(byte[] data, int pos)
        {
            uint netID = ReadUint32(data, pos); pos += 4;
            uint epoch = ReadUint32(data, pos); pos += 4;
            byte entityType = data[pos]; pos += 1;
            ulong producedAtMs = ReadUint64(data, pos); pos += 8;
            ushort deltaLen = ReadUint16(data, pos); pos += 2;
            byte[] deltaData = Slice(data, pos, deltaLen); pos += deltaLen;
            return (new DeltaEntryHeader { NetID = netID, Epoch = epoch, EntityType = entityType, ProducedAtMs = producedAtMs, DeltaData = deltaData }, pos);
        }

        public static (uint[] Ids, int Offset) DecodeRemovedIDs(byte[] data, int pos, int count)
        {
            var ids = new uint[count];
            for (int i = 0; i < count; i++) { ids[i] = ReadUint32(data, pos); pos += 4; }
            return (ids, pos);
        }

        // --- generic delta application (mirrors applyDelta in the TS) ---
        public static byte[] ApplyDelta(int[] fieldSizes, bool hasVarTail, byte[] baseline, byte[] delta)
        {
            int totalLogicalFields = fieldSizes.Length + (hasVarTail ? 1 : 0);
            int bitmaskSize = (totalLogicalFields + 7) / 8; // ceil(/8)

            int fixedSize = 0;
            for (int i = 0; i < fieldSizes.Length; i++) fixedSize += fieldSizes[i];

            int dpos = bitmaskSize;
            byte[] result = (byte[])baseline.Clone();

            int baseOff = 0;
            for (int i = 0; i < fieldSizes.Length; i++)
            {
                int bit = (delta[i / 8] >> (i % 8)) & 1;
                if (bit != 0)
                {
                    int sz = fieldSizes[i];
                    Array.Copy(delta, dpos, result, baseOff, sz);
                    dpos += sz;
                }
                baseOff += fieldSizes[i];
            }

            if (hasVarTail)
            {
                int tailIdx = fieldSizes.Length;
                int bit = (delta[tailIdx / 8] >> (tailIdx % 8)) & 1;
                if (bit != 0)
                {
                    int newLen = ReadUint16(delta, dpos); dpos += 2;
                    var rebuilt = new byte[fixedSize + 2 + newLen];
                    Array.Copy(result, 0, rebuilt, 0, fixedSize);
                    rebuilt[fixedSize] = (byte)((newLen >> 8) & 0xFF);
                    rebuilt[fixedSize + 1] = (byte)(newLen & 0xFF);
                    Array.Copy(delta, dpos, rebuilt, fixedSize + 2, newLen);
                    result = rebuilt;
                }
            }

            return result;
        }

        // --- length-prefixed strings (UTF-8) ---
        public static string DecodeLengthPrefixedStringU8(byte[] data)
        {
            if (data.Length < 1) return "";
            int len = data[0];
            if (len == 0 || 1 + len > data.Length) return "";
            return Encoding.UTF8.GetString(data, 1, len);
        }

        public static string DecodeLengthPrefixedStringU16(byte[] data)
        {
            if (data.Length < 2) return "";
            int len = ReadUint16(data, 0);
            if (len == 0 || 2 + len > data.Length) return "";
            return Encoding.UTF8.GetString(data, 2, len);
        }

        static byte[] Slice(byte[] data, int start, int len)
        {
            var b = new byte[len];
            Array.Copy(data, start, b, 0, len);
            return b;
        }
    }
}
```

- [ ] **Step 5: Run the golden tests**

Run: `cd csharp && dotnet test --filter DeltaDecoderGoldenTests 2>&1 | tail -20`
Expected: `Passed!  - Failed: 0, Passed: 5`.

- [ ] **Step 6: Run the full C# suite**

Run: `cd csharp && dotnet test 2>&1 | tail -8`
Expected: all tests pass (smoke + 8 interpolation + 5 golden = 14).

- [ ] **Step 7: Commit**

```bash
git add csharp/Mmokit.Sdk.Core/DeltaDecoderCore.cs csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs csharp/Mmokit.Sdk.Core.Tests/DeltaDecoderGoldenTests.cs
git commit -m "feat(csharp): port delta-decoder-core to DeltaDecoderCore.cs

Faithful big-endian port of pkg/quantize/ts/delta-decoder-core.ts: reads,
dequantizers, frame-header/full/delta/removed decode, applyDelta, baseline
store, string decoders. Verified byte-for-byte against Go-authored goldens
(cross-language drift guard, 5 golden tests).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage (§C, §F.3):** `DeltaDecoderCore.cs` + `InterpolationCore.cs` ports (Tasks 2, 4) with the full public surface from the TS sources; cross-language golden test driven by a Go authoritative generator (Tasks 3, 4 — §F.3 drift guard); `netstandard2.1` library = Unity-consumable source (Task 1); resolves the spec open-item home → `csharp/` workspace. UDP transport explicitly deferred to Plan 4 (scope note). The encryption-seam constraint is N/A here (no transport). ✅
- **Placeholder scan:** Complete code in every step. The one generated artifact (`delta_golden.json`) is produced by the fully-specified Go generator in Task 3, then committed — not a placeholder. The transient `Placeholder.cs` is created in Task 1 and `git rm`'d in Task 2 (explicit). ✅
- **Type/name consistency:** C# golden DTOs (`GoldenModel.cs`) field names match `cmd/csharp-golden/main.go`'s JSON tags (case-insensitive deserialization). `DeltaDecoderCore` method names used in `DeltaDecoderGoldenTests.cs` (`UnAngle/UnNorm/UnVel/UnRel`, `DecodeFrameHeader`, `FrameHeaderSize`, `DecodeFullEntry/DecodeDeltaEntry/DecodeRemovedIDs`, `ApplyDelta`, `DecodeLengthPrefixedStringU8/U16`) exactly match the signatures defined in `DeltaDecoderCore.cs`. `InterpolationCore` members (`Lerp/LerpAngle/PushSample/IsStaleSample/InterpolateRing`, `Sample`, `InterpolationResult`) match between port and tests. Dequantizer formulas match `pkg/quantize/quantize.go`. ✅
- **TDD order:** Each port task writes failing tests first (Steps 1-3), then implements (Step 4), then greens (Step 5). ✅

## Open items (resolve during planning, not blocking)

- xUnit / Test.Sdk package versions pinned above are current-as-of-writing; if `dotnet restore` reports a newer required version for net10.0, the implementer may bump them (note it in the commit) — the test code is version-agnostic.
