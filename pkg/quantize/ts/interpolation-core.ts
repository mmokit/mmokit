/**
 * Reference render-lag interpolation core for mmokit clients.
 *
 * Smooths 20Hz authoritative server samples into 60fps client motion
 * by interpolating on a per-entity ring keyed by producedAtMs (the
 * producer-side ClusterClock-aligned stamp). Games layer their
 * entity-type-specific glue on top.
 *
 * Wire format: see pkg/quantize/wireformat.go for producedAtMs
 * semantics.
 */

/**
 * One snapshot of an entity's authoritative state at a moment in
 * producer cluster-clock time. The interpolation core only reads the
 * fields below; games may store a richer per-entity record so long
 * as it carries a `samples: Sample[]` ring.
 */
export interface Sample {
  worldX: number;
  worldY: number;
  velX: number;
  velY: number;
  rotation: number;
  producedAtMs: number;
}

/** A ring of samples ordered ascending by producedAtMs. */
export interface SampleRing {
  samples: Sample[];
}

/** Interpolated render position computed by interpolateRing. */
export interface InterpolationResult {
  renderX: number;
  renderY: number;
  renderRot: number;
}

/** Linear interpolation between a and b at fraction t in [0, 1]. */
export function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

/**
 * Linear interpolation between two angles in radians, taking the
 * shortest path around the unit circle.
 */
export function lerpAngle(a: number, b: number, t: number): number {
  let diff = b - a;
  while (diff > Math.PI) diff -= Math.PI * 2;
  while (diff < -Math.PI) diff += Math.PI * 2;
  return a + diff * t;
}

/**
 * Append a sample to the ring.
 *
 * Drops samples whose stamp predates the ring tip — when authority
 * transfers across cells (or hosts under EMA-drifted ClusterClocks),
 * the ex-authority's final in-flight frame can race the new
 * authority's first frame and arrive last; without this drop the
 * ring becomes non-monotonic and interpolateRing's pair-finder picks
 * the wrong bracket — visible as a one-frame jump just past a cell
 * crossing.
 *
 * Evicts the oldest sample when the ring would exceed ringSize.
 */
export function pushSample(ring: SampleRing, s: Sample, ringSize: number): void {
  const samples = ring.samples;
  const tip = samples.length > 0 ? samples[samples.length - 1] : null;
  if (tip && s.producedAtMs < tip.producedAtMs) {
    return;
  }
  samples.push(s);
  if (samples.length > ringSize) {
    samples.shift();
  }
}

/**
 * Compute the interpolated render position for one sample ring at
 * the given render time.
 *
 * - Empty ring → returns null (caller should leave previous render
 *   state untouched).
 * - One sample → static at that sample.
 * - Two or more samples → finds the newest pair that brackets
 *   renderTimeMs and lerps. Past the newest sample, extrapolates
 *   with that sample's velocity, capped to maxExtrapolateMs.
 *
 * Effective-s0 cap: when s0 is much older than (s1 - renderDelayMs)
 * (entity was idle, then moved), tighten the lerp window to the most
 * recent renderDelayMs. Without this cap the first new sample after
 * a long idle gap snaps the render position to s1 in one frame.
 */
export function interpolateRing(
  ring: SampleRing,
  renderTimeMs: number,
  maxExtrapolateMs: number,
  renderDelayMs: number,
): InterpolationResult | null {
  const samples = ring.samples;
  const n = samples.length;
  if (n === 0) return null;
  if (n === 1) {
    const s = samples[0];
    return { renderX: s.worldX, renderY: s.worldY, renderRot: s.rotation };
  }

  let s0 = samples[0];
  let s1 = samples[1];
  for (let i = 1; i < n - 1; i++) {
    if (samples[i].producedAtMs <= renderTimeMs) {
      s0 = samples[i];
      s1 = samples[i + 1];
    }
  }

  const effS0Stamp = Math.max(s0.producedAtMs, s1.producedAtMs - renderDelayMs);

  if (renderTimeMs <= effS0Stamp) {
    return { renderX: s0.worldX, renderY: s0.worldY, renderRot: s0.rotation };
  }
  if (renderTimeMs >= s1.producedAtMs) {
    const extMs = Math.min(renderTimeMs - s1.producedAtMs, maxExtrapolateMs);
    const extS = extMs / 1000;
    return {
      renderX: s1.worldX + s1.velX * extS,
      renderY: s1.worldY + s1.velY * extS,
      renderRot: s1.rotation,
    };
  }
  const t = (renderTimeMs - effS0Stamp) / (s1.producedAtMs - effS0Stamp);
  return {
    renderX: lerp(s0.worldX, s1.worldX, t),
    renderY: lerp(s0.worldY, s1.worldY, t),
    renderRot: lerpAngle(s0.rotation, s1.rotation, t),
  };
}
