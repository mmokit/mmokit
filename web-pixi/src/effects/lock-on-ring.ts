import { Container, Graphics, Text } from "pixi.js";
import { px } from "../view";
import type { GameState } from "../state";

// One ring + label per lock slot. Drawn AROUND the target entity in
// world space; shows progress while locking (red → orange arc) and
// a solid green circle once locked. The active slot (state.lockTargetId)
// gets a slightly brighter ring so the firing target is distinguishable
// when multi-locking.
interface SlotVisual {
  container: Container;
  ring: Graphics;
  label: Text;
}

export class LockOnRing {
  private parent: Container;
  private visuals: SlotVisual[] = [];

  constructor(parent: Container) {
    this.parent = parent;
  }

  private ensureVisual(i: number): SlotVisual {
    if (this.visuals[i]) return this.visuals[i];
    const container = new Container();
    container.visible = false;
    this.parent.addChild(container);
    const ring = new Graphics();
    container.addChild(ring);
    const label = new Text({
      text: "",
      style: { fontFamily: "monospace", fontSize: 11, fill: 0xff4444 },
    });
    label.anchor.set(0.5, 0);
    label.scale.set(px(1), px(1));
    container.addChild(label);
    const v: SlotVisual = { container, ring, label };
    this.visuals[i] = v;
    return v;
  }

  update(state: GameState, _now: number): void {
    const slots = state.lockSlots;
    const activeID = state.lockTargetId;
    let drawn = 0;

    for (const slot of slots) {
      const tgt = state.entities.get(slot.targetNetID);
      if (!tgt) continue;

      const v = this.ensureVisual(drawn);
      v.container.visible = true;
      const radius = Math.max(tgt.current.radius || 0.7, px(25)) + px(12);
      v.container.position.set(tgt.renderX, tgt.renderY);
      v.ring.clear();

      const isActive = slot.targetNetID === activeID && activeID !== 0;
      const baseAlpha = isActive ? 1.0 : 0.75;

      if (slot.locked) {
        v.ring.circle(0, 0, radius).stroke({ color: 0x00ff00, width: px(2), alpha: baseAlpha });
        v.label.text = isActive ? "LOCKED" : "locked";
        v.label.style.fill = 0x00ff00;
      } else {
        const progress = Math.max(0, Math.min(1, slot.progress));
        const color = progress > 0.5 ? 0xffaa00 : 0xff4444;
        v.ring.circle(0, 0, radius).stroke({ color: 0x333333, width: px(2), alpha: 0.5 });
        const startAngle = -Math.PI / 2;
        const endAngle = startAngle + progress * Math.PI * 2;
        v.ring.arc(0, 0, radius, startAngle, endAngle).stroke({ color, width: px(2), alpha: baseAlpha });
        v.label.text = `LOCKING ${Math.floor(progress * 100)}%`;
        v.label.style.fill = color;
      }

      v.label.position.set(0, radius + px(6));
      drawn++;
    }

    for (let i = drawn; i < this.visuals.length; i++) {
      this.visuals[i].container.visible = false;
    }
  }
}
