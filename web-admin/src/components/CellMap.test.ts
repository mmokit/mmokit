import { describe, it, expect } from "vitest";
import { layoutCells } from "./cellmap-layout";

describe("layoutCells", () => {
  it("places depth-0 cells in a grid", () => {
    const cells = [
      { id: "0_0", depth: 0 },
      { id: "1_0", depth: 0 },
      { id: "0_1", depth: 0 },
      { id: "1_1", depth: 0 },
    ];
    const out = layoutCells(cells, { width: 400, height: 400, padding: 0 });
    expect(out.length).toBe(4);
    const sizes = new Set(out.map((c) => Math.round(c.w)));
    expect(sizes.size).toBe(1);
  });

  it("nests split children inside their parent rect", () => {
    const cells = [
      { id: "0_0", depth: 0 },
      { id: "0_0:1", depth: 1, parent: "0_0" },
      { id: "0_0:2", depth: 1, parent: "0_0" },
      { id: "0_0:3", depth: 1, parent: "0_0" },
      { id: "0_0:4", depth: 1, parent: "0_0" },
    ];
    const out = layoutCells(cells, { width: 200, height: 200, padding: 0 });
    const parent = out.find((c) => c.id === "0_0")!;
    const children = out.filter((c) => c.id.startsWith("0_0:"));
    for (const ch of children) {
      expect(ch.x).toBeGreaterThanOrEqual(parent.x);
      expect(ch.y).toBeGreaterThanOrEqual(parent.y);
      expect(ch.x + ch.w).toBeLessThanOrEqual(parent.x + parent.w + 0.01);
      expect(ch.y + ch.h).toBeLessThanOrEqual(parent.y + parent.h + 0.01);
    }
  });
});
