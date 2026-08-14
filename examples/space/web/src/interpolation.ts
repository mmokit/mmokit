import type { AnyEntity } from "../sdk/index.js";
import { lerp, lerpAngle } from "../sdk/_core/interpolation-core.js";
import { InterpolationBuffer } from "../sdk/_core/interpolation-buffer.js";
import type { AdaptivePlaybackController } from "../sdk/_core/playback-controller.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants";
import type { ClientEntity, EntitySample } from "./types";

function entityRotation(e: AnyEntity, fallbackPrev: number): number {
  if ("angle" in e) return e.angle as number;
  const moving = e.velX !== 0 || e.velY !== 0;
  return moving ? Math.atan2(e.velY, e.velX) : fallbackPrev;
}

function sampleFrom(e: AnyEntity, producedAtMs: number, prevRot: number): EntitySample {
  return {
    worldX: e.worldX,
    worldY: e.worldY,
    velX: e.velX,
    velY: e.velY,
    rotation: entityRotation(e, prevRot),
    producedAtMs,
    authorityEpoch: e.authorityEpoch,
  };
}

function newBuffer(): InterpolationBuffer {
  return new InterpolationBuffer({
    ringSize: RING_SIZE,
    renderDelayMs: RENDER_DELAY,
    maxExtrapolateMs: MAX_EXTRAPOLATE_MS,
  });
}

export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
  allowStreamReset = false,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  if (!existing) {
    const buffer = newBuffer();
    const first = sampleFrom(serverState, producedAtMs, 0);
    buffer.push(first);
    entities.set(id, {
      current: serverState,
      buffer,
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    });
    return;
  }
  // Gate the whole snapshot on the same monotonicity rule the ring uses, so
  // non-interpolated fields (size/health/…) don't snap backward at a cell
  // boundary when the ex-authority's final frame arrives last.
  if (existing.buffer.isStale(producedAtMs, serverState.authorityEpoch)) {
    if (!allowStreamReset) return;
    // A fresh snapshot on a newer enclosing stream supersedes the old
    // stream's per-entity authority history. This is needed when a transfer's
    // destination emitted epoch N+1 before rollback resumes the source at
    // stream N+2 with its still-valid entity epoch N.
    existing.buffer.reset();
  }
  existing.buffer.push(sampleFrom(serverState, producedAtMs, existing.renderRot));
  existing.current = serverState;
}

export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  playback: AdaptivePlaybackController,
  clientNowMs: number,
): void {
  const renderTime = playback.renderTime(clientNowMs);
  if (renderTime === null) return;
  for (const ent of entities.values()) {
    const r = ent.buffer.sampleAt(renderTime, playback.currentDelayMs);
    if (r) {
      ent.renderX = r.renderX;
      ent.renderY = r.renderY;
      ent.renderRot = r.renderRot;
    }
  }
}

export { lerp, lerpAngle };
