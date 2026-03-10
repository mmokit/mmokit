import { EntityType } from "@gen/game_pb.js";

export const TICK_INTERVAL = 50; // 20Hz = 50ms
export const MAX_CARGO = 100;
export const MAX_CHAT_DISPLAY = 50;
export const MAX_THRUSTER_PARTICLES = 20;
export const TOAST_DURATION = 3000;

export const ENTITY_COLORS: Record<number, number> = {
  [EntityType.SHIP]: 0x44aaff,
  [EntityType.ASTEROID]: 0xaa8866,
  [EntityType.PROJECTILE]: 0xffff44,
  [EntityType.STATION]: 0x88ff88,
};

export const RESOURCE_COLORS_HEX: number[] = [0xcc9900, 0xaa44ff, 0x44ddff, 0xaaaaaa];
export const RESOURCE_COLORS_CSS: string[] = ["#c90", "#a4f", "#4df", "#aaa"];
export const RESOURCE_NAMES: string[] = ["Ore", "Crystal", "Gas", "Metal"];
