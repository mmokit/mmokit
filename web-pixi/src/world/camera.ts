import { Container } from "pixi.js";
import { zoom } from "../view";

export class Camera {
  public x = 0;
  public y = 0;
  private screenW = 0;
  private screenH = 0;

  constructor(private worldContainer: Container) {
    // Apply the one-shot baseline zoom established by initZoom().
    // After this, worldContainer.scale is only touched by scroll-zoom,
    // never by window resize — entities keep their apparent size when
    // the window is resized, the visible world area just changes.
    const z = zoom();
    this.worldContainer.scale.set(z, z);
  }

  resize(w: number, h: number): void {
    // Intentionally does NOT touch worldContainer.scale. The camera's
    // pixel-per-unit zoom is a user-controlled property, independent
    // of window size. Resizing only changes how much world is visible,
    // and we update screenW/screenH so update() can re-center the
    // pivot and screenToWorld/worldToScreen reproject correctly.
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
