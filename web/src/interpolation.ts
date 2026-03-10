import type { EntityState } from "@gen/game_pb.js";
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

export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: EntityState,
): void {
  const id = serverState.id;
  const ent = entities.get(id);

  if (!ent) {
    entities.set(id, {
      prev: { ...serverState },
      curr: { ...serverState },
      renderX: serverState.x,
      renderY: serverState.y,
      renderRot: serverState.rotation,
      thrusterParticles: [],
    } as ClientEntity);
  } else {
    ent.prev = ent.curr;
    ent.curr = { ...serverState } as EntityState;
  }
}

export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  t: number,
): void {
  for (const [, ent] of entities) {
    const p = ent.prev;
    const c = ent.curr;

    if (t <= 1.0) {
      ent.renderX = lerp(p.x, c.x, t);
      ent.renderY = lerp(p.y, c.y, t);
      ent.renderRot = lerpAngle(p.rotation, c.rotation, t);
    } else {
      const dt = ((t - 1.0) * TICK_INTERVAL) / 1000;
      ent.renderX = c.x + c.vx * dt;
      ent.renderY = c.y + c.vy * dt;
      ent.renderRot = c.rotation;
    }
  }
}
