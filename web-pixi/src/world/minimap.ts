import { Container, Graphics } from "pixi.js";
import { EntityType } from "@gen/game_pb.js";
import { RESOURCE_COLORS_HEX } from "../constants";
import type { GameState } from "../state";
import { getAsteroid } from "../entity-accessors";
import { zoom } from "../view";

// How many world units the minimap shows in each direction from the player
const VIEW_RANGE = 100;
const SIZE = 300;

export class Minimap {
  private container: Container;
  private bg: Graphics;
  private gfx: Graphics;

  constructor(stage: Container) {
    this.container = new Container();
    this.container.zIndex = 100;
    stage.addChild(this.container);

    this.bg = new Graphics();
    this.container.addChild(this.bg);

    this.gfx = new Graphics();
    this.container.addChild(this.gfx);
  }

  update(state: GameState, screenW: number, screenH: number): void {
    // Position: bottom-right, shifted right when cargo panel is open
    const rightOffset = state.cargoPanelOpen ? 320 : 10;
    this.container.position.set(screenW - SIZE - rightOffset, screenH - SIZE - 10);

    const s = SIZE;
    const half = s / 2;
    const scale = half / VIEW_RANGE;

    const myEntity = state.entities.get(state.myEntityId);
    const cx = myEntity ? myEntity.renderX : 0;
    const cy = myEntity ? myEntity.renderY : 0;

    // Background
    this.bg.clear();
    this.bg.rect(0, 0, s, s).fill({ color: 0x000000, alpha: 0.85 });

    // Crosshair
    this.bg
      .moveTo(half, 0).lineTo(half, s)
      .stroke({ color: 0xffffff, width: 1, alpha: 0.1 });
    this.bg
      .moveTo(0, half).lineTo(s, half)
      .stroke({ color: 0xffffff, width: 1, alpha: 0.1 });

    // Entities
    this.gfx.clear();

    for (const [id, ent] of state.entities) {
      const ex = (ent.renderX - cx) * scale + half;
      const ey = (ent.renderY - cy) * scale + half;

      if (ex < -4 || ex > s + 4 || ey < -4 || ey > s + 4) continue;

      const isMe = id === state.myEntityId;

      if (isMe) {
        // Player — bright green diamond with glow
        this.gfx
          .poly([ex, ey - 5, ex + 3.5, ey, ex, ey + 5, ex - 3.5, ey])
          .fill({ color: 0x00ff00, alpha: 0.3 });
        this.gfx
          .poly([ex, ey - 4, ex + 3, ey, ex, ey + 4, ex - 3, ey])
          .fill({ color: 0x00ff00, alpha: 1.0 });
      } else {
        switch (ent.curr.entityType) {
          case EntityType.SHIP:
            this.gfx.rect(ex - 2, ey - 2, 4, 4).fill({ color: 0x4488ff });
            break;
          case EntityType.ASTEROID: {
            const resColor = RESOURCE_COLORS_HEX[getAsteroid(ent.curr)?.itemId ?? 0] ?? 0xaa8866;
            const dotSize = Math.max(2, Math.min((ent.curr.radius || 0.7) * scale * 0.5, 4));
            this.gfx.circle(ex, ey, dotSize).fill({ color: resColor });
            break;
          }
          case EntityType.STATION:
            this.gfx
              .circle(ex, ey, 5)
              .stroke({ color: 0x88ff88, width: 1 });
            this.gfx
              .circle(ex, ey, 2)
              .fill({ color: 0x88ff88 });
            break;
          case EntityType.LOOT_CRATE:
            this.gfx.rect(ex - 2, ey - 2, 4, 4).fill({ color: 0xffdd00 });
            break;
          case EntityType.NPC:
            this.gfx.rect(ex - 2, ey - 2, 4, 4).fill({ color: 0xff4444 });
            break;
        }
      }
    }

    // Camera viewport rectangle
    if (myEntity) {
      const vw = (screenW / zoom()) * scale;
      const vh = (screenH / zoom()) * scale;
      this.gfx
        .rect(half - vw / 2, half - vh / 2, vw, vh)
        .stroke({ color: 0xffffff, width: 1, alpha: 0.25 });
    }

    // Border
    this.gfx
      .rect(0, 0, s, s)
      .stroke({ color: 0xffffff, width: 1, alpha: 0.2 });
  }
}
