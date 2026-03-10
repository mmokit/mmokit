import { Container, Graphics } from "pixi.js";
import type { GameState } from "../state";

export class MiningLaserRenderer {
  private gfx: Graphics;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState, now: number): void {
    this.gfx.clear();

    for (const [, ent] of state.entities) {
      const e = ent.curr;
      if (e.miningActive && e.miningTargetId && state.entities.has(e.miningTargetId)) {
        const tgt = state.entities.get(e.miningTargetId)!;
        const pulse = 0.5 + 0.5 * Math.sin(now * 0.01);

        this.gfx.moveTo(ent.renderX, ent.renderY)
          .lineTo(tgt.renderX, tgt.renderY)
          .stroke({ color: 0x00ff80, width: 2 + pulse, alpha: 0.4 + pulse * 0.4 });
      }
    }
  }
}
