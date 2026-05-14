import { Container, Graphics } from "pixi.js";
import type { LanceTelegraphEntity } from "../../sdk/index.js";
import { EntityType } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// LanceTelegraph renders an MMO-style ground indicator for the Lancer's
// charge windup: a filled red rectangle showing exactly where the lance
// will sweep, with a brighter "fill sweep" growing along the path as the
// windup completes. WoW / Albion ground-marker convention — stay out of
// the red zone.
//
// Layers (back to front):
//   - dangerZone: dim red fill across the full charge path (you will be
//     hit if standing here when the charge fires)
//   - sweepFill: bright filling rectangle that grows 0 → length as the
//     windup progresses — visual countdown until contact
//   - outline: thick red border for unambiguous edge
//
// Position + rotation are owned by EntityManager (sets container.position
// from renderX/Y and container.rotation from renderRot). The rectangle
// starts at local origin (0, 0) and extends +X, so once the parent
// container is rotated to the charge angle the rectangle points the
// right way.

const COLOR_DANGER = 0xff2211;
const COLOR_SWEEP = 0xff5533;
const COLOR_OUTLINE = 0xff8855;

export function createLanceTelegraphDisplay(): EntityDisplayObject {
  const container = new Container();
  const dangerZone = new Graphics();
  const sweepFill = new Graphics();
  const outline = new Graphics();
  container.addChild(dangerZone, sweepFill, outline);

  let initialLifetime = -1;
  let lastLength = -1;
  let lastHalfW = -1;

  function redrawStatic(length: number, halfW: number) {
    dangerZone.clear();
    dangerZone.rect(0, -halfW, length, halfW * 2)
      .fill({ color: COLOR_DANGER, alpha: 0.22 });
    outline.clear();
    outline.rect(0, -halfW, length, halfW * 2)
      .stroke({ color: COLOR_OUTLINE, width: px(2.5), alpha: 0.9 });
    // Arrow tip at the end so players read direction instantly.
    const tipLen = Math.min(halfW, length * 0.1);
    outline.poly([
      length, -halfW,
      length + tipLen, 0,
      length, halfW,
    ]).stroke({ color: COLOR_OUTLINE, width: px(2), alpha: 0.9 });
    // Center line down the path so the sweep direction is obvious.
    outline.moveTo(0, 0).lineTo(length, 0)
      .stroke({ color: COLOR_OUTLINE, width: px(1), alpha: 0.35 });
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      if (ent.current.entityType !== EntityType.LanceTelegraph) return;
      const e = ent.current as LanceTelegraphEntity;
      const length = e.length || 30;
      const halfW = e.halfWidth || 3;
      const remaining = e.remaining ?? 0;

      if (initialLifetime < 0) {
        initialLifetime = remaining > 0 ? remaining : 0;
      }
      const progress =
        initialLifetime > 0
          ? Math.max(0, Math.min(1, 1 - remaining / initialLifetime))
          : 1;

      if (length !== lastLength || halfW !== lastHalfW) {
        lastLength = length;
        lastHalfW = halfW;
        redrawStatic(length, halfW);
      }

      sweepFill.clear();
      const fillLen = length * progress;
      if (fillLen > 0) {
        sweepFill.rect(0, -halfW, fillLen, halfW * 2)
          .fill({ color: COLOR_SWEEP, alpha: 0.55 });
      }

      // Faster pulse as charge approaches so urgency is legible.
      const pulseSpeed = 0.012 + 0.03 * progress;
      container.alpha = 0.85 + 0.15 * Math.sin(now * pulseSpeed);
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
