/**
 * Cell topology, and what the client can honestly say about it.
 *
 * Clients are topology-agnostic by design: they receive absolute world
 * coordinates and are never told which server owns what. The one exception is
 * the "topology" debug grant, which broadcasts DebugInfo carrying every cell's
 * origin, size and owning node.
 *
 * That is enough to answer the question that matters visually — "is this
 * entity in MY cell or a neighbour's?" — WITHOUT any wire change. It is not
 * the same as the server's Live/Replica presence, which is per-cell state the
 * client never sees and cannot derive: an entity near a border is Live on its
 * owner and a Replica on the neighbour, while the client sees one entity. What
 * this computes is that distinction from the viewer's own vantage point, which
 * is the useful half.
 *
 * Imports nothing, so it stays reachable from a test.
 */

/** One cell, as DebugInfo describes it. */
export interface CellRect {
  cellX: number;
  cellY: number;
  depth: number;
  size: number;
  originX: number;
  originY: number;
  nodeID: string;
}

export interface Topology {
  cells: CellRect[];
  baseCellSize: number;
}

/** Which cell contains a world position, or null if none does. */
export function cellAt(topo: Topology, x: number, y: number): CellRect | null {
  for (const c of topo.cells) {
    if (x >= c.originX && x < c.originX + c.size && y >= c.originY && y < c.originY + c.size) {
      return c;
    }
  }
  return null;
}

/** A stable key for a cell, usable for identity comparisons. */
export function cellKey(c: CellRect | null): string {
  return c ? `${c.depth}:${c.cellX}:${c.cellY}` : "";
}

/**
 * The world's bounding box, derived from the cells themselves.
 *
 * The grid used to be a fixed 4000-unit helper centred on the ORIGIN while the
 * world occupies 0..gridW*cellSize — so every cell sat in one quadrant of it
 * and the other three quadrants were empty space the grid implied was world.
 * Deriving the bounds means the grid is right at any grid size, and follows a
 * split, because DebugInfo re-broadcasts the cells after one.
 */
export function worldBounds(topo: Topology): {
  minX: number; minY: number; maxX: number; maxY: number;
  width: number; height: number; centerX: number; centerY: number;
} {
  if (topo.cells.length === 0) {
    return { minX: 0, minY: 0, maxX: 0, maxY: 0, width: 0, height: 0, centerX: 0, centerY: 0 };
  }
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const c of topo.cells) {
    minX = Math.min(minX, c.originX);
    minY = Math.min(minY, c.originY);
    maxX = Math.max(maxX, c.originX + c.size);
    maxY = Math.max(maxY, c.originY + c.size);
  }
  return {
    minX, minY, maxX, maxY,
    width: maxX - minX,
    height: maxY - minY,
    centerX: (minX + maxX) / 2,
    centerY: (minY + maxY) / 2,
  };
}

/** How an entity should be drawn, given where the viewer is. */
export type EntityClass = "self" | "local" | "remote" | "unknown";

/**
 * Classify an entity relative to the viewer's own cell.
 *
 * "local" means the entity is in the same cell as the viewer, so the viewer's
 * own authority owns it. "remote" means another cell owns it and it is
 * reaching this client across a border — the client-visible shadow of a
 * server-side replica.
 */
export function classify(
  topo: Topology | null,
  viewerCell: CellRect | null,
  entityNetID: number,
  entityX: number,
  entityY: number,
  viewerNetID: number | null,
): EntityClass {
  if (entityNetID === viewerNetID) return "self";
  if (!topo || topo.cells.length === 0 || !viewerCell) return "unknown";
  const c = cellAt(topo, entityX, entityY);
  if (!c) return "unknown";
  return cellKey(c) === cellKey(viewerCell) ? "local" : "remote";
}
