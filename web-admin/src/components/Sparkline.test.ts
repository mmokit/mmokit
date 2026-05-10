import { describe, it, expect } from "vitest";
import { scaleSeries } from "./Sparkline.helpers";

describe("scaleSeries", () => {
  it("maps min→0 and max→height when no clamps", () => {
    const out = scaleSeries([10, 20, 30], 100, 50);
    // x positions: 0, 50, 100 (3 points across width 100)
    expect(out.map((p) => p.x)).toEqual([0, 50, 100]);
    // y positions: 50 (min, drawn at canvas bottom), 25, 0 (max, top)
    expect(out[0].y).toBeCloseTo(50, 1);
    expect(out[2].y).toBeCloseTo(0, 1);
  });

  it("returns flat midline when all values equal", () => {
    const out = scaleSeries([5, 5, 5], 100, 50);
    for (const p of out) expect(p.y).toBeCloseTo(25, 1);
  });

  it("applies min/max clamps", () => {
    const out = scaleSeries([0, 50, 100], 100, 50, { min: 0, max: 200 });
    // top of the series is at value 100, clamped scale 0..200, so
    // 100/200 = 0.5 of canvas → y = 25
    expect(out[2].y).toBeCloseTo(25, 1);
  });

  it("returns empty for empty input", () => {
    expect(scaleSeries([], 100, 50)).toEqual([]);
  });
});
