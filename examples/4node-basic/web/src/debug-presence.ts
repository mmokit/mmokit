import type { DebugInfo } from "../sdk/index.js";

export type Presence = "LOCAL" | "REPLICA";

// Topology shape pulled from the typed DebugInfo broadcast — defined as
// an inline type by the sdk codegen, so we extract it here for ergonomic
// reuse at call sites.
export type Topology = DebugInfo["topology"];

interface Positioned {
  worldX: number;
  worldY: number;
}

interface CellRef {
  cellX: number;
  cellY: number;
  depth: number;
}

/**
 * Computes the presence (LOCAL vs REPLICA) of an entity from the
 * client's perspective by comparing the cell containing the entity to
 * the cell containing the viewer.
 *
 * Cell-based (rather than host-based) so the R marker fires on
 * entities that have crossed into a neighboring cell from the
 * viewer's, even when the whole cluster runs on one host (e.g.
 * `coord+host+gateway` single-process mode where every cell shares
 * the same nodeId). Matches the visual intent of "this entity isn't
 * in my cell, the server is mirroring it into my AoI."
 *
 * Returns LOCAL when the topology is empty (e.g. before SE_DEBUG_INFO
 * has arrived) or when the viewer's cell can't be resolved — without
 * context, all entities are indistinguishable and LOCAL is the safe
 * default that matches the "no debug overlay" rendering path.
 */
export function presenceOf(
  entity: Positioned,
  topology: Topology,
  myCell: CellRef | null,
): Presence {
  if (!topology?.cells || topology.cells.length === 0 || !myCell) {
    return "LOCAL";
  }
  // Linear scan — for v1 grids of 4-16 cells this is fast enough.
  // Replace with a quadtree lookup if dynamic-cell trees grow large.
  for (const cell of topology.cells) {
    const x0 = cell.originX;
    const y0 = cell.originY;
    const x1 = x0 + cell.size;
    const y1 = y0 + cell.size;
    if (entity.worldX >= x0 && entity.worldX < x1
        && entity.worldY >= y0 && entity.worldY < y1) {
      const sameCell = cell.cellX === myCell.cellX
        && cell.cellY === myCell.cellY
        && cell.depth === myCell.depth;
      return sameCell ? "LOCAL" : "REPLICA";
    }
  }
  return "LOCAL"; // entity is outside any known cell
}
