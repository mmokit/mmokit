import { Container, Graphics } from "pixi.js";
import { px } from "../view";
import type { GameState } from "../state";

const BEAM_COUNT = 4;
const BASE_COLOR = 0x00ff88;

export class TractorBeamRenderer {
  private gfx: Graphics;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState, now: number): void {
    this.gfx.clear();

    if (!state.isDockingInProgress || !state.dockingStationId) return;

    const myEnt = state.entities.get(state.myEntityId);
    const station = state.entities.get(state.dockingStationId);
    if (!myEnt || !station) return;

    const sx = station.renderX;
    const sy = station.renderY;
    const playerX = myEnt.renderX;
    const playerY = myEnt.renderY;
    const stationR = station.current.radius || 5;

    const progress = state.dockingProgress;
    const baseAlpha = 0.15 + progress * 0.35;

    for (let i = 0; i < BEAM_COUNT; i++) {
      const phase = now * 0.003 + (i * Math.PI * 2) / BEAM_COUNT;
      const wobble = Math.sin(phase) * px(8) * (1 - progress);
      const pulse = 0.7 + 0.3 * Math.sin(now * 0.005 + i);
      const width = px(1.5 + progress * 2) * pulse;
      const alpha = baseAlpha * pulse;

      // Offset beam start points around station perimeter
      const angle = phase * 0.5;
      const ox = Math.cos(angle) * stationR;
      const oy = Math.sin(angle) * stationR;

      this.gfx
        .moveTo(sx + ox, sy + oy)
        .lineTo(playerX + wobble, playerY + wobble)
        .stroke({ color: BASE_COLOR, width, alpha });
    }
  }
}
