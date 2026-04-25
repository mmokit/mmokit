import type { AnyEntity } from "../sdk/entities.js";
import {
  pushSample as coreSPush,
  interpolateRing,
  lerp,
  lerpAngle,
} from "../sdk/_core/interpolation-core.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants.js";
import type { ClientEntity, EntitySample } from "./state.js";
import { type ClockSync, estimatedServerNow } from "./clockSync.js";

function entityRotation(e: AnyEntity, fallbackPrev: number): number {
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

/**
 * updateEntityFromServer pushes one new authoritative snapshot into
 * the entity's ring (creating the ClientEntity if it doesn't exist
 * yet). The per-entity producedAtMs stamp lets the render loop
 * interpolate on true ClusterClock-aligned server-time deltas,
 * immune to network jitter and cell-tick phase drift.
 */
export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);

  if (!existing) {
    const rot = entityRotation(serverState, 0);
    const first = sampleFrom(serverState, producedAtMs, rot);
    const ent: ClientEntity = {
      ...serverState,
      prevX: serverState.worldX,
      prevY: serverState.worldY,
      isReplica: false,
      isGhost: false,
      samples: [first],
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    };
    entities.set(id, ent);
    return;
  }
  const prevRot = existing.renderRot;
  Object.assign(existing, serverState);
  existing.prevX = existing.renderX;
  existing.prevY = existing.renderY;
  pushSample(existing, sampleFrom(serverState, producedAtMs, prevRot));
}

/**
 * interpolateEntities sets renderX/Y/Rot on every entity by
 * interpolating between the two ring samples that bracket
 * (estimatedServerNow - RENDER_DELAY). Packet loss / phase drift
 * are absorbed naturally; extrapolation past the newest sample is
 * capped.
 */
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
