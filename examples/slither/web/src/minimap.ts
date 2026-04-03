import type { GameState } from "./state";
import type { Camera } from "./camera";
import type { InterpolatedSnake } from "./interpolation";

const MAP_SIZE = 200;
const MAP_MARGIN = 10;

// World bounds: 2x2 grid of 8192-unit cells
const CELL_SIZE = 8192;
const GRID_W = 2;
const GRID_H = 2;
const WORLD_MIN = 0;
const WORLD_W = GRID_W * CELL_SIZE;
const WORLD_H = GRID_H * CELL_SIZE;

export class Minimap {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  private frameCounter: number = 0;

  constructor() {
    this.canvas = document.createElement("canvas");
    this.canvas.width = MAP_SIZE;
    this.canvas.height = MAP_SIZE;
    this.canvas.style.cssText = `
      position: fixed;
      bottom: ${MAP_MARGIN}px;
      right: ${MAP_MARGIN}px;
      width: ${MAP_SIZE}px;
      height: ${MAP_SIZE}px;
      border: 1px solid rgba(255, 255, 255, 0.3);
      border-radius: 4px;
      background: rgba(0, 0, 0, 0.6);
      pointer-events: none;
      z-index: 100;
    `;
    this.ctx = this.canvas.getContext("2d")!;
    document.body.appendChild(this.canvas);
  }

  update(state: GameState, snakes: InterpolatedSnake[], camera: Camera) {
    this.frameCounter++;
    if (this.frameCounter % 3 !== 0) return; // Update every 3 frames

    const ctx = this.ctx;
    ctx.clearRect(0, 0, MAP_SIZE, MAP_SIZE);

    // World bounds
    ctx.strokeStyle = "rgba(255, 255, 255, 0.2)";
    ctx.lineWidth = 1;
    ctx.strokeRect(0, 0, MAP_SIZE, MAP_SIZE);

    // Grid lines (cells)
    ctx.strokeStyle = "rgba(255, 255, 255, 0.08)";
    ctx.beginPath();
    for (let x = WORLD_MIN; x <= WORLD_MIN + WORLD_W; x += CELL_SIZE) {
      const sx = ((x - WORLD_MIN) / WORLD_W) * MAP_SIZE;
      ctx.moveTo(sx, 0);
      ctx.lineTo(sx, MAP_SIZE);
    }
    for (let y = WORLD_MIN; y <= WORLD_MIN + WORLD_H; y += CELL_SIZE) {
      const sy = ((y - WORLD_MIN) / WORLD_H) * MAP_SIZE;
      ctx.moveTo(0, sy);
      ctx.lineTo(MAP_SIZE, sy);
    }
    ctx.stroke();

    // Draw snakes as dots
    for (const snake of snakes) {
      const mx = ((snake.headX - WORLD_MIN) / WORLD_W) * MAP_SIZE;
      const my = ((snake.headY - WORLD_MIN) / WORLD_H) * MAP_SIZE;
      const radius = Math.max(2, Math.sqrt(snake.mass) * 0.08);

      if (snake.id === state.myEntityID) {
        // Own position: bright white
        ctx.fillStyle = "#ffffff";
        ctx.beginPath();
        ctx.arc(mx, my, Math.max(3, radius), 0, Math.PI * 2);
        ctx.fill();
      } else {
        // Others: dim colored
        ctx.fillStyle = "rgba(200, 200, 200, 0.5)";
        ctx.beginPath();
        ctx.arc(mx, my, radius, 0, Math.PI * 2);
        ctx.fill();
      }
    }

    // Camera viewport rectangle
    const vpLeft = ((camera.screenToWorldX(0) - WORLD_MIN) / WORLD_W) * MAP_SIZE;
    const vpTop = ((camera.screenToWorldY(0) - WORLD_MIN) / WORLD_H) * MAP_SIZE;
    const vpRight = ((camera.screenToWorldX(camera.screenWidth) - WORLD_MIN) / WORLD_W) * MAP_SIZE;
    const vpBottom = ((camera.screenToWorldY(camera.screenHeight) - WORLD_MIN) / WORLD_H) * MAP_SIZE;
    ctx.strokeStyle = "rgba(255, 255, 255, 0.4)";
    ctx.lineWidth = 1;
    ctx.strokeRect(vpLeft, vpTop, vpRight - vpLeft, vpBottom - vpTop);
  }

  show() {
    this.canvas.style.display = "block";
  }

  hide() {
    this.canvas.style.display = "none";
  }

  destroy() {
    this.canvas.remove();
  }
}
