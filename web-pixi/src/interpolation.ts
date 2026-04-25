import type { AnyEntity } from "../sdk/index.js";
import {
  pushSample as coreSPush,
  interpolateRing,
  lerp,
  lerpAngle,
} from "../sdk/_core/interpolation-core.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants";
import type { ClientEntity, EntitySample } from "./types";
import { type ClockSync, estimatedServerNow } from "./clockSync";

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
  };
}

export function pushSample(ent: ClientEntity, s: EntitySample): void {
  coreSPush(ent, s, RING_SIZE);
}

export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  if (!existing) {
    const rot = entityRotation(serverState, 0);
    const first: EntitySample = sampleFrom(serverState, producedAtMs, rot);
    entities.set(id, {
      current: serverState,
      samples: [first],
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    });
    return;
  }
  pushSample(existing, sampleFrom(serverState, producedAtMs, existing.renderRot));
  existing.current = serverState;
}

export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  clock: ClockSync,
  clientNowMs: number,
): void {
  if (!clock.initialized) return;
  const renderTime = estimatedServerNow(clock, clientNowMs) - RENDER_DELAY;
  for (const ent of entities.values()) {
    const r = interpolateRing(ent, renderTime, MAX_EXTRAPOLATE_MS, RENDER_DELAY);
    if (r) {
      ent.renderX = r.renderX;
      ent.renderY = r.renderY;
      ent.renderRot = r.renderRot;
    }
  }
}

export { lerp, lerpAngle };
