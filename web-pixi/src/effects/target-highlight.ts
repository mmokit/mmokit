import { Container, Graphics, Text } from "pixi.js";
import { EntityType } from "@gen/game_pb.js";
import { RESOURCE_COLORS_HEX, RESOURCE_NAMES } from "../constants";
import type { GameState } from "../state";

export class TargetHighlight {
  private container: Container;
  private ring: Graphics;
  private label: Text;
  private sublabel: Text;

  constructor(parent: Container) {
    this.container = new Container();
    this.container.visible = false;
    parent.addChild(this.container);

    this.ring = new Graphics();
    this.container.addChild(this.ring);

    this.label = new Text({ text: "", style: { fontFamily: "monospace", fontSize: 12, fill: 0xffffff } });
    this.label.anchor.set(0.5, 1);
    this.container.addChild(this.label);

    this.sublabel = new Text({ text: "", style: { fontFamily: "monospace", fontSize: 12, fill: 0xcccccc } });
    this.sublabel.anchor.set(0.5, 1);
    this.container.addChild(this.sublabel);
  }

  update(state: GameState, _now: number): void {
    if (!state.targetId || !state.entities.has(state.targetId)) {
      this.container.visible = false;
      return;
    }

    this.container.visible = true;
    const tgt = state.entities.get(state.targetId)!;
    const tr = (tgt.curr.radius || 20) + 8;

    this.container.position.set(tgt.renderX, tgt.renderY);

    this.ring.clear();

    if (tgt.curr.entityType === EntityType.LOOT_CRATE) {
      // Yellow dashed ring
      this.ring.circle(0, 0, tr).stroke({ color: 0xffdd00, width: 2, alpha: 0.8 });
      this.label.text = "LOOT CRATE";
      this.label.style.fill = 0xffdd00;
      this.label.position.set(0, -tr - 14);

      const res = tgt.curr.resources;
      if (res && res.length >= 4) {
        const parts: string[] = [];
        for (let i = 0; i < 4; i++) {
          if (res[i] > 0) parts.push(`${RESOURCE_NAMES[i]}: ${Math.floor(res[i])}`);
        }
        this.sublabel.text = parts.join("  ");
        this.sublabel.visible = true;
      } else {
        this.sublabel.visible = false;
      }
      this.sublabel.position.set(0, -tr - 2);
    } else {
      // Asteroid target
      const resType = tgt.curr.resourceType || 0;
      const resColor = RESOURCE_COLORS_HEX[resType] || 0xaa8866;

      this.ring.circle(0, 0, tr).stroke({ color: resColor, width: 2, alpha: 0.8 });

      this.label.text = RESOURCE_NAMES[resType] || "???";
      this.label.style.fill = resColor;
      this.label.position.set(0, -tr - 14);

      const remaining = Math.floor(tgt.curr.resourceRemaining || 0);
      this.sublabel.text = `${remaining} remaining`;
      this.sublabel.visible = true;
      this.sublabel.position.set(0, -tr - 2);
    }
  }
}
