import type { ShipEntity, AsteroidEntity, NPCEntity, StationEntity, LootCrateEntity } from "../sdk/index.js";
import { EntityType } from "../sdk/index.js";
import type { ClientEntity } from "./types";

export function getShip(ent: ClientEntity): ShipEntity | undefined {
  return ent.current.entityType === EntityType.Ship ? ent.current : undefined;
}

export function getAsteroid(ent: ClientEntity): AsteroidEntity | undefined {
  return ent.current.entityType === EntityType.Asteroid ? ent.current : undefined;
}

export function getNpc(ent: ClientEntity): NPCEntity | undefined {
  return ent.current.entityType === EntityType.NPC ? ent.current : undefined;
}

export function getStation(ent: ClientEntity): StationEntity | undefined {
  return ent.current.entityType === EntityType.Station ? ent.current : undefined;
}

export function getLootCrate(ent: ClientEntity): LootCrateEntity | undefined {
  return ent.current.entityType === EntityType.LootCrate ? ent.current : undefined;
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
  if (e.entityType === EntityType.Ship) {
    return {
      health: e.healthCurrent,
      maxHealth: e.healthMax,
      shield: e.shieldCurrent,
      maxShield: e.shieldMax,
    };
  }
  if (e.entityType === EntityType.NPC) {
    return {
      health: e.healthCurrent,
      maxHealth: e.healthMax,
      shield: e.shieldCurrent,
      maxShield: e.shieldMax,
    };
  }
  return undefined;
}
