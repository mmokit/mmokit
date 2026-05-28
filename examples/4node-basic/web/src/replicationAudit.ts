// Diagnostic instrumentation for the rubber-band-after-split/merge bug.
//
// Hypothesis under test: when the player's cell splits (or merges), the
// server hands the conn to a freshly-created destination cell whose
// ReplicationSystem has hadPriorState=false → emits a FrameFlagFreshSnapshot
// frame. The client's set-wise reconciliation in pruneStaleOnFreshSnapshot
// then DELETES every entity not in that frame's entered+updated set.
// Border-replica entities from sibling children haven't arrived yet
// (their cross-cell BorderDispatchers take a tick to spin up), so they
// get culled. Over the next few ticks they re-enter as single-sample
// rings → renderer renders them static until a second sample arrives
// → snap-then-lerp = visible rubber-band.
//
// To confirm or refute: count freshSnapshot frames, deletions by reason,
// and re-entries (same netID created within RE_ENTRY_WINDOW_MS of being
// deleted). If a split triggers ≥1 freshSnapshot, a deletion spike, AND
// a near-equal re-entry spike within ~100-300ms — hypothesis is locked.

const RE_ENTRY_WINDOW_MS = 2000;
const BURST_HISTORY_MS = 5000;
const EVENT_RING_SIZE = 200;

export type DeletionReason = "fresh-reconcile" | "exited" | "removed";

export interface ReplicationAuditEvent {
  timeMs: number;
  kind: "fresh-frame" | "deletion" | "re-entry";
  netID?: number;
  reason?: DeletionReason;
  count?: number;
}

interface Burst {
  timeMs: number;
  type: "fresh" | "deletion" | "re-entry";
  reason?: DeletionReason;
  count: number;
}

export interface ReplicationAudit {
  // netID → ms of deletion (performance.now); used for re-entry detection.
  recentDeletions: Map<number, number>;

  // Lifetime counters.
  freshSnapshots: number;
  deletions: Record<DeletionReason, number>;
  reEntries: number;

  // Sliding-burst log used to derive 1s/5s windowed stats.
  bursts: Burst[];

  // Last EVENT_RING_SIZE events for offline inspection via window.replAudit().
  events: ReplicationAuditEvent[];
  eventIdx: number;
}

export function newReplicationAudit(): ReplicationAudit {
  return {
    recentDeletions: new Map(),
    freshSnapshots: 0,
    deletions: { "fresh-reconcile": 0, "exited": 0, "removed": 0 },
    reEntries: 0,
    bursts: [],
    events: [],
    eventIdx: 0,
  };
}

function recordEvent(a: ReplicationAudit, ev: ReplicationAuditEvent): void {
  if (a.events.length < EVENT_RING_SIZE) {
    a.events.push(ev);
  } else {
    a.events[a.eventIdx] = ev;
    a.eventIdx = (a.eventIdx + 1) % EVENT_RING_SIZE;
  }
}

function pruneBursts(a: ReplicationAudit, nowMs: number): void {
  while (a.bursts.length > 0 && nowMs - a.bursts[0].timeMs > BURST_HISTORY_MS) {
    a.bursts.shift();
  }
}

function pruneDeletions(a: ReplicationAudit, nowMs: number): void {
  for (const [netID, t] of a.recentDeletions) {
    if (nowMs - t > RE_ENTRY_WINDOW_MS) a.recentDeletions.delete(netID);
  }
}

export function recordFreshSnapshot(
  a: ReplicationAudit,
  nowMs: number,
  enteredCount: number,
): void {
  a.freshSnapshots++;
  a.bursts.push({ timeMs: nowMs, type: "fresh", count: enteredCount });
  recordEvent(a, { timeMs: nowMs, kind: "fresh-frame", count: enteredCount });
  pruneBursts(a, nowMs);
  console.warn(
    `[repl-audit] FreshSnapshot frame received: entered+updated=${enteredCount} t=${nowMs.toFixed(0)}ms`,
  );
}

export function recordDeletion(
  a: ReplicationAudit,
  nowMs: number,
  netID: number,
  reason: DeletionReason,
): void {
  a.deletions[reason]++;
  a.recentDeletions.set(netID, nowMs);
  a.bursts.push({ timeMs: nowMs, type: "deletion", reason, count: 1 });
  recordEvent(a, { timeMs: nowMs, kind: "deletion", netID, reason });
  pruneBursts(a, nowMs);
}

export function recordEntityCreate(
  a: ReplicationAudit,
  nowMs: number,
  netID: number,
): void {
  pruneDeletions(a, nowMs);
  if (a.recentDeletions.has(netID)) {
    a.reEntries++;
    a.bursts.push({ timeMs: nowMs, type: "re-entry", count: 1 });
    recordEvent(a, { timeMs: nowMs, kind: "re-entry", netID });
    pruneBursts(a, nowMs);
    a.recentDeletions.delete(netID);
  }
}

export interface ReplicationAuditWindow {
  freshSnapshots: number;
  deletions: Record<DeletionReason, number>;
  reEntries: number;
}

export function windowStats(
  a: ReplicationAudit,
  nowMs: number,
  windowMs: number,
): ReplicationAuditWindow {
  const cutoff = nowMs - windowMs;
  const w: ReplicationAuditWindow = {
    freshSnapshots: 0,
    deletions: { "fresh-reconcile": 0, "exited": 0, "removed": 0 },
    reEntries: 0,
  };
  for (const b of a.bursts) {
    if (b.timeMs < cutoff) continue;
    if (b.type === "fresh") w.freshSnapshots += b.count;
    else if (b.type === "deletion" && b.reason) w.deletions[b.reason] += b.count;
    else if (b.type === "re-entry") w.reEntries += b.count;
  }
  return w;
}

// Dump the recent event log to console — triggered via window.replAudit().
export function dumpAudit(a: ReplicationAudit): void {
  const ordered: ReplicationAuditEvent[] = [];
  if (a.events.length < EVENT_RING_SIZE) {
    ordered.push(...a.events);
  } else {
    for (let i = 0; i < EVENT_RING_SIZE; i++) {
      ordered.push(a.events[(a.eventIdx + i) % EVENT_RING_SIZE]);
    }
  }
  console.group(
    `[repl-audit] dump (lifetime fresh=${a.freshSnapshots} del.fresh-reconcile=${a.deletions["fresh-reconcile"]} del.exited=${a.deletions.exited} del.removed=${a.deletions.removed} reEntries=${a.reEntries})`,
  );
  for (const ev of ordered) console.log(ev);
  console.groupEnd();
}
