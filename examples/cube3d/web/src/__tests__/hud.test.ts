import { test, expect } from "bun:test";
import {
  FpsMeter, countByClass, formatCell, formatPos, fmt, hudRows, cellLegend,
  type HudInput,
} from "../hud";
import type { CellRect, EntityClass, Topology } from "../topology";

function cell(cellX: number, cellY: number, nodeID = "host-a", depth = 0, size = 1000): CellRect {
  return { cellX, cellY, depth, size, originX: cellX * size, originY: cellY * size, nodeID };
}

const TOPO: Topology = {
  cells: [cell(1, 1), cell(0, 0), cell(1, 0), cell(0, 1)],
  baseCellSize: 1000,
};

function input(over: Partial<HudInput> = {}): HudInput {
  return {
    connected: true,
    fps: 60,
    seq: 42,
    delayMs: 100,
    jitterMs: 3,
    lossRate: 0,
    myNetID: 7,
    myCell: cell(0, 0),
    pos: { x: 100, y: 200, z: 300 },
    counts: { self: 1, local: 5, remote: 3, unknown: 0 },
    topology: TOPO,
    aoiRadius: 1500,
    ...over,
  };
}

function rowValue(rows: { label: string; value: string }[], label: string): string {
  const row = rows.find((r) => r.label === label);
  if (!row) throw new Error(`no row ${label} in ${rows.map((r) => r.label).join(", ")}`);
  return row.value;
}

// --- FpsMeter --------------------------------------------------------------

// One timestamp is not a measurement: there is no interval to divide by, and
// reporting a rate from it makes the first frame of a session read as a stall.
test("fps reports nothing until it has two frames", () => {
  const m = new FpsMeter();
  expect(m.sample(0)).toBe(0);
});

test("fps measures the interval between frames, not the frame count", () => {
  const m = new FpsMeter();
  // 61 frames spanning exactly 1000 ms is 60 intervals of 16.67 ms = 60 fps.
  // Counting frames instead of intervals gives 61 — the classic off-by-one
  // that makes a HUD read consistently high.
  let rate = 0;
  for (let i = 0; i <= 60; i++) rate = m.sample((i * 1000) / 60);
  expect(rate).toBeCloseTo(60, 5);
});

test("fps forgets frames older than its window", () => {
  const m = new FpsMeter(1000);
  for (let i = 0; i < 30; i++) m.sample(i * 10); // a 100 fps burst
  // A long stall, then a steady 10 fps: the burst must not still be counted.
  let rate = 0;
  for (let i = 0; i < 12; i++) rate = m.sample(5000 + i * 100);
  expect(rate).toBeCloseTo(10, 1);
});

// --- counts ----------------------------------------------------------------

test("countByClass zero-fills every class", () => {
  const c = countByClass(["local", "local", "remote"] as EntityClass[]);
  expect(c).toEqual({ self: 0, local: 2, remote: 1, unknown: 0 });
});

// --- formatting ------------------------------------------------------------

test("cell label matches the console's own cell syntax", () => {
  expect(formatCell(cell(0, 0, ""))).toBe("0,0");
  expect(formatCell(cell(1, 0, "host-a"))).toBe("1,0 @host-a");
  expect(formatCell(cell(1, 0, "host-a", 2))).toBe("d2:1,0 @host-a");
  expect(formatCell(null)).toBe("—");
});

test("missing values read as an em dash, never as zero", () => {
  expect(formatPos(null)).toBe("—");
  expect(fmt(NaN)).toBe("—");
  expect(fmt(Infinity)).toBe("—");
  expect(formatPos({ x: 1.6, y: -2.4, z: 0 })).toBe("2 -2 0");
});

// --- rows ------------------------------------------------------------------

// The panel must not change height as values arrive: a row that appears when
// its data does moves every row under it, which is exactly the row you were
// reading.
test("the row set is stable whether or not data has arrived", () => {
  const full = hudRows(input()).map((r) => r.label);
  const empty = hudRows(input({
    seq: null, myNetID: null, myCell: null, pos: null, topology: null, aoiRadius: 0,
    counts: { self: 0, local: 0, remote: 0, unknown: 0 },
  })).map((r) => r.label);
  expect(empty).toEqual(full);
});

test("entity total counts every class", () => {
  const rows = hudRows(input());
  expect(rowValue(rows, "ENTITIES")).toBe("9");
  expect(rowValue(rows, "  local")).toBe("5");
  expect(rowValue(rows, "  remote")).toBe("3");
});

test("a missing topology grant says so rather than reporting zero cells", () => {
  const rows = hudRows(input({ topology: null }));
  expect(rowValue(rows, "CELLS")).toContain("no topology grant");
});

test("disconnected is a warning, connected is not", () => {
  expect(hudRows(input({ connected: false }))[0].tone).toBe("warn");
  expect(hudRows(input({ connected: true }))[0].tone).toBeUndefined();
});

test("heavy frame loss is flagged, ordinary loss is not", () => {
  const warn = (r: number) => hudRows(input({ lossRate: r })).find((x) => x.label === "LOSS")?.tone;
  expect(warn(0.02)).toBeUndefined();
  expect(warn(0.4)).toBe("warn");
});

// Before DebugInfo lands every entity is legitimately unplaceable. After it
// lands, an unplaced entity means a position outside every cell the server
// admitted to owning — which is worth a colour.
test("unplaced entities are only a warning once the topology is known", () => {
  const un = { self: 0, local: 0, remote: 0, unknown: 4 };
  const before = hudRows(input({ topology: null, counts: un })).find((r) => r.label === "  unplaced");
  const after = hudRows(input({ counts: un })).find((r) => r.label === "  unplaced");
  expect(before?.tone).toBeUndefined();
  expect(after?.tone).toBe("warn");
});

test("world extent comes from the cells", () => {
  expect(rowValue(hudRows(input()), "WORLD")).toBe("2000 × 2000");
});

// --- cell legend -----------------------------------------------------------

// The topology push does not promise an order, and a legend that re-sorts
// itself between pushes flickers.
test("the cell legend is sorted deterministically, whatever order cells arrive in", () => {
  const rows = cellLegend(TOPO, null);
  expect(rows.map((r) => r.value)).toEqual([
    "0,0 @host-a", "1,0 @host-a", "0,1 @host-a", "1,1 @host-a",
  ]);
});

test("the legend marks the viewer's own cell", () => {
  const rows = cellLegend(TOPO, cell(1, 0));
  expect(rows.filter((r) => r.label === "▸").map((r) => r.value)).toEqual(["1,0 @host-a"]);
});

test("the legend is empty without a topology grant", () => {
  expect(cellLegend(null, null)).toEqual([]);
});
