import {
  type ClockSync,
  estimatedServerNow,
  newClockSync,
  observeServerTime,
  resetClockSync,
} from "./clock-sync.js";

const UINT32_HALF_RANGE = 0x80000000;

export const DEFAULT_SERVER_TICK_MS = 50;
export const DEFAULT_MIN_PLAYBACK_DELAY_MS = 100;
export const DEFAULT_MAX_PLAYBACK_DELAY_MS = 300;
export const DEFAULT_MIN_PLAYBACK_RATE = 0.9;
export const DEFAULT_MAX_PLAYBACK_RATE = 1.1;

/** One observation per decoded frame, including frames with no entities. */
export interface FrameTimingObservation {
  seq: number;
  freshSnapshot: boolean;
  /**
   * Whether frame sequencing entered a new stream generation. When supplied,
   * this—not freshSnapshot—controls sequence reset: same-stream recovery
   * snapshots remain useful for packet-loss measurement.
   */
  streamChanged?: boolean;
  arrivalTimeMs: number;
  /** Newest producer stamp in the frame; absent for an empty frame. */
  producedAtMs?: number;
}

export interface AdaptivePlaybackConfig {
  clock?: ClockSync;
  tickIntervalMs?: number;
  minDelayMs?: number;
  maxDelayMs?: number;
  minPlaybackRate?: number;
  maxPlaybackRate?: number;
  /** Delay error that requests the full configured rate correction. */
  convergenceWindowMs?: number;
  /** EWMA coefficient used when the target must rise. */
  attackFactor?: number;
  /** EWMA coefficient used when the target can fall. */
  decayFactor?: number;
  jitterFactor?: number;
}

export interface PlaybackMetrics {
  currentDelayMs: number;
  targetDelayMs: number;
  jitterMs: number;
  excessDelayMs: number;
  receivedFrames: number;
  lostFrames: number;
  duplicateFrames: number;
  outOfOrderFrames: number;
  lossRate: number;
  playbackRate: number;
  renderTimeMs: number | null;
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value));
}

function finiteOr(value: number | undefined, fallback: number): number {
  return value === undefined || !Number.isFinite(value) ? fallback : value;
}

/**
 * Unsigned distance from `from` to `to`. A distance in (0, 2^31) is a
 * forward move, including the uint32 wrap from 0xffffffff to zero.
 */
export function sequenceDistance(from: number, to: number): number {
  return ((to >>> 0) - (from >>> 0)) >>> 0;
}

export function isForwardSequence(from: number, to: number): boolean {
  const distance = sequenceDistance(from, to);
  return distance > 0 && distance < UINT32_HALF_RANGE;
}

/**
 * Connection-level adaptive interpolation timeline.
 *
 * Feed every decoded frame to observeFrame, even when it has no entity stamp,
 * so sequence gaps remain meaningful. renderTime returns a producer-clock
 * cursor suitable for every entity buffer on the connection. It changes
 * playback speed within tight bounds to converge on a new delay target; it
 * never moves the cursor backwards when jitter makes that target rise.
 */
export class AdaptivePlaybackController {
  readonly clock: ClockSync;
  readonly tickIntervalMs: number;
  readonly minDelayMs: number;
  readonly maxDelayMs: number;
  readonly minPlaybackRate: number;
  readonly maxPlaybackRate: number;
  readonly convergenceWindowMs: number;
  readonly attackFactor: number;
  readonly decayFactor: number;
  readonly jitterFactor: number;

  private targetDelayValue: number;
  private currentDelayValue: number;
  private jitterValue = 0;
  private excessDelayValue = 0;
  private previousRawExcess: number | null = null;
  private lossPressureMs = 0;

  private lastSequence: number | null = null;
  private receivedCount = 0;
  private lostCount = 0;
  private duplicateCount = 0;
  private outOfOrderCount = 0;

  private renderCursor: number | null = null;
  private lastRenderClientTime: number | null = null;
  private playbackRateValue = 1;
  private clockReanchorPending = false;

  constructor(config: AdaptivePlaybackConfig = {}) {
    this.clock = config.clock ?? newClockSync();
    this.tickIntervalMs = Math.max(
      0.001,
      finiteOr(config.tickIntervalMs, DEFAULT_SERVER_TICK_MS),
    );
    this.minDelayMs = Math.max(
      0,
      finiteOr(config.minDelayMs, DEFAULT_MIN_PLAYBACK_DELAY_MS),
    );
    this.maxDelayMs = Math.max(
      this.minDelayMs,
      finiteOr(config.maxDelayMs, DEFAULT_MAX_PLAYBACK_DELAY_MS),
    );
    this.minPlaybackRate = clamp(
      finiteOr(config.minPlaybackRate, DEFAULT_MIN_PLAYBACK_RATE),
      0.01,
      1,
    );
    this.maxPlaybackRate = Math.max(
      1,
      finiteOr(config.maxPlaybackRate, DEFAULT_MAX_PLAYBACK_RATE),
    );
    this.convergenceWindowMs = Math.max(
      1,
      finiteOr(config.convergenceWindowMs, 1000),
    );
    this.attackFactor = clamp(finiteOr(config.attackFactor, 0.5), 0, 1);
    this.decayFactor = clamp(finiteOr(config.decayFactor, 0.05), 0, 1);
    this.jitterFactor = Math.max(0, finiteOr(config.jitterFactor, 2));
    this.targetDelayValue = this.minDelayMs;
    this.currentDelayValue = this.minDelayMs;
  }

  /** Observe a frame. Duplicate/reordered frames still inform timing. */
  observeFrame(observation: FrameTimingObservation): void {
    const sequence = observation.seq >>> 0;
    let gap = 0;
    const producedAtMs = observation.producedAtMs;
    const hasProducerStamp =
      producedAtMs !== undefined &&
      Number.isFinite(producedAtMs) &&
      Number.isFinite(observation.arrivalTimeMs);

    if (observation.streamChanged ?? observation.freshSnapshot) {
      // A new stream is a sanctioned sequence discontinuity. Seed sequence
      // continuity from it and drop the superseded producer's clock samples.
      // Keep the render cursor itself monotonic: RenderTime will pause if the
      // replacement authority's clock is behind, then resume on catch-up.
      this.lastSequence = null;
      this.clockReanchorPending = true;
      if (!hasProducerStamp) {
        // Do not let an ACK-only successor frame pair a prediction seed while
        // estimatedServerNow still reflects the superseded producer.
        resetClockSync(this.clock);
        this.previousRawExcess = null;
        this.clockReanchorPending = false;
      }
    }

    if (this.lastSequence === null) {
      this.lastSequence = sequence;
      this.receivedCount++;
    } else {
      const distance = sequenceDistance(this.lastSequence, sequence);
      if (distance === 0) {
        this.duplicateCount++;
      } else if (distance < UINT32_HALF_RANGE) {
        gap = distance - 1;
        this.lostCount += gap;
        this.receivedCount++;
        this.lastSequence = sequence;
      } else {
        this.outOfOrderCount++;
      }
    }

    if (gap > 0) {
      this.lossPressureMs = Math.max(
        this.lossPressureMs,
        Math.min(this.maxDelayMs - this.minDelayMs, gap * this.tickIntervalMs),
      );
    } else {
      // Loss pressure is intentionally short-lived. Lifetime counters remain
      // available in metrics, while healthy delivery can lower render delay.
      this.lossPressureMs *= 0.85;
    }

    if (hasProducerStamp) {
      // hasProducerStamp establishes this; the local assertion also keeps the
      // generated core compatible with TypeScript versions that do not carry
      // aliased-condition narrowing into this branch.
      const producerStamp = producedAtMs as number;
      if (this.clockReanchorPending) {
        resetClockSync(this.clock);
        this.previousRawExcess = null;
        this.clockReanchorPending = false;
      }
      observeServerTime(
        this.clock,
        producerStamp,
        observation.arrivalTimeMs,
      );

      const instant = producerStamp - observation.arrivalTimeMs;
      const rawExcess = Math.max(0, this.clock.offsetMs - instant);
      if (this.previousRawExcess !== null) {
        const variation = Math.abs(rawExcess - this.previousRawExcess);
        // RFC-style transit variation EWMA (1/8), expressed in milliseconds.
        this.jitterValue += (variation - this.jitterValue) / 8;
      }
      this.previousRawExcess = rawExcess;

      const excessFactor = rawExcess > this.excessDelayValue
        ? this.attackFactor
        : this.decayFactor;
      this.excessDelayValue += excessFactor * (rawExcess - this.excessDelayValue);
    }

    const rawTarget = clamp(
      this.minDelayMs +
        this.excessDelayValue +
        this.jitterFactor * this.jitterValue +
        this.lossPressureMs,
      this.minDelayMs,
      this.maxDelayMs,
    );
    const targetFactor = rawTarget > this.targetDelayValue
      ? this.attackFactor
      : this.decayFactor;
    this.targetDelayValue = clamp(
      this.targetDelayValue + targetFactor * (rawTarget - this.targetDelayValue),
      this.minDelayMs,
      this.maxDelayMs,
    );
  }

  /**
   * Advance and return the shared producer-clock render cursor. Returns null
   * until a stamped frame has initialized ClockSync.
   */
  renderTime(clientNowMs: number): number | null {
    if (!this.clock.initialized || !Number.isFinite(clientNowMs)) return null;

    const serverNowMs = estimatedServerNow(this.clock, clientNowMs);
    if (this.renderCursor === null || this.lastRenderClientTime === null) {
      this.renderCursor = serverNowMs - this.targetDelayValue;
      this.lastRenderClientTime = clientNowMs;
      this.currentDelayValue = this.targetDelayValue;
      this.playbackRateValue = 1;
      return this.renderCursor;
    }

    const elapsedMs = Math.max(0, clientNowMs - this.lastRenderClientTime);
    // Compare the target with the delay we would have after ordinary 1x
    // playback. Using the pre-advance cursor here would count this render
    // frame's elapsed time as extra lag and spuriously oscillate the rate.
    const baselineDelay = serverNowMs - (this.renderCursor + elapsedMs);
    const delayError = baselineDelay - this.targetDelayValue;
    const requestedPlaybackRate = clamp(
      1 + delayError / this.convergenceWindowMs,
      this.minPlaybackRate,
      this.maxPlaybackRate,
    );

    // Only move forward. In particular, increasing target delay slows this
    // cursor until the server timeline pulls ahead instead of rewinding it.
    // ClockSync can move serverNow backwards when its previous maximum ages
    // out. In that exceptional case, pause at the existing cursor rather than
    // advancing farther ahead of the corrected producer clock. This safety
    // clamp may temporarily apply a rate below minPlaybackRate.
    const requestedAdvance = elapsedMs * requestedPlaybackRate;
    const availableAdvance = Math.max(0, serverNowMs - this.renderCursor);
    const appliedAdvance = Math.min(requestedAdvance, availableAdvance);
    this.renderCursor += appliedAdvance;
    this.playbackRateValue = elapsedMs > 0
      ? appliedAdvance / elapsedMs
      : requestedPlaybackRate;
    if (clientNowMs > this.lastRenderClientTime) {
      this.lastRenderClientTime = clientNowMs;
    }
    // A downward clock correction may leave the already-published monotonic
    // cursor temporarily ahead of serverNow. Report the effective delay as
    // zero until the corrected producer clock catches up; never expose a
    // nonsensical negative interpolation delay.
    this.currentDelayValue = Math.max(0, serverNowMs - this.renderCursor);
    return this.renderCursor;
  }

  get currentDelayMs(): number {
    return this.currentDelayValue;
  }

  get targetDelayMs(): number {
    return this.targetDelayValue;
  }

  get metrics(): Readonly<PlaybackMetrics> {
    const delivered = this.receivedCount + this.lostCount;
    return {
      currentDelayMs: this.currentDelayValue,
      targetDelayMs: this.targetDelayValue,
      jitterMs: this.jitterValue,
      excessDelayMs: this.excessDelayValue,
      receivedFrames: this.receivedCount,
      lostFrames: this.lostCount,
      duplicateFrames: this.duplicateCount,
      outOfOrderFrames: this.outOfOrderCount,
      lossRate: delivered === 0 ? 0 : this.lostCount / delivered,
      playbackRate: this.playbackRateValue,
      renderTimeMs: this.renderCursor,
    };
  }
}
