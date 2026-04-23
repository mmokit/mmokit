import { CLOCK_SYNC_ALPHA } from "./constants";

/**
 * ClockSync maintains an exponentially-smoothed estimate of the offset
 * between client performance.now() and server ClusterClock milliseconds.
 * It's fed by the per-entity `producedAtMs` stamps on every incoming
 * replication frame and consulted by the render loop to compute
 * "server time right now" without making a network round-trip.
 */
export interface ClockSync {
  /** Smoothed server_ms − client_ms offset. */
  offsetMs: number;
  /** True once at least one frame has been observed. */
  initialized: boolean;
}

export function newClockSync(): ClockSync {
  return { offsetMs: 0, initialized: false };
}

/** Minimal shape required to anchor a frame on the clock — any decoded entity carries one. */
interface HasProducedAt { producedAtMs: number; }

/**
 * Feed one server timestamp observation with the client's
 * performance.now() at the moment of observation. Exposed for the
 * handful of call sites that have a single scalar stamp; frame
 * consumers should prefer observeFrameStamps below.
 */
export function observeServerTime(
  c: ClockSync,
  serverTimeMs: number,
  clientNowMs: number,
): void {
  const instant = serverTimeMs - clientNowMs;
  if (!c.initialized) {
    c.offsetMs = instant;
    c.initialized = true;
  } else {
    c.offsetMs = c.offsetMs * (1 - CLOCK_SYNC_ALPHA) + instant * CLOCK_SYNC_ALPHA;
  }
}

/**
 * Feed the clock the newest `producedAtMs` across a frame's decoded
 * entities. Every entity in a frame carries its own ClusterClock-aligned
 * stamp (per Phase K of the replication-timeline redesign); the clock
 * only needs the freshest one to track offset drift. No-ops if the
 * frame contains no entities with stamps.
 */
export function observeFrameStamps(
  c: ClockSync,
  entities: readonly HasProducedAt[],
  clientNowMs: number,
): void {
  let maxStamp = 0;
  for (const e of entities) {
    if (e.producedAtMs > maxStamp) maxStamp = e.producedAtMs;
  }
  if (maxStamp > 0) observeServerTime(c, maxStamp, clientNowMs);
}

/** Estimated current server wall-clock time in ms, given a client performance.now() reading. */
export function estimatedServerNow(c: ClockSync, clientNowMs: number): number {
  return clientNowMs + c.offsetMs;
}
