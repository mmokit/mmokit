import { describe, it, expect } from "vitest";
import { layoutBars } from "./BarChart.helpers";

describe("layoutBars", () => {
  it("scales widths to the largest value", () => {
    const out = layoutBars([10, 20, 40], 100);
    expect(out.map((b) => Math.round(b.width))).toEqual([25, 50, 100]);
  });

  it("returns zero widths when all values are zero", () => {
    const out = layoutBars([0, 0, 0], 100);
    for (const b of out) expect(b.width).toBe(0);
  });

  it("handles empty input", () => {
    expect(layoutBars([], 100)).toEqual([]);
  });
});
