import { EntityType } from "@gen/game_pb.js";

export const TICK_INTERVAL = 50; // 20Hz = 50ms
export const MAX_CARGO = 100;
export const MAX_CHAT_DISPLAY = 50;
export const MAX_THRUSTER_PARTICLES = 20;
export const TOAST_DURATION = 3000;

export const ENTITY_COLORS: Record<number, string> = {
  [EntityType.SHIP]: "#4af",
  [EntityType.ASTEROID]: "#a86",
  [EntityType.PROJECTILE]: "#ff4",
  [EntityType.STATION]: "#8f8",
};

export const RESOURCE_COLORS = ["#c90", "#a4f", "#4df", "#aaa"]; // Ore, Crystal, Gas, Metal
export const RESOURCE_NAMES = ["Ore", "Crystal", "Gas", "Metal"];
