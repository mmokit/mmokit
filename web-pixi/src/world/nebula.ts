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

interface NebulaRegion {
  container: Container;
  baseX: number;
  baseY: number;
  parallax: number;
  breatheSpeed: number;
  breatheOffset: number;
}

// Dark, desaturated space colours — NOT hot pink/cyan
const NEBULA_PALETTE = [
  0x0a1a5a, 0x180844, 0x0a2838, 0x280840, 0x082818, 0x2a1808,
];

// Three depth layers: far (0.1), mid (0.2), near (0.4)
// Far = larger, more transparent; near = smaller, slightly denser
const LAYER_CONFIGS = [
  { parallax: 0.1, count: 8, rxMin: 60, rxMax: 107, alphaBase: 0.06, seed: 99991 },
  { parallax: 0.2, count: 7, rxMin: 40, rxMax: 73, alphaBase: 0.09, seed: 99992 },
  { parallax: 0.4, count: 6, rxMin: 23, rxMax: 47, alphaBase: 0.12, seed: 99993 },
];

export class Nebula {
  private regions: NebulaRegion[] = [];
  private outerContainer: Container;

  constructor(parent: Container) {
    this.outerContainer = new Container();
    parent.addChild(this.outerContainer);

    const worldSize = 333;

    for (const layer of LAYER_CONFIGS) {
      const rng = mulberry32(layer.seed);

      for (let i = 0; i < layer.count; i++) {
        const container = new Container();

        const color = NEBULA_PALETTE[Math.floor(rng() * NEBULA_PALETTE.length)];
        const rx = layer.rxMin + rng() * (layer.rxMax - layer.rxMin);
        const ry = rx * (0.2 + rng() * 0.25);
        const rotation = rng() * Math.PI;
        const alpha = layer.alphaBase + rng() * 0.06;

        const gfx = new Graphics();
        gfx.ellipse(0, 0, rx, ry).fill({ color, alpha: alpha * 0.5 });
        gfx.ellipse(0, 0, rx * 0.55, ry * 0.55).fill({ color, alpha });

        container.addChild(gfx);
        container.rotation = rotation;
        this.outerContainer.addChild(container);

        this.regions.push({
          container,
          baseX: rng() * worldSize,
          baseY: rng() * worldSize,
          parallax: layer.parallax,
          breatheSpeed: 0.025 + rng() * 0.025,
          breatheOffset: rng() * Math.PI * 2,
        });
      }
    }
  }

  update(
    cameraX: number,
    cameraY: number,
    sectorOffX: number,
    sectorOffY: number,
    _screenW: number,
    _screenH: number,
    now: number,
  ): void {
    // Use absolute world coordinates for parallax so the pattern is
    // continuous across sector transfers.
    const absX = cameraX + sectorOffX;
    const absY = cameraY + sectorOffY;
    for (const region of this.regions) {
      const cx = absX * (1 - region.parallax);
      const cy = absY * (1 - region.parallax);
      region.container.position.set(region.baseX + cx, region.baseY + cy);
      const breath =
        0.88 +
        0.12 *
          Math.sin(now * 0.001 * region.breatheSpeed + region.breatheOffset);
      region.container.alpha = breath;
    }
  }

  destroy(): void {
    this.outerContainer.destroy({ children: true });
  }
}
