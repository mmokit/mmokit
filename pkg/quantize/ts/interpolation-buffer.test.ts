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
    // renderDelayMs MUST be >= the inter-sample gap (100ms here): interpolateRing
    // computes effS0Stamp = max(s0.t, s1.t - renderDelayMs); with renderDelayMs=0
    // it would clamp renderTime 1050 to s0 (x=0) instead of lerping to the midpoint.
    const b = new InterpolationBuffer({ renderDelayMs: 100 });
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
