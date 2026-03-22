import { Container, Graphics, Text } from "pixi.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

export function createStationDisplay(radius: number): EntityDisplayObject {
  const container = new Container();
  const r = radius;
  const sides = 8;

  // Rotating ring container
  const ring = new Container();
  ring.label = "ring";
  container.addChild(ring);

  // Build ring graphics
  const ringGfx = new Graphics();
  ring.addChild(ringGfx);

  // Outer octagon
  const outerPts: number[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    outerPts.push(Math.cos(angle) * r, Math.sin(angle) * r);
  }
  ringGfx
    .poly(outerPts)
    .fill({ color: 0x64ff8c, alpha: 0.03 })
    .stroke({ color: 0x88ff88, width: px(2), alpha: 0.6 });

  // Inner octagon ring
  const innerPts: number[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    innerPts.push(Math.cos(angle) * r * 0.88, Math.sin(angle) * r * 0.88);
  }
  ringGfx.poly(innerPts).stroke({ color: 0x88ff88, width: px(1), alpha: 0.2 });

  // Struts connecting outer ring to hub
  for (let i = 0; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const ox = Math.cos(angle) * r * 0.88;
    const oy = Math.sin(angle) * r * 0.88;
    const ix = Math.cos(angle) * r * 0.35;
    const iy = Math.sin(angle) * r * 0.35;
    ringGfx
      .moveTo(ox, oy)
      .lineTo(ix, iy)
      .stroke({ color: 0x88ff88, width: px(1.5), alpha: 0.3 });
  }

  // Solar panels on odd vertices
  for (let i = 1; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const bx = Math.cos(angle) * r;
    const by = Math.sin(angle) * r;
    const panelLen = r * 0.3;
    const panelW = r * 0.05;

    // Arm
    const armEndX = bx + Math.cos(angle) * panelLen;
    const armEndY = by + Math.sin(angle) * panelLen;
    ringGfx
      .moveTo(bx, by)
      .lineTo(armEndX, armEndY)
      .stroke({ color: 0x88ff88, width: px(1), alpha: 0.25 });

    // Two panel rectangles per arm
    for (let s = 0; s < 2; s++) {
      const panX = bx + Math.cos(angle) * (panelLen * 0.35 + s * panelLen * 0.35);
      const panY = by + Math.sin(angle) * (panelLen * 0.35 + s * panelLen * 0.35);
      const perpX = -Math.sin(angle) * panelW;
      const perpY = Math.cos(angle) * panelW;
      const along = Math.cos(angle) * panelLen * 0.12;
      const alongY = Math.sin(angle) * panelLen * 0.12;

      ringGfx
        .poly([
          panX - along + perpX,
          panY - alongY + perpY,
          panX + along + perpX,
          panY + alongY + perpY,
          panX + along - perpX,
          panY + alongY - perpY,
          panX - along - perpX,
          panY - alongY - perpY,
        ])
        .fill({ color: 0x50b478, alpha: 0.1 })
        .stroke({ color: 0x88ff88, width: px(1), alpha: 0.35 });
    }
  }

  // Inner hub hexagon
  const hubSides = 6;
  const hubR = r * 0.3;
  const hubPts: number[] = [];
  for (let i = 0; i < hubSides; i++) {
    const angle = (i / hubSides) * Math.PI * 2;
    hubPts.push(Math.cos(angle) * hubR, Math.sin(angle) * hubR);
  }
  ringGfx
    .poly(hubPts)
    .fill({ color: 0x64ff8c, alpha: 0.06 })
    .stroke({ color: 0x88ff88, width: px(1.5), alpha: 0.5 });

  // Inner detail ring
  ringGfx
    .circle(0, 0, hubR * 0.5)
    .stroke({ color: 0x88ff88, width: px(1), alpha: 0.25 });

  // Dynamic graphics layers
  const core = new Graphics();
  core.label = "core";
  ring.addChild(core);

  const navLights = new Graphics();
  navLights.label = "navLights";
  ring.addChild(navLights);

  // Label (not rotating) — render at normal pixel font size, counter-scale
  // so text stays crisp despite the 30x world zoom
  const labelY = -r * 1.35; // clear solar panels
  const label = new Text({
    text: "TRADE STATION",
    style: {
      fontFamily: "monospace",
      fontSize: 18,
      fontWeight: "bold",
      fill: 0x88ff88,
    },
  });
  label.anchor.set(0.5, 1);
  label.scale.set(px(1), px(1));
  label.position.set(0, labelY - px(16));
  container.addChild(label);

  const sublabel = new Text({
    text: "[ PRESS X TO DOCK ]",
    style: { fontFamily: "monospace", fontSize: 12, fill: 0x88ff88 },
  });
  sublabel.anchor.set(0.5, 1);
  sublabel.scale.set(px(1), px(1));
  sublabel.alpha = 0.4;
  sublabel.position.set(0, labelY);
  container.addChild(sublabel);

  return {
    container,
    update(_ent: ClientEntity, _isMe: boolean, now: number) {
      // Slow rotation
      ring.rotation = now * 0.00015;
      const pulse = 0.7 + 0.3 * Math.sin(now * 0.002);

      // Core glow
      core.clear();
      core.circle(0, 0, r * 0.05).fill({
        color: 0x96ffb4,
        alpha: 0.4 + pulse * 0.6,
      });
      // Subtle core ring
      core.circle(0, 0, r * 0.1).stroke({
        color: 0x96ffb4,
        width: px(1),
        alpha: 0.15 + pulse * 0.15,
      });

      // Nav lights
      navLights.clear();
      const blinkOn = Math.sin(now * 0.004) > 0;
      const blinkOn2 = Math.sin(now * 0.004 + Math.PI) > 0;
      for (let i = 0; i < sides; i += 2) {
        const angle = (i / sides) * Math.PI * 2;
        const lx = Math.cos(angle) * r * 0.95;
        const ly = Math.sin(angle) * r * 0.95;
        const on = i % 4 === 0 ? blinkOn : blinkOn2;
        if (on) {
          const color = i % 4 === 0 ? 0xff5050 : 0x5082ff;
          navLights.circle(lx, ly, px(1.5)).fill({ color, alpha: 0.8 });
        }
      }

      // Label pulse
      label.alpha = 0.5 + pulse * 0.3;
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
