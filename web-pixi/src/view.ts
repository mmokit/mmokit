/**
 * View/zoom management for world-space rendering.
 *
 * `viewportUnits` (world units across the canvas width) is the source
 * of truth. `currentZoom` (screen pixels per world unit) is derived as
 * `canvasWidth / viewportUnits` and recomputed on every resize and
 * scroll-zoom. This is the viewport-anchored model: window resize keeps
 * the visible world width roughly constant; entities scale with the
 * canvas. Sidebar-open shrinks the canvas, which proportionally shrinks
 * entity render size — the same world span stays visible.
 *
 * `MAX_VIEWPORT = 128` is the PvP fairness cap on visible world width.
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
let currentZoom = 0; // overwritten by first recomputeZoom (called from camera.resize)

/** Current zoom level (screen pixels per world unit). */
export function zoom(): number {
  return currentZoom;
}

/**
 * Convert screen pixels to world units at the current zoom.
 * Use for stroke widths, font sizes, offsets, particle sizes —
 * anything that should appear at a fixed screen-pixel size.
 */
export function px(screenPixels: number): number {
  return screenPixels / currentZoom;
}

/**
 * Recompute `currentZoom` from the canvas width. Called by `Camera.resize`
 * on every window resize and sidebar toggle, and by `scrollZoom` after
 * the user adjusts viewport units. No-op when canvasWidth <= 0
 * (degenerate during teardown).
 */
export function recomputeZoom(canvasWidth: number): void {
  if (canvasWidth > 0) {
    currentZoom = canvasWidth / viewportUnits;
  }
}

/**
 * Adjust viewport units by a scroll delta. Positive delta zooms out
 * (more world visible), negative zooms in. Clamped to
 * [MIN_VIEWPORT, MAX_VIEWPORT]. Recomputes `currentZoom` against the
 * supplied canvas width and returns the new zoom, or null if the
 * viewport was already at the limit.
 */
export function scrollZoom(delta: number, canvasWidth: number): number | null {
  const prev = viewportUnits;
  viewportUnits = Math.min(
    MAX_VIEWPORT,
    Math.max(MIN_VIEWPORT, viewportUnits + Math.sign(delta) * SCROLL_STEP),
  );
  if (viewportUnits === prev) return null;
  recomputeZoom(canvasWidth);
  return currentZoom;
}

/** Test-only: reset module state. Do not call from production code. */
export function _resetViewportForTest(): void {
  viewportUnits = DEFAULT_VIEWPORT;
  currentZoom = 0;
}
