// Canonical cell-ID parse + format. Mirrors pkg/universe/cell_id.go:
//
//   meshID()    — wire/internal form: "cell_X_Y" at depth 0,
//                 "cell_dN_X_Y" at depth N > 0. This is what the backend
//                 emits in /admin/api/cells, the `cells` SSE topic, and
//                 every other DTO field carrying a cell ID. Use it when
//                 building command payloads (cell.split, cell.merge,
//                 cell.migrate) so the server sees what it wrote.
//
//   displayID() — human-readable form: "X_Y" at depth 0, "dN_X_Y" at
//                 depth N > 0. Use it ONLY for rendering in the UI.
//                 Never feed it back into the API.
//
//   parseCellID() — accepts both forms (and the obsolete cell_X_Y:N
//                 quadrant suffix produced by an older codepath that
//                 no longer exists) and returns {x, y, depth} or null.
//
// Replaces ad-hoc regex/split parsers that were scattered across
// CellMap, WorldCanvas, CellDrawer, and cellmap-layout. Keeping a
// single implementation here ensures the TS side stays in lockstep
// with the Go canonical when the format evolves.

export type CellCoord = {
  x: number;
  y: number;
  depth: number;
};

const MESH_DEPTH_N = /^cell_d(\d+)_(-?\d+)_(-?\d+)$/;
const MESH_DEPTH_0 = /^cell_(-?\d+)_(-?\d+)$/;
const DISPLAY_DEPTH_N = /^d(\d+)_(-?\d+)_(-?\d+)$/;
const DISPLAY_DEPTH_0 = /^(-?\d+)_(-?\d+)$/;

export function parseCellID(id: string): CellCoord | null {
  if (!id) return null;
  // The obsolete `:N` quadrant suffix was produced by an older
  // codepath. We still strip it so any cached old DTOs parse cleanly.
  const colon = id.indexOf(":");
  const s = colon >= 0 ? id.slice(0, colon) : id;

  let m = MESH_DEPTH_N.exec(s);
  if (m) {
    return { depth: Number.parseInt(m[1], 10), x: Number.parseInt(m[2], 10), y: Number.parseInt(m[3], 10) };
  }
  m = MESH_DEPTH_0.exec(s);
  if (m) {
    return { depth: 0, x: Number.parseInt(m[1], 10), y: Number.parseInt(m[2], 10) };
  }
  m = DISPLAY_DEPTH_N.exec(s);
  if (m) {
    return { depth: Number.parseInt(m[1], 10), x: Number.parseInt(m[2], 10), y: Number.parseInt(m[3], 10) };
  }
  m = DISPLAY_DEPTH_0.exec(s);
  if (m) {
    return { depth: 0, x: Number.parseInt(m[1], 10), y: Number.parseInt(m[2], 10) };
  }
  return null;
}

export function meshID(c: CellCoord): string {
  if (c.depth === 0) return `cell_${c.x}_${c.y}`;
  return `cell_d${c.depth}_${c.x}_${c.y}`;
}

export function displayID(c: CellCoord): string {
  if (c.depth === 0) return `${c.x}_${c.y}`;
  return `d${c.depth}_${c.x}_${c.y}`;
}

// toDisplay converts any cell-ID string to its display form. Returns
// the input unchanged on parse failure so unexpected IDs still render
// (just less prettily) instead of vanishing from the UI.
export function toDisplay(id: string): string {
  const c = parseCellID(id);
  return c ? displayID(c) : id;
}

// parentID returns the meshID of the parent cell, or null when c is
// at depth 0. Matches Go's CellID.Parent: {X/2, Y/2, Depth-1}.
export function parentID(c: CellCoord): string | null {
  if (c.depth === 0) return null;
  return meshID({ x: Math.floor(c.x / 2), y: Math.floor(c.y / 2), depth: c.depth - 1 });
}
