import { Container, Graphics } from "pixi.js";
import type { AoEMarkerEntity } from "../../sdk/index.js";
import { EntityType } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// AoEMarker renders the telegraphed-AoE cast/detonate visual: an outer
// ring drawn at the AoE radius (intent) plus an inner disc that grows
// from 0 → radius over the marker's expected lifetime (commitment).
//
// The server doesn't replicate the per-tick remaining lifetime, so the
// client treats first observation as t=0 and uses a default-cast-time
// fallback if the marker disappears faster than expected. The visual
// only needs to communicate "danger zone, growing soon" — it doesn't
// need to be perfectly synced with server detonation.
//
// Instant-resolve markers (Kamikaze detonate, projectile splash) have
// lifetime=0 server-side and despawn the same tick they appear. The
// client may never observe them; that's fine — damage is server-side,
// the visual layer is handled by ability/impact effects elsewhere.

// Default expected cast time, used when the marker outlives the assumed
// duration (we cap inner disc at full radius). Most v2 telegraphed AoE
// casts are 0.5–2.0s; pick 1.5s as a sane mid-range default.
const DEFAULT_CAST_SECONDS = 1.5;

const COLOR_FILL = 0xff3322;
const COLOR_STROKE = 0xff5544;

export function createAoEMarkerDisplay(): EntityDisplayObject {
  const container = new Container();

  const outerRing = new Graphics();
  container.addChild(outerRing);

  const innerDisc = new Graphics();
  container.addChild(innerDisc);

  let lastRadius = 0;
  let spawnMs = 0; // performance.now() at first observation

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
      const radius = e.aoESpecRadius || 0;
      if (radius <= 0) return;

      if (spawnMs === 0) spawnMs = now;
      if (radius !== lastRadius) {
        lastRadius = radius;
        redrawOuter(radius);
      }
      const elapsedSec = (now - spawnMs) / 1000;
      const progress = elapsedSec / DEFAULT_CAST_SECONDS;
      redrawInner(radius, progress);

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
