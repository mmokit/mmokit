import type { ClientEntity } from "./state.js";
import {
  recordDeletion,
  recordFreshSnapshot,
  windowStats,
  type ReplicationAudit,
} from "./replicationAudit.js";

// Diagnostic delay for the settled-summary log. ≥ RE_ENTRY_WINDOW_MS so
// every re-entry triggered by the fresh-frame's churn is counted.
const SETTLE_LOG_DELAY_MS = 2000;

// Fresh-snapshot frames (set by the server on the first frame from a given
// ReplicationSystem — login or cross-cell handoff) are authoritative about the
// full visible set. The server does NOT compute an `exited` list on a fresh
// frame because the destination cell has no record of what the source cell had
// visible. Drop any entity whose netID isn't in the fresh visible set so stale
// replicas from the previous cell's perspective don't linger forever, frozen
// at their last position. Keeps playerNetID defensively in case it isn't
// echoed in the frame.
//
// audit: every fresh frame and every deletion is recorded under
// reason="fresh-reconcile" so we can correlate split/merge events with
// the entity churn they trigger. Two diagnostic lines are written to
// the console per fresh-frame: an IMMEDIATE post-reconcile counting
// what was deleted, and a SETTLED line 2s later counting how many
// re-entered as new entities. If the server-side first-frame defer
// is working, `deleted` should be near zero (the fresh frame contains
// border-replicas, so nothing gets reconciled out).
export function pruneStaleOnFreshSnapshot(
  entities: Map<number, ClientEntity>,
  fresh: { netID: number }[],
  playerNetID: number,
  audit: ReplicationAudit,
  nowMs: number,
): void {
  recordFreshSnapshot(audit, nowMs, fresh.length);
  const stateBefore = entities.size;

  const visible = new Set<number>();
  for (const e of fresh) visible.add(e.netID);
  if (playerNetID) visible.add(playerNetID);
  for (const id of Array.from(entities.keys())) {
    if (visible.has(id)) continue;
    recordDeletion(audit, nowMs, id, "fresh-reconcile");
    entities.delete(id);
  }
  const stateAfter = entities.size;
  const deleted = stateBefore - stateAfter;
  console.warn(
    `[repl-audit] post-reconcile  t=${nowMs.toFixed(0)}ms  fresh.in=${fresh.length}  stateBefore=${stateBefore}  stateAfter=${stateAfter}  deleted=${deleted}`,
  );

  // Schedule the settled-summary log so we capture re-entries that
  // dribble in over the next ~2s. With the server-side first-frame
  // defer working, re-entries should drop to near zero — every
  // border-visible entity is in the fresh frame, so the reconcile
  // doesn't delete anyone who's about to re-enter.
  const frameStamp = nowMs;
  setTimeout(() => {
    const tNow = performance.now();
    const stateNow = entities.size;
    const recovered = stateNow - stateAfter;
    const w = windowStats(audit, tNow, SETTLE_LOG_DELAY_MS);
    console.warn(
      `[repl-audit] settled       frame+${(tNow - frameStamp).toFixed(0)}ms  stateNow=${stateNow} (recovered ${recovered} since reconcile)  2s-window: fresh=${w.freshSnapshots} fr=${w.deletions["fresh-reconcile"]} ex=${w.deletions.exited} rm=${w.deletions.removed} re=${w.reEntries}`,
    );
  }, SETTLE_LOG_DELAY_MS);
}
