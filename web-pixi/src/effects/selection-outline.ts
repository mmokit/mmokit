import { Container, Graphics } from "pixi.js";
import type { GameState } from "../state";
import { EntityType } from "../../sdk";
import { px } from "../view";

// SelectionOutline draws a 2px stroke + faint halo on the currently
// selected entity. Color is type-derived so the player can tell at a
// glance what kind of thing they've selected:
//   asteroid  → green (mineable)
//   NPC       → red (hostile)
//   station   → cyan (neutral / dockable)
//   anything else → white (loot crate, POI, etc.)
//
// Redrawn each frame from state.selectedNetID + state.entities. Empty
// when nothing is selected.
const COLOR_ASTEROID = 0x44ff88;
const COLOR_NPC      = 0xff5544;
const COLOR_STATION  = 0x55ccff;
const COLOR_DEFAULT  = 0xffffff;

function colorFor(entityType: number): number {
  switch (entityType) {
    case EntityType.Asteroid: return COLOR_ASTEROID;
    case EntityType.NPC:      return COLOR_NPC;
    case EntityType.Station:  return COLOR_STATION;
    default:                  return COLOR_DEFAULT;
  }
}

export class SelectionOutline {
  private gfx: Graphics;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState): void {
    this.gfx.clear();
    if (state.selectedNetID === 0) return;
    const ent = state.entities.get(state.selectedNetID);
    if (!ent || !ent.current) return;

    const x = ent.renderX;
    const y = ent.renderY;
    const radius = (ent.current.radius ?? 8) + px(4);
    const color = colorFor(ent.current.entityType);

    this.gfx.circle(x, y, radius).stroke({ color, width: px(2), alpha: 0.9 });
    this.gfx.circle(x, y, radius + px(3)).stroke({ color, width: px(1), alpha: 0.3 });
  }
}
