import { describe, it, expect } from "vitest";
import { layoutCells, parseXY } from "./cellmap-layout";

describe("parseXY (compat shim over parseCellID)", () => {
  it("parses depth-0 mesh IDs", () => {
    expect(parseXY("cell_0_0")).toEqual({ x: 0, y: 0 });
    expect(parseXY("cell_3_2")).toEqual({ x: 3, y: 2 });
  });
  it("parses display-form depth-0 IDs", () => {
    expect(parseXY("0_0")).toEqual({ x: 0, y: 0 });
    expect(parseXY("3_2")).toEqual({ x: 3, y: 2 });
  });
  it("parses depth-N mesh IDs", () => {
    expect(parseXY("cell_d1_0_0")).toEqual({ x: 0, y: 0 });
    expect(parseXY("cell_d2_3_2")).toEqual({ x: 3, y: 2 });
  });
  it("returns null on garbage", () => {
    expect(parseXY("not-a-cell")).toBeNull();
  });
});

describe("layoutCells", () => {
  it("places depth-0 cells in a grid", () => {
    const cells = [
      { id: "cell_0_0", depth: 0 },
      { id: "cell_1_0", depth: 0 },
      { id: "cell_0_1", depth: 0 },
      { id: "cell_1_1", depth: 0 },
    ];
    const out = layoutCells(cells, { width: 400, height: 400, padding: 0 });
    expect(out.length).toBe(4);
    const sizes = new Set(out.map((c) => Math.round(c.w)));
    expect(sizes.size).toBe(1);
  });

  it("nests cell_dN split children inside the synthesized parent rect", () => {
    // Real backend post-split state: original cell_0_0 is replaced by its
    // 4 children; the OTHER 3 base cells are still present at depth 0.
    const cells = [
      { id: "cell_d1_0_0", depth: 1, parent: "cell_0_0" },
      { id: "cell_d1_1_0", depth: 1, parent: "cell_0_0" },
      { id: "cell_d1_0_1", depth: 1, parent: "cell_0_0" },
      { id: "cell_d1_1_1", depth: 1, parent: "cell_0_0" },
      { id: "cell_1_0", depth: 0 },
      { id: "cell_0_1", depth: 0 },
      { id: "cell_1_1", depth: 0 },
    ];
    const out = layoutCells(cells, { width: 200, height: 200, padding: 0 });

    // All 7 input cells should be in the output.
    expect(out.length).toBe(7);

    // The 4 children should pack into the (0,0) quadrant of the 2x2 grid:
    // each child has half the width/height of a base cell, and together
    // their bounding box exactly covers (0,0)..(100,100).
    const children = out.filter((c) => c.id.startsWith("cell_d1_"));
    const minX = Math.min(...children.map((c) => c.x));
    const maxX = Math.max(...children.map((c) => c.x + c.w));
    const minY = Math.min(...children.map((c) => c.y));
    const maxY = Math.max(...children.map((c) => c.y + c.h));
    expect(minX).toBeCloseTo(0, 1);
    expect(maxX).toBeCloseTo(100, 1);
    expect(minY).toBeCloseTo(0, 1);
    expect(maxY).toBeCloseTo(100, 1);

    // The 3 untouched base cells should occupy their original quadrants.
    const c10 = out.find((c) => c.id === "cell_1_0")!;
    expect(c10.x).toBeCloseTo(100, 1);
    expect(c10.y).toBeCloseTo(0, 1);
    expect(c10.w).toBeCloseTo(100, 1);
  });

  it("synthesizes parent when post-split parent is missing from input", () => {
    // Same as above but without ANY cell_0_0 anchor — synthesizeAncestor
    // must materialize the depth-0 parent from grid math so children dock.
    const cells = [
      { id: "cell_d1_0_0", depth: 1 },
      { id: "cell_d1_1_0", depth: 1 },
      { id: "cell_d1_0_1", depth: 1 },
      { id: "cell_d1_1_1", depth: 1 },
      { id: "cell_1_0", depth: 0 },
      { id: "cell_0_1", depth: 0 },
      { id: "cell_1_1", depth: 0 },
    ];
    const out = layoutCells(cells, { width: 200, height: 200, padding: 0 });
    const children = out.filter((c) => c.id.startsWith("cell_d1_"));
    expect(children.length).toBe(4);
    const minX = Math.min(...children.map((c) => c.x));
    const minY = Math.min(...children.map((c) => c.y));
    expect(minX).toBeCloseTo(0, 1);
    expect(minY).toBeCloseTo(0, 1);
  });
});
