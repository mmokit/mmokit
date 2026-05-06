import { describe, test, expect } from "bun:test";
import { presenceOf, type Topology } from "../debug-presence";

// 2x2 grid of 1000-unit cells. Two of them share host-a (single-host
// mode would have all four sharing one host); the cell-based derive
// works either way because it compares the entity's cell to the
// viewer's cell, not nodeIds.
const sampleTopology: Topology = {
  gridW: 2, gridH: 2, baseCellSize: 1000,
  cells: [
    { cellX: 0, cellY: 0, depth: 0, size: 1000, originX: 0, originY: 0, nodeID: "host-a" },
    { cellX: 1, cellY: 0, depth: 0, size: 1000, originX: 1000, originY: 0, nodeID: "host-b" },
    { cellX: 0, cellY: 1, depth: 0, size: 1000, originX: 0, originY: 1000, nodeID: "host-a" },
    { cellX: 1, cellY: 1, depth: 0, size: 1000, originX: 1000, originY: 1000, nodeID: "host-b" },
  ],
};

const cell00 = { cellX: 0, cellY: 0, depth: 0 };

describe("presenceOf", () => {
  test("returns LOCAL when entity is in viewer's cell", () => {
    expect(presenceOf({ worldX: 500, worldY: 500 }, sampleTopology, cell00))
      .toBe("LOCAL");
  });

  test("returns REPLICA when entity is in a different cell (different host)", () => {
    expect(presenceOf({ worldX: 1500, worldY: 500 }, sampleTopology, cell00))
      .toBe("REPLICA");
  });

  test("returns REPLICA when entity is in a different cell on the SAME host (single-host mode)", () => {
    // Cell (0,1) shares host-a with viewer's (0,0) — but it's a
    // different cell, so the entity is a replica from the viewer's
    // perspective. This is the case that breaks if presenceOf
    // compared nodeIds instead of cell coords.
    expect(presenceOf({ worldX: 500, worldY: 1500 }, sampleTopology, cell00))
      .toBe("REPLICA");
  });

  test("returns LOCAL fallback when topology is empty", () => {
    expect(presenceOf({ worldX: 500, worldY: 500 }, { ...sampleTopology, cells: [] }, cell00))
      .toBe("LOCAL");
  });

  test("returns LOCAL fallback when myCell is null", () => {
    expect(presenceOf({ worldX: 500, worldY: 500 }, sampleTopology, null))
      .toBe("LOCAL");
  });

  test("returns REPLICA for entity in the diagonally-opposite cell", () => {
    expect(presenceOf({ worldX: 1500, worldY: 1500 }, sampleTopology, cell00))
      .toBe("REPLICA");
  });
});
