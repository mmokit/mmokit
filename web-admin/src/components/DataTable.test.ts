import { describe, it, expect } from "vitest";
import { sortRows, type SortDir } from "./DataTable.helpers";

describe("sortRows", () => {
  type Row = { id: string; load: number };
  const rows: Row[] = [
    { id: "b", load: 0.5 },
    { id: "a", load: 0.9 },
    { id: "c", load: 0.1 },
  ];

  it("sorts ascending by string", () => {
    const out = sortRows(rows, (r) => r.id, "asc");
    expect(out.map((r) => r.id)).toEqual(["a", "b", "c"]);
  });

  it("sorts descending by number", () => {
    const out = sortRows(rows, (r) => r.load, "desc");
    expect(out.map((r) => r.id)).toEqual(["a", "b", "c"]);
  });

  it("returns original array (not a mutation)", () => {
    const out = sortRows(rows, (r) => r.id, "asc");
    expect(out).not.toBe(rows);
    expect(rows[0].id).toBe("b"); // original unchanged
  });

  it("handles undefined gracefully (sorts to end)", () => {
    type R = { id: string; v?: number };
    const xs: R[] = [{ id: "a", v: 1 }, { id: "b" }, { id: "c", v: 2 }];
    const out = sortRows(xs, (r) => r.v, "asc" as SortDir);
    expect(out[out.length - 1].id).toBe("b");
  });
});
