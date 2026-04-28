import type { CellTopologyMsg } from "@gen/enginepb/engine_pb.js";

export type Presence = "LOCAL" | "REPLICA";

interface Positioned {
  worldX: number;
  worldY: number;
}

/**
 * Computes the presence (LOCAL vs REPLICA) of an entity from the
 * client's perspective, given the cluster topology and the host the
 * client is connected to.
 *
 * Walks the topology's cell list to find the cell containing the
 * entity's position, looks up the host owning that cell, and returns
 * LOCAL if it matches the viewer's host, REPLICA otherwise.
 *
 * Returns LOCAL when the topology is empty (e.g. before SE_DEBUG_INFO
 * has arrived) — without topology context, all entities are
 * indistinguishable, and LOCAL is the safe default that matches the
 * "no debug overlay" rendering path.
 */
export function presenceOf(
  entity: Positioned,
  topology: CellTopologyMsg,
  myHost: string,
): Presence {
  if (!topology?.cells || topology.cells.length === 0) {
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
      return cell.nodeId === myHost ? "LOCAL" : "REPLICA";
    }
  }
  return "LOCAL"; // entity is outside any known cell
}
