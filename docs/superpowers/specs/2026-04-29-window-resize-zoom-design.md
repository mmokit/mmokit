# Window-Resize-Aware Zoom (web-pixi)

**Date:** 2026-04-29
**Status:** Approved (design phase)
**Scope:** `web-pixi/` only — no server or proto changes.

## Problem

The web-pixi client currently uses an FPS/strategy-style zoom model: `currentZoom` is pixels-per-world-unit and is locked to the user's chosen zoom level. Window resize changes how much world is visible without changing entity render size. ([view.ts:1-18](../../../web-pixi/src/view.ts#L1-L18) is explicit about this being intentional.)

For a server-authoritative PvP game, this is a fairness problem on two axes:

1. A player who starts the browser small and then maximizes the window sees a much larger world area than a player who started maximized — without ever scroll-zooming.
2. There is no effective ceiling on visible world area: `MAX_VIEWPORT = 128` exists but is meaningless because a 4K user at viewport=128 simply gets giant pixels-per-unit; the world span they see is governed by their monitor, not the cap.

## Goal

Invert the model so visible world width stays constant across window sizes (entities scale with the canvas instead). The existing `MAX_VIEWPORT = 128` constant becomes the actual fairness ceiling: nobody can ever see more than 128 world units across the canvas width, regardless of monitor size or starting window size.

Width-anchored is acceptable: an ultra-wide monitor sees exactly the same horizontal world span as everyone else and proportionally less vertical. A portrait window sees the same horizontal span and proportionally more vertical. That asymmetry is fine for a landscape PvP game; we explicitly choose not to add aspect-ratio handling.

## Design

### Source of truth flip

- **Before:** `currentZoom` (pixels/unit) is set at startup from `initZoom(width)` and only changed by scroll-zoom. `viewportUnits` is derived for the scroll-zoom step calculation.
- **After:** `viewportUnits` (world units across the canvas width, clamped `[MIN_VIEWPORT, MAX_VIEWPORT]`) is the source of truth. `currentZoom` is derived: `currentZoom = canvasWidth / viewportUnits`. It is recomputed whenever the canvas width changes (window resize, sidebar toggle) or `viewportUnits` changes (scroll-zoom).

### `view.ts` changes

- Drop `baselineScreenWidth` (no longer needed; we always derive against current width).
- Replace `initZoom(width)` with `recomputeZoom(width)` that simply does `currentZoom = width / viewportUnits`. Same body — different role: it's now called every time the canvas width changes, not just at startup.
- `scrollZoom(delta, canvasWidth)` clamps `viewportUnits` and calls `recomputeZoom(canvasWidth)`. Returns the new `currentZoom` or `null` if at limit (unchanged signature shape).
- `MAX_VIEWPORT = 128` and `MIN_VIEWPORT = 32` stay as-is; they are now the fairness clamp on visible world width.
- `zoom()` and `px()` keep their current contract — callers don't change.

### `camera.ts` changes

`Camera.resize(w, h)` becomes the single point that:

1. Updates `screenW` / `screenH`.
2. Calls `recomputeZoom(w)`.
3. Applies `worldContainer.scale.set(zoom(), zoom())`.

The constructor's one-shot scale apply goes away — `resize()` is now the canonical place. The header comments invert ("scale tracks canvas width" instead of "scale is independent of window size").

### `main.ts` changes

- `applyViewport()` already computes the canvas width (window width minus sidebar) and calls `camera.resize(w, h)`. That single existing call site becomes the trigger for zoom recomputation — no new resize listeners.
- The wheel handler passes the current canvas width (`app.renderer.width`) to `scrollZoom`. `scrollZoom` recomputes `currentZoom` internally; the wheel handler then applies the new scale by calling `camera.applyZoom()` — a small new method on `Camera` that does `worldContainer.scale.set(zoom(), zoom())`. `Camera.resize` calls `applyZoom` internally too, so it's the single helper for "push the current zoom into the scene graph."

### Sidebar-open behavior (decided)

When the cargo sidebar opens, the canvas shrinks to make room for it. With width-anchored zoom, this means: same `viewportUnits`, smaller canvas width → smaller `currentZoom` → entities render smaller in pixels, but the same world span stays visible (compressed into the narrower canvas). This is the consistent answer and matches the fairness goal — opening the sidebar costs you visual size, not coverage.

## Out of Scope

- New zoom limits or a different `MAX_VIEWPORT` value. The current cap is the entire fairness mechanism; tuning it is a separate decision.
- Aspect-ratio handling (letterboxing for narrow windows, vertical-anchored mode for portrait, etc.).
- Per-player server-enforced zoom limits. The cap lives in client code; we accept that a modified client could exceed it. Server-side enforcement is a future concern, tied to broader anti-cheat work.
- Smooth-animated zoom transitions on resize. The change is instantaneous (one frame).

## Files Changed

- `web-pixi/src/view.ts`
- `web-pixi/src/world/camera.ts`
- `web-pixi/src/main.ts`

No tests exist for `view.ts` or `camera.ts` today; we do not add any in this change. Manual verification:

1. Open at small window → ship at expected size, then maximize → ship grows, same world span visible.
2. Reverse: open maximized → resize to small → ship shrinks, same world span visible.
3. Scroll-wheel zoom in/out at any window size → behaves smoothly, clamped at MIN/MAX_VIEWPORT.
4. Open cargo sidebar → entities shrink, world span unchanged. Close sidebar → entities return to full size.
