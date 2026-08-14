import { Container, Graphics } from "pixi.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// Item IDs for resource types (must match server item registry).
const ITEM_ORE = 2;
const ITEM_CRYSTAL = 3;
const ITEM_GAS = 4;
const ITEM_METAL = 5;

export function createAsteroidDisplay(itemId: number, radius: number): EntityDisplayObject {
  const container = new Container();

  switch (itemId) {
    case ITEM_ORE:
      drawOre(container, radius);
      break;
    case ITEM_CRYSTAL:
      drawCrystal(container, radius);
      break;
    case ITEM_GAS:
      drawGas(container, radius);
      break;
    case ITEM_METAL:
      drawMetal(container, radius);
      break;
    default:
      drawOre(container, radius);
      break;
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      // Gas clouds have animated children
      if (itemId === ITEM_GAS) {
        updateGas(container, radius, now);
      }
      if (itemId === ITEM_CRYSTAL) {
        updateCrystal(container, radius, now);
      }
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}

function drawOre(container: Container, radius: number): void {
  const gfx = new Graphics();
  container.addChild(gfx);

  // Rocky body
  const sides = 10;
  const points: number[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    const jag = 0.72 + Math.sin(i * 5.1 + 2.3) * 0.18 + Math.cos(i * 3.7) * 0.1;
    const r = radius * jag;
    points.push(Math.cos(angle) * r, Math.sin(angle) * r);
  }
  gfx.poly(points).fill({ color: 0x3a2a15 }).stroke({ color: 0xcc9900, width: px(1.5) });

  // Surface veins
  for (let i = 0; i < 3; i++) {
    const a1 = i * 2.2 + 0.5;
    const a2 = a1 + 0.6 + Math.sin(i * 4.1) * 0.3;
    gfx.moveTo(Math.cos(a1) * radius * 0.2, Math.sin(a1) * radius * 0.2)
      .lineTo(Math.cos(a2) * radius * 0.6, Math.sin(a2) * radius * 0.6)
      .stroke({ color: 0xffb428, width: px(1.5), alpha: 0.5 });
  }

  // Ore glints
  for (let i = 0; i < 4; i++) {
    const ga = i * 1.7 + 0.8;
    const gr = radius * (0.25 + Math.sin(i * 3.3) * 0.2);
    gfx.circle(Math.cos(ga) * gr, Math.sin(ga) * gr, radius * 0.06)
      .fill({ color: 0xffc83c, alpha: 0.7 });
  }
}

function drawCrystal(container: Container, radius: number): void {
  // Spikes (will be updated for shimmer)
  const spikesGfx = new Graphics();
  spikesGfx.label = "spikes";
  container.addChild(spikesGfx);

  // Center facet
  const center = new Graphics();
  const spikes = 6;
  const facetPts: number[] = [];
  for (let i = 0; i < spikes; i++) {
    const angle = (i / spikes) * Math.PI * 2;
    const r = radius * 0.22;
    facetPts.push(Math.cos(angle) * r, Math.sin(angle) * r);
  }
  center.poly(facetPts).fill({ color: 0xc896ff, alpha: 0.4 }).stroke({ color: 0xdcb4ff, width: px(1), alpha: 0.7 });
  container.addChild(center);

  // Initial draw
  drawCrystalSpikes(spikesGfx, radius, performance.now());
}

function drawCrystalSpikes(gfx: Graphics, radius: number, now: number): void {
  gfx.clear();

  // Core glow
  gfx.circle(0, 0, radius * 0.6).fill({ color: 0xaa44ff, alpha: 0.15 });

  const spikes = 6;
  for (let i = 0; i < spikes; i++) {
    const angle = (i / spikes) * Math.PI * 2;
    const len = radius * (0.7 + Math.sin(i * 2.9 + 1.1) * 0.3);
    const width = radius * (0.12 + Math.sin(i * 4.3) * 0.05);
    const shimmer = 0.5 + 0.3 * Math.sin(now * 0.003 + i * 1.5);

    const cos = Math.cos(angle);
    const sin = Math.sin(angle);

    // Spike points (tip, left base, center, right base)
    const tipX = cos * len;
    const tipY = sin * len;
    const baseL_X = cos * len * 0.3 - sin * width;
    const baseL_Y = sin * len * 0.3 + cos * width;
    const baseR_X = cos * len * 0.3 + sin * width;
    const baseR_Y = sin * len * 0.3 - cos * width;

    gfx.poly([tipX, tipY, baseL_X, baseL_Y, 0, 0, baseR_X, baseR_Y])
      .fill({ color: 0xb464ff, alpha: 0.25 + shimmer * 0.2 })
      .stroke({ color: 0xc88cff, width: px(1), alpha: 0.6 + shimmer * 0.3 });
  }
}

function updateCrystal(container: Container, radius: number, now: number): void {
  for (const child of container.children) {
    if (child.label === "spikes") {
      drawCrystalSpikes(child as Graphics, radius, now);
      break;
    }
  }
}

function drawGas(container: Container, radius: number): void {
  // Main gas graphics (animated)
  const gasGfx = new Graphics();
  gasGfx.label = "gas";
  container.addChild(gasGfx);

  // Wisp particles
  const wisps = new Graphics();
  wisps.label = "wisps";
  container.addChild(wisps);

  // Boundary ring
  const boundary = new Graphics();
  boundary.circle(0, 0, radius).stroke({ color: 0x3cc8f0, width: px(1), alpha: 0.12 });
  container.addChild(boundary);

  drawGasBlobs(gasGfx, wisps, radius, performance.now());
}

function drawGasBlobs(gasGfx: Graphics, wisps: Graphics, radius: number, now: number): void {
  gasGfx.clear();

  // Blobs
  const blobs = 5;
  for (let i = 0; i < blobs; i++) {
    const phase = now * 0.0008 + i * 1.3;
    const drift = Math.sin(phase) * radius * 0.15;
    const driftY = Math.cos(phase * 0.7 + i) * radius * 0.12;
    const blobR = radius * (0.5 + Math.sin(i * 2.1 + 0.5) * 0.25);
    const bx = Math.cos(i * 1.26) * radius * 0.3 + drift;
    const by = Math.sin(i * 1.26) * radius * 0.3 + driftY;

    gasGfx.circle(bx, by, blobR).fill({ color: 0x3cc8f0, alpha: 0.08 + Math.sin(phase) * 0.03 });
  }

  // Bright core
  gasGfx.circle(0, 0, radius * 0.35).fill({ color: 0x64e6ff, alpha: 0.2 });

  // Wisps
  wisps.clear();
  for (let i = 0; i < 6; i++) {
    const wPhase = now * 0.001 + i * 1.05;
    const wa = (i / 6) * Math.PI * 2 + Math.sin(wPhase) * 0.5;
    const wr = radius * (0.3 + Math.sin(wPhase * 0.8) * 0.3);
    const wx = Math.cos(wa) * wr;
    const wy = Math.sin(wa) * wr;
    const walpha = 0.3 + 0.2 * Math.sin(wPhase * 1.3);
    wisps.circle(wx, wy, radius * 0.04).fill({ color: 0x96f0ff, alpha: walpha });
  }
}

function updateGas(container: Container, radius: number, now: number): void {
  let gasGfx: Graphics | null = null;
  let wisps: Graphics | null = null;
  for (const child of container.children) {
    if (child.label === "gas") gasGfx = child as Graphics;
    if (child.label === "wisps") wisps = child as Graphics;
  }
  if (gasGfx && wisps) {
    drawGasBlobs(gasGfx, wisps, radius, now);
  }
}

function drawMetal(container: Container, radius: number): void {
  const gfx = new Graphics();
  container.addChild(gfx);

  const sides = 7;
  const points: { x: number; y: number }[] = [];
  const flatPoints: number[] = [];
  for (let i = 0; i < sides; i++) {
    const angle = (i / sides) * Math.PI * 2;
    const r = radius * (0.85 + Math.sin(i * 2.6 + 1.0) * 0.12);
    const ptx = Math.cos(angle) * r;
    const pty = Math.sin(angle) * r;
    points.push({ x: ptx, y: pty });
    flatPoints.push(ptx, pty);
  }

  // Main body
  gfx.poly(flatPoints).fill({ color: 0x2a2a2e }).stroke({ color: 0x999999, width: px(1.5) });

  // Facet lines
  for (const pt of points) {
    gfx.moveTo(0, 0).lineTo(pt.x * 0.95, pt.y * 0.95).stroke({ color: 0xb4b4be, width: px(1), alpha: 0.2 });
  }

  // Reflective panels
  for (let i = 0; i < points.length; i++) {
    const next = (i + 1) % points.length;
    if (i % 2 === 0) {
      gfx.poly([0, 0, points[i].x, points[i].y, points[next].x, points[next].y])
        .fill({ color: 0xc8c8d2, alpha: 0.08 });
    }
  }

  // Specular highlight
  gfx.circle(-radius * 0.2, -radius * 0.2, radius * 0.15).fill({ color: 0xffffff, alpha: 0.15 });

  // Rivets
  for (const pt of points) {
    gfx.circle(pt.x * 0.7, pt.y * 0.7, radius * 0.03).fill({ color: 0xa0a0aa, alpha: 0.6 });
  }
}
