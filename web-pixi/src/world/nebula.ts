import { Container, Graphics } from "pixi.js";

function mulberry32(a: number): () => number {
  return function () {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

interface NebulaCloud {
  x: number;
  y: number;
  container: Container;
  baseAlpha: number;
  breatheSpeed: number;
  breatheOffset: number;
}

const NEBULA_PALETTE = [0xff2266, 0x8822ff, 0x2266ff, 0x00ccff];

export class Nebula {
  private clouds: NebulaCloud[] = [];
  private outerContainer: Container;
  private readonly tileSize = 2000;
  private readonly parallax = 0.02;

  constructor(parent: Container) {
    this.outerContainer = new Container();
    parent.addChild(this.outerContainer);

    const rng = mulberry32(99999);
    const count = 5;

    for (let i = 0; i < count; i++) {
      const container = new Container();
      this.outerContainer.addChild(container);

      // --- Color blobs: 3–4 overlapping ellipses ---
      const blobGfx = new Graphics();
      const numBlobs = 3 + Math.floor(rng() * 2); // 3 or 4
      const color = NEBULA_PALETTE[Math.floor(rng() * NEBULA_PALETTE.length)];
      const rx = 1200 + rng() * 1600; // 1200–2800 — screen-spanning haze
      const ry = rx * (0.18 + rng() * 0.28); // 0.18–0.46 aspect — flat/elongated

      const blobAlphas = [0.04, 0.07, 0.11, 0.17];
      const blobScales = [1.0, 0.72, 0.50, 0.30];
      for (let b = 0; b < numBlobs; b++) {
        blobGfx
          .ellipse(0, 0, rx * blobScales[b], ry * blobScales[b])
          .fill({ color, alpha: blobAlphas[b] });
      }
      container.addChild(blobGfx);

      // --- Wisps: 4–6 bezier strokes at very low alpha ---
      const wispGfx = new Graphics();
      const numWisps = 4 + Math.floor(rng() * 3); // 4–6
      for (let w = 0; w < numWisps; w++) {
        const wispAlpha = 0.03 + rng() * 0.03; // 0.03–0.06
        const wispWidth = 12 + rng() * 13;     // 12–25
        const spread = rx * 0.9;
        const x0 = (rng() - 0.5) * spread * 2;
        const y0 = (rng() - 0.5) * ry * 1.5;
        const x3 = (rng() - 0.5) * spread * 2;
        const y3 = (rng() - 0.5) * ry * 1.5;
        const cx1 = (rng() - 0.5) * spread * 1.5;
        const cy1 = (rng() - 0.5) * ry;
        const cx2 = (rng() - 0.5) * spread * 1.5;
        const cy2 = (rng() - 0.5) * ry;
        wispGfx
          .moveTo(x0, y0)
          .bezierCurveTo(cx1, cy1, cx2, cy2, x3, y3)
          .stroke({ color, alpha: wispAlpha, width: wispWidth, cap: "round" });
      }
      container.addChild(wispGfx);

      const baseAlpha = 0.8 + rng() * 0.2;
      container.alpha = baseAlpha;

      this.clouds.push({
        x: rng() * this.tileSize,
        y: rng() * this.tileSize,
        container,
        baseAlpha,
        breatheSpeed: 0.04 + rng() * 0.04,
        breatheOffset: rng() * Math.PI * 2,
      });
    }
  }

  update(cameraX: number, cameraY: number, screenW: number, screenH: number, now: number): void {
    const offX = (cameraX * this.parallax) % this.tileSize;
    const offY = (cameraY * this.parallax) % this.tileSize;
    const cullMargin = 2800;

    this.outerContainer.position.set(cameraX - screenW / 2, cameraY - screenH / 2);

    for (const cloud of this.clouds) {
      const sx = ((cloud.x - offX) % this.tileSize + this.tileSize) % this.tileSize;
      const sy = ((cloud.y - offY) % this.tileSize + this.tileSize) % this.tileSize;

      if (sx > screenW + cullMargin || sy > screenH + cullMargin) {
        cloud.container.visible = false;
        continue;
      }

      cloud.container.visible = true;
      cloud.container.position.set(sx, sy);
      const breath = 0.85 + 0.15 * Math.sin(now * 0.001 * cloud.breatheSpeed + cloud.breatheOffset);
      cloud.container.alpha = cloud.baseAlpha * breath;
    }
  }

  destroy(): void {
    this.outerContainer.destroy({ children: true });
  }
}
