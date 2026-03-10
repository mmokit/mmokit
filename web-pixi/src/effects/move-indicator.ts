import { Container, Graphics } from "pixi.js";
import type { GameState } from "../state";

export class MoveIndicator {
  private container: Container;
  private marker: Graphics;
  private showTime: number = 0;

  constructor(parent: Container) {
    this.container = new Container();
    this.container.visible = false;
    parent.addChild(this.container);

    this.marker = new Graphics();
    this.container.addChild(this.marker);
  }

  show(x: number, y: number): void {
    this.container.visible = true;
    this.container.position.set(x, y);
    this.showTime = performance.now();

    this.marker.clear();
    // Small green diamond
    this.marker
      .moveTo(0, -8)
      .lineTo(8, 0)
      .lineTo(0, 8)
      .lineTo(-8, 0)
      .closePath()
      .stroke({ color: 0x00ff88, width: 1.5, alpha: 0.8 });
  }

  update(state: GameState, now: number): void {
    if (!this.container.visible) return;

    const elapsed = now - this.showTime;
    if (elapsed > 1000) {
      this.container.visible = false;
      return;
    }

    // Fade out
    this.container.alpha = 1 - elapsed / 1000;
  }
}
