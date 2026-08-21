import { test, expect } from "bun:test";
import { unQuat, QUAT_WIRE_SIZE, type Quat } from "./delta-decoder-core";
import { slerpQuat, SLERP_DOT_THRESHOLD } from "./interpolation-core";

const golden = require("../../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json");

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.substr(i * 2, 2), 16);
  return out;
}

// Compare on EXACT float32 bit patterns, matching the Go and C# suites. A
// tolerance here would hide precisely the rounding disagreement between three
// independent implementations that this corpus exists to catch.
function bitsOf(v: number): number {
  const buf = new ArrayBuffer(4);
  new Float32Array(buf)[0] = v;
  return new Uint32Array(buf)[0];
}

test("unQuat reproduces every golden vector bit-exactly", () => {
  expect(golden.quat.length).toBeGreaterThan(100);
  for (const c of golden.quat) {
    const bytes = hexToBytes(c.hex);
    expect(bytes.length).toBe(QUAT_WIRE_SIZE);
    const q = unQuat(bytes, 0);
    const got = [bitsOf(q.x), bitsOf(q.y), bitsOf(q.z), bitsOf(q.w)];
    expect({ name: c.name, bits: got }).toEqual({ name: c.name, bits: c.bits });
  }
});

test("unQuat decodes at a non-zero offset", () => {
  const c = golden.quat[0];
  const padded = new Uint8Array(QUAT_WIRE_SIZE + 5);
  padded.set(hexToBytes(c.hex), 3);
  const q = unQuat(padded, 3);
  expect([bitsOf(q.x), bitsOf(q.y), bitsOf(q.z), bitsOf(q.w)]).toEqual(c.bits);
});

test("slerpQuat reproduces every golden case", () => {
  expect(golden.slerp.length).toBeGreaterThan(50);
  for (const c of golden.slerp) {
    const a: Quat = { x: c.a[0], y: c.a[1], z: c.a[2], w: c.a[3] };
    const b: Quat = { x: c.b[0], y: c.b[1], z: c.b[2], w: c.b[3] };
    const out = slerpQuat(a, b, c.t);
    for (const [i, got] of [out.x, out.y, out.z, out.w].entries()) {
      expect(Math.abs(got - c.out[i])).toBeLessThan(1e-6);
    }
  }
});

// The threshold is shared with the Go reference rather than restated, because
// it is the single most port-divergent line in the orientation path.
test("SLERP_DOT_THRESHOLD matches the Go reference", () => {
  expect(SLERP_DOT_THRESHOLD).toBe(0.9995);
});

// --- 3D interpolation -------------------------------------------------------

import { interpolateRing, type Sample } from "./interpolation-core";

function sample(t: number, x: number, z: number, q: Quat): Sample {
  return { worldX: x, worldY: 0, velX: 0, velY: 0, velZ: 0, rotation: 0, worldZ: z, quat: q, producedAtMs: t };
}

test("interpolateRing slerps orientation and lerps Z when samples carry them", () => {
  const a = { x: 0, y: 0, z: 0, w: 1 };
  const b = { x: 0, y: 0, z: Math.SQRT1_2, w: Math.SQRT1_2 };
  const ring = { samples: [sample(0, 0, 10, a), sample(100, 100, 20, b)] };

  // renderDelayMs must be >= the sample gap, or effS0Stamp clamps forward to
  // s1 and the ring holds instead of interpolating.
  const r = interpolateRing(ring, 50, 0, 100)!;
  expect(r.mode).toBe("interpolate");
  expect(r.renderZ).toBeCloseTo(15, 6);
  // Halfway between identity and 90-about-Z is 45-about-Z.
  const half = slerpQuat(a, b, 0.5);
  expect(r.renderQuat!.z).toBeCloseTo(half.z, 6);
  expect(r.renderQuat!.w).toBeCloseTo(half.w, 6);
});

// A 2D client's result must be untouched by the 3D fields existing: absent
// inputs produce absent outputs, not zeros a renderer would apply.
test("interpolateRing omits 3D fields entirely for 2D samples", () => {
  const twoD: Sample = { worldX: 0, worldY: 0, velX: 0, velY: 0, rotation: 0, producedAtMs: 0 };
  const twoD2: Sample = { worldX: 10, worldY: 0, velX: 0, velY: 0, rotation: 1, producedAtMs: 100 };
  const r = interpolateRing({ samples: [twoD, twoD2] }, 50, 0, 100)!;
  expect("renderZ" in r).toBe(false);
  expect("renderQuat" in r).toBe(false);
});
