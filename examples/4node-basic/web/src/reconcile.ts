import type { ClientEntity } from "./state.js";

// Fresh-snapshot frames (set by the server on the first frame from a given
// ReplicationSystem — login or cross-cell handoff) are authoritative about the
// full visible set. The server does NOT compute an `exited` list on a fresh
// frame because the destination cell has no record of what the source cell had
// visible. Drop any entity whose netID isn't in the fresh visible set so stale
// replicas from the previous cell's perspective don't linger forever, frozen
// at their last position. Keeps playerNetID defensively in case it isn't
// echoed in the frame.
export function pruneStaleOnFreshSnapshot(
  entities: Map<number, ClientEntity>,
  fresh: { netID: number }[],
  playerNetID: number,
): void {
  const visible = new Set<number>();
  for (const e of fresh) visible.add(e.netID);
  if (playerNetID) visible.add(playerNetID);
  for (const id of Array.from(entities.keys())) {
    if (!visible.has(id)) entities.delete(id);
  }
}
