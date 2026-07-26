/**
 * Optional stateful per-entity playback buffer: a Sample ring + the two
 * orchestration footguns bundled (stale-gated push, interpolate-at-render-
 * time). Operates only on Sample — no game semantics. The consumer owns one
 * buffer per entity, converts its entity -> Sample (incl. rotation rule via
 * newest()), and applies the InterpolationResult to its view. Headless/bot
 * consumers ignore this and use interpolation-core directly.
 */
import {
  type Sample,
  type SampleRing,
  type InterpolationResult,
  type PushSampleResult,
  pushSample,
  isStaleSample,
  interpolateRing,
} from "./interpolation-core.js";

export const DEFAULT_RING_SIZE = 8;
export const DEFAULT_RENDER_DELAY_MS = 100;
export const DEFAULT_MAX_EXTRAPOLATE_MS = 50;

export interface InterpolationBufferConfig {
  ringSize?: number;
  renderDelayMs?: number;
  maxExtrapolateMs?: number;
}

export class InterpolationBuffer implements SampleRing {
  samples: Sample[] = [];
  authorityEpoch?: number;
  readonly ringSize: number;
  readonly renderDelayMs: number;
  readonly maxExtrapolateMs: number;

  constructor(cfg: InterpolationBufferConfig = {}) {
    this.ringSize = cfg.ringSize ?? DEFAULT_RING_SIZE;
    this.renderDelayMs = cfg.renderDelayMs ?? DEFAULT_RENDER_DELAY_MS;
    this.maxExtrapolateMs = cfg.maxExtrapolateMs ?? DEFAULT_MAX_EXTRAPOLATE_MS;
  }

  /** Stale-gated append (drops frames older than the newest held). */
  push(s: Sample): PushSampleResult {
    return pushSample(this, s, this.ringSize);
  }

  /** Whether push(s) would drop s as stale — gate non-position field snapshots on this. */
  isStale(producedAtMs: number, authorityEpoch?: number): boolean {
    return isStaleSample(this, producedAtMs, authorityEpoch);
  }

  /**
   * Forget samples and their authority fence. Use this only when an enclosing
   * stream generation supersedes the old stream (for example, transfer
   * rollback); an ordinary fresh snapshot should retain a compatible ring.
   */
  reset(): void {
    this.samples = [];
    this.authorityEpoch = undefined;
  }

  /** Newest sample held, or null when empty (for prev-rotation fallback). */
  newest(): Sample | null {
    return this.samples.length > 0 ? this.samples[this.samples.length - 1] : null;
  }

  /** Interpolated render pose at the given server render time, or null when empty. */
  sampleAt(
    renderTimeMs: number,
    renderDelayMs: number = this.renderDelayMs,
  ): InterpolationResult | null {
    return interpolateRing(this, renderTimeMs, this.maxExtrapolateMs, renderDelayMs);
  }
}
