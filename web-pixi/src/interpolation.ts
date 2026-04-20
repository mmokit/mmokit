import type { AnyEntity } from "../sdk/index.js";
import { TICK_INTERVAL } from "./constants";
import type { ClientEntity } from "./types";

export function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

export function lerpAngle(a: number, b: number, t: number): number {
  let diff = b - a;
  while (diff > Math.PI) diff -= Math.PI * 2;
  while (diff < -Math.PI) diff += Math.PI * 2;
  return a + diff * t;
}

/**
 * entityRotation extracts the server-authoritative rotation if the entity
 * carries one, otherwise falls back to velocity direction (for moving
 * entities) or 0 (stationary). Only ShipEntity currently replicates rotation
 * — asteroids, stations, and loot crates derive it from velocity direction
 * since they either don't rotate or spin independently.
 */
function entityRotation(e: AnyEntity, fallbackPrev: number): number {
  if ("angle" in e) return e.angle;
  const moving = e.velX !== 0 || e.velY !== 0;
  return moving ? Math.atan2(e.velY, e.velX) : fallbackPrev;
}

/**
 * updateEntityFromServer promotes the current render state to prev and installs
 * the new server snapshot as `current`. Ships use the server-authoritative
 * angle so rotation-in-place is visible; other entities derive rotation from
 * velocity direction.
 *
 * anchorToRender=true is set on FRAME_FLAG_FRESH_SNAPSHOT frames (login or
 * cross-cell handoff). On those frames the destination cell's tick phase may
 * differ from the source's, so anchoring prev to the currently-rendered
 * position — rather than to the previous server snapshot — hides the phase
 * mismatch by interpolating from the last drawn position to the new
 * authoritative state. Accepted cost: a slight apparent speed-up for one
 * tick window on handoff (the client is catching up with server
 * simulation that advanced 50ms during 25ms of wall-clock). Proper fix is
 * a ring-buffered snapshot interpolator with render delay (Source/Gaffer);
 * left as a follow-up.
 */
export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  anchorToRender = false,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  if (!existing) {
    const targetRot = entityRotation(serverState, 0);
    entities.set(id, {
      current: serverState,
      prevX: serverState.worldX,
      prevY: serverState.worldY,
      prevRot: targetRot,
      renderX: serverState.worldX,
      renderY: serverState.worldY,
      renderRot: targetRot,
    });
    return;
  }
  if (anchorToRender) {
    existing.prevX = existing.renderX;
    existing.prevY = existing.renderY;
    existing.prevRot = existing.renderRot;
  } else {
    existing.prevX = existing.current.worldX;
    existing.prevY = existing.current.worldY;
    existing.prevRot = entityRotation(existing.current, existing.prevRot);
  }
  existing.current = serverState;
}

export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  t: number,
): void {
  for (const ent of entities.values()) {
    const c = ent.current;
    const targetRot = entityRotation(c, ent.prevRot);
    if (t <= 1.0) {
      ent.renderX = lerp(ent.prevX, c.worldX, t);
      ent.renderY = lerp(ent.prevY, c.worldY, t);
      ent.renderRot = lerpAngle(ent.prevRot, targetRot, t);
    } else {
      const dt = ((t - 1.0) * TICK_INTERVAL) / 1000;
      ent.renderX = c.worldX + c.velX * dt;
      ent.renderY = c.worldY + c.velY * dt;
      ent.renderRot = targetRot;
    }
  }
}
