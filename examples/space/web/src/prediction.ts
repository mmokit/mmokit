/** Default server simulation cadence: 20 Hz. */
export const DEFAULT_PREDICTION_TICK_INTERVAL_MS = 50;

/** Keep a missing or delayed authority update from allowing runaway replay. */
export const DEFAULT_MAX_PREDICTION_HORIZON_MS = 250;

export interface ShipPredictionMoveTarget {
  /** World-absolute target coordinates, not cell-local coordinates. */
  active: boolean;
  x: number;
  y: number;
}

/** Runtime state required to resume the authoritative ship simulation. */
export interface ShipPredictionState {
  x: number;
  y: number;
  velocityX: number;
  velocityY: number;
  angle: number;
  angularVelocity: number;
  moveTarget: ShipPredictionMoveTarget;
}

/** Authoritative movement parameters captured with the prediction seed. */
export interface ShipPredictionParams {
  thrust: number;
  maxSpeed: number;
  turnRate: number;
  turnAccel: number;
  dragCoefficient: number;
  arrivalDistance: number;
  decelerationDistance: number;
  /** Combined afterburner/supercruise/slow multiplier. Defaults to 1. */
  speedMultiplier?: number;
}

/** An unacknowledged SetMoveTarget retained for reconciliation replay. */
export interface ShipPredictionInput {
  sequence: number;
  issuedAtMs: number;
  active: boolean;
  x: number;
  y: number;
}

export interface ShipPredictionProjection {
  seedState: Readonly<ShipPredictionState>;
  params: Readonly<ShipPredictionParams>;
  /** Cluster/server time represented by seedState. */
  seedServerTimeMs: number;
  /** Cluster/server time to which the caller wants to render. */
  targetServerTimeMs: number;
  /** Inputs remaining after the cumulative movement ACK was applied. */
  pendingInputs?: readonly Readonly<ShipPredictionInput>[];
  tickIntervalMs?: number;
  maxHorizonMs?: number;
}

/** Owner-only authoritative checkpoint used to reconcile local movement. */
export interface AuthoritativeShipPredictionSeed {
  tick: number;
  producedAtMs: number;
  entityNetID: number;
  streamEpoch: number;
  processedSequence: number;
  tickIntervalMs: number;
  /** Zero disables replay; otherwise the maximum safe fixed steps. */
  predictionTicks: number;
  state: ShipPredictionState;
  params: ShipPredictionParams;
}

const f32 = Math.fround;
const ANGLE_EPSILON = f32(0.001);
const STOP_SPEED = f32(0.5);
const PI = f32(Math.PI);
const TWO_PI = f32(2 * PI);

function sign(value: number): number {
  return value > 0 ? 1 : value < 0 ? -1 : 0;
}

function normalizeAngle(angle: number): number {
  let normalized = f32(angle);
  while (normalized > PI) normalized = f32(normalized - TWO_PI);
  while (normalized < -PI) normalized = f32(normalized + TWO_PI);
  return normalized;
}

function magnitude(x: number, y: number): number {
  const squared = f32(f32(f32(x) * f32(x)) + f32(f32(y) * f32(y)));
  return f32(Math.sqrt(squared));
}

function cloneState(seed: Readonly<ShipPredictionState>): ShipPredictionState {
  return {
    x: seed.x,
    y: seed.y,
    velocityX: seed.velocityX,
    velocityY: seed.velocityY,
    angle: seed.angle,
    angularVelocity: seed.angularVelocity,
    moveTarget: { ...seed.moveTarget },
  };
}

function interpolateForecast(
  current: ShipPredictionState,
  nextTick: Readonly<ShipPredictionState>,
  alphaValue: number,
): void {
  const alpha = f32(alphaValue);
  current.x = f32(current.x + f32(f32(nextTick.x - current.x) * alpha));
  current.y = f32(current.y + f32(f32(nextTick.y - current.y) * alpha));
  current.velocityX = f32(
    current.velocityX + f32(f32(nextTick.velocityX - current.velocityX) * alpha),
  );
  current.velocityY = f32(
    current.velocityY + f32(f32(nextTick.velocityY - current.velocityY) * alpha),
  );
  const angleDelta = normalizeAngle(f32(nextTick.angle - current.angle));
  current.angle = normalizeAngle(f32(current.angle + f32(angleDelta * alpha)));
  current.angularVelocity = f32(
    current.angularVelocity +
      f32(f32(nextTick.angularVelocity - current.angularVelocity) * alpha),
  );
  current.moveTarget = { ...nextTick.moveTarget };
}

function applyMoveTarget(state: ShipPredictionState, input: Readonly<ShipPredictionInput>): void {
  if (!input.active) {
    // Match MoveTarget.Cancel: retain the last destination while deactivating.
    state.moveTarget.active = false;
    return;
  }

  // The authoritative handler marks invalid commands processed but leaves the
  // previous target intact. World-bound validation remains server-owned.
  const targetX = f32(input.x);
  const targetY = f32(input.y);
  if (!Number.isFinite(targetX) || !Number.isFinite(targetY)) return;
  state.moveTarget.x = targetX;
  state.moveTarget.y = targetY;
  state.moveTarget.active = true;
}

/**
 * Mirror one ShipDynamicsSystem update followed by PhysicsSystem integration.
 * This deliberately does not predict terrain/entity collisions, docking pulls,
 * transfer partial-dt handling, or other systems that run around those two.
 */
function stepShip(
  state: ShipPredictionState,
  params: Readonly<ShipPredictionParams>,
  dtSeconds: number,
): void {
  const dt = f32(dtSeconds);
  if (dt <= 0) return;

  const speedMultiplier = f32(params.speedMultiplier ?? 1);
  const thrust = f32(f32(params.thrust) * speedMultiplier);
  const maxSpeed = f32(f32(params.maxSpeed) * speedMultiplier);
  const turnAccel = f32(params.turnAccel);
  const turnRate = f32(params.turnRate);

  // 1. Frame-rate independent drag.
  const dragExponent = f32(-f32(f32(params.dragCoefficient) * dt));
  const dragFactor = f32(Math.exp(dragExponent));
  state.velocityX = f32(f32(state.velocityX) * dragFactor);
  state.velocityY = f32(f32(state.velocityY) * dragFactor);

  // 2. Dead-stop floor.
  let speed = magnitude(state.velocityX, state.velocityY);
  if (speed < STOP_SPEED) {
    state.velocityX = 0;
    state.velocityY = 0;
  }

  if (!state.moveTarget.active) {
    // 3a. No target: drag linear velocity and bleed angular velocity.
    if (state.angularVelocity !== 0) {
      const angularStep = f32(turnAccel * dt);
      if (f32(Math.abs(state.angularVelocity)) <= angularStep) {
        state.angularVelocity = 0;
      } else if (state.angularVelocity > 0) {
        state.angularVelocity = f32(state.angularVelocity - angularStep);
      } else {
        state.angularVelocity = f32(state.angularVelocity + angularStep);
      }
      state.angle = f32(state.angle + f32(state.angularVelocity * dt));
    }
  } else {
    const dx = f32(f32(state.moveTarget.x) - f32(state.x));
    const dy = f32(f32(state.moveTarget.y) - f32(state.y));
    const distance = magnitude(dx, dy);

    if (distance < f32(params.arrivalDistance)) {
      // Arrival stops thrust; the already-dragged velocity coasts this tick.
      state.moveTarget.active = false;
    } else {
      const targetAngle = f32(Math.atan2(dy, dx));
      // ShipDynamics uses this pre-turn difference for this tick's alignment.
      const angleDifference = normalizeAngle(f32(targetAngle - f32(state.angle)));
      const absoluteDifference = f32(Math.abs(angleDifference));

      if (
        absoluteDifference < ANGLE_EPSILON &&
        f32(Math.abs(state.angularVelocity)) < ANGLE_EPSILON
      ) {
        state.angle = targetAngle;
        state.angularVelocity = 0;
      } else {
        let desiredMagnitude = f32(
          Math.sqrt(f32(f32(f32(2) * turnAccel) * absoluteDifference)),
        );
        if (desiredMagnitude > turnRate) desiredMagnitude = turnRate;
        const desiredAngularVelocity = f32(sign(angleDifference) * desiredMagnitude);

        let delta = f32(desiredAngularVelocity - f32(state.angularVelocity));
        const maximumDelta = f32(turnAccel * dt);
        if (delta > maximumDelta) delta = maximumDelta;
        else if (delta < -maximumDelta) delta = f32(-maximumDelta);
        state.angularVelocity = f32(state.angularVelocity + delta);

        if (state.angularVelocity > turnRate) state.angularVelocity = turnRate;
        else if (state.angularVelocity < -turnRate) state.angularVelocity = f32(-turnRate);

        const angularIntegration = f32(state.angularVelocity * dt);
        if (
          sign(angularIntegration) === sign(angleDifference) &&
          f32(Math.abs(angularIntegration)) >= absoluteDifference
        ) {
          state.angle = targetAngle;
          state.angularVelocity = 0;
        } else {
          state.angle = f32(state.angle + angularIntegration);
        }
      }

      let alignment = f32(Math.cos(angleDifference));
      if (alignment < 0) alignment = 0;

      let distanceFactor = f32(1);
      const decelerationDistance = f32(params.decelerationDistance);
      if (distance < decelerationDistance) {
        distanceFactor = f32(distance / decelerationDistance);
      }

      let thrustMagnitude = f32(thrust * alignment);
      thrustMagnitude = f32(thrustMagnitude * distanceFactor);
      thrustMagnitude = f32(thrustMagnitude * dt);
      const facingX = f32(Math.cos(state.angle));
      const facingY = f32(Math.sin(state.angle));
      state.velocityX = f32(state.velocityX + f32(facingX * thrustMagnitude));
      state.velocityY = f32(state.velocityY + f32(facingY * thrustMagnitude));

      // The server intentionally clamps only while a movement status is active.
      if (speedMultiplier !== 1) {
        speed = magnitude(state.velocityX, state.velocityY);
        if (speed > maxSpeed) {
          const scale = f32(maxSpeed / speed);
          state.velocityX = f32(state.velocityX * scale);
          state.velocityY = f32(state.velocityY * scale);
        }
      }
    }
  }

  // PhysicsSystem runs downstream of ShipDynamicsSystem.
  state.x = f32(state.x + f32(state.velocityX * dt));
  state.y = f32(state.y + f32(state.velocityY * dt));
}

/**
 * Project an authoritative seed through the still-pending movement commands.
 *
 * Commands are quantized into the fixed server step that can consume them.
 * Every command in a step is applied before ShipDynamics runs once, matching
 * the server's input-drain-before-systems ordering; the last target in that
 * batch therefore wins without introducing artificial substeps. Between
 * fixed endpoints, the next complete server step is forecast and its pose is
 * interpolated; the fractional pose is never fed back into simulation. The
 * input array and seed are never mutated.
 */
export function projectShipPrediction({
  seedState,
  params,
  seedServerTimeMs,
  targetServerTimeMs,
  pendingInputs = [],
  tickIntervalMs = DEFAULT_PREDICTION_TICK_INTERVAL_MS,
  maxHorizonMs = DEFAULT_MAX_PREDICTION_HORIZON_MS,
}: ShipPredictionProjection): ShipPredictionState {
  if (!Number.isFinite(seedServerTimeMs) || !Number.isFinite(targetServerTimeMs)) {
    throw new RangeError("prediction times must be finite");
  }
  if (!Number.isFinite(tickIntervalMs) || tickIntervalMs <= 0) {
    throw new RangeError("tickIntervalMs must be finite and greater than zero");
  }
  if (!Number.isFinite(maxHorizonMs) || maxHorizonMs < 0) {
    throw new RangeError("maxHorizonMs must be finite and non-negative");
  }

  const state = cloneState(seedState);
  const projectionEndMs = Math.max(
    seedServerTimeMs,
    Math.min(targetServerTimeMs, seedServerTimeMs + maxHorizonMs),
  );

  let previousEffectiveTimeMs = seedServerTimeMs;
  const commands = pendingInputs.map((input) => {
    if (!Number.isFinite(input.issuedAtMs)) {
      throw new RangeError("prediction input issuedAtMs must be finite");
    }
    // PredictionBuffer order is transport/send order. ClockSync can move
    // backward as old samples age out, so timestamps are clamped monotonic
    // instead of sorted and accidentally reversing commands.
    const effectiveTimeMs = Math.max(previousEffectiveTimeMs, input.issuedAtMs);
    previousEffectiveTimeMs = effectiveTimeMs;
    return { input, effectiveTimeMs };
  });

  const durationMs = projectionEndMs - seedServerTimeMs;
  const fullTicks = Math.floor(durationMs / tickIntervalMs);
  const tickSeconds = tickIntervalMs / 1000;
  let commandIndex = 0;

  const applyThrough = (inclusiveServerTimeMs: number): void => {
    while (
      commandIndex < commands.length &&
      commands[commandIndex].effectiveTimeMs <= inclusiveServerTimeMs
    ) {
      applyMoveTarget(state, commands[commandIndex].input);
      commandIndex++;
    }
  };

  for (let tick = 1; tick <= fullTicks; tick++) {
    applyThrough(seedServerTimeMs + tick * tickIntervalMs);
    stepShip(state, params, tickSeconds);
  }

  const tailMs = durationMs - fullTicks * tickIntervalMs;
  if (tailMs > 1e-9) {
    applyThrough(projectionEndMs);
    const nextTick = cloneState(state);
    stepShip(nextTick, params, tickSeconds);
    interpolateForecast(state, nextTick, tailMs / tickIntervalMs);
  }
  return state;
}
