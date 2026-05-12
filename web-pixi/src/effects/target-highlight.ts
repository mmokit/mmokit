import { Container, Graphics, Text } from "pixi.js";
import { RESOURCE_COLORS_HEX, RESOURCE_NAMES } from "../constants";
import { px } from "../view";
import type { GameState } from "../state";
import { getAsteroid } from "../entity-accessors";
import { EntityType } from "../../sdk/index.js";

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
    const kind = tgt.current.entityType;
    const tr = (tgt.current.radius || 0.7) + px(8);

    this.container.position.set(tgt.renderX, tgt.renderY);

    this.ring.clear();

    if (kind === EntityType.Ship || kind === EntityType.NPC) {
      // Combat target — tight ring around hull
      const color = kind === EntityType.NPC ? 0xff4444 : 0x44aaff;
      this.ring.circle(0, 0, tr).stroke({ color, width: px(2), alpha: 0.8 });

      this.label.visible = false;
      this.sublabel.visible = false;
    } else if (kind === EntityType.LootCrate) {
      // Yellow ring + inline item preview from the replicated inventory var-tail.
      this.ring.circle(0, 0, tr).stroke({ color: 0xffdd00, width: px(2), alpha: 0.8 });
      this.label.visible = true;
      this.label.text = "LOOT CRATE";
      this.label.style.fill = 0xffdd00;
      this.label.position.set(0, -tr - px(26));

      const crateItems = (tgt.current as { items?: Array<{ itemId: number; quantity: number }> }).items ?? [];
      if (crateItems.length > 0) {
        const lines = crateItems.slice(0, 5).map((it) => {
          const name =
            state.itemDefs.get(it.itemId)?.name ||
            RESOURCE_NAMES[it.itemId] ||
            `item${it.itemId}`;
          return `${name} x${it.quantity}`;
        });
        if (crateItems.length > 5) lines.push(`+${crateItems.length - 5} more`);
        this.sublabel.text = lines.join("\n");
        this.sublabel.style.fill = 0xffdd00;
        this.sublabel.visible = true;
        this.sublabel.position.set(0, -tr - px(14));
      } else {
        this.sublabel.visible = false;
      }
    } else if (kind === EntityType.Asteroid) {
      const asteroid = getAsteroid(tgt);
      const resType = asteroid?.itemID || 0;
      const resColor = RESOURCE_COLORS_HEX[resType] || 0xaa8866;

      this.ring.circle(0, 0, tr).stroke({ color: resColor, width: px(2), alpha: 0.8 });

      this.label.visible = true;
      this.label.text = RESOURCE_NAMES[resType] || "???";
      this.label.style.fill = resColor;
      this.label.position.set(0, -tr - px(26));

      const remaining = Math.floor(asteroid?.remaining || 0);
      this.sublabel.text = `${remaining} remaining`;
      this.sublabel.visible = true;
      this.sublabel.position.set(0, -tr - px(14));
    } else {
      this.label.visible = false;
      this.sublabel.visible = false;
    }
  }
}
