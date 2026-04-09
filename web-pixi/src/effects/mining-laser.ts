import { Container, Graphics } from "pixi.js";
import { px } from "../view";
import type { GameState } from "../state";
import { audio } from "../audio/audio-manager";
import { SoundId } from "../audio/sounds";
import { getAsteroid } from "../entity-accessors";

/**
 * MiningLaserRenderer draws the local player's mining laser beam to their
 * active mining target. After Phase 2, mining beam state is no longer
 * broadcast per-entity; the client only renders its own beam based on
 * state.targetId (the locally targeted asteroid). Other players' mining
 * visuals are no longer shown — acceptable degradation for the simplified
 * wire protocol.
 */
export class MiningLaserRenderer {
  private gfx: Graphics;
  private wasMining = false;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState, now: number): void {
    this.gfx.clear();

    const myEnt = state.myEntityId ? state.entities.get(state.myEntityId) : undefined;
    const targetEnt = state.targetId ? state.entities.get(state.targetId) : undefined;
    const targetIsAsteroid = !!(targetEnt && getAsteroid(targetEnt));
    const isMining = !!(myEnt && targetIsAsteroid);

    if (isMining && !this.wasMining) {
      audio.loop(SoundId.MiningLaser);
    } else if (!isMining && this.wasMining) {
      audio.stopLoop(SoundId.MiningLaser);
    }
    this.wasMining = isMining;

    if (isMining && myEnt && targetEnt) {
      const pulse = 0.5 + 0.5 * Math.sin(now * 0.01);
      this.gfx
        .moveTo(myEnt.renderX, myEnt.renderY)
        .lineTo(targetEnt.renderX, targetEnt.renderY)
        .stroke({ color: 0x00ff80, width: px(2 + pulse), alpha: 0.4 + pulse * 0.4 });
    }
  }
}
