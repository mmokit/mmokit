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
 * world outside dims, suggesting you've ducked into a cave system.
 *
 * Implementation: each Graphics draws a screen rect filled at the
 * target alpha, then uses Graphics.cut() to subtract a circle out
 * of that fill — producing real geometry with no blend-mode trick.
 * The previous version used the "erase" blend mode against a dark
 * rect inside the same Container, but in Pixi v8 erase operates
 * against the framebuffer (not the local container), so the "hole"
 * subtracted from the scene underneath — darkening the spotlight
 * area instead of brightening it. cut() avoids that entirely.
 *
 * Two concentric bands give a soft rim falloff:
 *   - outer band: rect ∖ circle(r * 1.05) at α≈0.30
 *   - inner band: rect ∖ circle(r * 0.94) at α≈0.25
 * Areas overlap additively (both Graphics rendered with normal blend),
 * giving:
 *   inside r*0.94          → 0.00 dark (clear)
 *   r*0.94 .. r*1.05 rim   → 0.30 dark (soft transition)
 *   outside r*1.05         → 0.55 dark (full)
 *
 * Lives on app.stage above worldContainer but below the minimap so
 * the minimap stays at full brightness.
 */
export class DungeonOverlay {
  private container: Container;
  private outerDark: Graphics;
  private innerDark: Graphics;
  private active = false;

  constructor(stage: Container) {
    this.container = new Container();
    // zIndex below minimap (100) but above the world (default 0).
    this.container.zIndex = 50;
    this.container.visible = false;
    stage.addChild(this.container);

    this.outerDark = new Graphics();
    this.innerDark = new Graphics();
    this.container.addChild(this.outerDark, this.innerDark);
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

    // Outer dark band — rect with circle(r * 1.05) cut out. The
    // sequence is: draw + fill rect, then draw circle + cut() — the
    // circle's geometry is subtracted from the previously filled rect.
    // Result is a single shape that paints the rect alpha everywhere
    // EXCEPT inside the circle. No blend modes, no framebuffer coupling.
    this.outerDark.clear();
    this.outerDark
      .rect(0, 0, screenW, screenH)
      .fill({ color: 0x000000, alpha: 0.30 })
      .circle(screenCenter.x, screenCenter.y, screenRadius * 1.05)
      .cut();

    // Inner dark band — same trick with a tighter hole. Adds extra
    // darkening to the area outside r*0.94 so the rim has a two-band
    // falloff: rim (0.30 dark) → outside (0.30 + 0.25 = ~0.55 dark).
    this.innerDark.clear();
    this.innerDark
      .rect(0, 0, screenW, screenH)
      .fill({ color: 0x000000, alpha: 0.25 })
      .circle(screenCenter.x, screenCenter.y, screenRadius * 0.94)
      .cut();
  }

  private setActive(on: boolean): void {
    if (this.active === on) return;
    this.active = on;
    this.container.visible = on;
  }
}
