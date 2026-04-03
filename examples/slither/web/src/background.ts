import { Graphics, Container } from "pixi.js";
import type { Camera } from "./camera";

const GRID_SIZE = 200;
const GRID_COLOR = 0x222222;

export class Background {
  private graphics: Graphics;

  constructor(
    parentContainer: Container,
    private cellSize: number = 8192,
    private gridWidth: number = 2,
    private gridHeight: number = 2
  ) {
    this.graphics = new Graphics();
    parentContainer.addChild(this.graphics);
  }

  render(camera: Camera) {
    const g = this.graphics;
    g.clear();

    const left = camera.screenToWorldX(0);
    const right = camera.screenToWorldX(camera.screenWidth);
    const top = camera.screenToWorldY(0);
    const bottom = camera.screenToWorldY(camera.screenHeight);

    // --- Subtle dot grid ---
    const startX = Math.floor(left / GRID_SIZE) * GRID_SIZE;
    const startY = Math.floor(top / GRID_SIZE) * GRID_SIZE;

    for (let y = startY; y <= bottom; y += GRID_SIZE) {
      const row = Math.round(y / GRID_SIZE);
      const xOff = (row % 2 === 0) ? 0 : GRID_SIZE / 2;
      for (let x = startX + xOff; x <= right; x += GRID_SIZE) {
        const sx = camera.worldToScreenX(x);
        const sy = camera.worldToScreenY(y);
        g.circle(sx, sy, 1.5);
        g.fill(GRID_COLOR);
      }
    }

    // --- Cell boundaries (dashed blue lines, interior only) ---
    const worldW = this.gridWidth * this.cellSize;
    const worldH = this.gridHeight * this.cellSize;

    // Interior vertical cell lines
    for (let sx = 1; sx < this.gridWidth; sx++) {
      const worldX = sx * this.cellSize;
      const screenX = camera.worldToScreenX(worldX);
      this.drawDashedLineV(g, screenX, 0, camera.screenHeight, 0x2244aa, 0.35, 1);
    }
    // Interior horizontal cell lines
    for (let sy = 1; sy < this.gridHeight; sy++) {
      const worldY = sy * this.cellSize;
      const screenY = camera.worldToScreenY(worldY);
      this.drawDashedLineH(g, screenY, 0, camera.screenWidth, 0x2244aa, 0.35, 1);
    }

    // --- World edge (solid bright border with glow) ---
    const edgeColor = 0xff4444;
    const edges = [
      { type: "v" as const, world: 0 },
      { type: "v" as const, world: worldW },
      { type: "h" as const, world: 0 },
      { type: "h" as const, world: worldH },
    ];

    for (const edge of edges) {
      if (edge.type === "v") {
        const sx = camera.worldToScreenX(edge.world);
        if (sx < -50 || sx > camera.screenWidth + 50) continue;
        // Glow
        g.rect(sx - 8, 0, 16, camera.screenHeight);
        g.fill({ color: edgeColor, alpha: 0.06 });
        // Solid line
        g.setStrokeStyle({ width: 2.5, color: edgeColor, alpha: 0.5 });
        g.moveTo(sx, 0);
        g.lineTo(sx, camera.screenHeight);
        g.stroke();
      } else {
        const sy = camera.worldToScreenY(edge.world);
        if (sy < -50 || sy > camera.screenHeight + 50) continue;
        // Glow
        g.rect(0, sy - 8, camera.screenWidth, 16);
        g.fill({ color: edgeColor, alpha: 0.06 });
        // Solid line
        g.setStrokeStyle({ width: 2.5, color: edgeColor, alpha: 0.5 });
        g.moveTo(0, sy);
        g.lineTo(camera.screenWidth, sy);
        g.stroke();
      }
    }

    // --- Out-of-bounds darkening ---
    // Darken area outside the world with a semi-transparent overlay
    const worldLeft = camera.worldToScreenX(0);
    const worldRight = camera.worldToScreenX(worldW);
    const worldTop = camera.worldToScreenY(0);
    const worldBottom = camera.worldToScreenY(worldH);

    const oobAlpha = 0.5;
    // Left of world
    if (worldLeft > 0) {
      g.rect(0, 0, worldLeft, camera.screenHeight);
      g.fill({ color: 0x000000, alpha: oobAlpha });
    }
    // Right of world
    if (worldRight < camera.screenWidth) {
      g.rect(worldRight, 0, camera.screenWidth - worldRight, camera.screenHeight);
      g.fill({ color: 0x000000, alpha: oobAlpha });
    }
    // Top of world
    if (worldTop > 0) {
      g.rect(
        Math.max(0, worldLeft),
        0,
        Math.min(camera.screenWidth, worldRight) - Math.max(0, worldLeft),
        worldTop
      );
      g.fill({ color: 0x000000, alpha: oobAlpha });
    }
    // Bottom of world
    if (worldBottom < camera.screenHeight) {
      g.rect(
        Math.max(0, worldLeft),
        worldBottom,
        Math.min(camera.screenWidth, worldRight) - Math.max(0, worldLeft),
        camera.screenHeight - worldBottom
      );
      g.fill({ color: 0x000000, alpha: oobAlpha });
    }
  }

  private drawDashedLineV(
    g: Graphics,
    x: number,
    y1: number,
    y2: number,
    color: number,
    alpha: number,
    width: number
  ) {
    const dashLen = 12;
    const gapLen = 12;
    g.setStrokeStyle({ width, color, alpha });
    let y = y1;
    while (y < y2) {
      g.moveTo(x, y);
      g.lineTo(x, Math.min(y + dashLen, y2));
      g.stroke();
      y += dashLen + gapLen;
    }
  }

  private drawDashedLineH(
    g: Graphics,
    y: number,
    x1: number,
    x2: number,
    color: number,
    alpha: number,
    width: number
  ) {
    const dashLen = 12;
    const gapLen = 12;
    g.setStrokeStyle({ width, color, alpha });
    let x = x1;
    while (x < x2) {
      g.moveTo(x, y);
      g.lineTo(Math.min(x + dashLen, x2), y);
      g.stroke();
      x += dashLen + gapLen;
    }
  }

  destroy() {
    this.graphics.destroy();
  }
}
