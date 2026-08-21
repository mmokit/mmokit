import { test, expect } from "bun:test";
import { cellAt, cellKey, worldBounds, classify, type Topology, type CellRect } from "../topology";

function cell(cellX: number, cellY: number, size = 1000, depth = 0): CellRect {
  return { cellX, cellY, depth, size, originX: cellX * size, originY: cellY * size, nodeID: "n" };
}

// A 2x2 grid of 1000-unit cells: the world is 0..2000, centred on (1000,1000).
const TOPO: Topology = {
  cells: [cell(0, 0), cell(1, 0), cell(0, 1), cell(1, 1)],
  baseCellSize: 1000,
};

// The grid used to be a 4000-unit helper centred on the ORIGIN, so the world
// sat in one quadrant of it and the other three implied world that was not
// there. These bounds are what the grid is drawn from now.
test("world bounds come from the cells, not from the origin", () => {
  const b = worldBounds(TOPO);
  expect(b.minX).toBe(0);
  expect(b.maxX).toBe(2000);
  expect(b.width).toBe(2000);
  expect(b.centerX).toBe(1000);
  expect(b.centerY).toBe(1000);
});

test("bounds survive a split, where cells are smaller and more numerous", () => {
  // cell_0_0 split into four 500-unit children; the world is unchanged.
  const split: Topology = {
    cells: [
      { cellX: 0, cellY: 0, depth: 1, size: 500, originX: 0, originY: 0, nodeID: "n" },
      { cellX: 1, cellY: 0, depth: 1, size: 500, originX: 500, originY: 0, nodeID: "n" },
      { cellX: 0, cellY: 1, depth: 1, size: 500, originX: 0, originY: 500, nodeID: "n" },
      { cellX: 1, cellY: 1, depth: 1, size: 500, originX: 500, originY: 500, nodeID: "n" },
      cell(1, 0), cell(0, 1), cell(1, 1),
    ],
    baseCellSize: 1000,
  };
  const b = worldBounds(split);
  expect(b.width).toBe(2000);
  expect(b.height).toBe(2000);
});

test("empty topology yields zero bounds rather than Infinity", () => {
  const b = worldBounds({ cells: [], baseCellSize: 0 });
  expect(Number.isFinite(b.width)).toBe(true);
  expect(b.width).toBe(0);
});

test("cellAt is half-open, so a point on a boundary belongs to exactly one cell", () => {
  expect(cellAt(TOPO, 999, 999)!.cellX).toBe(0);
  // x=1000 is the start of cell 1, not the end of cell 0.
  expect(cellAt(TOPO, 1000, 999)!.cellX).toBe(1);
  expect(cellAt(TOPO, 1999, 1999)!.cellY).toBe(1);
  expect(cellAt(TOPO, 2000, 0)).toBeNull();
  expect(cellAt(TOPO, -1, 0)).toBeNull();
});

test("cells at different depths are distinct keys", () => {
  const parent = cell(0, 0);
  const child = { ...cell(0, 0, 500), depth: 1 };
  expect(cellKey(parent)).not.toBe(cellKey(child));
});

test("classify separates self, own cell, and another cell", () => {
  const viewerCell = cellAt(TOPO, 500, 500)!;
  expect(classify(TOPO, viewerCell, 1, 500, 500, 1)).toBe("self");
  expect(classify(TOPO, viewerCell, 2, 600, 400, 1)).toBe("local");
  // Same world, different cell — the client-visible shadow of a replica.
  expect(classify(TOPO, viewerCell, 3, 1500, 500, 1)).toBe("remote");
});

// Before DebugInfo arrives there is no topology, and guessing would colour
// every entity wrongly for the first second.
test("classify degrades to unknown without topology", () => {
  expect(classify(null, null, 2, 0, 0, 1)).toBe("unknown");
  expect(classify(TOPO, null, 2, 0, 0, 1)).toBe("unknown");
  expect(classify(TOPO, cellAt(TOPO, 500, 500), 2, 99999, 99999, 1)).toBe("unknown");
});

test("self wins even before topology arrives", () => {
  expect(classify(null, null, 7, 0, 0, 7)).toBe("self");
});
