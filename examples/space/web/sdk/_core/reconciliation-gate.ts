import { isForwardSequence } from "./playback-controller.js";

export const DEFAULT_STAGED_PAIRS = 8;

/** The identity a server frame and a prediction seed must agree on to pair. */
export interface AckedFrame {
  streamEpoch: number;
  tick: number;
  processedSequence: number;
}

function pairKey(epoch: number, tick: number, sequence: number): string {
  return `${epoch >>> 0}:${tick >>> 0}:${sequence >>> 0}`;
}

/**
 * Insert-or-refresh with oldest-first eviction.
 *
 * Relies on JS Map insertion order: deleting before setting moves an existing
 * key to the back, and `keys().next()` yields the oldest. Any port must
 * reproduce BOTH properties — a plain hash map loses them silently, and the
 * loss only shows up under staging pressure during a handoff, which is exactly
 * the scenario this gate exists for.
 */
function addBounded<T>(map: Map<string, T>, key: string, value: T, limit: number): void {
  map.delete(key);
  map.set(key, value);
  while (map.size > limit) {
    const oldest = map.keys().next().value as string | undefined;
    if (oldest === undefined) break;
    map.delete(oldest);
  }
}

/**
 * Pairs server-authoritative prediction seeds with accepted delta frames.
 *
 * A seed is only usable once the frame it describes has been accepted by the
 * decoder — otherwise a stale authority could retire commands the current one
 * never processed. The decoder's whole-frame stream fence runs first; matching
 * on (streamEpoch, tick, processedSequence) is what makes the pairing safe
 * across a handoff. Both delivery orders are supported and staging is bounded
 * so a client that never sees the matching half cannot grow without limit.
 *
 * This is deliberately game-neutral: it contains no movement or physics, only
 * the pairing. TSeed is whatever the game's seed type is, keyed by the same
 * triple.
 */
export class ReconciliationGate<TSeed extends AckedFrame> {
  private readonly maxStaged: number;
  private seeds = new Map<string, TSeed>();
  private frames = new Map<string, AckedFrame>();
  private currentStreamEpoch: number | null = null;

  constructor(maxStaged = DEFAULT_STAGED_PAIRS) {
    if (!Number.isSafeInteger(maxStaged) || maxStaged < 1) {
      throw new RangeError("maxStaged must be a positive safe integer");
    }
    this.maxStaged = maxStaged;
  }

  /** Returns true when an accepted frame establishes a fresh replication stream. */
  observeStream(streamEpoch: number): boolean {
    const epoch = streamEpoch >>> 0;
    if (this.currentStreamEpoch === null) {
      this.currentStreamEpoch = epoch;
      this.retainEpoch(epoch);
      return false;
    }
    if (epoch === this.currentStreamEpoch) return false;
    if (!isForwardSequence(this.currentStreamEpoch, epoch)) return false;
    this.currentStreamEpoch = epoch;
    this.retainEpoch(epoch);
    return true;
  }

  /** Clear pre-reset pair records while preserving this frame's early seed. */
  resetForFreshSnapshot(frame: AckedFrame | null): void {
    let matchingSeed: TSeed | undefined;
    let key = "";
    if (frame) {
      key = pairKey(frame.streamEpoch, frame.tick, frame.processedSequence);
      matchingSeed = this.seeds.get(key);
    }
    this.seeds.clear();
    this.frames.clear();
    if (matchingSeed) this.seeds.set(key, matchingSeed);
  }

  /** Allow the current stream or an early seed from its forward successor. */
  acceptsSeedStream(streamEpoch: number): boolean {
    const epoch = streamEpoch >>> 0;
    return this.currentStreamEpoch === null ||
      epoch === this.currentStreamEpoch ||
      isForwardSequence(this.currentStreamEpoch, epoch);
  }

  /**
   * Whether a seed may affect prediction before its exact frame arrives.
   * Successor-stream seeds remain quarantined until the delta decoder accepts
   * that stream; an unestablished connection may use its first seed directly.
   */
  canApplySeedImmediately(streamEpoch: number): boolean {
    const epoch = streamEpoch >>> 0;
    return this.currentStreamEpoch === null || epoch === this.currentStreamEpoch;
  }

  private retainEpoch(epoch: number): void {
    const prefix = `${epoch >>> 0}:`;
    for (const key of this.seeds.keys()) {
      if (!key.startsWith(prefix)) this.seeds.delete(key);
    }
    for (const key of this.frames.keys()) {
      if (!key.startsWith(prefix)) this.frames.delete(key);
    }
  }

  stageSeed(seed: TSeed): TSeed | null {
    if (!this.acceptsSeedStream(seed.streamEpoch)) return null;
    const key = pairKey(seed.streamEpoch, seed.tick, seed.processedSequence);
    if (this.frames.delete(key)) return seed;
    addBounded(this.seeds, key, seed, this.maxStaged);
    return null;
  }

  stageFrame(frame: AckedFrame): TSeed | null {
    const key = pairKey(frame.streamEpoch, frame.tick, frame.processedSequence);
    const seed = this.seeds.get(key);
    if (seed) {
      this.seeds.delete(key);
      return seed;
    }
    addBounded(this.frames, key, frame, this.maxStaged);
    return null;
  }

  reset(): void {
    this.currentStreamEpoch = null;
    this.seeds.clear();
    this.frames.clear();
  }
}
