# Window-Resize-Aware Zoom Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Flip the web-pixi zoom model so visible world width stays roughly constant across window resizes (entities scale with the canvas), making `MAX_VIEWPORT = 128` the actual fairness ceiling for visible world span.

**Architecture:** Make `viewportUnits` (world units across the canvas width) the source of truth in `view.ts`; derive `currentZoom = canvasWidth / viewportUnits` and recompute on every resize and scroll-zoom. `Camera.resize` becomes the single point that updates screen dimensions, recomputes zoom, and applies the new scale to `worldContainer`. `main.ts` already routes all canvas-width changes (window resize, sidebar toggle) through `applyViewport() → camera.resize`, so no new event listeners.

**Tech Stack:** TypeScript, PixiJS 8, Vite, bun:test.

**Spec:** [docs/superpowers/specs/2026-04-29-window-resize-zoom-design.md](../specs/2026-04-29-window-resize-zoom-design.md)

---

## File Structure

- **Modify:** `web-pixi/src/view.ts` — drop `baselineScreenWidth` and `initZoom`; add `recomputeZoom(canvasWidth)`; `scrollZoom` takes a `canvasWidth` arg and recomputes via the new helper. Add a test-only reset hook.
- **Modify:** `web-pixi/src/world/camera.ts` — drop the constructor's one-shot scale apply; add `applyZoom()` method; `resize(w, h)` calls `recomputeZoom(w)` and `applyZoom()`.
- **Modify:** `web-pixi/src/main.ts` — drop the `initZoom(window.innerWidth)` call (the first `applyViewport()` invocation seeds zoom now); wheel handler passes `app.renderer.width` to `scrollZoom` and calls `camera.applyZoom()` on success.
- **Create:** `web-pixi/src/__tests__/view.test.ts` — unit tests for the new model.

---

## Task 1: Add failing unit tests for the new view.ts model

**Files:**
- Create: `web-pixi/src/__tests__/view.test.ts`

The tests describe the inverted contract: `recomputeZoom(width)` sets `currentZoom = width / viewportUnits`, `scrollZoom` clamps and recomputes, `px` reflects `currentZoom`. They will fail because the new API doesn't exist yet — that's TDD.

- [ ] **Step 1: Write the test file**

Create `web-pixi/src/__tests__/view.test.ts` with:

```typescript
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
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `cd web-pixi && bun test src/__tests__/view.test.ts`

Expected: tests fail with import errors — `recomputeZoom`, `_resetViewportForTest` don't exist yet, and `scrollZoom`'s old signature only takes one arg.

- [ ] **Step 3: Commit the failing tests**

```bash
git add web-pixi/src/__tests__/view.test.ts
git commit -m "test(web-pixi): add failing tests for viewport-anchored zoom

Tests describe the inverted model: viewportUnits is the source of
truth, currentZoom is derived from canvasWidth / viewportUnits.
Will pass after the view.ts refactor lands."
```

---

## Task 2: Refactor view.ts to the viewport-anchored model

**Files:**
- Modify: `web-pixi/src/view.ts` (full rewrite, ~50 lines)

Drop `baselineScreenWidth` and `initZoom`. Add `recomputeZoom(canvasWidth)`. Make `scrollZoom` take a `canvasWidth` and recompute through the new helper. Add `_resetViewportForTest`.

- [ ] **Step 1: Replace view.ts with the new implementation**

Replace the entire contents of `web-pixi/src/view.ts` with:

```typescript
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
const INITIAL_ZOOM_FALLBACK = 30;

let viewportUnits = DEFAULT_VIEWPORT;
let currentZoom = INITIAL_ZOOM_FALLBACK; // overwritten by first recomputeZoom

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
  currentZoom = INITIAL_ZOOM_FALLBACK;
}
```

- [ ] **Step 2: Run the view.ts tests — they should now pass**

Run: `cd web-pixi && bun test src/__tests__/view.test.ts`

Expected: all 8 tests pass.

- [ ] **Step 3: Run typecheck to surface any callers broken by the API change**

Run: `cd web-pixi && bun run typecheck`

Expected: type errors in `world/camera.ts` (uses `zoom()` — still fine) and `main.ts` (calls `initZoom(width)` and `scrollZoom(delta)` with the old signatures). The next two tasks fix both. Note any other callers reported and verify they're already covered by the next tasks before proceeding.

- [ ] **Step 4: Commit**

```bash
git add web-pixi/src/view.ts
git commit -m "refactor(web-pixi): invert zoom model — viewportUnits is now the source of truth

view.ts now derives currentZoom from canvasWidth / viewportUnits.
recomputeZoom(width) replaces initZoom(width) and is meant to be called
on every canvas-width change. scrollZoom takes the current canvas width
and recomputes through the new helper. baselineScreenWidth is gone.

Camera and main still need updating; typecheck currently fails."
```

---

## Task 3: Update Camera to drive zoom from canvas width

**Files:**
- Modify: `web-pixi/src/world/camera.ts` (full rewrite, ~80 lines)

Drop the constructor's one-shot scale apply. Add `applyZoom()`. `resize(w, h)` becomes the single point that updates screen dimensions, recomputes zoom, and applies the result. `screenToWorld` / `worldToScreen` / `update` are unchanged — they already read `zoom()` each call.

- [ ] **Step 1: Replace camera.ts with the new implementation**

Replace the entire contents of `web-pixi/src/world/camera.ts` with:

```typescript
import { Container } from "pixi.js";
import { recomputeZoom, zoom } from "../view";

export class Camera {
  public x = 0;
  public y = 0;
  private screenW = 0;
  private screenH = 0;

  constructor(private worldContainer: Container) {
    // Scale is set by the first resize() call (driven by applyViewport()
    // in main.ts on startup). Constructor no longer touches scale —
    // viewport-anchored zoom is meaningless without a known canvas width.
  }

  /**
   * Push the current `zoom()` value into the world container scale.
   * Use after any operation that changes `currentZoom` without going
   * through resize() — e.g. scroll-wheel zoom.
   */
  applyZoom(): void {
    const z = zoom();
    this.worldContainer.scale.set(z, z);
  }

  /**
   * Update screen dimensions, recompute zoom against the new canvas
   * width, and apply the resulting scale. This is the canonical "canvas
   * size changed" entry point — both window resize and sidebar toggle
   * route through here via applyViewport().
   */
  resize(w: number, h: number): void {
    this.screenW = w;
    this.screenH = h;
    recomputeZoom(w);
    this.applyZoom();
  }

  update(
    playerX: number,
    playerY: number,
    shake?: { intensity: number; startTime: number; duration: number } | null,
  ): void {
    this.x = playerX;
    this.y = playerY;
    this.worldContainer.pivot.set(playerX, playerY);

    let offsetX = 0;
    let offsetY = 0;
    if (shake) {
      const elapsed = performance.now() - shake.startTime;
      if (elapsed < shake.duration) {
        const decay = 1 - elapsed / shake.duration;
        offsetX = (Math.random() - 0.5) * shake.intensity * decay * 2;
        offsetY = (Math.random() - 0.5) * shake.intensity * decay * 2;
      }
    }

    this.worldContainer.position.set(
      this.screenW / 2 + offsetX,
      this.screenH / 2 + offsetY,
    );
  }

  /** Convert screen coordinates to world coordinates */
  screenToWorld(sx: number, sy: number): { x: number; y: number } {
    const z = zoom();
    return {
      x: (sx - this.screenW / 2) / z + this.x,
      y: (sy - this.screenH / 2) / z + this.y,
    };
  }

  /** Convert world coordinates to screen coordinates */
  worldToScreen(wx: number, wy: number): { x: number; y: number } {
    const z = zoom();
    return {
      x: (wx - this.x) * z + this.screenW / 2,
      y: (wy - this.y) * z + this.screenH / 2,
    };
  }
}
```

- [ ] **Step 2: Run typecheck**

Run: `cd web-pixi && bun run typecheck`

Expected: errors only in `main.ts` (still calls `initZoom` and old `scrollZoom`). camera.ts should typecheck clean.

- [ ] **Step 3: Commit**

```bash
git add web-pixi/src/world/camera.ts
git commit -m "refactor(web-pixi): camera.resize drives zoom from canvas width

Camera.resize is now the single point that updates screen dimensions,
recomputes zoom via recomputeZoom(w), and pushes the result onto the
worldContainer scale. applyZoom() is the small helper for the
scroll-wheel path which needs to push current zoom without changing
screen dimensions. Constructor no longer applies scale — viewport-
anchored zoom is meaningless without a known canvas width."
```

---

## Task 4: Update main.ts wiring

**Files:**
- Modify: `web-pixi/src/main.ts:8` (import line)
- Modify: `web-pixi/src/main.ts:85-92` (drop initZoom call)
- Modify: `web-pixi/src/main.ts:163-173` (wheel handler)

Drop the `initZoom(window.innerWidth)` call — `applyViewport()` runs immediately after Camera construction and seeds zoom via `camera.resize`. Update the wheel handler to pass canvas width to `scrollZoom` and use `camera.applyZoom()` instead of poking `worldContainer.scale` directly.

- [ ] **Step 1: Update the import**

In `web-pixi/src/main.ts`, find:

```typescript
import { initZoom, scrollZoom } from "./view";
```

Replace with:

```typescript
import { scrollZoom } from "./view";
```

- [ ] **Step 2: Drop the `initZoom` call and update the surrounding comment**

In `web-pixi/src/main.ts`, find:

```typescript
  // Zoom baseline must be established BEFORE the Camera constructor
  // reads zoom() and applies it to the world container scale. This is
  // the only place zoom is computed from window width; subsequent
  // resizes leave the baseline alone.
  initZoom(window.innerWidth);

  // Camera
  const camera = new Camera(worldContainer);
```

Replace with:

```typescript
  // Camera. Scale is seeded by the first applyViewport() call below;
  // every subsequent resize / sidebar toggle re-derives zoom from the
  // canvas width, so visible world width stays roughly constant.
  const camera = new Camera(worldContainer);
```

- [ ] **Step 3: Update the wheel handler**

In `web-pixi/src/main.ts`, find:

```typescript
  // Scroll-wheel zoom
  window.addEventListener("wheel", (e) => {
    if (state.cellMapOpen) {
      cellMap.handleWheel(e.deltaY);
      return;
    }
    const z = scrollZoom(e.deltaY);
    if (z != null) {
      worldContainer.scale.set(z, z);
    }
  }, { passive: true });
```

Replace with:

```typescript
  // Scroll-wheel zoom. scrollZoom updates viewportUnits and recomputes
  // currentZoom against the live canvas width; camera.applyZoom() then
  // pushes the new scale onto the world container.
  window.addEventListener("wheel", (e) => {
    if (state.cellMapOpen) {
      cellMap.handleWheel(e.deltaY);
      return;
    }
    if (scrollZoom(e.deltaY, app.renderer.width) != null) {
      camera.applyZoom();
    }
  }, { passive: true });
```

- [ ] **Step 4: Typecheck**

Run: `cd web-pixi && bun run typecheck`

Expected: clean — no errors.

- [ ] **Step 5: Run the full test suite**

Run: `cd web-pixi && bun test`

Expected: all tests pass (existing clockSync/interpolation tests unaffected; new view tests pass).

- [ ] **Step 6: Commit**

```bash
git add web-pixi/src/main.ts
git commit -m "refactor(web-pixi): wire main.ts into viewport-anchored zoom

initZoom is gone — the first applyViewport() call seeds zoom via
camera.resize, so the boot order doesn't need a separate one-shot.
Wheel handler now passes app.renderer.width to scrollZoom and uses
camera.applyZoom() to push the result onto the world container."
```

---

## Task 5: Manual verification

No automated test exercises the full pixi/dom integration. Walk through the four scenarios from the spec and confirm behavior matches.

**Files:** none modified.

- [ ] **Step 1: Start the dev server**

From the repo root, run: `just dev` (or, equivalently, `cd web-pixi && bun run dev`). Wait for vite to print its local URL (typically `http://localhost:5173` for the web-pixi tree, or `http://localhost:8080` if you used `just dev` which proxies through the server). Open it in a browser and log in.

- [ ] **Step 2: Verify resize from small → large**

Resize the browser window to ~600px wide. Note your ship's apparent size. Drag the window to maximize. Expected: the ship grows in apparent pixel size, but the visible world span (e.g. the spacing of cell-grid lines, distance to nearest asteroid) stays roughly the same. Before this change, you'd see a much wider world area; now you see the same area, drawn larger.

- [ ] **Step 3: Verify resize from large → small**

Maximize, then shrink the window to ~600px. Expected: ship shrinks in apparent pixel size; same world span visible.

- [ ] **Step 4: Verify scroll-wheel zoom still works**

At a stable window size, scroll wheel up to zoom in (smaller viewport, larger ship). Scroll until you hit the floor — ship can't grow further. Scroll down to zoom out, until you hit the ceiling at the default viewport. The cap should be reached cleanly with no flicker.

- [ ] **Step 5: Verify cargo sidebar interaction**

Open the cargo panel (default keybind, e.g. `c` or click the icon). Expected: the canvas shrinks horizontally, ship visibly shrinks, but the same world span stays in view (just compressed into the narrower canvas). Close the panel — ship returns to full size.

- [ ] **Step 6: Spot-check that no rendering artifacts appear**

Move around, mine an asteroid or trigger an ability, scroll-zoom while moving. Expected: no flicker, no missing entities, no broken HUD. Stroke widths and font sizes (anything using `px()`) should still look right at every zoom level.

- [ ] **Step 7: Stop the dev server. No commit needed for verification.**

If any step fails, reopen the relevant task, fix the bug, and re-verify the affected scenarios.

---

## Self-review notes

- **Spec coverage:** All four spec sections (`view.ts`, `camera.ts`, `main.ts`, sidebar behavior) are covered by Tasks 2, 3, 4, and verified in Task 5 step 5. The fairness ceiling (`MAX_VIEWPORT = 128`) is exercised by the test in Task 1 ("scrollZoom with positive delta is clamped at MAX_VIEWPORT").
- **API consistency:** `recomputeZoom(width: number): void` and `scrollZoom(delta: number, canvasWidth: number): number | null` use the same arg name (`canvasWidth`) across view.ts, the tests, camera.ts callers, and main.ts callers.
- **No placeholders:** every step contains the actual code or command.
