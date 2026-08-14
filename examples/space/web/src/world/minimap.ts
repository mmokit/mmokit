import { Container, Graphics } from "pixi.js";
import { RESOURCE_COLORS_HEX } from "../constants";
import type { GameState } from "../state";
import { getAsteroid, getPoi } from "../entity-accessors";
import { zoom } from "../view";
import { EntityType } from "../../sdk/index.js";

// Fraction of the minimap occupied by the "what you can see on screen"
// rectangle at max fit (so the long axis of the viewport reaches this
// fraction of the minimap edge). Anything beyond is world the minimap
// shows for context.
const VIEWPORT_FIT = 0.7;
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
    // Position: bottom-right of the canvas. The canvas itself shrinks
    // to make room for the cargo/loadout sidebar (see main.applyViewport),
    // so the minimap just anchors to `screenW - SIZE - 10` without needing
    // its own sidebar offset.
    this.container.position.set(screenW - SIZE - 10, screenH - SIZE - 10);

    const s = SIZE;
    const half = s / 2;

    // Dynamic scale so the "what you can see" rectangle always reaches
    // VIEWPORT_FIT * half along its longer axis, regardless of the
    // current window dimensions or zoom level. Without this, the
    // minimap either shows a tiny rect lost in the middle (wide
    // monitors, new constant-scale zoom) or crops the rect off the
    // edges (narrow monitors / zoomed in).
    const z = zoom();
    const visibleW = screenW / z;
    const visibleH = screenH / z;
    const longestVisible = Math.max(visibleW, visibleH);
    const range = longestVisible / (2 * VIEWPORT_FIT);
    const scale = half / range;

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
        switch (ent.current.entityType) {
          case EntityType.Ship:
            this.gfx.rect(ex - 2, ey - 2, 4, 4).fill({ color: 0x4488ff });
            break;
          case EntityType.Asteroid: {
            const resColor = RESOURCE_COLORS_HEX[getAsteroid(ent)?.itemID ?? 0] ?? 0xaa8866;
            const dotSize = Math.max(2, Math.min((ent.current.radius || 0.7) * scale * 0.5, 4));
            this.gfx.circle(ex, ey, dotSize).fill({ color: resColor });
            break;
          }
          case EntityType.Station:
            this.gfx
              .circle(ex, ey, 5)
              .stroke({ color: 0x88ff88, width: 1 });
            this.gfx
              .circle(ex, ey, 2)
              .fill({ color: 0x88ff88 });
            break;
          case EntityType.LootCrate:
            this.gfx.rect(ex - 2, ey - 2, 4, 4).fill({ color: 0xffdd00 });
            break;
          case EntityType.NPC:
            this.gfx.rect(ex - 2, ey - 2, 4, 4).fill({ color: 0xff4444 });
            break;
          case EntityType.POI: {
            // POIs render as a small hollow diamond, greyed out when not Active (status != 0).
            const poi = getPoi(ent);
            const active = poi?.status === 0;
            const color = active ? 0xff3344 : 0x666666;
            const alpha = active ? 0.9 : 0.4;
            this.gfx
              .poly([ex, ey - 4, ex + 4, ey, ex, ey + 4, ex - 4, ey])
              .stroke({ color, width: 1.5, alpha });
            break;
          }
          case EntityType.Dungeon: {
            // Dungeons render as an orange asteroid-icon — a filled
            // circle with three radial notches mirroring the cave
            // entrances. Scale lightly with the dungeon's actual
            // outer radius so big dungeons are visually larger on the
            // map.
            const dotR = Math.max(
              4,
              Math.min((ent.current.radius || 1800) * scale * 0.5, 10),
            );
            this.gfx.circle(ex, ey, dotR).fill({ color: 0x3a2e22 });
            this.gfx
              .circle(ex, ey, dotR)
              .stroke({ color: 0xffaa55, width: 1.5, alpha: 0.9 });
            // Cave-mouth dots — south, NE, NW positions.
            for (const a of [Math.PI / 2, -Math.PI / 6, Math.PI + Math.PI / 6]) {
              const mx = ex + Math.cos(a) * dotR;
              const my = ey + Math.sin(a) * dotR;
              this.gfx.circle(mx, my, 1.5).fill({ color: 0xffeeaa, alpha: 1.0 });
            }
            break;
          }
          // DungeonWall entities aren't drawn on the minimap — they'd
          // be a wall of static dots that drowns out the dungeon icon.
          case EntityType.DungeonWall:
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
