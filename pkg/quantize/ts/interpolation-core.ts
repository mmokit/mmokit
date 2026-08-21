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
import type { Quat } from "./delta-decoder-core.js";

export interface Sample {
  worldX: number;
  worldY: number;
  velX: number;
  velY: number;
  rotation: number;
  producedAtMs: number;
  /**
   * Optional 3D state. A 2D profile omits all three and every path below
   * behaves exactly as it did — which is what keeps a 2D client's render
   * loop untouched by the 3D profile existing.
   */
  worldZ?: number;
  velZ?: number;
  /** Full orientation. Takes precedence over `rotation` when present. */
  quat?: Quat;
  /**
   * Optional authority generation for this entity. When present, newer
   * generations supersede older in-flight samples even if those samples
   * arrive later. Older clients and games may omit it.
   */
  authorityEpoch?: number;
}

/** A ring of samples ordered ascending by producedAtMs. */
export interface SampleRing {
  samples: Sample[];
  /** Newest explicit authority epoch accepted by this ring. */
  authorityEpoch?: number;
}

/** Interpolated render position computed by interpolateRing. */
export interface InterpolationResult {
  renderX: number;
  renderY: number;
  renderRot: number;
  /** Present only when the samples carried 3D state. */
  renderZ?: number;
  renderQuat?: Quat;
  /** How the returned pose was produced. */
  mode: "interpolate" | "extrapolate" | "hold";
  /** Velocity-projection time used by the returned pose. */
  extrapolatedMs: number;
}

export interface PushSampleResult {
  accepted: boolean;
  /** True when a newer authority epoch forced a backwards timeline reset. */
  reset: boolean;
  reason?: "stale-time" | "older-epoch";
}

const UINT32_HALF_RANGE = 0x80000000;

function authorityEpochRelation(
  current: number,
  incoming: number,
): "same" | "newer" | "older" {
  const distance = ((incoming >>> 0) - (current >>> 0)) >>> 0;
  if (distance === 0) return "same";
  // RFC 1982 serial-number ordering. The exact half-range is deliberately
  // treated as ambiguous/old rather than allowing both sides to be newer.
  return distance < UINT32_HALF_RANGE ? "newer" : "older";
}

/** Linear interpolation between a and b at fraction t in [0, 1]. */
/**
 * The |dot| above which slerpQuat falls back to a normalized lerp.
 *
 * Kept identical to component.SlerpDotThreshold in the Go reference, which
 * exports it for exactly this reason: it is the single most port-divergent
 * line in the orientation path, and a client that picked a different value
 * would produce visibly different intermediate frames near the seam with
 * nothing else to catch it.
 */
export const SLERP_DOT_THRESHOLD = 0.9995;

/**
 * Interpolate from a to b along the shortest arc.
 *
 * Mirrors component.Rotation.Slerp clause for clause: t clamps to [0,1]; a
 * negative dot negates the target first, because q and -q are the same
 * rotation and without it the interpolation takes the long way round; above
 * SLERP_DOT_THRESHOLD it degrades to a normalized lerp, where sin(theta)
 * underflows and the trigonometric form loses precision; the result is always
 * normalized and a zero-norm input is identity.
 */
export function slerpQuat(a: Quat, b: Quat, t: number): Quat {
  if (t <= 0) return normalizeQuat(a);
  if (t >= 1) return normalizeQuat(b);

  const na = normalizeQuat(a);
  const nb = normalizeQuat(b);

  let bx = nb.x, by = nb.y, bz = nb.z, bw = nb.w;
  let dot = na.x * bx + na.y * by + na.z * bz + na.w * bw;
  if (dot < 0) {
    bx = -bx; by = -by; bz = -bz; bw = -bw;
    dot = -dot;
  }
  if (dot > 1) dot = 1;

  let wa: number;
  let wb: number;
  if (dot > SLERP_DOT_THRESHOLD) {
    wa = 1 - t;
    wb = t;
  } else {
    const theta = Math.acos(dot);
    const sin = Math.sin(theta);
    wa = Math.sin((1 - t) * theta) / sin;
    wb = Math.sin(t * theta) / sin;
  }

  return normalizeQuat({
    x: wa * na.x + wb * bx,
    y: wa * na.y + wb * by,
    z: wa * na.z + wb * bz,
    w: wa * na.w + wb * bw,
  });
}

function normalizeQuat(q: Quat): Quat {
  const n = Math.sqrt(q.x * q.x + q.y * q.y + q.z * q.z + q.w * q.w);
  if (n === 0) return { x: 0, y: 0, z: 0, w: 1 };
  return { x: q.x / n, y: q.y / n, z: q.z / n, w: q.w / n };
}

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
export function pushSample(ring: SampleRing, s: Sample, ringSize: number): PushSampleResult {
  const samples = ring.samples;
  let tip = samples.length > 0 ? samples[samples.length - 1] : null;

  // Authority epochs are uint32 serial numbers and can wrap. An omitted epoch
  // preserves the legacy timestamp-only behavior.
  if (s.authorityEpoch !== undefined) {
    const incomingEpoch = s.authorityEpoch >>> 0;
    const relation = ring.authorityEpoch === undefined
      ? "newer"
      : authorityEpochRelation(ring.authorityEpoch, incomingEpoch);
    if (relation === "older") {
      return { accepted: false, reset: false, reason: "older-epoch" };
    }
    if (relation === "newer") {
      ring.authorityEpoch = incomingEpoch;
      // A handoff can move to a host whose clock estimate stamps its first
      // sample behind the old tip. Start a new monotonic timeline in that
      // case. Keep the old samples when timestamps remain seamless so a
      // normal handoff does not introduce an avoidable interpolation hitch.
      if (tip && s.producedAtMs < tip.producedAtMs) {
        samples.length = 0;
        tip = null;
        samples.push(s);
        return { accepted: true, reset: true };
      }
    }
  }

  if (tip && s.producedAtMs < tip.producedAtMs) {
    return { accepted: false, reset: false, reason: "stale-time" };
  }
  samples.push(s);
  if (samples.length > ringSize) {
    samples.shift();
  }
  return { accepted: true, reset: false };
}

/**
 * Reports whether a sample stamped producedAtMs is older than the newest
 * sample already in the ring — i.e. whether pushSample would drop it.
 *
 * Glue code snapshots an entity's non-interpolated fields (radius/size,
 * health, shield, …) straight onto the latest server state, separately from
 * the position ring. Gate that snapshot on this predicate so those fields obey
 * the SAME monotonicity rule pushSample enforces for position: at a cell
 * boundary the same netID is delivered from two cell authorities (the demoting
 * source and the promoting destination), and the ex-authority's final
 * in-flight frame can arrive last. Without the gate it snaps non-interpolated
 * fields backward for a frame — a visible flicker — even though the position
 * ring correctly ignores it.
 */
export function isStaleSample(
  ring: SampleRing,
  producedAtMs: number,
  authorityEpoch?: number,
): boolean {
  if (
    authorityEpoch !== undefined &&
    ring.authorityEpoch !== undefined
  ) {
    const relation = authorityEpochRelation(ring.authorityEpoch, authorityEpoch);
    if (relation === "older") return true;
    // A newer epoch is accepted even with a backwards stamp: pushSample will
    // reset the old timeline safely.
    if (relation === "newer") return false;
  }
  const samples = ring.samples;
  const tip = samples.length > 0 ? samples[samples.length - 1] : null;
  return tip !== null && producedAtMs < tip.producedAtMs;
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
    return {
      renderX: s.worldX,
      renderY: s.worldY,
      renderRot: s.rotation,
      ...(s.worldZ !== undefined ? { renderZ: s.worldZ } : {}),
      ...(s.quat !== undefined ? { renderQuat: s.quat } : {}),
      mode: "hold",
      extrapolatedMs: 0,
    };
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
    return {
      renderX: s0.worldX,
      renderY: s0.worldY,
      renderRot: s0.rotation,
      ...(s0.worldZ !== undefined ? { renderZ: s0.worldZ } : {}),
      ...(s0.quat !== undefined ? { renderQuat: s0.quat } : {}),
      mode: "hold",
      extrapolatedMs: 0,
    };
  }
  if (renderTimeMs >= s1.producedAtMs) {
    const requestedMs = Math.max(0, renderTimeMs - s1.producedAtMs);
    const extMs = Math.min(requestedMs, Math.max(0, maxExtrapolateMs));
    const extS = extMs / 1000;
    return {
      renderX: s1.worldX + s1.velX * extS,
      renderY: s1.worldY + s1.velY * extS,
      renderRot: s1.rotation,
      // Orientation is HELD rather than extrapolated, matching renderRot.
      // Projecting a rotation forward needs an angular velocity the wire
      // does not carry, and guessing one produces visible overshoot.
      ...(s1.worldZ !== undefined ? { renderZ: s1.worldZ + (s1.velZ ?? 0) * extS } : {}),
      ...(s1.quat !== undefined ? { renderQuat: s1.quat } : {}),
      mode: extMs > 0 && requestedMs <= maxExtrapolateMs ? "extrapolate" : "hold",
      extrapolatedMs: extMs,
    };
  }
  const t = (renderTimeMs - effS0Stamp) / (s1.producedAtMs - effS0Stamp);
  return {
    renderX: lerp(s0.worldX, s1.worldX, t),
    renderY: lerp(s0.worldY, s1.worldY, t),
    renderRot: lerpAngle(s0.rotation, s1.rotation, t),
    ...(s0.worldZ !== undefined && s1.worldZ !== undefined
      ? { renderZ: lerp(s0.worldZ, s1.worldZ, t) }
      : {}),
    ...(s0.quat !== undefined && s1.quat !== undefined
      ? { renderQuat: slerpQuat(s0.quat, s1.quat, t) }
      : {}),
    mode: "interpolate",
    extrapolatedMs: 0,
  };
}
