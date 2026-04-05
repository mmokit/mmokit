import { state } from "./state.js";
import { PREDICTION_TIMEOUT_MS } from "./constants.js";
const MOVE_SPEED = 300;
const DECEL_DIST = 100;
const MIN_SPEED = 30;

/** Interpolate between two known positions, extrapolate with velocity past interp=1. */
export function interpPos(prevX: number, currX: number, vx: number, interp: number): number {
  if (interp <= 1.0) {
    return prevX + (currX - prevX) * interp;
  }
  return currX + vx * (interp - 1.0) * state.dt;
}

/** Returns the current interpolation factor (0-2) based on time since last tick. */
export function getInterp(): number {
  return Math.min((performance.now() - state.lastTickTime) / state.tickMs, 2.0);
}

/** Advance client prediction toward move target, blend with server position. */
export function updatePrediction(now: number): void {
  const frameDt = state.lastFrameTime > 0 ? (now - state.lastFrameTime) / 1000 : 1 / 60;

  if (!state.predictionActive || !state.moveTargetActive) return;

  if (now - state.predictionStartTime > PREDICTION_TIMEOUT_MS) {
    state.predictionActive = false;
    return;
  }

  const pdx = state.moveTargetX - state.predictedX;
  const pdy = state.moveTargetY - state.predictedY;
  const pdist = Math.sqrt(pdx * pdx + pdy * pdy);

  if (pdist < 5) {
    state.predictionActive = false;
    return;
  }

  let speed = MOVE_SPEED;
  if (pdist < DECEL_DIST) speed *= pdist / DECEL_DIST;
  if (speed < MIN_SPEED) speed = MIN_SPEED;
  const step = speed * frameDt;
  state.predictedX += (pdx / pdist) * Math.min(step, pdist);
  state.predictedY += (pdy / pdist) * Math.min(step, pdist);

  // Blend toward server position to correct drift.
  const player = state.entities.get(state.playerNetID);
  if (player) {
    const interp = getInterp();
    const serverX = interpPos(player.prevX, player.worldX, player.velX, interp);
    const serverY = interpPos(player.prevY, player.worldY, player.velY, interp);
    const blend = 0.15;
    state.predictedX += (serverX - state.predictedX) * blend;
    state.predictedY += (serverY - state.predictedY) * blend;
  }
}
