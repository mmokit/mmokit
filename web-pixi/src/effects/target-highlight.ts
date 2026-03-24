import { Container, Graphics, Text } from "pixi.js";
import { EntityType } from "@gen/game_pb.js";
import { RESOURCE_COLORS_HEX, RESOURCE_NAMES } from "../constants";
import { px } from "../view";
import type { GameState } from "../state";
import { getAsteroid, getLootCrate } from "../entity-accessors";

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
    this.label.scale.set(px(1), px(1));
    this.container.addChild(this.label);

    this.sublabel = new Text({ text: "", style: { fontFamily: "monospace", fontSize: 12, fill: 0xcccccc } });
    this.sublabel.anchor.set(0.5, 1);
    this.sublabel.scale.set(px(1), px(1));
    this.container.addChild(this.sublabel);
  }

  update(state: GameState, _now: number): void {
    if (!state.targetId || !state.entities.has(state.targetId)) {
      this.container.visible = false;
      return;
    }

    this.container.visible = true;
    const tgt = state.entities.get(state.targetId)!;
    const tr = (tgt.curr.radius || 0.7) + px(8);

    this.container.position.set(tgt.renderX, tgt.renderY);

    this.ring.clear();

    if (tgt.curr.entityType === EntityType.SHIP || tgt.curr.entityType === EntityType.NPC) {
      // Combat target — tight ring around hull
      const color = tgt.curr.entityType === EntityType.NPC ? 0xff4444 : 0x44aaff;
      this.ring.circle(0, 0, tr).stroke({ color, width: px(2), alpha: 0.8 });

      this.label.visible = false;
      this.sublabel.visible = false;
    } else if (tgt.curr.entityType === EntityType.LOOT_CRATE) {
      // Yellow ring
      this.ring.circle(0, 0, tr).stroke({ color: 0xffdd00, width: px(2), alpha: 0.8 });
      this.label.visible = true;
      this.label.text = "LOOT CRATE";
      this.label.style.fill = 0xffdd00;
      this.label.position.set(0, -tr - px(26));

      const lootCrate = getLootCrate(tgt.curr);
      const cargoItems = lootCrate?.cargoItems;
      if (cargoItems && cargoItems.length > 0) {
        const parts: string[] = [];
        for (const item of cargoItems) {
          const def = state.itemDefs.get(item.itemId);
          const name = def ? def.name : `#${item.itemId}`;
          if (item.quantity > 0) parts.push(`${name}: ${Math.floor(item.quantity)}`);
        }
        this.sublabel.text = parts.join("  ");
        this.sublabel.visible = true;
      } else {
        this.sublabel.visible = false;
      }
      this.sublabel.position.set(0, -tr - px(14));
    } else {
      // Asteroid target
      const asteroid = getAsteroid(tgt.curr);
      const resType = asteroid?.itemId || 0;
      const resColor = RESOURCE_COLORS_HEX[resType] || 0xaa8866;

      this.ring.circle(0, 0, tr).stroke({ color: resColor, width: px(2), alpha: 0.8 });

      this.label.visible = true;
      this.label.text = RESOURCE_NAMES[resType] || "???";
      this.label.style.fill = resColor;
      this.label.position.set(0, -tr - px(26));

      const remaining = Math.floor(asteroid?.resourceRemaining || 0);
      this.sublabel.text = `${remaining} remaining`;
      this.sublabel.visible = true;
      this.sublabel.position.set(0, -tr - px(14));
    }
  }
}
