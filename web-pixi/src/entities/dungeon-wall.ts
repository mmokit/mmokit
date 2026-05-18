import { Container, Graphics } from "pixi.js";
import type { DungeonWallEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// ──────────────────────────────────────────────────────────────────────────────
// Cave-wall slab. Part of the "Sublime Threat" aesthetic — solid basalt
// with a thin molten amber vein running along its long axis. The vein
// pulses slowly, hinting that the rock is alive with deep energy.
//
// Layers (back to front):
//   1. Base fill — dark cracked-basalt body
//   2. Highlight stripe — slightly lighter rock along the top edge
//   3. Lava vein — bright animated amber line along center (long axis)
//   4. Dark outline stroke for definition against the silhouette
//   5. Edge crystal nubs — small jagged points on the long edges
//
// Rotation/position are applied by EntityManager via renderRot /
// renderX / renderY each frame. The Graphics geometry itself is
// drawn in local (centered) coords and only redraws when w/h change.
// The vein pulse is a per-frame alpha animation on a dedicated layer.
// ──────────────────────────────────────────────────────────────────────────────

const COL_BASE = 0x2a1a2e; // dark cracked basalt
const COL_HIGHLIGHT = 0x4d3050; // lighter rock face
const COL_OUTLINE = 0x0e0612; // very dark edge stroke
const COL_VEIN_HOT = 0xffaa55; // bright molten core
const COL_VEIN_BLEED = 0xff5523; // surrounding glow

export function createDungeonWallDisplay(): EntityDisplayObject {
  const container = new Container();

  // Static body layer — base fill + highlight + outline + crystals.
  const body = new Graphics();
  // Animated layer — lava vein that pulses each frame.
  const vein = new Graphics();
  container.addChild(body, vein);

  let drawnW = 0;
  let drawnH = 0;
  // Deterministic seed per-spawn so the crystal nubs / crack pattern
  // are stable for the wall's lifetime. We don't have the wall's NetID
  // here; seed from initial dims (close enough).
  let seedBase = 0;

  function drawBody(w: number, h: number) {
    body.clear();
    const hw = w / 2;
    const hh = h / 2;

    // ── 1. Base fill ──────────────────────────────────────────────────
    body.rect(-hw, -hh, w, h).fill({ color: COL_BASE, alpha: 1.0 });

    // ── 2. Top-edge highlight band ────────────────────────────────────
    // Suggests light from "above" catching the upper face. We define
    // "above" as -Y in the wall's local frame; for rotated walls this
    // becomes the leading edge in whatever direction the rotation gives.
    const bandH = Math.max(h * 0.18, 0.4);
    body
      .rect(-hw, -hh, w, bandH)
      .fill({ color: COL_HIGHLIGHT, alpha: 0.45 });

    // ── 3. Dark outline stroke ────────────────────────────────────────
    body
      .rect(-hw, -hh, w, h)
      .stroke({ color: COL_OUTLINE, width: px(1.5), alpha: 0.95 });

    // ── 5. Edge crystal nubs ──────────────────────────────────────────
    // A few small triangles protruding from the long edges, making the
    // wall feel like a sliver of jagged rock instead of cardboard.
    // Skip if the wall is too thin — the nubs would dwarf it.
    const longAxis = Math.max(w, h);
    const wIsLong = w >= h;
    if (longAxis > 6) {
      const nubCount = Math.max(2, Math.floor(longAxis / 8));
      for (let i = 0; i < nubCount; i++) {
        // Deterministic skip + sizing so adjacent walls don't all look
        // identical. seedBase rolls in some per-wall variation.
        const s = ((i * 37 + seedBase * 13) % 100) / 100;
        if (s < 0.45) continue;
        const t = (i + 0.5) / nubCount;
        const size = (0.6 + (s * 1.4)) * Math.min(h, w) * 0.5;
        if (wIsLong) {
          // Nub on top edge (-hh side)
          const cx = -hw + t * w;
          const cy = -hh;
          const sideFlip = ((i * 71) % 100) > 50 ? -1 : 1; // top or bottom
          const cy2 = sideFlip < 0 ? hh : -hh;
          body
            .poly([cx - size * 0.5, cy2, cx + size * 0.5, cy2, cx, cy2 + sideFlip * -size])
            .fill({ color: COL_HIGHLIGHT, alpha: 0.65 })
            .stroke({ color: COL_OUTLINE, width: px(1), alpha: 0.7 });
        } else {
          const cx = -hw;
          const cy = -hh + t * h;
          const sideFlip = ((i * 71) % 100) > 50 ? -1 : 1;
          const cx2 = sideFlip < 0 ? hw : -hw;
          body
            .poly([cx2, cy - size * 0.5, cx2, cy + size * 0.5, cx2 + sideFlip * -size, cy])
            .fill({ color: COL_HIGHLIGHT, alpha: 0.65 })
            .stroke({ color: COL_OUTLINE, width: px(1), alpha: 0.7 });
        }
      }
    }
  }

  function drawVein(w: number, h: number, alphaMul: number) {
    vein.clear();
    const hw = w / 2;
    const hh = h / 2;
    const wIsLong = w >= h;
    // Vein runs along the LONG axis with a slight offset from center
    // so it doesn't perfectly bisect the wall (more interesting visually).
    if (wIsLong) {
      // Wider, dimmer bleed layer underneath
      vein
        .moveTo(-hw * 0.92, 0)
        .lineTo(hw * 0.92, 0)
        .stroke({ color: COL_VEIN_BLEED, width: px(3.5), alpha: 0.5 * alphaMul });
      // Bright hot core line on top
      vein
        .moveTo(-hw * 0.92, 0)
        .lineTo(hw * 0.92, 0)
        .stroke({ color: COL_VEIN_HOT, width: px(1.3), alpha: 0.95 * alphaMul });
      // Two tiny crystal sparks along the vein for visual rhythm
      vein
        .circle(-hw * 0.4, 0, px(1.6))
        .fill({ color: COL_VEIN_HOT, alpha: 0.95 * alphaMul });
      vein
        .circle(hw * 0.4, 0, px(1.6))
        .fill({ color: COL_VEIN_HOT, alpha: 0.95 * alphaMul });
    } else {
      vein
        .moveTo(0, -hh * 0.92)
        .lineTo(0, hh * 0.92)
        .stroke({ color: COL_VEIN_BLEED, width: px(3.5), alpha: 0.5 * alphaMul });
      vein
        .moveTo(0, -hh * 0.92)
        .lineTo(0, hh * 0.92)
        .stroke({ color: COL_VEIN_HOT, width: px(1.3), alpha: 0.95 * alphaMul });
      vein
        .circle(0, -hh * 0.4, px(1.6))
        .fill({ color: COL_VEIN_HOT, alpha: 0.95 * alphaMul });
      vein
        .circle(0, hh * 0.4, px(1.6))
        .fill({ color: COL_VEIN_HOT, alpha: 0.95 * alphaMul });
    }
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      const e = ent.current as DungeonWallEntity;
      const w = e.width || 0;
      const h = e.height || 0;
      if (w <= 0 || h <= 0) return;
      if (w !== drawnW || h !== drawnH) {
        drawnW = w;
        drawnH = h;
        // Cheap, stable seed from the dimensions — adjacent walls in
        // a corridor have different W/H so they get different crystal
        // patterns. Cast to int.
        seedBase = Math.floor((w * 31 + h * 127) % 1000);
        drawBody(w, h);
      }
      // Subtle vein pulse — slow breathing, slight phase shift so
      // adjacent walls don't pulse in unison.
      const phase = seedBase * 0.013;
      const pulse = 0.75 + 0.25 * Math.sin(now * 0.0022 + phase);
      drawVein(w, h, pulse);
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
