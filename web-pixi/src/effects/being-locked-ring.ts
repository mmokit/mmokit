import { Container, Graphics, Text } from "pixi.js";
import { px } from "../view";
import type { GameState } from "../state";
import { getShip } from "../entity-accessors";

const COLOR_WARNING = 0xff4444;
const COLOR_LOCKING = 0xff6600;
const COLOR_LOCKED = 0xff0000;

export class BeingLockedRing {
  private container: Container;
  private ring: Graphics;
  private label: Text;
  private prevLockedById = 0;

  constructor(parent: Container) {
    this.container = new Container();
    this.container.visible = false;
    parent.addChild(this.container);

    this.ring = new Graphics();
    this.container.addChild(this.ring);

    this.label = new Text({
      text: "",
      style: { fontFamily: "monospace", fontSize: 11, fill: COLOR_WARNING },
    });
    this.label.anchor.set(0.5, 1);
    this.label.scale.set(px(1), px(1));
    this.container.addChild(this.label);
  }

  update(state: GameState, now: number): void {
    if (!state.beingLockedById || !state.myEntityId) {
      this.container.visible = false;
      this.prevLockedById = 0;
      return;
    }

    const me = state.entities.get(state.myEntityId);
    if (!me) {
      this.container.visible = false;
      return;
    }

    this.container.visible = true;
    this.container.position.set(me.renderX, me.renderY);

    const baseRadius = Math.max(me.curr.width, me.curr.height, 1) * 0.5 + px(18);
    const progress = state.beingLockedProgress;
    const locked = progress >= 1.0;

    // Resolve locker name
    const locker = state.entities.get(state.beingLockedById);
    const lockerName = (locker ? getShip(locker.curr)?.pilotName : undefined) || "???";

    this.ring.clear();

    if (locked) {
      // Solid pulsing red ring
      const pulse = 0.6 + 0.4 * Math.sin(now * 0.006);
      this.ring
        .circle(0, 0, baseRadius)
        .stroke({ color: COLOR_LOCKED, width: px(3), alpha: pulse });

      this.label.text = `LOCKED BY ${lockerName.toUpperCase()}`;
      this.label.style.fill = COLOR_LOCKED;
    } else {
      // Dashed background ring (dim)
      this.drawDashedCircle(baseRadius, 0x333333, px(1.5), 0.4, now);

      // Progress arc — moveTo arc start to avoid flat line from origin
      const color = progress > 0.5 ? COLOR_WARNING : COLOR_LOCKING;
      const startAngle = -Math.PI / 2;
      const endAngle = startAngle + progress * Math.PI * 2;
      const pulse = 0.6 + 0.4 * Math.sin(now * 0.008);

      const sx = Math.cos(startAngle) * baseRadius;
      const sy = Math.sin(startAngle) * baseRadius;
      this.ring
        .moveTo(sx, sy)
        .arc(0, 0, baseRadius, startAngle, endAngle)
        .stroke({ color, width: px(3), alpha: pulse });

      this.label.text = `LOCKING: ${lockerName.toUpperCase()} ${Math.floor(progress * 100)}%`;
      this.label.style.fill = color;
    }

    // Position label above the ship name/bars.
    // Ship bars sit at ~(-halfDiag - 24) and name at ~(-halfDiag - 36) with ~11px font.
    // So top of name text is around (-halfDiag - 47). We need to clear that.
    const hw = (me.curr.width || 1) * 0.5;
    const hh = (me.curr.height || 1) * 0.5;
    const halfDiag = Math.sqrt(hw * hw + hh * hh);
    this.label.position.set(0, -(halfDiag + px(52)));
    this.prevLockedById = state.beingLockedById;
  }

  private drawDashedCircle(
    radius: number,
    color: number,
    width: number,
    alpha: number,
    now: number,
  ): void {
    const dashCount = 24;
    const gapFraction = 0.4;
    const segmentAngle = (Math.PI * 2) / dashCount;
    const dashAngle = segmentAngle * (1 - gapFraction);
    const rotation = now * 0.001;

    for (let i = 0; i < dashCount; i++) {
      const startAngle = rotation + i * segmentAngle;
      const endAngle = startAngle + dashAngle;

      const x0 = Math.cos(startAngle) * radius;
      const y0 = Math.sin(startAngle) * radius;
      this.ring.moveTo(x0, y0);

      const steps = 3;
      for (let s = 1; s <= steps; s++) {
        const a = startAngle + (endAngle - startAngle) * (s / steps);
        this.ring.lineTo(Math.cos(a) * radius, Math.sin(a) * radius);
      }

      this.ring.stroke({ color, width, alpha });
    }
  }
}
