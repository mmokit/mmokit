import { CLOCK_SYNC_ALPHA } from "./constants";

/**
 * ClockSync maintains an exponentially-smoothed estimate of the offset
 * between client performance.now() and server wall-clock milliseconds.
 * It's fed by every incoming replication frame's serverTimeMs field
 * and consulted by the render loop to compute "server time right now"
 * without making a network round-trip.
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

/** Feed one server timestamp observation with the client's performance.now() at the moment of observation. */
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

/** Estimated current server wall-clock time in ms, given a client performance.now() reading. */
export function estimatedServerNow(c: ClockSync, clientNowMs: number): number {
  return clientNowMs + c.offsetMs;
}
