import { Container, Graphics } from "pixi.js";
import { zoom } from "../view";

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
      { count: 300, parallax: 0.02, sizeMin: 0.01, sizeMax: 0.023, alphaMin: 0.08, alphaMax: 0.22 },
      { count: 200, parallax: 0.05, sizeMin: 0.017, sizeMax: 0.033, alphaMin: 0.15, alphaMax: 0.35 },
      { count: 120, parallax: 0.15, sizeMin: 0.027, sizeMax: 0.05, alphaMin: 0.25, alphaMax: 0.5 },
      { count: 60, parallax: 0.3, sizeMin: 0.033, sizeMax: 0.067, alphaMin: 0.4, alphaMax: 0.7 },
      { count: 25, parallax: 0.5, sizeMin: 0.05, sizeMax: 0.1, alphaMin: 0.5, alphaMax: 0.9 },
    ];

    const rng = mulberry32(12345);
    const tileSize = 133;

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
    // Visible world extent (screen pixels / zoom)
    const viewW = screenW / zoom();
    const viewH = screenH / zoom();

    for (const layer of this.layers) {
      // Position the layer container so stars tile correctly with parallax
      const offX = (cameraX * layer.parallax) % layer.tileSize;
      const offY = (cameraY * layer.parallax) % layer.tileSize;

      // Place container in world space at camera position so it renders in view
      layer.container.position.set(
        cameraX - viewW / 2,
        cameraY - viewH / 2,
      );

      for (const star of layer.stars) {
        const sx = ((star.x - offX) % layer.tileSize + layer.tileSize) % layer.tileSize;
        const sy = ((star.y - offY) % layer.tileSize + layer.tileSize) % layer.tileSize;

        if (sx > viewW + 0.1 || sy > viewH + 0.1) {
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
