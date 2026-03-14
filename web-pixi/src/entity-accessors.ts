import type { EntityState, CombatState, ShipState, NpcState, AsteroidState, LootCrateState } from "@gen/game_pb.js";

export function getCombat(e: EntityState): CombatState | undefined {
  switch (e.typeData.case) {
    case "ship": return e.typeData.value.combat;
    case "npc": return e.typeData.value.combat;
    default: return undefined;
  }
}

export function getShip(e: EntityState): ShipState | undefined {
  return e.typeData.case === "ship" ? e.typeData.value : undefined;
}

export function getNpc(e: EntityState): NpcState | undefined {
  return e.typeData.case === "npc" ? e.typeData.value : undefined;
}

export function getAsteroid(e: EntityState): AsteroidState | undefined {
  return e.typeData.case === "asteroid" ? e.typeData.value : undefined;
}

export function getLootCrate(e: EntityState): LootCrateState | undefined {
  return e.typeData.case === "lootCrate" ? e.typeData.value : undefined;
}
