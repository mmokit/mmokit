import { describe, it, expect } from "vitest";
import { fuzzyScore } from "./commands";

describe("fuzzyScore", () => {
  it("returns 0 for non-matching query", () => {
    expect(fuzzyScore("cell.split", "xyz")).toBe(0);
  });
  it("returns higher score for prefix match than mid-string match", () => {
    const prefix = fuzzyScore("cell.split", "cell");
    const mid = fuzzyScore("entity.cell", "cell");
    expect(prefix).toBeGreaterThan(mid);
  });
  it("matches characters in order even when not contiguous", () => {
    // "cs" matches "cell.split" (c at 0, s at 5).
    expect(fuzzyScore("cell.split", "cs")).toBeGreaterThan(0);
  });
  it("returns 0 when query characters appear in the wrong order", () => {
    // "sc" — s at 5, c at 0 — out of order, no match.
    expect(fuzzyScore("cell.split", "sc")).toBe(0);
  });
  it("is case-insensitive", () => {
    expect(fuzzyScore("Cell.Split", "cs")).toBeGreaterThan(0);
  });
});
