import { Container } from "pixi.js";

export class Camera {
  public x = 0;
  public y = 0;
  private screenW = 0;
  private screenH = 0;

  constructor(private worldContainer: Container) {}

  resize(w: number, h: number): void {
    this.screenW = w;
    this.screenH = h;
  }

  update(
    playerX: number,
    playerY: number,
    shake?: { intensity: number; startTime: number; duration: number } | null,
  ): void {
    this.x = playerX;
    this.y = playerY;
    this.worldContainer.pivot.set(playerX, playerY);

    let offsetX = 0;
    let offsetY = 0;
    if (shake) {
      const elapsed = performance.now() - shake.startTime;
      if (elapsed < shake.duration) {
        const decay = 1 - elapsed / shake.duration;
        offsetX = (Math.random() - 0.5) * shake.intensity * decay * 2;
        offsetY = (Math.random() - 0.5) * shake.intensity * decay * 2;
      }
    }

    this.worldContainer.position.set(
      this.screenW / 2 + offsetX,
      this.screenH / 2 + offsetY,
    );
  }

  /** Convert screen coordinates to world coordinates */
  screenToWorld(sx: number, sy: number): { x: number; y: number } {
    return {
      x: sx - this.screenW / 2 + this.x,
      y: sy - this.screenH / 2 + this.y,
    };
  }

  /** Convert world coordinates to screen coordinates */
  worldToScreen(wx: number, wy: number): { x: number; y: number } {
    return {
      x: wx - this.x + this.screenW / 2,
      y: wy - this.y + this.screenH / 2,
    };
  }
}
