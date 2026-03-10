import type { StarLayer } from "../types";

function mulberry32(a: number): () => number {
  return function () {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function createStarLayers(): StarLayer[] {
  const layerConfigs = [
    { count: 200, parallax: 0.05, sizeMin: 0.5, sizeMax: 1.0, alphaMin: 0.15, alphaMax: 0.35 },
    { count: 120, parallax: 0.15, sizeMin: 0.8, sizeMax: 1.5, alphaMin: 0.25, alphaMax: 0.5 },
    { count: 60, parallax: 0.3, sizeMin: 1.0, sizeMax: 2.0, alphaMin: 0.4, alphaMax: 0.7 },
  ];

  const rng = mulberry32(12345);
  const layers: StarLayer[] = [];

  for (const cfg of layerConfigs) {
    const stars = [];
    const tileSize = 4000;
    for (let i = 0; i < cfg.count; i++) {
      stars.push({
        x: rng() * tileSize,
        y: rng() * tileSize,
        size: cfg.sizeMin + rng() * (cfg.sizeMax - cfg.sizeMin),
        alpha: cfg.alphaMin + rng() * (cfg.alphaMax - cfg.alphaMin),
        twinkleSpeed: 0.5 + rng() * 2.0,
        twinkleOffset: rng() * Math.PI * 2,
      });
    }
    layers.push({ stars, parallax: cfg.parallax, tileSize });
  }

  return layers;
}

export function drawStarfield(
  ctx: CanvasRenderingContext2D,
  layers: StarLayer[],
  cameraX: number,
  cameraY: number,
  canvasW: number,
  canvasH: number,
  now: number,
): void {
  for (const layer of layers) {
    const offX = (cameraX * layer.parallax) % layer.tileSize;
    const offY = (cameraY * layer.parallax) % layer.tileSize;
    for (const star of layer.stars) {
      const sx = ((star.x - offX) % layer.tileSize + layer.tileSize) % layer.tileSize;
      const sy = ((star.y - offY) % layer.tileSize + layer.tileSize) % layer.tileSize;
      if (sx > canvasW + 2) continue;
      if (sy > canvasH + 2) continue;
      const twinkle = 0.7 + 0.3 * Math.sin(now * 0.001 * star.twinkleSpeed + star.twinkleOffset);
      const a = star.alpha * twinkle;
      ctx.fillStyle = `rgba(255, 255, 255, ${a})`;
      ctx.fillRect(sx, sy, star.size, star.size);
    }
  }
}
