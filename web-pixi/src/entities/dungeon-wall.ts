import { Container, Graphics } from "pixi.js";
import type { DungeonWallEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// Static rectangular cave walls. Width/Height come from the entity's
// Collider (replicated as `width`/`height` on the wire) and the
// rotation is replicated via the per-kind QAngle binding on
// DungeonWall (the SDK surfaces it as `angle`). EntityManager applies
// `renderRot` from the rotation interpolation, but walls don't move
// or rotate after spawn — we still rely on EntityManager to set
// container.rotation each frame from `renderRot`, which is seeded
// from the entity's `angle` on first sample.
//
// We render the wall once on first valid dimensions and never redraw.
export function createDungeonWallDisplay(): EntityDisplayObject {
  const container = new Container();

  const gfx = new Graphics();
  container.addChild(gfx);

  let drawnW = 0;
  let drawnH = 0;

  function drawWall(w: number, h: number) {
    gfx.clear();
    // Slightly lighter than the dungeon silhouette (rock interior tone)
    // with a darker stroke so adjacent walls remain visually distinct.
    gfx
      .rect(-w / 2, -h / 2, w, h)
      .fill({ color: 0x3a2e22, alpha: 1.0 })
      .stroke({ color: 0x1a1612, width: px(1.5), alpha: 0.9 });

    // Faint inner stroke for a beveled feel — makes the wall read as a
    // solid mass rather than a flat sprite.
    const inset = Math.min(w, h) * 0.08;
    if (inset > 0.05) {
      gfx
        .rect(-w / 2 + inset, -h / 2 + inset, w - 2 * inset, h - 2 * inset)
        .stroke({ color: 0x4a3a2a, width: px(1), alpha: 0.3 });
    }
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, _now: number) {
      const e = ent.current as DungeonWallEntity;
      const w = e.width || 0;
      const h = e.height || 0;
      if (w <= 0 || h <= 0) return;
      if (w !== drawnW || h !== drawnH) {
        drawnW = w;
        drawnH = h;
        drawWall(w, h);
      }
      // Rotation/position are applied by EntityManager via renderRot /
      // renderX / renderY each frame — no per-tick work here.
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
