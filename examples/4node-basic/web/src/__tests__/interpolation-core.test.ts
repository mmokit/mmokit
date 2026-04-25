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
