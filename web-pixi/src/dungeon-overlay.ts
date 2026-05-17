import { Container, Graphics } from "pixi.js";
import { EntityType } from "../sdk/index.js";
import type { GameState } from "./state";
import type { Camera } from "./world/camera";
import { zoom } from "./view";

/**
 * Atmospheric "inside the dungeon" visual: when the player ship's
 * distance to a dungeon's center is less than the dungeon's outer
 * radius, a full-screen darkening overlay is rendered everywhere
 * EXCEPT a soft circular spotlight that follows the dungeon's
 * silhouette. The world inside the asteroid stays normal-lit; the
 * world outside dims to ~60% brightness, suggesting you've ducked
 * into a cave system.
 *
 * Implementation: one PIXI.Graphics holds a screen-space rectangle
 * at MULTIPLY blend that darkens the canvas. The "hole" is achieved
 * by drawing a circular ERASE on top, which subtracts alpha from the
 * dark rect inside the spotlight radius. We render two concentric
 * erases (a hard inner edge + a softer outer edge) so the transition
 * feels rocky rather than vignette-sharp.
 *
 * Lives on app.stage above worldContainer but below the minimap so
 * the minimap stays at full brightness.
 */
export class DungeonOverlay {
  private container: Container;
  private dark: Graphics;
  private hole: Graphics;
  private softHole: Graphics;
  private active = false;

  constructor(stage: Container) {
    this.container = new Container();
    // zIndex below minimap (100) but above the world (default 0).
    this.container.zIndex = 50;
    this.container.visible = false;
    stage.addChild(this.container);

    this.dark = new Graphics();
    this.container.addChild(this.dark);

    this.softHole = new Graphics();
    this.softHole.blendMode = "erase";
    this.container.addChild(this.softHole);

    this.hole = new Graphics();
    this.hole.blendMode = "erase";
    this.container.addChild(this.hole);
  }

  update(state: GameState, camera: Camera, screenW: number, screenH: number): void {
    const me = state.entities.get(state.myEntityId);
    if (!me) {
      this.setActive(false);
      return;
    }

    // Find the nearest dungeon (in practice there's only one or two visible)
    // and check whether we're inside any of them.
    let insideDungeon: { x: number; y: number; r: number } | null = null;
    let bestD2 = Infinity;
    for (const ent of state.entities.values()) {
      if (ent.current.entityType !== EntityType.Dungeon) continue;
      const dx = me.renderX - ent.renderX;
      const dy = me.renderY - ent.renderY;
      const d2 = dx * dx + dy * dy;
      const r = ent.current.radius || 1800;
      if (d2 < r * r && d2 < bestD2) {
        bestD2 = d2;
        insideDungeon = { x: ent.renderX, y: ent.renderY, r };
      }
    }

    if (!insideDungeon) {
      this.setActive(false);
      return;
    }

    this.setActive(true);

    // Convert dungeon center to screen coords; the spotlight follows the
    // asteroid's silhouette so a player roaming the dungeon sees the
    // "lit" region track with the cave system.
    const screenCenter = camera.worldToScreen(insideDungeon.x, insideDungeon.y);
    const screenRadius = insideDungeon.r * zoom();

    // Full-screen darkening — multiply blend would tint everything, but a
    // normal-blend semi-transparent black gives a more controllable result
    // and pairs cleanly with ERASE-mode holes.
    this.dark.clear();
    this.dark
      .rect(0, 0, screenW, screenH)
      .fill({ color: 0x000000, alpha: 0.55 });

    // Inner hard erase — restores 100% of the alpha inside the spotlight.
    this.hole.clear();
    this.hole
      .circle(screenCenter.x, screenCenter.y, screenRadius * 0.94)
      .fill({ color: 0xffffff, alpha: 1.0 });

    // Soft outer erase — partial cut between rInner and rOuter so the
    // darkening fades into the rock edge rather than appearing as a
    // hard ring.
    this.softHole.clear();
    this.softHole
      .circle(screenCenter.x, screenCenter.y, screenRadius * 1.05)
      .fill({ color: 0xffffff, alpha: 0.5 });
  }

  private setActive(on: boolean): void {
    if (this.active === on) return;
    this.active = on;
    this.container.visible = on;
  }
}
