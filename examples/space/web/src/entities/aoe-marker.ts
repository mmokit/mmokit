import { Container, Graphics } from "pixi.js";
import type { AoEMarkerEntity } from "../../sdk/index.js";
import { EntityType } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// AoEMarker renders an MMO-style ground indicator: a filled red zone
// showing exactly where the AoE will land, with a brighter "fill sweep"
// growing radially as the cast progresses. Pattern matches WoW / Albion
// "stand outside the circle" markers.
//
// Layers (drawn back to front):
//   - dangerZone: dim red fill across the full damage radius (the "you
//     will die if standing here" zone)
//   - sweepFill: bright filling disc that grows 0 → radius as the cast
//     progresses — visual countdown for when the AoE actually lands
//   - outerRing: thick red outline at full radius for unambiguous edge
//   - pulse: gentle alpha breath so the marker reads as ACTIVE not static
//
// Damage radius comes from AoESpec.Radius (the real server-side number),
// not the collider broad-phase Width/Height which is just for indexing.

const COLOR_DANGER = 0xff2211;
const COLOR_SWEEP = 0xff5533;
const COLOR_OUTLINE = 0xff8855;

export function createAoEMarkerDisplay(): EntityDisplayObject {
  const container = new Container();

  const dangerZone = new Graphics();
  const sweepFill = new Graphics();
  const outerRing = new Graphics();
  container.addChild(dangerZone, sweepFill, outerRing);

  let lastRadius = 0;
  let initialLifetime = -1;

  function redrawStatic(radius: number) {
    dangerZone.clear();
    dangerZone.circle(0, 0, radius).fill({ color: COLOR_DANGER, alpha: 0.22 });
    outerRing.clear();
    outerRing.circle(0, 0, radius).stroke({ color: COLOR_OUTLINE, width: px(2.5), alpha: 0.9 });
    outerRing.circle(0, 0, radius * 0.66).stroke({ color: COLOR_OUTLINE, width: px(1), alpha: 0.35 });
    outerRing.circle(0, 0, radius * 0.33).stroke({ color: COLOR_OUTLINE, width: px(1), alpha: 0.2 });
  }

  function redrawSweep(radius: number, progress: number) {
    sweepFill.clear();
    if (radius <= 0 || progress <= 0) return;
    const r = radius * Math.min(1, progress);
    sweepFill.circle(0, 0, r).fill({ color: COLOR_SWEEP, alpha: 0.55 });
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      if (ent.current.entityType !== EntityType.AoEMarker) return;
      const e = ent.current as AoEMarkerEntity;
      const aoeRadius = e.aoESpecRadius || 0;
      if (aoeRadius <= 0) return;

      const remaining = e.remaining ?? 0;
      if (initialLifetime < 0) {
        initialLifetime = remaining > 0 ? remaining : 0;
      }

      if (aoeRadius !== lastRadius) {
        lastRadius = aoeRadius;
        redrawStatic(aoeRadius);
      }

      const progress =
        initialLifetime > 0 ? 1 - remaining / initialLifetime : 1;
      redrawSweep(aoeRadius, Math.max(0, Math.min(1, progress)));

      // Faster pulse as detonation approaches so urgency is legible.
      const pulseSpeed = 0.008 + 0.025 * Math.min(1, progress);
      container.alpha = 0.85 + 0.15 * Math.sin(now * pulseSpeed);
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
