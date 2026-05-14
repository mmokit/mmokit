import { Container, Graphics } from "pixi.js";
import { abilityParamsForSlot, TargetingMode } from "../ui/ability-bar";
import type { GameState } from "../state";
import { px } from "../view";

// Per-slot beam color for the aim preview. Mirrors the HTML ability-bar
// slot colors so the on-canvas indicator visually matches the key that
// the player is holding to aim.
const SLOT_COLORS: Record<number, number> = {
  0: 0x4488ff, 1: 0x4488ff, // Q/W weapon1
  2: 0xaa44ff, 3: 0xaa44ff, // E/R weapon2
  4: 0x44ff88,              // D shield
  5: 0xffff44,              // F thruster
};

// AimIndicator renders the active aim preview for whichever slot is
// being aimed. One Graphics, redrawn each frame from state.aimingSlot
// + state.cursorWorldX/Y. SkillshotLine/Channel draw a beam from the
// ship out to its range cap along the cursor vector; SkillshotGround
// draws a clamped damage circle at the cursor plus a range ring around
// the ship. Self / LockOn skip the preview — handleAbilityPress fires
// those immediately without an aim phase.
export class AimIndicator {
  private gfx: Graphics;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState): void {
    this.gfx.clear();
    if (state.aimingSlot === 0) return;
    const slot = state.aimingSlot - 1;
    const params = abilityParamsForSlot(state, slot);
    if (!params) return;

    const me = state.entities.get(state.myEntityId);
    if (!me) return;

    const sx = me.renderX;
    const sy = me.renderY;
    const cx = state.cursorWorldX;
    const cy = state.cursorWorldY;
    const dx = cx - sx;
    const dy = cy - sy;
    const dist = Math.sqrt(dx * dx + dy * dy);
    const range = params.range || 30;
    const color = SLOT_COLORS[slot] ?? 0xffffff;

    switch (params.mode) {
      case TargetingMode.SkillshotLine:
      case TargetingMode.SkillshotChannel: {
        if (dist < 1e-3) return;
        const nx = dx / dist;
        const ny = dy / dist;
        const ex = sx + nx * range;
        const ey = sy + ny * range;
        // Half-width in world units; small enough to read as a beam, not a slab.
        const beamHalfWidth = px(6);
        const pxX = -ny * beamHalfWidth;
        const pxY = nx * beamHalfWidth;
        this.gfx
          .poly([
            sx + pxX, sy + pxY,
            ex + pxX, ey + pxY,
            ex - pxX, ey - pxY,
            sx - pxX, sy - pxY,
          ])
          .fill({ color, alpha: 0.15 });
        this.gfx.moveTo(sx, sy).lineTo(ex, ey).stroke({ color, width: px(2), alpha: 0.85 });
        break;
      }
      case TargetingMode.SkillshotGround: {
        const clamped = dist > range && dist > 0 ? range / dist : 1;
        const tx = sx + dx * clamped;
        const ty = sy + dy * clamped;
        const splashR = params.splashRadius || 6;
        this.gfx.circle(tx, ty, splashR).fill({ color: 0xff4422, alpha: 0.25 });
        this.gfx.circle(tx, ty, splashR).stroke({ color: 0xff6644, width: px(2), alpha: 0.85 });
        this.gfx.circle(sx, sy, range).stroke({ color, width: px(1), alpha: 0.25 });
        break;
      }
      // Self / LockOn — no aim preview; handleAbilityPress fires those immediately.
    }
  }
}
