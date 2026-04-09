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
 * updateEntityFromServer promotes the current render state to prev and installs
 * the new server snapshot as `current`. The server no longer broadcasts per-entity
 * rotation — we derive a visual rotation from the velocity direction for moving
 * entities and keep the previous rotation for stationary ones.
 */
export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  const moving = serverState.velX !== 0 || serverState.velY !== 0;
  const targetRot = moving ? Math.atan2(serverState.velY, serverState.velX) : 0;
  if (!existing) {
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
  // Promote current render position to prev so interpolation starts from where
  // the entity visually is right now (avoids snap-back on each new snapshot).
  existing.prevX = existing.renderX;
  existing.prevY = existing.renderY;
  existing.prevRot = existing.renderRot;
  existing.current = serverState;
}

export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  t: number,
): void {
  for (const ent of entities.values()) {
    const c = ent.current;
    const moving = c.velX !== 0 || c.velY !== 0;
    const targetRot = moving ? Math.atan2(c.velY, c.velX) : ent.prevRot;
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
