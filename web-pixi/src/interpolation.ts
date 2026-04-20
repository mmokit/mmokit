import type { AnyEntity } from "../sdk/index.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants";
import type { ClientEntity, EntitySample } from "./types";
import { type ClockSync, estimatedServerNow } from "./clockSync";

export function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

export function lerpAngle(a: number, b: number, t: number): number {
  let diff = b - a;
  while (diff > Math.PI) diff -= Math.PI * 2;
  while (diff < -Math.PI) diff += Math.PI * 2;
  return a + diff * t;
}

function entityRotation(e: AnyEntity, fallbackPrev: number): number {
  if ("angle" in e) return e.angle;
  const moving = e.velX !== 0 || e.velY !== 0;
  return moving ? Math.atan2(e.velY, e.velX) : fallbackPrev;
}

function sampleFrom(e: AnyEntity, serverTimeMs: number, prevRot: number): EntitySample {
  return {
    worldX: e.worldX,
    worldY: e.worldY,
    velX: e.velX,
    velY: e.velY,
    rotation: entityRotation(e, prevRot),
    serverTimeMs,
  };
}

/** Append a sample to the entity's ring, evicting the oldest when full. */
export function pushSample(ent: ClientEntity, s: EntitySample): void {
  ent.samples.push(s);
  if (ent.samples.length > RING_SIZE) {
    ent.samples.shift();
  }
}

/**
 * updateEntityFromServer pushes one new authoritative snapshot into the
 * entity's ring (creating the entity if it doesn't exist yet). The
 * server timestamp lets the render loop interpolate on true server-time
 * deltas, immune to network jitter and cell-tick phase drift.
 */
export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  serverTimeMs: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  if (!existing) {
    const rot = entityRotation(serverState, 0);
    const first: EntitySample = sampleFrom(serverState, serverTimeMs, rot);
    entities.set(id, {
      current: serverState,
      samples: [first],
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    });
    return;
  }
  pushSample(existing, sampleFrom(serverState, serverTimeMs, existing.renderRot));
  existing.current = serverState;
}

/**
 * interpolateEntities sets renderX/Y/Rot on every entity by
 * interpolating between the two ring samples that bracket
 * (estimatedServerNow - RENDER_DELAY). Packet loss / phase drift are
 * absorbed naturally; extrapolation past the newest sample is capped.
 */
export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  clock: ClockSync,
  clientNowMs: number,
): void {
  if (!clock.initialized) return;
  const renderTime = estimatedServerNow(clock, clientNowMs) - RENDER_DELAY;

  for (const ent of entities.values()) {
    const n = ent.samples.length;
    if (n === 0) continue;

    if (n === 1) {
      applyStatic(ent, ent.samples[0]);
      continue;
    }

    // Find the newest pair (s0, s1) where s0.time ≤ renderTime ≤ s1.time.
    let s0 = ent.samples[0];
    let s1 = ent.samples[1];
    for (let i = 1; i < n - 1; i++) {
      if (ent.samples[i].serverTimeMs <= renderTime) {
        s0 = ent.samples[i];
        s1 = ent.samples[i + 1];
      }
    }

    if (renderTime <= s0.serverTimeMs) {
      applyStatic(ent, s0);
    } else if (renderTime >= s1.serverTimeMs) {
      // Past newest — extrapolate using current sample's velocity, capped.
      const extMs = Math.min(renderTime - s1.serverTimeMs, MAX_EXTRAPOLATE_MS);
      const extS = extMs / 1000;
      ent.renderX = s1.worldX + s1.velX * extS;
      ent.renderY = s1.worldY + s1.velY * extS;
      ent.renderRot = s1.rotation;
    } else {
      const t = (renderTime - s0.serverTimeMs) / (s1.serverTimeMs - s0.serverTimeMs);
      ent.renderX = lerp(s0.worldX, s1.worldX, t);
      ent.renderY = lerp(s0.worldY, s1.worldY, t);
      ent.renderRot = lerpAngle(s0.rotation, s1.rotation, t);
    }
  }
}

function applyStatic(ent: ClientEntity, s: EntitySample): void {
  ent.renderX = s.worldX;
  ent.renderY = s.worldY;
  ent.renderRot = s.rotation;
}
