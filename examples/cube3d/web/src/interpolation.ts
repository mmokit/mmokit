/**
 * Per-entity snapshot interpolation for the 3D profile.
 *
 * Without this the client renders whatever the last snapshot said, which at a
 * 20 Hz tick rate means 50 ms steps — visible as jitter on everything,
 * worst on the tumbling cubes because their orientation changes every tick.
 *
 * Nothing here is 3D-specific machinery: the SDK's InterpolationBuffer stores
 * whole `Sample` values and `worldZ`, `velZ` and `quat` are optional fields on
 * `Sample`, so height and orientation ride through the existing ring for free.
 * `interpolateRing` lerps Z and slerps the quaternion. This file is only the
 * wiring examples/space has had all along.
 *
 * Imports the SDK's _core, which imports nothing bare — so this module stays
 * reachable from a test without pulling three.js in. See flycontrol.ts.
 */
import type { Quat } from "../sdk/_core/delta-decoder-core.js";
import type { Sample } from "../sdk/_core/interpolation-core.js";
import { InterpolationBuffer } from "../sdk/_core/interpolation-buffer.js";
import type { AdaptivePlaybackController } from "../sdk/_core/playback-controller.js";

/** Ring depth. Must cover RENDER_DELAY plus a couple of ticks of slack. */
export const RING_SIZE = 8;

/**
 * How far behind the producer's clock to render, in ms.
 *
 * Two server ticks at 20 Hz. Interpolation can only smooth between samples it
 * already has, so rendering at "now" would always be extrapolating; the delay
 * is what buys a sample on each side. It is the latency you trade for
 * smoothness, and it applies to other entities only — your own input is
 * unaffected because nothing here predicts it.
 */
export const RENDER_DELAY_MS = 100;

/** Cap on how far past the newest sample the ring will project. */
export const MAX_EXTRAPOLATE_MS = 50;

/** The minimum an entity must expose for this module to interpolate it. */
export interface ServerEntity {
  netID: number;
  worldX: number;
  worldY: number;
  worldZ: number;
  velX: number;
  velY: number;
  velZ: number;
  rot: Quat;
  authorityEpoch: number;
}

/** An entity plus its interpolated render pose. */
export interface RenderEntity {
  current: ServerEntity;
  buffer: InterpolationBuffer;
  renderX: number;
  renderY: number;
  renderZ: number;
  renderQuat: Quat;
}

/**
 * Build the Sample the ring stores.
 *
 * `rotation` is left at 0 deliberately: it is the 2D yaw channel, and this
 * profile carries full orientation in `quat` instead. Populating both would
 * mean two sources of truth for the same thing, and interpolateRing would
 * lerpAngle one of them into a value nothing reads.
 */
export function sampleFrom(e: ServerEntity, producedAtMs: number): Sample {
  return {
    worldX: e.worldX,
    worldY: e.worldY,
    worldZ: e.worldZ,
    velX: e.velX,
    velY: e.velY,
    velZ: e.velZ,
    quat: e.rot,
    rotation: 0,
    producedAtMs,
    authorityEpoch: e.authorityEpoch,
  };
}

export function newBuffer(): InterpolationBuffer {
  return new InterpolationBuffer({
    ringSize: RING_SIZE,
    renderDelayMs: RENDER_DELAY_MS,
    maxExtrapolateMs: MAX_EXTRAPOLATE_MS,
  });
}

/**
 * Fold one server snapshot into an entity's ring.
 *
 * The staleness gate covers the WHOLE snapshot, not just the ring: a frame
 * that loses the monotonicity check is dropped entirely, so non-interpolated
 * fields cannot snap backward when an ex-authority's final frame arrives after
 * the new authority's first. That is the cell-boundary case.
 */
export function updateEntityFromServer(
  entities: Map<number, RenderEntity>,
  serverState: ServerEntity,
  producedAtMs: number,
  allowStreamReset = false,
): void {
  const existing = entities.get(serverState.netID);
  if (!existing) {
    const buffer = newBuffer();
    const first = sampleFrom(serverState, producedAtMs);
    buffer.push(first);
    entities.set(serverState.netID, {
      current: serverState,
      buffer,
      renderX: serverState.worldX,
      renderY: serverState.worldY,
      renderZ: serverState.worldZ,
      renderQuat: serverState.rot,
    });
    return;
  }
  if (existing.buffer.isStale(producedAtMs, serverState.authorityEpoch)) {
    if (!allowStreamReset) return;
    // A fresh snapshot on a newer enclosing stream supersedes the old
    // stream's per-entity history — needed when a transfer's destination
    // emitted a newer epoch before a rollback resumed the source.
    existing.buffer.reset();
  }
  existing.buffer.push(sampleFrom(serverState, producedAtMs));
  existing.current = serverState;
}

/** Advance every entity's render pose to the current playback time. */
export function interpolateEntities(
  entities: Map<number, RenderEntity>,
  playback: AdaptivePlaybackController,
  clientNowMs: number,
): void {
  const renderTime = playback.renderTime(clientNowMs);
  if (renderTime === null) return;
  for (const ent of entities.values()) {
    const r = ent.buffer.sampleAt(renderTime, playback.currentDelayMs);
    if (!r) continue;
    ent.renderX = r.renderX;
    ent.renderY = r.renderY;
    // Absent means the samples carried no 3D state, which for this profile
    // would be a bug — but holding the last pose beats snapping to the origin.
    if (r.renderZ !== undefined) ent.renderZ = r.renderZ;
    if (r.renderQuat !== undefined) ent.renderQuat = r.renderQuat;
  }
}
