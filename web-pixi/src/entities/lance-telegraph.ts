import { Container, Graphics } from "pixi.js";
import type { LanceTelegraphEntity } from "../../sdk/index.js";
import { EntityType } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// LanceTelegraph renders the Lancer windup telegraph: a glowing red
// rectangle from the lancer's position pointing along its rotation, of
// length `length` and half-width `halfWidth`. The inner fill grows from
// 0 → length over the marker's lifetime (commitment), giving the player
// a clear cue of when the charge is about to fire.
//
// Position + rotation are owned by EntityManager (sets container.position
// from renderX/Y and container.rotation from renderRot). The rectangle
// starts at local origin (0, 0) and extends +X, so once the parent
// container is rotated to the charge angle the rectangle points the
// right way.

const COLOR_OUTLINE = 0xff4422;
const COLOR_FILL = 0xff2211;

export function createLanceTelegraphDisplay(): EntityDisplayObject {
  const container = new Container();
  const fill = new Graphics();
  const outline = new Graphics();
  container.addChild(fill);
  container.addChild(outline);

  let initialLifetime = -1; // seconds; set on first observation

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

      outline.clear();
      outline
        .rect(0, -halfW, length, halfW * 2)
        .stroke({ color: COLOR_OUTLINE, width: px(1.5), alpha: 0.85 });

      fill.clear();
      const fillLen = length * progress;
      if (fillLen > 0) {
        fill
          .rect(0, -halfW, fillLen, halfW * 2)
          .fill({ color: COLOR_FILL, alpha: 0.25 + 0.3 * progress });
      }

      // Subtle pulse on the whole telegraph as the windup approaches
      // commit — telegraphs urgency without competing with the growing
      // fill.
      container.alpha = 0.85 + 0.15 * Math.sin(now * 0.018);
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
