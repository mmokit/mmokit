// Window of recent `instant = serverTime - clientNow` observations used
// to derive offsetMs. 40 frames @ 20Hz server tick ≈ 2s of history —
// enough to absorb the bursty-delivery pattern seen on the WSL2 path
// (multi-frame bursts every ~300ms) without going so long that a real
// shift in base latency takes forever to track.
const INSTANT_WINDOW = 40;

/**
 * ClockSync derives the server-to-client wall-clock offset from per-frame
 * `producedAtMs` observations.
 *
 * Algorithm: **sliding-window max of `instant = serverTime - clientNow`**.
 *
 * Why max instead of EWMA: bursty WebSocket delivery (OS-level coalescing,
 * main-thread stalls queueing onmessage events, etc.) produces clusters of
 * frames where clientNow stays fixed while serverStamp ticks forward —
 * each frame in the cluster has a progressively LESS NEGATIVE instant.
 * Once normal cadence resumes, instant stabilizes near the value of the
 * LAST burst frame. The earlier burst frames carry pure delay noise and
 * are pathological for an EWMA — they pull offsetMs hundreds of ms away
 * from the truth.
 *
 * The maximum (least negative) instant in the recent window corresponds
 * to the frame that arrived with the LEAST delay — the closest sample to
 * a true zero-buffering reading. Sliding-window scope (~2s) lets the
 * offset drift if base latency genuinely shifts, by aging out the old
 * max sample.
 */
export interface ClockSync {
  /** Best estimate of server_ms − client_ms offset. */
  offsetMs: number;
  /** True once at least one frame has been observed. */
  initialized: boolean;
  /** Ring of recent instant observations. */
  instants: Float64Array;
  instantsIdx: number;
  instantsCount: number;
}

export function newClockSync(): ClockSync {
  return {
    offsetMs: 0,
    initialized: false,
    instants: new Float64Array(INSTANT_WINDOW),
    instantsIdx: 0,
    instantsCount: 0,
  };
}

/** Minimal shape required to anchor a frame on the clock. */
interface HasProducedAt { producedAtMs: number; }

/**
 * Feed one server timestamp observation with the client's
 * performance.now() at the moment of observation. Pushes into the
 * sliding window and recomputes offsetMs as the window's max.
 */
export function observeServerTime(
  c: ClockSync,
  serverTimeMs: number,
  clientNowMs: number,
): void {
  const instant = serverTimeMs - clientNowMs;

  c.instants[c.instantsIdx] = instant;
  c.instantsIdx = (c.instantsIdx + 1) % c.instants.length;
  if (c.instantsCount < c.instants.length) c.instantsCount++;

  if (!c.initialized) {
    c.offsetMs = instant;
    c.initialized = true;
    return;
  }

  // Max over the populated portion of the ring. O(40) per frame is
  // negligible (called per server frame ≈ 20Hz).
  let maxInstant = -Infinity;
  for (let i = 0; i < c.instantsCount; i++) {
    if (c.instants[i] > maxInstant) maxInstant = c.instants[i];
  }
  c.offsetMs = maxInstant;
}

/**
 * Feed the clock the newest `producedAtMs` across a frame's decoded
 * entities. Every entity in a frame carries its own ClusterClock-aligned
 * stamp; we use the freshest one as this frame's observation. No-ops if
 * the frame contains no entities with stamps.
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
