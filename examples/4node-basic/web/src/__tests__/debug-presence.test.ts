import { describe, test, expect } from "bun:test";
import { presenceOf } from "../debug-presence";
import type { CellTopologyMsg } from "@gen/enginepb/engine_pb.js";

const sampleTopology = {
  $typeName: "enginepb.CellTopologyMsg" as const,
  gridW: 2, gridH: 2, baseCellSize: 1000,
  cells: [
    { $typeName: "enginepb.CellInfo", cellX: 0, cellY: 0, depth: 0, size: 1000, originX: 0, originY: 0, nodeId: "host-a" },
    { $typeName: "enginepb.CellInfo", cellX: 1, cellY: 0, depth: 0, size: 1000, originX: 1000, originY: 0, nodeId: "host-b" },
    { $typeName: "enginepb.CellInfo", cellX: 0, cellY: 1, depth: 0, size: 1000, originX: 0, originY: 1000, nodeId: "host-a" },
    { $typeName: "enginepb.CellInfo", cellX: 1, cellY: 1, depth: 0, size: 1000, originX: 1000, originY: 1000, nodeId: "host-b" },
  ],
} as unknown as CellTopologyMsg;

describe("presenceOf", () => {
  test("returns LOCAL when entity is in viewer's host's cell", () => {
    expect(presenceOf({ worldX: 500, worldY: 500 }, sampleTopology, "host-a"))
      .toBe("LOCAL");
  });

  test("returns REPLICA when entity is in another host's cell", () => {
    expect(presenceOf({ worldX: 1500, worldY: 500 }, sampleTopology, "host-a"))
      .toBe("REPLICA");
  });

  test("returns LOCAL fallback when topology is empty", () => {
    expect(presenceOf({ worldX: 500, worldY: 500 }, { ...sampleTopology, cells: [] } as unknown as CellTopologyMsg, "host-a"))
      .toBe("LOCAL");
  });

  test("handles a position in a different host's cell", () => {
    expect(presenceOf({ worldX: 1500, worldY: 1500 }, sampleTopology, "host-a"))
      .toBe("REPLICA");
  });

  test("handles a position in the same host's other cell", () => {
    expect(presenceOf({ worldX: 500, worldY: 1500 }, sampleTopology, "host-a"))
      .toBe("LOCAL");
  });
});
