import type { AnyEntity } from "../sdk/entities.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE, PREDICTION_TIMEOUT_MS } from "./constants.js";
import type { ClientEntity, EntitySample } from "./state.js";
import { type ClockSync, estimatedServerNow } from "./clockSync.js";
import { state } from "./state.js";

export function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

export function lerpAngle(a: number, b: number, t: number): number {
  let diff = b - a;
  while (diff > Math.PI) diff -= Math.PI * 2;
  while (diff < -Math.PI) diff += Math.PI * 2;
  return a + diff * t;
}

function entityRotation(e: AnyEntity, fallbackPrev: number): number {
  const moving = e.velX !== 0 || e.velY !== 0;
  return moving ? Math.atan2(e.velY, e.velX) : fallbackPrev;
}

function sampleFrom(e: AnyEntity, producedAtMs: number, prevRot: number): EntitySample {
  return {
    worldX: e.worldX,
    worldY: e.worldY,
    velX: e.velX,
    velY: e.velY,
    rotation: entityRotation(e, prevRot),
    producedAtMs,
  };
}

/** Append a sample to the entity's ring, evicting the oldest when full. */
export function pushSample(ent: ClientEntity, s: EntitySample): void {
  ent.samples.push(s);
  if (ent.samples.length > RING_SIZE) {
    ent.samples.shift();
  }
}

/**
 * updateEntityFromServer pushes one new authoritative snapshot into the
 * entity's ring (creating the ClientEntity if it doesn't exist yet). The
 * per-entity `producedAtMs` stamp lets the render loop interpolate on
 * true ClusterClock-aligned server-time deltas, immune to network
 * jitter and cell-tick phase drift.
 */
export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
): void {
  const id = serverState.netID;
  const existing = entities.get(id);
  if (!existing) {
    const rot = entityRotation(serverState, 0);
    const first = sampleFrom(serverState, producedAtMs, rot);
    const ent: ClientEntity = {
      ...serverState,
      prevX: serverState.worldX,
      prevY: serverState.worldY,
      isReplica: false,
      isGhost: false,
      samples: [first],
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    };
    entities.set(id, ent);
    return;
  }
  // Merge latest server data into existing entity (updates worldX/Y/velX/Y etc.)
  // then push a new ring sample. We spread serverState fields so the ClientEntity
  // stays current for checkPlayerArrival and velocity-arrow rendering.
  const prevRot = existing.renderRot;
  Object.assign(existing, serverState);
  existing.prevX = existing.renderX;
  existing.prevY = existing.renderY;
  pushSample(existing, sampleFrom(serverState, producedAtMs, prevRot));
}

/**
 * interpolateEntities sets renderX/Y/Rot on every entity by
 * interpolating between the two ring samples that bracket
 * (estimatedServerNow - RENDER_DELAY). Packet loss / phase drift are
 * absorbed naturally; extrapolation past the newest sample is capped.
 */
export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  clock: ClockSync,
  clientNowMs: number,
): void {
  if (!clock.initialized) return;
  const renderTime = estimatedServerNow(clock, clientNowMs) - RENDER_DELAY;

  for (const ent of entities.values()) {
    const n = ent.samples.length;
    if (n === 0) continue;

    if (n === 1) {
      applyStatic(ent, ent.samples[0]);
      continue;
    }

    // Find the newest pair (s0, s1) where s0.time ≤ renderTime ≤ s1.time.
    let s0 = ent.samples[0];
    let s1 = ent.samples[1];
    for (let i = 1; i < n - 1; i++) {
      if (ent.samples[i].producedAtMs <= renderTime) {
        s0 = ent.samples[i];
        s1 = ent.samples[i + 1];
      }
    }

    if (renderTime <= s0.producedAtMs) {
      applyStatic(ent, s0);
    } else if (renderTime >= s1.producedAtMs) {
      // Past newest — extrapolate using current sample's velocity, capped.
      const extMs = Math.min(renderTime - s1.producedAtMs, MAX_EXTRAPOLATE_MS);
      const extS = extMs / 1000;
      ent.renderX = s1.worldX + s1.velX * extS;
      ent.renderY = s1.worldY + s1.velY * extS;
      ent.renderRot = s1.rotation;
    } else {
      const t = (renderTime - s0.producedAtMs) / (s1.producedAtMs - s0.producedAtMs);
      ent.renderX = lerp(s0.worldX, s1.worldX, t);
      ent.renderY = lerp(s0.worldY, s1.worldY, t);
      ent.renderRot = lerpAngle(s0.rotation, s1.rotation, t);
    }
  }
}

function applyStatic(ent: ClientEntity, s: EntitySample): void {
  ent.renderX = s.worldX;
  ent.renderY = s.worldY;
  ent.renderRot = s.rotation;
}

const MOVE_SPEED = 300;
const DECEL_DIST = 100;
const MIN_SPEED = 30;

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

  // Blend toward server position to correct drift. Asymmetric factor:
  // when the server is AHEAD of predicted along the move direction
  // (server catching up or predicted lagging), pull at the full rate
  // to tighten tracking. When predicted is AHEAD of the server (the
  // normal case during the first ~100 ms after click, before the
  // server has processed the input and emitted a confirming frame),
  // pull at a much smaller rate so predicted doesn't get tugged
  // backward noticeably — avoids the "rubber-band on first click"
  // artifact. Once server motion samples start flowing steadily, the
  // two sides converge and the asymmetric bias has no effect.
  const player = state.entities.get(state.playerNetID);
  if (player) {
    const rdx = player.renderX - state.predictedX;
    const rdy = player.renderY - state.predictedY;
    const serverAhead = rdx * pdx + rdy * pdy > 0;
    const blend = serverAhead ? 0.15 : 0.02;
    state.predictedX += rdx * blend;
    state.predictedY += rdy * blend;
  }
}
