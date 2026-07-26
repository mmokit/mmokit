import { describe, test, expect } from "bun:test";
import { DEFAULT_RING_SIZE, InterpolationBuffer } from "./interpolation-buffer";
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
    const result = b.sampleAt(1050)!;
    expect(result.renderX).toBeCloseTo(50, 6);
    expect(result.mode).toBe("interpolate");
    expect(result.extrapolatedMs).toBe(0);
  });

  test("ring evicts beyond size", () => {
    const b = new InterpolationBuffer({ ringSize: 2 });
    b.push(S(1, 1000));
    b.push(S(2, 1100));
    b.push(S(3, 1200)); // evicts 1000
    expect(b.sampleAt(900)!.renderX).toBeCloseTo(2, 6);
  });

  test("defaults to an eight-sample jitter ring", () => {
    const b = new InterpolationBuffer();
    expect(b.ringSize).toBe(DEFAULT_RING_SIZE);
    for (let i = 0; i < DEFAULT_RING_SIZE + 1; i++) b.push(S(i, 1000 + i));
    expect(b.samples).toHaveLength(DEFAULT_RING_SIZE);
    expect(b.samples[0].worldX).toBe(1);
  });

  test("accepts a dynamic effective render delay", () => {
    const b = new InterpolationBuffer({ renderDelayMs: 100 });
    b.push(S(0, 1000));
    b.push(S(100, 1200));

    expect(b.sampleAt(1050)!.mode).toBe("hold");
    const widened = b.sampleAt(1050, 250)!;
    expect(widened.mode).toBe("interpolate");
    expect(widened.renderX).toBeCloseTo(25, 6);
  });

  test("reports extrapolate until its cap, then hold", () => {
    const b = new InterpolationBuffer({ maxExtrapolateMs: 50 });
    b.push({ ...S(0, 1000), velX: 20 });
    b.push({ ...S(10, 1100), velX: 20 });

    const projected = b.sampleAt(1125)!;
    expect(projected.mode).toBe("extrapolate");
    expect(projected.extrapolatedMs).toBe(25);
    expect(projected.renderX).toBeCloseTo(10.5, 6);

    const capped = b.sampleAt(1250)!;
    expect(capped.mode).toBe("hold");
    expect(capped.extrapolatedMs).toBe(50);
    expect(capped.renderX).toBeCloseTo(11, 6);
  });

  test("rejects older epochs and resets a newer backwards timeline", () => {
    const b = new InterpolationBuffer();
    expect(b.push({ ...S(10, 1000), authorityEpoch: 2 }).accepted).toBe(true);

    const old = b.push({ ...S(99, 1100), authorityEpoch: 1 });
    expect(old).toEqual({ accepted: false, reset: false, reason: "older-epoch" });
    expect(b.newest()!.worldX).toBe(10);
    expect(b.isStale(1200, 1)).toBe(true);

    const next = b.push({ ...S(20, 900), authorityEpoch: 3 });
    expect(next).toEqual({ accepted: true, reset: true });
    expect(b.samples).toHaveLength(1);
    expect(b.newest()!.worldX).toBe(20);
  });

  test("keeps a seamless monotonic ring across a newer epoch", () => {
    const b = new InterpolationBuffer();
    b.push({ ...S(0, 1000), authorityEpoch: 7 });
    const result = b.push({ ...S(10, 1050), authorityEpoch: 8 });
    expect(result).toEqual({ accepted: true, reset: false });
    expect(b.samples).toHaveLength(2);
    expect(b.sampleAt(1025)!.renderX).toBeCloseTo(5, 6);
  });

  test("orders authority epochs across uint32 wrap", () => {
    const b = new InterpolationBuffer();
    b.push({ ...S(1, 1000), authorityEpoch: 0xffffffff });

    const wrapped = b.push({ ...S(2, 900), authorityEpoch: 0 });
    expect(wrapped).toEqual({ accepted: true, reset: true });
    expect(b.authorityEpoch).toBe(0);

    const lateOld = b.push({ ...S(99, 1100), authorityEpoch: 0xffffffff });
    expect(lateOld.reason).toBe("older-epoch");
    expect(b.newest()!.worldX).toBe(2);
  });

  test("reset forgets samples and authority history", () => {
    const b = new InterpolationBuffer();
    b.push({ ...S(1, 1000), authorityEpoch: 9 });

    b.reset();

    expect(b.samples).toHaveLength(0);
    expect(b.authorityEpoch).toBeUndefined();
    expect(b.push({ ...S(2, 900), authorityEpoch: 7 }).accepted).toBe(true);
    expect(b.newest()!.worldX).toBe(2);
  });
});
