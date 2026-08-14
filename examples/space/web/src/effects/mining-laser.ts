import { Container, Graphics } from "pixi.js";
import { px } from "../view";
import type { GameState } from "../state";
import { audio } from "../audio/audio-manager";
import { SoundId } from "../audio/sounds";
import { getShip } from "../entity-accessors";
import type { ClientEntity } from "../types";

/**
 * MiningLaserRenderer draws mining beams between ships and their targets.
 * Reads the replicated ActiveMining component (beam0Active, beam1Active,
 * miningTargetNetID) from every visible ship entity — not just the local player.
 */
export class MiningLaserRenderer {
  private gfx: Graphics;
  private wasMiningLocally = false;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState, now: number): void {
    this.gfx.clear();

    let localMining = false;
    const pulse = 0.5 + 0.5 * Math.sin(now * 0.01);

    for (const [netID, ent] of state.entities) {
      if (!getShip(ent)) continue; // filter to ship entities only
      const beams = extractMiningState(ent);
      if (!beams || (!beams.beam0Active && !beams.beam1Active)) continue;

      const target = state.entities.get(beams.miningTargetNetID);
      if (!target) continue;

      this.gfx
        .moveTo(ent.renderX, ent.renderY)
        .lineTo(target.renderX, target.renderY)
        .stroke({ color: 0x00ff80, width: px(2 + pulse), alpha: 0.4 + pulse * 0.4 });

      if (netID === state.myEntityId) {
        localMining = true;
      }
    }

    if (localMining && !this.wasMiningLocally) {
      audio.loop(SoundId.MiningLaser);
    } else if (!localMining && this.wasMiningLocally) {
      audio.stopLoop(SoundId.MiningLaser);
    }
    this.wasMiningLocally = localMining;
  }
}

interface MiningState {
  beam0Active: boolean;
  beam1Active: boolean;
  miningTargetNetID: number;
}

function extractMiningState(ent: ClientEntity): MiningState | null {
  // Field names match the SDK generator output for ActiveMining. Confirm
  // casing after regen with `grep miningTargetNetID web-pixi/sdk/entities.ts`.
  const e = ent.current as { beam0Active?: boolean; beam1Active?: boolean; miningTargetNetID?: number };
  if (typeof e.beam0Active !== "boolean") return null;
  return {
    beam0Active: !!e.beam0Active,
    beam1Active: !!e.beam1Active,
    miningTargetNetID: e.miningTargetNetID ?? 0,
  };
}
