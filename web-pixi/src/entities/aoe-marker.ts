import { Container, Graphics } from "pixi.js";
import type { AoEMarkerEntity } from "../../sdk/index.js";
import { EntityType } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// AoEMarker renders the telegraphed-AoE cast/detonate visual: an outer
// ring drawn at the AoE radius (intent) plus an inner disc that grows
// from 0 → radius over the marker's lifetime (commitment).
//
// The server replicates Lifetime.Remaining (f32 seconds) so the inner
// disc tracks actual cast progress rather than a client-side heuristic.
//
// On first observation we capture initialLifetime = remaining. Each
// render frame: progress = 1 - (remaining / initialLifetime), clamped
// [0, 1]. Inner disc radius = aoeRadius * progress.
//
// Instant-resolve markers (Kamikaze detonate, projectile splash) have
// lifetime=0 server-side and despawn the same tick they appear. The
// client may never observe them; that's fine — damage is server-side,
// the visual layer is handled by ability/impact effects elsewhere.

const COLOR_FILL = 0xff3322;
const COLOR_STROKE = 0xff5544;

export function createAoEMarkerDisplay(): EntityDisplayObject {
  const container = new Container();

  const outerRing = new Graphics();
  container.addChild(outerRing);

  const innerDisc = new Graphics();
  container.addChild(innerDisc);

  let lastRadius = 0;
  let initialLifetime = -1; // seconds; set on first observation

  function redrawOuter(radius: number) {
    outerRing.clear();
    // Outer boundary — solid ring at full AoE radius. This is the
    // "intent" — anything inside this ring at detonation will take
    // damage.
    outerRing.circle(0, 0, radius).stroke({ color: COLOR_STROKE, width: px(2), alpha: 0.6 });
    // Faint inner reference ring at 50% — gives players a visual
    // distance cue without redrawing every frame.
    outerRing.circle(0, 0, radius * 0.5).stroke({ color: COLOR_STROKE, width: px(1), alpha: 0.25 });
  }

  function redrawInner(radius: number, progress: number) {
    innerDisc.clear();
    if (radius <= 0 || progress <= 0) return;
    const innerR = radius * Math.min(1, progress);
    innerDisc.circle(0, 0, innerR).fill({ color: COLOR_FILL, alpha: 0.25 });
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      if (ent.current.entityType !== EntityType.AoEMarker) return;
      const e = ent.current as AoEMarkerEntity;
      // AoESpec.Radius is the actual damage radius. The entity's
      // `radius` collider field is the broad-phase trigger; for the
      // telegraphed visual we want the damage radius.
      const aoeRadius = e.aoESpecRadius || 0;
      if (aoeRadius <= 0) return;

      // Capture initial lifetime on first observation.
      const remaining = e.remaining ?? 0;
      if (initialLifetime < 0) {
        initialLifetime = remaining > 0 ? remaining : 0;
      }

      if (aoeRadius !== lastRadius) {
        lastRadius = aoeRadius;
        redrawOuter(aoeRadius);
      }

      // progress: 0 at cast start → 1 at detonation.
      // If initialLifetime is 0 (instant marker), show fully filled disc.
      const progress =
        initialLifetime > 0 ? 1 - remaining / initialLifetime : 1;

      redrawInner(aoeRadius, Math.max(0, Math.min(1, progress)));

      // Subtle pulse on the outer ring — telegraphs that the cast is
      // active. Range stays gentle so it doesn't compete with the
      // growing inner disc.
      container.alpha = 0.85 + 0.15 * Math.sin(now * 0.012);
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
