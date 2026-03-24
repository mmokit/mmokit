import { EntityType } from "@gen/game_pb.js";

export const TICK_INTERVAL = 50; // 20Hz = 50ms
export const SECTOR_SIZE = 8192; // must match pkg/coords SectorSize
export const MAX_CHAT_DISPLAY = 50;
export const MAX_THRUSTER_PARTICLES = 20;
export const TOAST_DURATION = 3000;

export const ENTITY_COLORS: Record<number, number> = {
  [EntityType.SHIP]: 0x44aaff,
  [EntityType.ASTEROID]: 0xaa8866,
  [EntityType.PROJECTILE]: 0xffff44,
  [EntityType.STATION]: 0x88ff88,
  [EntityType.NPC]: 0xff4444,
};

// Item color mappings by item ID
export const ITEM_COLORS_HEX: Record<number, number> = {
  1: 0x44ff88, // Credits (currency)
  2: 0xcc9900, // Ore
  3: 0xaa44ff, // Crystal
  4: 0x44ddff, // Gas
  5: 0xaaaaaa, // Metal
};

export const ITEM_COLORS_CSS: Record<number, string> = {
  1: "#4f8", // Credits
  2: "#c90", // Ore
  3: "#a4f", // Crystal
  4: "#4df", // Gas
  5: "#aaa", // Metal
};

// Fallback names/colors for items not in the registry (shouldn't happen in practice)
export const DEFAULT_ITEM_COLOR = "#888";

// Resource visuals keyed by item ID.
export const RESOURCE_COLORS_HEX: Record<number, number> = {
  2: 0xcc9900,  // Ore
  3: 0xaa44ff,  // Crystal
  4: 0x44ddff,  // Gas
  5: 0xaaaaaa,  // Metal
};
export const RESOURCE_COLORS_CSS: Record<number, string> = {
  2: "#c90",    // Ore
  3: "#a4f",    // Crystal
  4: "#4df",    // Gas
  5: "#aaa",    // Metal
};
export const RESOURCE_NAMES: Record<number, string> = {
  2: "Ore",
  3: "Crystal",
  4: "Gas",
  5: "Metal",
};
