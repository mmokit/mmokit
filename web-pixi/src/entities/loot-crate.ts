import { Container, Graphics } from "pixi.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

export function createLootCrateDisplay(radius: number): EntityDisplayObject {
  const container = new Container();
  const r = radius * 2;

  // Ambient glow (static)
  const glow = new Graphics();
  glow.circle(0, 0, r * 1.6).fill({ color: 0xffaa00, alpha: 0.04 });
  container.addChild(glow);

  // Crate body (static shape)
  const body = new Graphics();
  container.addChild(body);

  // Main hexagonal crate shape
  const sides = 6;
  const pts: number[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    pts.push(Math.cos(angle) * r * 0.7, Math.sin(angle) * r * 0.7);
  }
  body.poly(pts)
    .fill({ color: 0x2a1f0a, alpha: 0.6 })
    .stroke({ color: 0xffaa00, width: px(1.5), alpha: 0.7 });

  // Inner frame
  const innerPts: number[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    innerPts.push(Math.cos(angle) * r * 0.55, Math.sin(angle) * r * 0.55);
  }
  body.poly(innerPts).stroke({ color: 0xffaa00, width: px(1), alpha: 0.25 });

  // Panel lines from vertices to center
  for (let i = 0; i < sides; i += 2) {
    const angle = (i / sides) * Math.PI * 2;
    const ox = Math.cos(angle) * r * 0.7;
    const oy = Math.sin(angle) * r * 0.7;
    body.moveTo(ox, oy)
      .lineTo(Math.cos(angle) * r * 0.2, Math.sin(angle) * r * 0.2)
      .stroke({ color: 0xffaa00, width: px(1), alpha: 0.2 });
  }

  // Corner rivets
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    body.circle(Math.cos(angle) * r * 0.62, Math.sin(angle) * r * 0.62, r * 0.03)
      .fill({ color: 0xffcc44, alpha: 0.5 });
  }

  // Animated layers
  const beacon = new Graphics();
  beacon.label = "beacon";
  container.addChild(beacon);

  const scanLine = new Graphics();
  scanLine.label = "scan";
  container.addChild(scanLine);

  return {
    container,
    update(_ent: ClientEntity, _isMe: boolean, now: number) {
      const pulse = 0.6 + 0.4 * Math.sin(now * 0.003);
      const bob = Math.sin(now * 0.002) * r * 0.06;

      // Gentle bobbing
      body.position.y = bob;
      beacon.position.y = bob;
      scanLine.position.y = bob;

      // Center beacon
      beacon.clear();
      beacon.circle(0, 0, r * 0.12)
        .fill({ color: 0xffcc00, alpha: 0.3 + pulse * 0.4 });
      beacon.circle(0, 0, r * 0.05)
        .fill({ color: 0xffeebb, alpha: 0.6 + pulse * 0.3 });

      // Beacon ring pulse (expanding outward)
      const ringPhase = (now * 0.001) % 1;
      const ringR = r * (0.15 + ringPhase * 0.5);
      const ringAlpha = (1 - ringPhase) * 0.3;
      beacon.circle(0, 0, ringR)
        .stroke({ color: 0xffaa00, width: px(1), alpha: ringAlpha });

      // Slow rotation on the whole crate
      container.rotation = Math.sin(now * 0.0005) * 0.15;

      // Scan line sweep
      scanLine.clear();
      const sweepAngle = now * 0.002;
      const sx = Math.cos(sweepAngle) * r * 0.65;
      const sy = Math.sin(sweepAngle) * r * 0.65;
      scanLine.moveTo(0, 0).lineTo(sx, sy)
        .stroke({ color: 0xffdd44, width: px(1), alpha: 0.15 + pulse * 0.1 });
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
