import { Container, Graphics } from "pixi.js";
import { abilityParamsForSlot, TargetingMode } from "../ui/ability-bar";
import type { GameState } from "../state";
import { px } from "../view";
import { EntityType } from "../../sdk";

// Per-slot beam color for the aim preview. Mirrors the HTML ability-bar
// slot colors so the on-canvas indicator visually matches the key that
// the player is holding to aim.
const SLOT_COLORS: Record<number, number> = {
  0: 0x4488ff, 1: 0x4488ff, // Q/W weapon1
  2: 0xaa44ff, 3: 0xaa44ff, // E/R weapon2
  4: 0x44ff88,              // D shield
  5: 0xffff44,              // F thruster
};

// AimIndicator renders two related previews from one Graphics, redrawn
// each frame:
//   1. Aim preview while state.aimingSlot != 0 (before fire) — beam for
//      SkillshotLine/Channel, clamped damage circle + range ring for
//      SkillshotGround. Dim alpha; will be replaced by either a fire or
//      a cancel.
//   2. Live channel beam while state.channelingSlot != 0 — solid bright
//      beam tracking the cursor while a SkillshotChannel (SustainedBeam)
//      is held; matches the server's tickChannels hitscan direction.
// Self / LockOn skip the preview entirely — handleAbilityPress fires
// those immediately without an aim phase.
export class AimIndicator {
  private gfx: Graphics;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState): void {
    this.gfx.clear();

    // Channeling takes precedence — once a channel is in flight, aimingSlot
    // has already been cleared by fireSkillshot, but we still want a live
    // beam visual until the channel ends.
    if (state.channelingSlot !== 0) {
      this.drawChannelBeam(state, state.channelingSlot - 1);
      return;
    }
    if (state.aimingSlot !== 0) {
      this.drawAimPreview(state, state.aimingSlot - 1);
    }
  }

  private drawChannelBeam(state: GameState, slot: number): void {
    const params = abilityParamsForSlot(state, slot);
    if (!params) return;
    const me = state.entities.get(state.myEntityId);
    if (!me) return;

    const sx = me.renderX;
    const sy = me.renderY;
    const dx = state.cursorWorldX - sx;
    const dy = state.cursorWorldY - sy;
    const dist = Math.sqrt(dx * dx + dy * dy);
    if (dist < 1e-3) return;
    const range = params.range || 30;
    const color = SLOT_COLORS[slot] ?? 0xffffff;

    const nx = dx / dist;
    const ny = dy / dist;
    const ex = sx + nx * range;
    const ey = sy + ny * range;
    // Channel beam: brighter + thicker than aim preview, with a slow pulse.
    const halfW = px(8);
    const pxX = -ny * halfW;
    const pxY = nx * halfW;
    this.gfx
      .poly([
        sx + pxX, sy + pxY,
        ex + pxX, ey + pxY,
        ex - pxX, ey - pxY,
        sx - pxX, sy - pxY,
      ])
      .fill({ color, alpha: 0.35 });
    this.gfx.moveTo(sx, sy).lineTo(ex, ey).stroke({ color, width: px(3), alpha: 1.0 });
  }

  private drawAimPreview(state: GameState, slot: number): void {
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
      case TargetingMode.CursorPick: {
        // Hover-highlight on the enemy nearest the cursor — feedback for
        // "this is who you'd hit." Same pick math as the server-side
        // dispatchCursorPick (within ~5u of aim, within params.Range of ship).
        let best: { x: number; y: number; radius: number } | null = null;
        let bestDist2 = 5 * 5; // pickRadius squared
        for (const ent of state.entities.values()) {
          if (!ent.current) continue;
          if (ent.current.entityType !== EntityType.NPC) continue;
          const ex = ent.renderX;
          const ey = ent.renderY;
          // Range from caster.
          const rx = ex - sx;
          const ry = ey - sy;
          if (range > 0 && rx * rx + ry * ry > range * range) continue;
          // Distance from cursor.
          const cdx = ex - cx;
          const cdy = ey - cy;
          const d2 = cdx * cdx + cdy * cdy;
          if (d2 < bestDist2) {
            bestDist2 = d2;
            const r = (ent.current.radius ?? 6) + px(3);
            best = { x: ex, y: ey, radius: r };
          }
        }
        // Range ring around ship — always visible during aim.
        this.gfx.circle(sx, sy, range).stroke({ color, width: px(1), alpha: 0.25 });
        if (best) {
          // Soft highlight ring on the picked enemy.
          this.gfx.circle(best.x, best.y, best.radius).stroke({ color: 0xff5544, width: px(2), alpha: 0.9 });
          this.gfx.circle(best.x, best.y, best.radius + px(3)).stroke({ color: 0xff5544, width: px(1), alpha: 0.3 });
        }
        break;
      }
      // Self / LockOn — no aim preview; handleAbilityPress fires those immediately.
    }
  }
}
