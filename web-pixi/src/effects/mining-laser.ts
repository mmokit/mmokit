import { Container, Graphics } from "pixi.js";
import type { GameState } from "../state";
import { audio } from "../audio/audio-manager";
import { SoundId } from "../audio/sounds";
import { weaponMountOffset } from "./ability-effects";

export class MiningLaserRenderer {
  private gfx: Graphics;
  private wasMining = false;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState, now: number): void {
    this.gfx.clear();

    // Check if the local player is actively mining (has a visible laser)
    const myEnt = state.entities.get(state.myEntityId);
    const isMining = !!(myEnt && myEnt.curr.miningActive && myEnt.curr.miningTargetId && state.entities.has(myEnt.curr.miningTargetId));

    if (isMining && !this.wasMining) {
      audio.loop(SoundId.MiningLaser);
    } else if (!isMining && this.wasMining) {
      audio.stopLoop(SoundId.MiningLaser);
    }
    this.wasMining = isMining;

    for (const [, ent] of state.entities) {
      const e = ent.curr;
      if (e.miningActive && e.miningTargetId && state.entities.has(e.miningTargetId)) {
        const tgt = state.entities.get(e.miningTargetId)!;
        const mask = e.miningBeamMask || 0;

        // Draw beam for weapon1 (port/left) if bit 0 set
        if (mask & 1) {
          const pulse = 0.5 + 0.5 * Math.sin(now * 0.01);
          const m = weaponMountOffset(ent.renderRot, e.height, 0);
          this.gfx.moveTo(ent.renderX + m.x, ent.renderY + m.y)
            .lineTo(tgt.renderX, tgt.renderY)
            .stroke({ color: 0x00ff80, width: 2 + pulse, alpha: 0.4 + pulse * 0.4 });
        }

        // Draw beam for weapon2 (starboard/right) if bit 1 set
        if (mask & 2) {
          const pulse = 0.5 + 0.5 * Math.sin(now * 0.01 + 1.5); // phase offset for visual distinction
          const m = weaponMountOffset(ent.renderRot, e.height, 2);
          this.gfx.moveTo(ent.renderX + m.x, ent.renderY + m.y)
            .lineTo(tgt.renderX, tgt.renderY)
            .stroke({ color: 0x00ff80, width: 2 + pulse, alpha: 0.4 + pulse * 0.4 });
        }

        // Fallback: if mask is 0 but mining is active (legacy), draw from center
        if (mask === 0) {
          const pulse = 0.5 + 0.5 * Math.sin(now * 0.01);
          this.gfx.moveTo(ent.renderX, ent.renderY)
            .lineTo(tgt.renderX, tgt.renderY)
            .stroke({ color: 0x00ff80, width: 2 + pulse, alpha: 0.4 + pulse * 0.4 });
        }
      }
    }
  }
}
