import { Container, Graphics } from "pixi.js";

interface Star {
  x: number;
  y: number;
  size: number;
  alpha: number;
  twinkleSpeed: number;
  twinkleOffset: number;
  gfx: Graphics;
}

interface StarLayer {
  stars: Star[];
  parallax: number;
  tileSize: number;
  container: Container;
}

function mulberry32(a: number): () => number {
  return function () {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export class Starfield {
  private layers: StarLayer[] = [];
  private parentContainer: Container;

  constructor(parent: Container) {
    this.parentContainer = parent;

    const layerConfigs = [
      { count: 300, parallax: 0.02, sizeMin: 0.3, sizeMax: 0.7, alphaMin: 0.08, alphaMax: 0.22 },
      { count: 200, parallax: 0.05, sizeMin: 0.5, sizeMax: 1.0, alphaMin: 0.15, alphaMax: 0.35 },
      { count: 120, parallax: 0.15, sizeMin: 0.8, sizeMax: 1.5, alphaMin: 0.25, alphaMax: 0.5 },
      { count: 60, parallax: 0.3, sizeMin: 1.0, sizeMax: 2.0, alphaMin: 0.4, alphaMax: 0.7 },
      { count: 25, parallax: 0.5, sizeMin: 1.5, sizeMax: 3.0, alphaMin: 0.5, alphaMax: 0.9 },
    ];

    const rng = mulberry32(12345);
    const tileSize = 4000;

    for (const cfg of layerConfigs) {
      const container = new Container();
      parent.addChild(container);

      const stars: Star[] = [];
      for (let i = 0; i < cfg.count; i++) {
        const gfx = new Graphics();
        const size = cfg.sizeMin + rng() * (cfg.sizeMax - cfg.sizeMin);
        gfx.rect(0, 0, size, size).fill({ color: 0xffffff });
        container.addChild(gfx);

        stars.push({
          x: rng() * tileSize,
          y: rng() * tileSize,
          size,
          alpha: cfg.alphaMin + rng() * (cfg.alphaMax - cfg.alphaMin),
          twinkleSpeed: 0.5 + rng() * 2.0,
          twinkleOffset: rng() * Math.PI * 2,
          gfx,
        });
      }

      this.layers.push({ stars, parallax: cfg.parallax, tileSize, container });
    }
  }

  update(cameraX: number, cameraY: number, screenW: number, screenH: number, now: number): void {
    for (const layer of this.layers) {
      // Position the layer container so stars tile correctly with parallax
      const offX = (cameraX * layer.parallax) % layer.tileSize;
      const offY = (cameraY * layer.parallax) % layer.tileSize;

      // Place container in world space at camera position so it renders in view
      layer.container.position.set(
        cameraX - screenW / 2,
        cameraY - screenH / 2,
      );

      for (const star of layer.stars) {
        const sx = ((star.x - offX) % layer.tileSize + layer.tileSize) % layer.tileSize;
        const sy = ((star.y - offY) % layer.tileSize + layer.tileSize) % layer.tileSize;

        if (sx > screenW + 2 || sy > screenH + 2) {
          star.gfx.visible = false;
          continue;
        }

        star.gfx.visible = true;
        star.gfx.position.set(sx, sy);
        const twinkle = 0.7 + 0.3 * Math.sin(now * 0.001 * star.twinkleSpeed + star.twinkleOffset);
        star.gfx.alpha = star.alpha * twinkle;
      }
    }
  }

  destroy(): void {
    for (const layer of this.layers) {
      this.parentContainer.removeChild(layer.container);
      layer.container.destroy({ children: true });
    }
    this.layers = [];
  }
}
