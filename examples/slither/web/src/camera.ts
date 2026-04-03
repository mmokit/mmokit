export class Camera {
  x: number = 0;
  y: number = 0;
  zoom: number = 1;
  screenWidth: number = 0;
  screenHeight: number = 0;

  private targetZoom: number = 1;

  snapTo(x: number, y: number) {
    this.x = x;
    this.y = y;
  }

  update(targetX: number, targetY: number, mass: number, dt: number) {
    // Frame-rate independent exponential smoothing
    // ~8 means "reach 99% of target in ~0.5s"
    const posFactor = 1 - Math.exp(-8 * dt);
    this.x += (targetX - this.x) * posFactor;
    this.y += (targetY - this.y) * posFactor;

    // Zoom out as snake grows
    this.targetZoom = Math.max(0.5, 1.2 - Math.log10(Math.max(mass, 10)) * 0.2);
    const zoomFactor = 1 - Math.exp(-3 * dt);
    this.zoom += (this.targetZoom - this.zoom) * zoomFactor;
  }

  setScreenSize(width: number, height: number) {
    this.screenWidth = width;
    this.screenHeight = height;
  }

  worldToScreenX(wx: number): number {
    return (wx - this.x) * this.zoom + this.screenWidth / 2;
  }

  worldToScreenY(wy: number): number {
    return (wy - this.y) * this.zoom + this.screenHeight / 2;
  }

  screenToWorldX(sx: number): number {
    return (sx - this.screenWidth / 2) / this.zoom + this.x;
  }

  screenToWorldY(sy: number): number {
    return (sy - this.screenHeight / 2) / this.zoom + this.y;
  }

  isVisible(wx: number, wy: number, margin: number = 100): boolean {
    const sx = this.worldToScreenX(wx);
    const sy = this.worldToScreenY(wy);
    return sx >= -margin && sx <= this.screenWidth + margin && sy >= -margin && sy <= this.screenHeight + margin;
  }
}
