import { describe, it, expect } from "vitest";
import { parseCellID, meshID, displayID, toDisplay, parentID } from "./cellid";

describe("parseCellID", () => {
  it("parses mesh form at depth 0", () => {
    expect(parseCellID("cell_0_0")).toEqual({ x: 0, y: 0, depth: 0 });
    expect(parseCellID("cell_3_2")).toEqual({ x: 3, y: 2, depth: 0 });
    expect(parseCellID("cell_-1_5")).toEqual({ x: -1, y: 5, depth: 0 });
  });

  it("parses mesh form at depth > 0", () => {
    expect(parseCellID("cell_d1_0_0")).toEqual({ x: 0, y: 0, depth: 1 });
    expect(parseCellID("cell_d2_3_2")).toEqual({ x: 3, y: 2, depth: 2 });
    expect(parseCellID("cell_d3_-4_7")).toEqual({ x: -4, y: 7, depth: 3 });
  });

  it("parses display form at any depth", () => {
    expect(parseCellID("0_0")).toEqual({ x: 0, y: 0, depth: 0 });
    expect(parseCellID("d1_0_0")).toEqual({ x: 0, y: 0, depth: 1 });
    expect(parseCellID("d2_3_2")).toEqual({ x: 3, y: 2, depth: 2 });
  });

  it("strips obsolete :N quadrant suffix", () => {
    // No live producer writes this format, but we still parse it so any
    // cached old DTO doesn't break the UI.
    expect(parseCellID("cell_0_0:1")).toEqual({ x: 0, y: 0, depth: 0 });
    expect(parseCellID("0_0:3")).toEqual({ x: 0, y: 0, depth: 0 });
  });

  it("returns null on garbage", () => {
    expect(parseCellID("")).toBeNull();
    expect(parseCellID("not a cell")).toBeNull();
    expect(parseCellID("cell_foo_bar")).toBeNull();
    expect(parseCellID("d_0_0")).toBeNull();
  });
});

describe("meshID", () => {
  it("formats depth 0 without dN_", () => {
    expect(meshID({ x: 0, y: 0, depth: 0 })).toBe("cell_0_0");
    expect(meshID({ x: 3, y: 2, depth: 0 })).toBe("cell_3_2");
  });

  it("formats depth > 0 with dN_", () => {
    expect(meshID({ x: 0, y: 0, depth: 1 })).toBe("cell_d1_0_0");
    expect(meshID({ x: 3, y: 2, depth: 2 })).toBe("cell_d2_3_2");
  });

  it("round-trips with parseCellID", () => {
    const samples = [
      { x: 0, y: 0, depth: 0 },
      { x: 5, y: -3, depth: 0 },
      { x: 1, y: 1, depth: 1 },
      { x: 7, y: 4, depth: 3 },
    ];
    for (const c of samples) {
      expect(parseCellID(meshID(c))).toEqual(c);
    }
  });
});

describe("displayID + toDisplay", () => {
  it("formats depth 0 without prefix", () => {
    expect(displayID({ x: 0, y: 0, depth: 0 })).toBe("0_0");
    expect(displayID({ x: 3, y: 2, depth: 0 })).toBe("3_2");
  });

  it("formats depth > 0 with dN_", () => {
    expect(displayID({ x: 0, y: 0, depth: 1 })).toBe("d1_0_0");
  });

  it("toDisplay strips cell_ from mesh form", () => {
    expect(toDisplay("cell_0_0")).toBe("0_0");
    expect(toDisplay("cell_d1_3_2")).toBe("d1_3_2");
  });

  it("toDisplay passes unparseable input through", () => {
    expect(toDisplay("garbage")).toBe("garbage");
  });
});

describe("parentID", () => {
  it("returns null at depth 0", () => {
    expect(parentID({ x: 0, y: 0, depth: 0 })).toBeNull();
  });

  it("computes parent at depth 1 (parent is depth 0)", () => {
    expect(parentID({ x: 0, y: 0, depth: 1 })).toBe("cell_0_0");
    expect(parentID({ x: 1, y: 0, depth: 1 })).toBe("cell_0_0");
    expect(parentID({ x: 1, y: 1, depth: 1 })).toBe("cell_0_0");
    expect(parentID({ x: 2, y: 2, depth: 1 })).toBe("cell_1_1");
  });

  it("computes parent at depth 2 (parent is depth 1)", () => {
    expect(parentID({ x: 0, y: 0, depth: 2 })).toBe("cell_d1_0_0");
    expect(parentID({ x: 3, y: 3, depth: 2 })).toBe("cell_d1_1_1");
  });
});
