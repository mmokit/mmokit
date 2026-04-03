import { Graphics, Container, Text } from "pixi.js";
import type { Camera } from "./camera";
import type { InterpolatedSnake } from "./interpolation";

// 8 vibrant skin palettes: each has 2 alternating body colors
const SKIN_COLORS: [number, number][] = [
  [0xff2020, 0xff6020], // red / orange-red
  [0x20ff40, 0x20c080], // green / teal-green
  [0x2080ff, 0x20d0ff], // blue / sky blue
  [0xffff20, 0xffcc00], // yellow / gold
  [0xcc20ff, 0x8040ff], // purple / indigo
  [0xff8000, 0xffbb00], // orange / amber
  [0x00ffcc, 0x00cc80], // teal / emerald
  [0xffffff, 0xccccff], // white / lavender
];

const BOOST_BRIGHTEN = 0x222222;

function brighten(color: number, amount: number = BOOST_BRIGHTEN): number {
  const r = Math.min(255, ((color >> 16) & 0xff) + ((amount >> 16) & 0xff));
  const g = Math.min(255, ((color >> 8) & 0xff) + ((amount >> 8) & 0xff));
  const b = Math.min(255, (color & 0xff) + (amount & 0xff));
  return (r << 16) | (g << 8) | b;
}

function darken(color: number, amount: number): number {
  const r = Math.max(0, ((color >> 16) & 0xff) - amount);
  const g = Math.max(0, ((color >> 8) & 0xff) - amount);
  const b = Math.max(0, (color & 0xff) - amount);
  return (r << 16) | (g << 8) | b;
}

/** Subdivide path for smooth curves between segment points. */
function subdividePath(
  points: { x: number; y: number }[]
): { x: number; y: number }[] {
  if (points.length < 2) return points;
  const result: { x: number; y: number }[] = [points[0]];
  for (let i = 1; i < points.length; i++) {
    const p0 = points[i - 1];
    const p1 = points[i];
    const dx = p1.x - p0.x;
    const dy = p1.y - p0.y;
    const dist = Math.sqrt(dx * dx + dy * dy);
    const steps = Math.max(1, Math.floor(dist / 6));
    for (let s = 1; s <= steps; s++) {
      const t = s / steps;
      result.push({ x: p0.x + dx * t, y: p0.y + dy * t });
    }
  }
  return result;
}

export class SnakeRenderer {
  private container: Container;
  private graphics: Graphics;
  private nameTexts: Map<number, Text> = new Map();

  constructor(parentContainer: Container) {
    this.container = new Container();
    parentContainer.addChild(this.container);
    this.graphics = new Graphics();
    this.container.addChild(this.graphics);
  }

  render(snakes: InterpolatedSnake[], camera: Camera, myEntityID: number) {
    this.graphics.clear();

    const usedIds = new Set<number>();

    // Draw non-player snakes first, then the player on top
    const sorted = [...snakes].sort((a, b) => {
      if (a.id === myEntityID) return 1;
      if (b.id === myEntityID) return -1;
      return 0;
    });

    for (const snake of sorted) {
      usedIds.add(snake.id);
      this.drawSnake(snake, camera, snake.id === myEntityID);
    }

    // Clean up unused name texts
    for (const [id, text] of this.nameTexts) {
      if (!usedIds.has(id)) {
        this.container.removeChild(text);
        text.destroy();
        this.nameTexts.delete(id);
      }
    }
  }

  private drawSnake(
    snake: InterpolatedSnake,
    camera: Camera,
    isMe: boolean
  ) {
    const g = this.graphics;
    const skinIdx = snake.skinID % SKIN_COLORS.length;
    let [color1, color2] = SKIN_COLORS[skinIdx];
    const outlineColor = darken(color1, 40);
    const color3 = darken(color2, 25);

    if (snake.boosting) {
      color1 = brighten(color1);
      color2 = brighten(color2);
    }

    // Base radius from mass
    const baseRadius = Math.max(10, Math.sqrt(snake.mass) * 2.0);

    // Build screen-space path: head + segments
    const rawPoints: { x: number; y: number }[] = [];
    const headSX = camera.worldToScreenX(snake.headX);
    const headSY = camera.worldToScreenY(snake.headY);
    rawPoints.push({ x: headSX, y: headSY });

    for (const seg of snake.segments) {
      rawPoints.push({
        x: camera.worldToScreenX(seg.x),
        y: camera.worldToScreenY(seg.y),
      });
    }

    // Subdivide for smooth connected body
    const points = subdividePath(rawPoints);
    const totalPoints = points.length;

    // --- Draw outline (slightly larger, color-tinted) ---
    for (let i = totalPoints - 1; i >= 0; i--) {
      const t = totalPoints > 1 ? i / (totalPoints - 1) : 0;
      const radius = baseRadius * (1.0 - t * 0.4) * camera.zoom + 1.5;
      g.circle(points[i].x, points[i].y, radius);
      g.fill({ color: outlineColor, alpha: 0.85 });
    }

    // --- Draw body circles from tail to head ---
    const colors3 = [color1, color2, color3];
    for (let i = totalPoints - 1; i >= 0; i--) {
      const t = totalPoints > 1 ? i / (totalPoints - 1) : 0;
      const radius = baseRadius * (1.0 - t * 0.4) * camera.zoom;
      const band = Math.floor(i / 3) % 3;
      const color = colors3[band];

      g.circle(points[i].x, points[i].y, radius);
      g.fill(color);
    }

    // --- Boost glow effect ---
    if (snake.boosting) {
      const glowRadius = baseRadius * camera.zoom * 1.5;
      g.circle(headSX, headSY, glowRadius);
      g.fill({ color: color1, alpha: 0.12 });
      if (totalPoints > 3) {
        const tailPt = points[totalPoints - 1];
        g.circle(tailPt.x, tailPt.y, glowRadius * 0.6);
        g.fill({ color: color1, alpha: 0.08 });
      }
    }

    // --- Head features (eyes) ---
    const headRadius = baseRadius * camera.zoom;
    const eyeOffset = headRadius * 0.35;
    const eyeRadius = headRadius * 0.38;
    const pupilRadius = eyeRadius * 0.55;

    const perpAngle = snake.angle + Math.PI / 2;
    const eyeFwdX = Math.cos(snake.angle) * eyeOffset * 0.7;
    const eyeFwdY = Math.sin(snake.angle) * eyeOffset * 0.7;
    const perpX = Math.cos(perpAngle);
    const perpY = Math.sin(perpAngle);

    const pupilOffX = Math.cos(snake.angle) * pupilRadius * 0.35;
    const pupilOffY = Math.sin(snake.angle) * pupilRadius * 0.35;

    // Left eye
    const lx = headSX + eyeFwdX + perpX * eyeOffset;
    const ly = headSY + eyeFwdY + perpY * eyeOffset;
    g.circle(lx, ly, eyeRadius);
    g.fill(0xffffff);
    g.circle(lx + pupilOffX, ly + pupilOffY, pupilRadius);
    g.fill(0x111111);
    g.circle(lx - eyeRadius * 0.15, ly - eyeRadius * 0.2, eyeRadius * 0.25);
    g.fill({ color: 0xffffff, alpha: 0.8 });

    // Right eye
    const rx = headSX + eyeFwdX - perpX * eyeOffset;
    const ry = headSY + eyeFwdY - perpY * eyeOffset;
    g.circle(rx, ry, eyeRadius);
    g.fill(0xffffff);
    g.circle(rx + pupilOffX, ry + pupilOffY, pupilRadius);
    g.fill(0x111111);
    g.circle(rx - eyeRadius * 0.15, ry - eyeRadius * 0.2, eyeRadius * 0.25);
    g.fill({ color: 0xffffff, alpha: 0.8 });

    // Name label
    this.updateNameText(snake, headSX, headSY - headRadius - 18);
  }

  private updateNameText(snake: InterpolatedSnake, x: number, y: number) {
    let text = this.nameTexts.get(snake.id);
    if (!text) {
      text = new Text({
        text: snake.name,
        style: {
          fontFamily: "Arial",
          fontSize: 14,
          fontWeight: "bold",
          fill: 0xffffff,
          stroke: { color: 0x000000, width: 3 },
          align: "center",
        },
      });
      text.anchor.set(0.5, 1);
      this.container.addChild(text);
      this.nameTexts.set(snake.id, text);
    }
    text.text = snake.name;
    text.x = x;
    text.y = y;
  }

  destroy() {
    for (const [, text] of this.nameTexts) {
      text.destroy();
    }
    this.nameTexts.clear();
    this.graphics.destroy();
    this.container.destroy();
  }
}
