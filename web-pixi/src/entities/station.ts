import { Container, Graphics, Text } from "pixi.js";
import type { ClientEntity, EntityDisplayObject } from "../types";

export function createStationDisplay(radius: number): EntityDisplayObject {
  const container = new Container();
  const r = radius * 1.6;
  const sides = 8;

  // Ambient glow
  const glow = new Graphics();
  glow.circle(0, 0, r * 1.8).fill({ color: 0x64ff8c, alpha: 0.04 });
  container.addChild(glow);

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
  ringGfx.poly(outerPts).fill({ color: 0x64ff8c, alpha: 0.04 }).stroke({ color: 0x88ff88, width: 2, alpha: 0.7 });

  // Inner octagon ring
  const innerPts: number[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    innerPts.push(Math.cos(angle) * r * 0.92, Math.sin(angle) * r * 0.92);
  }
  ringGfx.poly(innerPts).stroke({ color: 0x88ff88, width: 1, alpha: 0.25 });

  // Struts
  for (let i = 0; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const ox = Math.cos(angle) * r * 0.92;
    const oy = Math.sin(angle) * r * 0.92;
    const ix = Math.cos(angle) * r * 0.38;
    const iy = Math.sin(angle) * r * 0.38;
    ringGfx.moveTo(ox, oy).lineTo(ix, iy).stroke({ color: 0x88ff88, width: 2, alpha: 0.4 });
  }

  // Solar panels
  for (let i = 1; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const bx = Math.cos(angle) * r;
    const by = Math.sin(angle) * r;
    const panelLen = r * 0.35;
    const panelW = r * 0.06;

    // Panel arm
    const armEndX = bx + Math.cos(angle) * panelLen;
    const armEndY = by + Math.sin(angle) * panelLen;
    ringGfx.moveTo(bx, by).lineTo(armEndX, armEndY).stroke({ color: 0x88ff88, width: 1, alpha: 0.3 });

    // Panel rectangles (simplified - axis-aligned approach)
    for (let s = 0; s < 2; s++) {
      const px = bx + Math.cos(angle) * (panelLen * 0.35 + s * panelLen * 0.35);
      const py = by + Math.sin(angle) * (panelLen * 0.35 + s * panelLen * 0.35);
      const perpX = -Math.sin(angle) * panelW;
      const perpY = Math.cos(angle) * panelW;
      const along = Math.cos(angle) * panelLen * 0.14;
      const alongY = Math.sin(angle) * panelLen * 0.14;

      ringGfx.poly([
        px - along + perpX, py - alongY + perpY,
        px + along + perpX, py + alongY + perpY,
        px + along - perpX, py + alongY - perpY,
        px - along - perpX, py - alongY - perpY,
      ]).fill({ color: 0x50b478, alpha: 0.12 }).stroke({ color: 0x88ff88, width: 1, alpha: 0.4 });
    }
  }

  // Inner hub (hexagon)
  const hubSides = 6;
  const hubR = r * 0.35;
  const hubPts: number[] = [];
  for (let i = 0; i < hubSides; i++) {
    const angle = (i / hubSides) * Math.PI * 2;
    hubPts.push(Math.cos(angle) * hubR, Math.sin(angle) * hubR);
  }
  ringGfx.poly(hubPts).fill({ color: 0x64ff8c, alpha: 0.08 }).stroke({ color: 0x88ff88, width: 1.5, alpha: 0.6 });

  // Inner detail ring
  ringGfx.circle(0, 0, hubR * 0.55).stroke({ color: 0x88ff88, width: 1, alpha: 0.3 });

  // Center core
  const core = new Graphics();
  core.label = "core";
  ring.addChild(core);

  // Nav lights
  const navLights = new Graphics();
  navLights.label = "navLights";
  ring.addChild(navLights);

  // Label (not rotating)
  const label = new Text({ text: "TRADE STATION", style: { fontFamily: "monospace", fontSize: 14, fontWeight: "bold", fill: 0x88ff88 } });
  label.anchor.set(0.5, 1);
  label.position.set(0, -r * 1.4 - 14);
  container.addChild(label);

  const sublabel = new Text({ text: "[ PRESS E TO SELL ]", style: { fontFamily: "monospace", fontSize: 10, fill: 0x88ff88 } });
  sublabel.anchor.set(0.5, 1);
  sublabel.alpha = 0.4;
  sublabel.position.set(0, -r * 1.4);
  container.addChild(sublabel);

  return {
    container,
    update(_ent: ClientEntity, _isMe: boolean, now: number) {
      // Slow rotation
      ring.rotation = now * 0.0002;
      const pulse = 0.7 + 0.3 * Math.sin(now * 0.002);

      // Core
      core.clear();
      core.circle(0, 0, r * 0.06).fill({ color: 0x96ffb4, alpha: 0.5 + pulse * 0.5 });

      // Nav lights
      navLights.clear();
      const blinkOn = Math.sin(now * 0.005) > 0;
      const blinkOn2 = Math.sin(now * 0.005 + Math.PI) > 0;
      for (let i = 0; i < sides; i += 2) {
        const angle = (i / sides) * Math.PI * 2;
        const lx = Math.cos(angle) * r * 0.98;
        const ly = Math.sin(angle) * r * 0.98;
        const on = i % 4 === 0 ? blinkOn : blinkOn2;
        if (on) {
          const color = i % 4 === 0 ? 0xff5050 : 0x5082ff;
          navLights.circle(lx, ly, 2).fill({ color, alpha: 0.9 });
        }
      }

      // Docking port markers
      for (let i = 0; i < sides; i++) {
        const midAngle = ((i + 0.5) / sides) * Math.PI * 2;
        const dx = Math.cos(midAngle) * r;
        const dy = Math.sin(midAngle) * r;
        navLights.rect(dx - 3, dy - 1.5, 6, 3).fill({ color: 0x88ff88, alpha: 0.15 + pulse * 0.1 });
      }

      // Label pulse
      label.alpha = 0.6 + pulse * 0.3;
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
