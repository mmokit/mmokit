import { describe, test, expect, beforeEach } from "bun:test";
import {
  zoom,
  px,
  recomputeZoom,
  scrollZoom,
  _resetViewportForTest,
} from "../view";

describe("view (viewport-anchored zoom)", () => {
  beforeEach(() => {
    _resetViewportForTest();
  });

  test("recomputeZoom sets zoom = canvasWidth / viewportUnits at default viewport", () => {
    // Default viewport is 128 world units across.
    recomputeZoom(800);
    expect(zoom()).toBeCloseTo(800 / 128, 6);

    recomputeZoom(1600);
    expect(zoom()).toBeCloseTo(1600 / 128, 6);
  });

  test("recomputeZoom is a no-op when canvasWidth <= 0", () => {
    recomputeZoom(800);
    const z = zoom();
    recomputeZoom(0);
    expect(zoom()).toBe(z);
    recomputeZoom(-100);
    expect(zoom()).toBe(z);
  });

  test("px converts screen pixels to world units at the current zoom", () => {
    recomputeZoom(800); // zoom = 6.25
    expect(px(2)).toBeCloseTo(2 / 6.25, 6);
    expect(px(0)).toBe(0);
  });

  test("scrollZoom with positive delta is clamped at MAX_VIEWPORT (default = max)", () => {
    // Default viewportUnits is at the max, so zooming out is a no-op.
    const result = scrollZoom(+1, 800);
    expect(result).toBeNull();
  });

  test("scrollZoom with negative delta zooms in (smaller viewport, higher zoom)", () => {
    recomputeZoom(800); // viewport=128, zoom=6.25
    const z1 = scrollZoom(-1, 800);
    // Step is 4 world units: viewport 128 → 124.
    expect(z1).toBeCloseTo(800 / 124, 6);
    expect(zoom()).toBeCloseTo(800 / 124, 6);
  });

  test("scrollZoom clamps at MIN_VIEWPORT (32) after enough zoom-in steps", () => {
    // (128 - 32) / 4 = 24 steps to reach the floor.
    for (let i = 0; i < 30; i++) {
      scrollZoom(-1, 800);
    }
    // At MIN_VIEWPORT = 32: zoom = 800 / 32 = 25.
    expect(zoom()).toBeCloseTo(800 / 32, 6);
    // Further zoom-in calls return null (clamped).
    expect(scrollZoom(-1, 800)).toBeNull();
  });

  test("scrollZoom recomputes against the passed canvasWidth", () => {
    scrollZoom(-1, 800); // viewport now 124
    expect(zoom()).toBeCloseTo(800 / 124, 6);

    // Caller resizes window without scrolling: recomputeZoom against the new width.
    recomputeZoom(1600);
    expect(zoom()).toBeCloseTo(1600 / 124, 6);
  });

  test("recomputeZoom after a zoom-out attempt at the cap leaves viewport unchanged", () => {
    scrollZoom(+1, 800); // already at MAX_VIEWPORT, no change
    recomputeZoom(800);
    expect(zoom()).toBeCloseTo(800 / 128, 6);
  });
});
