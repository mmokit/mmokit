import { Container, Graphics, Text } from "pixi.js";
import type { GameState } from "../state";

export class LockOnRing {
  private container: Container;
  private ring: Graphics;
  private label: Text;

  constructor(parent: Container) {
    this.container = new Container();
    this.container.visible = false;
    parent.addChild(this.container);

    this.ring = new Graphics();
    this.container.addChild(this.ring);

    this.label = new Text({
      text: "LOCKING",
      style: { fontFamily: "monospace", fontSize: 11, fill: 0xff4444 },
    });
    this.label.anchor.set(0.5, 0);
    this.container.addChild(this.label);
  }

  update(state: GameState, _now: number): void {
    if (!state.lockTargetId || !state.entities.has(state.lockTargetId)) {
      this.container.visible = false;
      return;
    }

    this.container.visible = true;
    const tgt = state.entities.get(state.lockTargetId)!;
    const radius = Math.max(tgt.curr.radius || 20, 25) + 12;

    this.container.position.set(tgt.renderX, tgt.renderY);
    this.ring.clear();

    const progress = state.lockProgress;
    const locked = progress >= 1.0;

    if (locked) {
      // Full solid ring — green
      this.ring.circle(0, 0, radius).stroke({ color: 0x00ff00, width: 2, alpha: 0.9 });
      this.label.text = "LOCKED";
      this.label.style.fill = 0x00ff00;
    } else {
      // Partial arc — red/yellow
      const color = progress > 0.5 ? 0xffaa00 : 0xff4444;
      // Background ring (dim)
      this.ring.circle(0, 0, radius).stroke({ color: 0x333333, width: 2, alpha: 0.5 });
      // Progress arc
      const startAngle = -Math.PI / 2;
      const endAngle = startAngle + progress * Math.PI * 2;
      this.ring.arc(0, 0, radius, startAngle, endAngle).stroke({ color, width: 2, alpha: 0.9 });

      this.label.text = `LOCKING ${Math.floor(progress * 100)}%`;
      this.label.style.fill = color;
    }

    this.label.position.set(0, radius + 6);
  }
}
