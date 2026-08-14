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

// Tier color palette. Matches the spec's gradient: green = T1 frontier,
// amber = T2 expansion belt, red = T3 deep-space destination raid.
const TIER_COLORS: Record<number, number> = {
  1: 0x5bd078,
  2: 0xe8b53b,
  3: 0xd04545,
};

// Tier-relative marker size. T3 destinations render visibly larger;
// T1 starter camps stay small to convey "background content".
const TIER_RADIUS_MUL: Record<number, number> = {
  1: 0.7,
  2: 1.0,
  3: 1.4,
};

function colorForTier(tier: number): number {
  return TIER_COLORS[tier] ?? TIER_COLORS[1];
}

function radiusMulForTier(tier: number): number {
  return TIER_RADIUS_MUL[tier] ?? 1.0;
}

export function createPoiDisplay(): EntityDisplayObject {
  const container = new Container();

  const ring = new Graphics();
  container.addChild(ring);

  const label = new Text({
    text: "POI",
    style: { fontFamily: "monospace", fontSize: 11, fontWeight: "bold", fill: 0xffffff },
  });
  label.anchor.set(0.5, 1);
  label.scale.set(px(1), px(1));
  container.addChild(label);

  const subLabel = new Text({
    text: "",
    style: { fontFamily: "monospace", fontSize: 9, fill: 0xffffff },
  });
  subLabel.anchor.set(0.5, 0);
  subLabel.scale.set(px(1), px(1));
  container.addChild(subLabel);

  let lastStatus = -1;
  let lastType = -1;
  let lastRadius = 0;
  let lastTier = -1;

  function redraw(status: number, type: number, radius: number, tier: number) {
    const active = status === POI_STATUS_ACTIVE;
    const tierColor = colorForTier(tier);
    const baseColor = active ? tierColor : 0x666666;
    const baseAlpha = active ? 1.0 : 0.4;
    const r = radius * radiusMulForTier(tier);

    ring.clear();
    ring.circle(0, 0, r).stroke({ color: baseColor, width: px(2), alpha: baseAlpha });
    ring.circle(0, 0, r * 0.65).stroke({ color: baseColor, width: px(1), alpha: baseAlpha * 0.4 });
    const tickInner = r * 0.85;
    const tickOuter = r * 1.05;
    for (let i = 0; i < 4; i++) {
      const a = (i / 4) * Math.PI * 2;
      ring.moveTo(Math.cos(a) * tickInner, Math.sin(a) * tickInner)
        .lineTo(Math.cos(a) * tickOuter, Math.sin(a) * tickOuter)
        .stroke({ color: baseColor, width: px(1.5), alpha: baseAlpha });
    }
    ring.circle(0, 0, px(2)).fill({ color: baseColor, alpha: baseAlpha });

    const name = POI_TYPE_NAMES[type] ?? `POI #${type}`;
    label.text = `${name.toUpperCase()} T${tier}`;
    label.style.fill = baseColor;
    label.alpha = baseAlpha;
    label.position.set(0, -r - px(4));

    const statusText = status === POI_STATUS_ACTIVE
      ? ""
      : status === POI_STATUS_CLEARED
        ? "CLEARED"
        : "COOLDOWN";
    subLabel.text = statusText;
    subLabel.style.fill = baseColor;
    subLabel.alpha = baseAlpha * 0.8;
    subLabel.position.set(0, r + px(4));
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      const e = ent.current as POIEntity;
      const radius = Math.max(e.radius || 60, 60);
      const tier = e.tier || 1;
      if (
        e.status !== lastStatus ||
        e.type !== lastType ||
        radius !== lastRadius ||
        tier !== lastTier
      ) {
        lastStatus = e.status;
        lastType = e.type;
        lastRadius = radius;
        lastTier = tier;
        redraw(e.status, e.type, radius, tier);
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
