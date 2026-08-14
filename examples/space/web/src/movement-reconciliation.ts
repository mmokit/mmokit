import { estimatedServerNow } from "../sdk/_core/clock-sync.js";
import { isForwardSequence } from "../sdk/_core/playback-controller.js";
import {
  type AckedFrame,
  ReconciliationGate,
} from "../sdk/_core/reconciliation-gate.js";
import { rebaseInputSequence, type GameState } from "./state";
import {
  DEFAULT_MAX_PREDICTION_HORIZON_MS,
  projectShipPrediction,
  type AuthoritativeShipPredictionSeed,
  type ShipPredictionInput,
} from "./prediction";

export interface MovementSeedWire {
  valid: boolean;
  predictionTicks: number;
  tick: number;
  producedAtMs: number;
  entityNetID: number;
  streamEpoch: number;
  processedSequence: number;
  tickIntervalMs: number;
  worldX: number;
  worldY: number;
  velocityX: number;
  velocityY: number;
  angle: number;
  angularVelocity: number;
  targetActive: boolean;
  targetX: number;
  targetY: number;
  thrust: number;
  turnRate: number;
  turnAccel: number;
  maxSpeed: number;
  speedMultiplier: number;
  dragCoefficient: number;
  arrivalDistance: number;
  decelerationDist: number;
}

/**
 * The frame identity this game pairs on. Structurally the shared core's
 * AckedFrame; kept as a named export because network.ts and the tests use it.
 */
export type AcceptedMovementFrame = AckedFrame;

/**
 * Ship-typed view of the generic reconciliation gate.
 *
 * The pairing logic itself — bounded oldest-first staging, forward-only stream
 * advance, epoch retention, both arrival orders — is game-neutral and now
 * lives in the SDK core (`pkg/quantize/ts/reconciliation-gate.ts`) so a Unity
 * client does not have to reinvent the most race-prone part of client
 * reconciliation. Only the seed type is game-specific.
 */
export class MovementReconciliationGate extends ReconciliationGate<AuthoritativeShipPredictionSeed> {}

const finitePoseFields: (keyof MovementSeedWire)[] = [
  "worldX",
  "worldY",
  "velocityX",
  "velocityY",
  "angle",
  "angularVelocity",
  "targetX",
  "targetY",
];

const finiteDynamicsFields: (keyof MovementSeedWire)[] = [
  "thrust",
  "turnRate",
  "turnAccel",
  "maxSpeed",
  "speedMultiplier",
  "dragCoefficient",
  "arrivalDistance",
  "decelerationDist",
];

export function decodeMovementSeed(
  movement: MovementSeedWire,
): AuthoritativeShipPredictionSeed | null {
  if (
    !movement.valid ||
    !Number.isSafeInteger(movement.tick) ||
    !Number.isSafeInteger(movement.entityNetID) ||
    !Number.isSafeInteger(movement.streamEpoch) ||
    !Number.isSafeInteger(movement.processedSequence)
  ) {
    return null;
  }

  const poseValid = finitePoseFields.every((field) => Number.isFinite(movement[field] as number));
  const dynamicsFinite = finiteDynamicsFields.every((field) =>
    Number.isFinite(movement[field] as number),
  );
  const dynamicsValid = dynamicsFinite && !(
    movement.thrust < 0 ||
    movement.turnRate < 0 ||
    movement.turnAccel < 0 ||
    movement.maxSpeed < 0 ||
    movement.speedMultiplier < 0 ||
    movement.dragCoefficient < 0 ||
    movement.arrivalDistance < 0 ||
    movement.decelerationDist < 0
  );
  const timingValid =
    Number.isFinite(movement.producedAtMs) &&
    movement.producedAtMs > 0 &&
    Number.isSafeInteger(movement.tickIntervalMs) &&
    movement.tickIntervalMs > 0;
  const predictionValid = poseValid && dynamicsValid && timingValid;
  const safe = (value: number): number => Number.isFinite(value) ? value : 0;

  return {
    tick: movement.tick >>> 0,
    producedAtMs: timingValid ? movement.producedAtMs : 0,
    entityNetID: movement.entityNetID >>> 0,
    streamEpoch: movement.streamEpoch >>> 0,
    processedSequence: movement.processedSequence >>> 0,
    tickIntervalMs: timingValid ? movement.tickIntervalMs >>> 0 : 0,
    predictionTicks: predictionValid
      ? Math.min(255, Math.max(0, Math.floor(movement.predictionTicks)))
      : 0,
    state: {
      x: safe(movement.worldX),
      y: safe(movement.worldY),
      velocityX: safe(movement.velocityX),
      velocityY: safe(movement.velocityY),
      angle: safe(movement.angle),
      angularVelocity: safe(movement.angularVelocity),
      moveTarget: {
        active: movement.targetActive,
        x: safe(movement.targetX),
        y: safe(movement.targetY),
      },
    },
    params: {
      thrust: safe(movement.thrust),
      turnRate: safe(movement.turnRate),
      turnAccel: safe(movement.turnAccel),
      maxSpeed: safe(movement.maxSpeed),
      speedMultiplier: safe(movement.speedMultiplier),
      dragCoefficient: safe(movement.dragCoefficient),
      arrivalDistance: safe(movement.arrivalDistance),
      decelerationDistance: safe(movement.decelerationDist),
    },
  };
}

/** Accept a seed for immediate pose replay without retiring buffered inputs. */
export function acceptMovementSeed(state: GameState, seed: AuthoritativeShipPredictionSeed): boolean {
  if (seed.entityNetID !== state.myEntityId) return false;
  const current = state.movementSeed;
  let streamAdvanced = false;
  if (current) {
    if (seed.streamEpoch !== current.streamEpoch) {
      if (!isForwardSequence(current.streamEpoch, seed.streamEpoch)) return false;
      streamAdvanced = true;
    } else if (seed.tick !== current.tick && !isForwardSequence(current.tick, seed.tick)) {
      return false;
    }
  }
  state.movementSeed = seed;
  if (seed.processedSequence !== 0) {
    // A reconnecting browser may receive this owner seed just before its
    // matching delta. Seed the outbound counter now so the first click is not
    // rejected against an older server frontier; buffered entries still wait
    // for the exact pair before physical retirement.
    state.inputSeq = rebaseInputSequence(state.inputSeq, seed.processedSequence);
  }
  // A new authority may stamp its first seed behind the old producer's
  // timeline. Reset the prediction cursor just as entity interpolation resets
  // its sample ring; otherwise the first new-owner pose jumps immediately to
  // the maximum prediction horizon.
  state.movementPredictionTimeMs = streamAdvanced
    ? seed.producedAtMs
    : Math.max(state.movementPredictionTimeMs ?? seed.producedAtMs, seed.producedAtMs);
  return true;
}

/** Retire commands only after the seed's matching delta passed stream fencing. */
export function acknowledgeMovementSeed(
  state: GameState,
  seed: AuthoritativeShipPredictionSeed,
): void {
  state.movementPrediction.acknowledge(seed.processedSequence);
  state.processedMovementSeq = state.movementPrediction.lastAcknowledgedSequence;
  state.inputSeq = seed.processedSequence === 0
    ? state.inputSeq
    : rebaseInputSequence(state.inputSeq, seed.processedSequence);
}

/** Override only the local render pose; authoritative entity samples stay intact. */
export function applyLocalMovementPrediction(state: GameState, clientNowMs: number): void {
  const seed = state.movementSeed;
  if (!seed || seed.predictionTicks === 0 || seed.entityNetID !== state.myEntityId) return;
  const entity = state.entities.get(state.myEntityId);
  if (!entity) return;

  const estimatedNow = state.clockSync.initialized
    ? estimatedServerNow(state.clockSync, clientNowMs)
    : seed.producedAtMs;
  const predictionTime = Math.max(
    seed.producedAtMs,
    state.movementPredictionTimeMs ?? seed.producedAtMs,
    estimatedNow,
  );
  state.movementPredictionTimeMs = predictionTime;

  const pendingInputs: ShipPredictionInput[] = [];
  for (const pending of state.movementPrediction.pendingInputs) {
    // A seed can arrive before its paired delta. Avoid replaying inputs it has
    // already consumed without physically retiring them until the pair lands.
    if (!isForwardSequence(seed.processedSequence, pending.seq)) continue;
    pendingInputs.push({ sequence: pending.seq, ...pending.input });
  }

  const predicted = projectShipPrediction({
    seedState: seed.state,
    params: seed.params,
    seedServerTimeMs: seed.producedAtMs,
    targetServerTimeMs: predictionTime,
    pendingInputs,
    tickIntervalMs: seed.tickIntervalMs,
    maxHorizonMs: Math.min(
      DEFAULT_MAX_PREDICTION_HORIZON_MS,
      seed.tickIntervalMs * seed.predictionTicks,
    ),
  });
  entity.renderX = predicted.x;
  entity.renderY = predicted.y;
  entity.renderRot = predicted.angle;
}
