import type { ShipEntity, AsteroidEntity, NPCEntity, StationEntity, LootCrateEntity } from "../sdk/index.js";
import type { ClientEntity } from "./types";

export function getShip(ent: ClientEntity): ShipEntity | undefined {
  return ent.current.entityType === 0 ? ent.current : undefined;
}

export function getAsteroid(ent: ClientEntity): AsteroidEntity | undefined {
  return ent.current.entityType === 1 ? ent.current : undefined;
}

export function getNpc(ent: ClientEntity): NPCEntity | undefined {
  return ent.current.entityType === 5 ? ent.current : undefined;
}

export function getStation(ent: ClientEntity): StationEntity | undefined {
  return ent.current.entityType === 3 ? ent.current : undefined;
}

export function getLootCrate(ent: ClientEntity): LootCrateEntity | undefined {
  return ent.current.entityType === 4 ? ent.current : undefined;
}

/**
 * CombatView is a uniform view over Ship/NPC health and shield for renderers.
 * Undefined for entity types that don't have combat (asteroid, station, loot crate).
 */
export interface CombatView {
  health: number;
  maxHealth: number;
  shield: number;
  maxShield: number;
}

export function getCombat(ent: ClientEntity): CombatView | undefined {
  const e = ent.current;
  if (e.entityType === 0) {
    return {
      health: e.healthCurrent,
      maxHealth: e.healthMax,
      shield: e.shieldCurrent,
      maxShield: e.shieldMax,
    };
  }
  if (e.entityType === 5) {
    return {
      health: e.healthCurrent,
      maxHealth: e.healthMax,
      shield: e.shieldCurrent,
      maxShield: e.shieldMax,
    };
  }
  return undefined;
}
