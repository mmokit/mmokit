import { Container, Graphics, RenderTexture, Sprite } from "pixi.js";
import type { Renderer } from "pixi.js";
import { EntityType } from "../sdk/index.js";
import type { GameState } from "./state";
import type { Camera } from "./world/camera";
import { zoom } from "./view";

/**
 * Atmospheric "inside the dungeon" visual: when the player ship's
 * distance to a dungeon's center is less than the dungeon's outer
 * radius, a full-screen darkening overlay is rendered everywhere
 * EXCEPT a soft circular spotlight that follows the dungeon's
 * silhouette.
 *
 * Implementation history:
 *   v1 — "erase" blend on Graphics inside a plain Container. BROKEN:
 *        Pixi v8 "erase" operates against the destination framebuffer,
 *        not the local container, so it darkened the scene below
 *        instead of cutting through the dark rect.
 *   v2 — Graphics.cut() with rect-minus-circle path. Works but runs
 *        CSG + tessellation on every render frame even though only
 *        position/scale change. Was the dominant cost in Pixi's
 *        _tick (60–70 ms long task identified via Chrome Performance
 *        + the dev-overlay `Pixi auto-render duration` measurement).
 *   v3 — isRenderGroup = true + erase blend. Failed because Pixi's
 *        render-group framebuffer initialization didn't give us the
 *        transparent backing we need; dark rect solidified, erase
 *        produced color=black/alpha=0 showing as solid black.
 *   v4 (current) — manual RenderTexture composition. We own a
 *        Sprite on the stage whose texture is a RenderTexture that
 *        we re-render each frame. The composition Container holds the
 *        dark rect + two erase circles, and we call
 *        renderer.render({target, clear:true}) which gives us
 *        guaranteed-transparent initial alpha. Erase blends then
 *        correctly subtract from the dark rect inside the texture.
 *        The dark rect rebuilds only on canvas resize; the circles
 *        are unit-circles (radius=256, scaled per frame). All
 *        per-frame work is GPU-side: clear + 3 simple primitive draws
 *        into the texture, then one textured-quad onto the screen.
 *
 * Two concentric erase passes give a soft rim falloff:
 *   - outer hole at α=0.5, scale → r * 1.05 → dims rim band by 50%
 *   - inner hole at α=1.0, scale → r * 0.94 → fully clears center
 *
 * Lives on app.stage above worldContainer but below the minimap so
 * the minimap stays at full brightness.
 */
export class DungeonOverlay {
  private renderer: Renderer;
  // Stage-attached sprite that displays the composed texture.
  private sprite: Sprite;
  private renderTexture: RenderTexture | null = null;
  // Composition container — NEVER added to the scene graph; rendered
  // into the RenderTexture each frame.
  private composeContainer: Container;
  private dark: Graphics;
  private outerHole: Graphics;
  private innerHole: Graphics;
  private active = false;
  private lastScreenW = 0;
  private lastScreenH = 0;

  constructor(stage: Container, renderer: Renderer) {
    this.renderer = renderer;

    // The visible artifact on the stage is a single Sprite. Its
    // texture is updated each frame from the offscreen composition.
    this.sprite = new Sprite();
    this.sprite.zIndex = 50; // above world (zIndex 0), below minimap (zIndex 100)
    this.sprite.visible = false;
    stage.addChild(this.sprite);

    this.composeContainer = new Container();

    // Dark fill — rebuilt only on canvas resize (rare).
    this.dark = new Graphics();
    this.composeContainer.addChild(this.dark);

    // Unit circles at origin, animated via position+scale only. The
    // 256 radius gives enough tessellation detail that scaling up to
    // typical screen-radius sizes (~800–1500 px) still looks round.
    // Pixi caches the tessellation; scaling does NOT re-tessellate.
    this.outerHole = new Graphics();
    this.outerHole.circle(0, 0, 256).fill({ color: 0xffffff, alpha: 0.5 });
    this.outerHole.blendMode = "erase";
    this.composeContainer.addChild(this.outerHole);

    this.innerHole = new Graphics();
    this.innerHole.circle(0, 0, 256).fill({ color: 0xffffff, alpha: 1.0 });
    this.innerHole.blendMode = "erase";
    this.composeContainer.addChild(this.innerHole);
  }

  update(state: GameState, camera: Camera, screenW: number, screenH: number): void {
    const me = state.entities.get(state.myEntityId);
    if (!me) {
      this.setActive(false);
      return;
    }

    // Find the nearest dungeon (in practice there's only one or two
    // visible) and check whether we're inside any of them.
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

    // Lazily create / resize the render texture on first activation
    // and on canvas resize. Rebuild the dark rect at the new size.
    if (this.renderTexture === null) {
      this.renderTexture = RenderTexture.create({ width: screenW, height: screenH });
      this.sprite.texture = this.renderTexture;
      this.lastScreenW = screenW;
      this.lastScreenH = screenH;
      this.dark.clear();
      this.dark.rect(0, 0, screenW, screenH).fill({ color: 0x000000, alpha: 0.55 });
    } else if (screenW !== this.lastScreenW || screenH !== this.lastScreenH) {
      this.lastScreenW = screenW;
      this.lastScreenH = screenH;
      this.renderTexture.resize(screenW, screenH);
      this.dark.clear();
      this.dark.rect(0, 0, screenW, screenH).fill({ color: 0x000000, alpha: 0.55 });
    }

    // Position + scale the holes — pure transform updates, no geometry
    // rebuild, no tessellation.
    const screenCenter = camera.worldToScreen(insideDungeon.x, insideDungeon.y);
    const screenRadius = insideDungeon.r * zoom();
    this.outerHole.position.set(screenCenter.x, screenCenter.y);
    this.outerHole.scale.set((screenRadius * 1.05) / 256);
    this.innerHole.position.set(screenCenter.x, screenCenter.y);
    this.innerHole.scale.set((screenRadius * 0.94) / 256);

    // Render the composition into the texture. clear:true wipes the
    // framebuffer to transparent first; erase blends inside this pass
    // then cleanly subtract from the dark rect's alpha. The composed
    // texture is what the stage sprite displays.
    this.renderer.render({
      container: this.composeContainer,
      target: this.renderTexture,
      clear: true,
    });
  }

  private setActive(on: boolean): void {
    if (this.active === on) return;
    this.active = on;
    this.sprite.visible = on;
  }
}
