/**
 * View/zoom management for world-space rendering.
 *
 * The world container is scaled so that a fixed number of world units
 * are visible across the screen width. All pixel-based rendering values
 * (stroke widths, font sizes, particle sizes) should use px() to convert
 * screen pixels into world units at the current zoom level.
 */

const DEFAULT_VIEWPORT = 128;
const MIN_VIEWPORT = 32;
const MAX_VIEWPORT = DEFAULT_VIEWPORT;

let viewportUnits = DEFAULT_VIEWPORT;
let currentZoom = 30; // pixels per world unit; recomputed on resize
let lastScreenWidth = 0;

/** Current zoom level (screen pixels per world unit). */
export function zoom(): number {
  return currentZoom;
}

/**
 * Convert screen pixels to world units at the current zoom.
 * Use for stroke widths, font sizes, offsets, particle sizes —
 * anything that should appear at a fixed screen-pixel size.
 *
 * Example: `px(2)` → a 2-pixel stroke width in world units.
 */
export function px(screenPixels: number): number {
  return screenPixels / currentZoom;
}

/**
 * Recompute zoom from screen width. Call on resize.
 * Returns the new zoom value for applying to the world container scale.
 */
export function updateZoom(screenWidth: number): number {
  lastScreenWidth = screenWidth;
  currentZoom = screenWidth / viewportUnits;
  return currentZoom;
}

/**
 * Adjust viewport units by a scroll delta. Positive delta zooms out.
 * Returns the new zoom value, or null if unchanged (already at limit).
 */
export function scrollZoom(delta: number): number | null {
  const step = 4;
  const prev = viewportUnits;
  viewportUnits = Math.min(MAX_VIEWPORT, Math.max(MIN_VIEWPORT, viewportUnits + Math.sign(delta) * step));
  if (viewportUnits === prev) return null;
  if (lastScreenWidth > 0) currentZoom = lastScreenWidth / viewportUnits;
  return currentZoom;
}
