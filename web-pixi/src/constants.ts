import { EntityType } from "@gen/game_pb.js";

export const TICK_INTERVAL = 50; // 20Hz = 50ms
export const CELL_SIZE = 8192; // must match pkg/coords CellSize
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

// Snapshot interpolation constants (Spec 1 Time & Transparency).

/**
 * Number of samples retained per entity in the interpolation ring. Must
 * be ≥ ⌈RENDER_DELAY / tickInterval⌉ + 2 so the ring always covers the
 * render-time cursor even when a post-split cell hands off frames on a
 * compressed phase (e.g. 25ms after the source's last frame instead of
 * the steady 50ms). Dropping to 3 visibly snaps by ~25ms at handoff;
 * 4 keeps a full tick of margin behind renderTime.
 */
export const RING_SIZE = 4;

/**
 * How far behind the latest server snapshot the client renders, in
 * milliseconds. Matches Source's cl_interp 0.1 default. Two tick
 * intervals at 20Hz — absorbs one dropped frame plus typical phase
 * jitter between cell goroutines.
 */
export const RENDER_DELAY = 100;

/**
 * Maximum forward velocity extrapolation when render time runs past the
 * newest sample (sustained packet loss). Capped at one tick's worth so
 * extrapolation stays bounded and visibly pauses rather than diverging.
 */
export const MAX_EXTRAPOLATE_MS = 50;

/**
 * Smoothing factor for the clock-offset exponential moving average.
 * 0.1 = converges to a new steady-state offset in ~20 observations
 * (one second at 20Hz) while rejecting short spikes.
 */
export const CLOCK_SYNC_ALPHA = 0.1;
