/** Server tick rate — defaults to 20, overridden by SE_SERVER_CONFIG on connect. */
export let TICK_RATE = 20;
export let TICK_MS = 1000 / TICK_RATE;
export let DT = TICK_MS / 1000;

export function setTickRate(rate: number): void {
  TICK_RATE = rate;
  TICK_MS = 1000 / rate;
  DT = TICK_MS / 1000;
}

/** Viewport scale factor: world units mapped to the smaller canvas dimension. */
export const VIEWPORT_SCALE = 3500;

/** Max duration (ms) for client-side movement prediction before giving up. */
export const PREDICTION_TIMEOUT_MS = 3000;
