import { Graphics, Container } from "pixi.js";
import type { Camera } from "./camera";
import type { FoodData, EatenFoodAnim } from "./state";
import type { InterpolatedSnake } from "./interpolation";

const FOOD_COLORS: number[] = [
  0xff0000, // red
  0xffff00, // yellow
  0x00ff00, // green
  0xff00ff, // magenta
  0xffffff, // white
  0x00ffff, // cyan
  0x7fff00, // chartreuse
  0xffcc00, // amber
];

// Duration of the eat animation in ms
const EAT_ANIM_MS = 250;

export class FoodRenderer {
  private graphics: Graphics;

  constructor(parentContainer: Container) {
    this.graphics = new Graphics();
    parentContainer.addChild(this.graphics);
  }

  render(
    foods: Map<number, FoodData>,
    camera: Camera,
    snakes: InterpolatedSnake[],
    eatenFood: EatenFoodAnim[]
  ) {
    this.graphics.clear();
    const g = this.graphics;
    const now = performance.now();

    // --- Draw living food ---
    for (const [, food] of foods) {
      if (!camera.isVisible(food.x, food.y, 80)) continue;

      const sx = camera.worldToScreenX(food.x);
      const sy = camera.worldToScreenY(food.y);
      const colorIdx = food.color % FOOD_COLORS.length;
      const color = FOOD_COLORS[colorIdx];

      const baseRadius = (2.5 + food.value * 1.8) * camera.zoom;
      const phase = now * 0.003 + food.x * 0.1 + food.y * 0.1;
      const pulse = 0.85 + 0.15 * Math.sin(phase);
      const radius = baseRadius * pulse;

      if (food.value > 2.0) {
        g.circle(sx, sy, radius * 1.8);
        g.fill({ color, alpha: 0.12 });
      }

      g.circle(sx, sy, radius);
      g.fill(color);
    }

    // --- Animate eaten food flying to nearest snake head ---
    for (const eaten of eatenFood) {
      const elapsed = now - eaten.startTime;
      if (elapsed >= EAT_ANIM_MS) continue;

      // Find nearest snake head
      let nearestDist = Infinity;
      let targetX = eaten.x;
      let targetY = eaten.y;
      for (const snake of snakes) {
        const dx = eaten.x - snake.headX;
        const dy = eaten.y - snake.headY;
        const d = dx * dx + dy * dy;
        if (d < nearestDist) {
          nearestDist = d;
          targetX = snake.headX;
          targetY = snake.headY;
        }
      }

      // Ease-in: accelerate toward the head
      const t = elapsed / EAT_ANIM_MS;
      const ease = t * t; // quadratic ease-in
      const drawX = eaten.x + (targetX - eaten.x) * ease;
      const drawY = eaten.y + (targetY - eaten.y) * ease;
      const alpha = 1 - t; // fade out

      if (!camera.isVisible(drawX, drawY, 80)) continue;

      const sx = camera.worldToScreenX(drawX);
      const sy = camera.worldToScreenY(drawY);
      const colorIdx = eaten.color % FOOD_COLORS.length;
      const color = FOOD_COLORS[colorIdx];
      const radius = (2.5 + eaten.value * 1.8) * camera.zoom * (1 - t * 0.5);

      g.circle(sx, sy, radius);
      g.fill({ color, alpha });
    }
  }

  destroy() {
    this.graphics.destroy();
  }
}
