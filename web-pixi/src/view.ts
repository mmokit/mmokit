/**
 * View/zoom management for world-space rendering.
 *
 * `currentZoom` is the number of screen pixels per world unit. It is a
 * property of the user's chosen zoom level — NOT the window size — so
 * resizing the window changes how much world is visible without
 * changing how large entities appear on screen. (This is the
 * FPS/strategy-game model, not the CSS-responsive model.)
 *
 * Baseline zoom is computed once from the initial window width so the
 * first-load experience matches whatever screen the player happens to
 * be on. Subsequent resizes do NOT touch the baseline; the user can
 * scroll-zoom from there.
 *
 * All pixel-based rendering values (stroke widths, font sizes, particle
 * sizes) should use px() to convert screen pixels into world units at
 * the current zoom level.
 */

const DEFAULT_VIEWPORT = 128;
const MIN_VIEWPORT = 32;
const MAX_VIEWPORT = DEFAULT_VIEWPORT;
const SCROLL_STEP = 4;

let viewportUnits = DEFAULT_VIEWPORT;
let currentZoom = 30; // pixels per world unit; overwritten by initZoom()
let baselineScreenWidth = 0;

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
 * One-shot zoom initialization from the initial window width. Call
 * exactly once at startup, before creating the Camera. After this the
 * baseline is frozen — resize events must not call this again, or
 * entities will rescale with the window (the longstanding bug).
 */
export function initZoom(screenWidth: number): void {
  baselineScreenWidth = screenWidth;
  currentZoom = screenWidth / viewportUnits;
}

/**
 * Adjust viewport units by a scroll delta. Positive delta zooms out.
 * Returns the new zoom value, or null if unchanged (already at limit).
 *
 * Anchored to the baseline screen width, not the current window width,
 * so the zoom step feels consistent regardless of how the user has
 * resized the window since startup.
 */
export function scrollZoom(delta: number): number | null {
  const prev = viewportUnits;
  viewportUnits = Math.min(
    MAX_VIEWPORT,
    Math.max(MIN_VIEWPORT, viewportUnits + Math.sign(delta) * SCROLL_STEP),
  );
  if (viewportUnits === prev) return null;
  if (baselineScreenWidth > 0) {
    currentZoom = baselineScreenWidth / viewportUnits;
  }
  return currentZoom;
}
