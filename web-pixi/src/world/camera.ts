import { Container } from "pixi.js";
import { recomputeZoom, zoom } from "../view";

export class Camera {
  public x = 0;
  public y = 0;
  private screenW = 0;
  private screenH = 0;

  constructor(private worldContainer: Container) {
    // Scale is set by the first resize() call (driven by applyViewport()
    // in main.ts on startup). Constructor no longer touches scale —
    // viewport-anchored zoom is meaningless without a known canvas width.
  }

  /**
   * Push the current `zoom()` value into the world container scale.
   * Use after any operation that changes `currentZoom` without going
   * through resize() — e.g. scroll-wheel zoom.
   */
  applyZoom(): void {
    const z = zoom();
    this.worldContainer.scale.set(z, z);
  }

  /**
   * Update screen dimensions, recompute zoom against the new canvas
   * width, and apply the resulting scale. This is the canonical "canvas
   * size changed" entry point — both window resize and sidebar toggle
   * route through here via applyViewport().
   */
  resize(w: number, h: number): void {
    this.screenW = w;
    this.screenH = h;
    recomputeZoom(w);
    this.applyZoom();
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
    const z = zoom();
    return {
      x: (sx - this.screenW / 2) / z + this.x,
      y: (sy - this.screenH / 2) / z + this.y,
    };
  }

  /** Convert world coordinates to screen coordinates */
  worldToScreen(wx: number, wy: number): { x: number; y: number } {
    const z = zoom();
    return {
      x: (wx - this.x) * z + this.screenW / 2,
      y: (wy - this.y) * z + this.screenH / 2,
    };
  }
}
