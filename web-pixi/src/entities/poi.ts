import { Container, Graphics, Text } from "pixi.js";
import type { POIEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// POI status values (must match server-side game.POIStatus enum).
// 0 = Active, 1 = Cleared, 2 = Cooldown. Active draws full color, the
// other two states grey out the marker.
const POI_STATUS_ACTIVE = 0;
const POI_STATUS_CLEARED = 1;

// POI type names — currently a single archetype on the server side; this
// table is a placeholder so future archetypes can label themselves
// without a v2 of this file. Keys must align with game.POIType uint8.
const POI_TYPE_NAMES: Record<number, string> = {
  0: "POI",
  1: "Anomaly",
  2: "Distress",
  3: "Convoy",
};

export function createPoiDisplay(): EntityDisplayObject {
  const container = new Container();

  // Outer ring + crosshair drawn into a single Graphics so we can re-tint
  // the whole marker on status changes.
  const ring = new Graphics();
  container.addChild(ring);

  const label = new Text({
    text: "POI",
    style: { fontFamily: "monospace", fontSize: 11, fontWeight: "bold", fill: 0xff3344 },
  });
  label.anchor.set(0.5, 1);
  label.scale.set(px(1), px(1));
  container.addChild(label);

  const subLabel = new Text({
    text: "",
    style: { fontFamily: "monospace", fontSize: 9, fill: 0xff8899 },
  });
  subLabel.anchor.set(0.5, 0);
  subLabel.scale.set(px(1), px(1));
  container.addChild(subLabel);

  let lastStatus = -1;
  let lastType = -1;
  let lastRadius = 0;

  function redraw(status: number, type: number, radius: number) {
    const active = status === POI_STATUS_ACTIVE;
    const baseColor = active ? 0xff3344 : 0x666666;
    const baseAlpha = active ? 1.0 : 0.4;

    ring.clear();
    // Outer ring
    ring.circle(0, 0, radius).stroke({ color: baseColor, width: px(2), alpha: baseAlpha });
    // Inner faint ring
    ring.circle(0, 0, radius * 0.65).stroke({ color: baseColor, width: px(1), alpha: baseAlpha * 0.4 });
    // Crosshair tick marks at compass points
    const tickInner = radius * 0.85;
    const tickOuter = radius * 1.05;
    for (let i = 0; i < 4; i++) {
      const a = (i / 4) * Math.PI * 2;
      const ix = Math.cos(a) * tickInner;
      const iy = Math.sin(a) * tickInner;
      const ox = Math.cos(a) * tickOuter;
      const oy = Math.sin(a) * tickOuter;
      ring.moveTo(ix, iy).lineTo(ox, oy).stroke({ color: baseColor, width: px(1.5), alpha: baseAlpha });
    }
    // Center dot
    ring.circle(0, 0, px(2)).fill({ color: baseColor, alpha: baseAlpha });

    const name = POI_TYPE_NAMES[type] || `POI #${type}`;
    label.text = name.toUpperCase();
    label.style.fill = baseColor;
    label.alpha = baseAlpha;
    label.position.set(0, -radius - px(4));

    const statusText = status === POI_STATUS_ACTIVE
      ? ""
      : status === POI_STATUS_CLEARED
        ? "CLEARED"
        : "COOLDOWN";
    subLabel.text = statusText;
    subLabel.style.fill = baseColor;
    subLabel.alpha = baseAlpha * 0.8;
    subLabel.position.set(0, radius + px(4));
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      const e = ent.current as POIEntity;
      const radius = Math.max(e.radius || 60, 60);
      if (e.status !== lastStatus || e.type !== lastType || radius !== lastRadius) {
        lastStatus = e.status;
        lastType = e.type;
        lastRadius = radius;
        redraw(e.status, e.type, radius);
      }

      // Subtle pulse on the outer ring while active — purely cosmetic so we
      // don't redraw the full graphic each frame; just tweak container alpha.
      if (e.status === POI_STATUS_ACTIVE) {
        container.alpha = 0.75 + 0.25 * Math.sin(now * 0.003);
      } else {
        container.alpha = 1.0;
      }
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
