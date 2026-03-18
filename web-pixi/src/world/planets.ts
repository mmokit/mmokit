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

interface Planet {
  x: number;
  y: number;
  container: Container;
  atmoGfx: Graphics;
  pulseSpeed: number;
  pulseOffset: number;
}

const BODY_PALETTE  = [0x1a3a6e, 0x2e1050, 0x0e2e1a, 0x3a2210, 0x1a1a40];
const ACCENT_PALETTE = [0x3388ff, 0xaa66ff, 0x44cc88, 0xff8844, 0x88aaff];

export class Planets {
  private planets: Planet[] = [];
  private outerContainer: Container;
  private readonly tileSize = 2000;
  private readonly parallax = 0.08;

  constructor(parent: Container) {
    this.outerContainer = new Container();
    parent.addChild(this.outerContainer);

    const rng = mulberry32(77777);
    const count = 4 + Math.floor(rng() * 3); // 4–6

    for (let i = 0; i < count; i++) {
      const container = new Container();
      this.outerContainer.addChild(container);

      const r = 35 + rng() * 50; // 35–85
      const bodyColor  = BODY_PALETTE[Math.floor(rng() * BODY_PALETTE.length)];
      const accentColor = ACCENT_PALETTE[Math.floor(rng() * ACCENT_PALETTE.length)];
      const hasRings = rng() < 0.4;

      // 1. Atmosphere glow (4 concentric circles, outermost first)
      const atmoGfx = new Graphics();
      const atmoRadii  = [r * 1.45, r * 1.25, r * 1.12, r * 1.05];
      const atmoAlphas = [0.07,     0.14,     0.20,     0.28];
      for (let a = 0; a < 4; a++) {
        atmoGfx.circle(0, 0, atmoRadii[a]).fill({ color: accentColor, alpha: atmoAlphas[a] });
      }
      container.addChild(atmoGfx);

      // 2. Body (fill + highlight + cloud bands + outline) — one Graphics object
      const bodyGfx = new Graphics();

      // Body fill
      bodyGfx.circle(0, 0, r).fill({ color: bodyColor });

      // Lit highlight (upper-left offset circle)
      const hlR = r * 0.55;
      const hlOffset = r * 0.28;
      const hlColor = Math.min(bodyColor + 0x151515, 0xffffff);
      bodyGfx.circle(-hlOffset, -hlOffset, hlR).fill({ color: hlColor, alpha: 0.12 });

      // Cloud bands (2–3 thin ellipses across the disk)
      const numBands = 2 + Math.floor(rng() * 2);
      for (let b = 0; b < numBands; b++) {
        const bandY = -r * 0.6 + (b + 1) * (r * 1.2 / (numBands + 1));
        const bandH = r * (0.12 + rng() * 0.1);
        bodyGfx.ellipse(0, bandY, r, bandH).fill({ color: accentColor, alpha: 0.07 + rng() * 0.04 });
      }

      // Body outline
      bodyGfx.circle(0, 0, r).stroke({ color: accentColor, alpha: 0.4, width: 1 });
      container.addChild(bodyGfx);

      // 3. Rings (optional)
      if (hasRings) {
        const ringGfx = new Graphics();
        ringGfx
          .ellipse(0, 0, r * 1.35, r * 0.22)
          .stroke({ color: accentColor, alpha: 0.35, width: 1.5 });
        ringGfx
          .ellipse(0, 0, r * 1.55, r * 0.26)
          .stroke({ color: accentColor, alpha: 0.2, width: 1 });
        container.addChild(ringGfx);
      }

      this.planets.push({
        x: rng() * this.tileSize,
        y: rng() * this.tileSize,
        container,
        atmoGfx,
        pulseSpeed: 0.06 + rng() * 0.06,
        pulseOffset: rng() * Math.PI * 2,
      });
    }
  }

  update(cameraX: number, cameraY: number, screenW: number, screenH: number, now: number): void {
    const offX = (cameraX * this.parallax) % this.tileSize;
    const offY = (cameraY * this.parallax) % this.tileSize;
    const cullMargin = 185; // max planet radius (85) + 100

    this.outerContainer.position.set(cameraX - screenW / 2, cameraY - screenH / 2);

    for (const planet of this.planets) {
      const sx = ((planet.x - offX) % this.tileSize + this.tileSize) % this.tileSize;
      const sy = ((planet.y - offY) % this.tileSize + this.tileSize) % this.tileSize;

      if (sx > screenW + cullMargin || sy > screenH + cullMargin) {
        planet.container.visible = false;
        continue;
      }

      planet.container.visible = true;
      planet.container.position.set(sx, sy);
      const pulse = 0.8 + 0.2 * Math.sin(now * 0.001 * planet.pulseSpeed + planet.pulseOffset);
      planet.atmoGfx.alpha = pulse;
    }
  }

  destroy(): void {
    this.outerContainer.destroy({ children: true });
  }
}
