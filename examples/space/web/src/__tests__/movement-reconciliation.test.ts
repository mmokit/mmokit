import { describe, expect, test } from "bun:test";
import { newClockSync } from "../../sdk/_core/clock-sync.js";
import { AdaptivePlaybackController } from "../../sdk/_core/playback-controller.js";
import { PredictionBuffer } from "../../sdk/_core/prediction-buffer.js";
import {
  MovementReconciliationGate,
  acceptMovementSeed,
  acknowledgeMovementSeed,
  applyLocalMovementPrediction,
  decodeMovementSeed,
  type MovementSeedWire,
} from "../movement-reconciliation";
import type { AuthoritativeShipPredictionSeed } from "../prediction";
import type { GameState, MovementPredictionInput } from "../state";

function seed(overrides: Partial<AuthoritativeShipPredictionSeed> = {}): AuthoritativeShipPredictionSeed {
  return {
    tick: 10,
    producedAtMs: 1_000,
    entityNetID: 7,
    streamEpoch: 3,
    processedSequence: 4,
    tickIntervalMs: 50,
    predictionTicks: 5,
    state: {
      x: 0,
      y: 0,
      velocityX: 0,
      velocityY: 0,
      angle: 0,
      angularVelocity: 0,
      moveTarget: { active: false, x: 0, y: 0 },
    },
    params: {
      thrust: 20,
      maxSpeed: 68,
      turnRate: 8,
      turnAccel: 8,
      dragCoefficient: 0,
      arrivalDistance: 2.7,
      decelerationDistance: 10,
      speedMultiplier: 1,
    },
    ...overrides,
  };
}

function wire(overrides: Partial<MovementSeedWire> = {}): MovementSeedWire {
  return {
    valid: true,
    predictionTicks: 5,
    tick: 10,
    producedAtMs: 1_000,
    entityNetID: 7,
    streamEpoch: 3,
    processedSequence: 4,
    tickIntervalMs: 50,
    worldX: 1,
    worldY: 2,
    velocityX: 3,
    velocityY: 4,
    angle: 0.5,
    angularVelocity: 0.25,
    targetActive: true,
    targetX: 100,
    targetY: 200,
    thrust: 20,
    turnRate: 8,
    turnAccel: 8,
    maxSpeed: 68,
    speedMultiplier: 1,
    dragCoefficient: 1.5,
    arrivalDistance: 2.7,
    decelerationDist: 10,
    ...overrides,
  };
}

describe("MovementReconciliationGate", () => {
  test("pairs seed-then-delta and delta-then-seed", () => {
    const seedFirst = new MovementReconciliationGate();
    expect(seedFirst.stageSeed(seed())).toBeNull();
    expect(seedFirst.stageFrame({ streamEpoch: 3, tick: 10, processedSequence: 4 })).toEqual(seed());

    const deltaFirst = new MovementReconciliationGate();
    expect(deltaFirst.stageFrame({ streamEpoch: 3, tick: 10, processedSequence: 4 })).toBeNull();
    expect(deltaFirst.stageSeed(seed())).toEqual(seed());
  });

  test("quarantines tick, sequence, and authority mismatches", () => {
    const gate = new MovementReconciliationGate();
    gate.stageSeed(seed());
    expect(gate.stageFrame({ streamEpoch: 3, tick: 11, processedSequence: 4 })).toBeNull();
    expect(gate.stageFrame({ streamEpoch: 3, tick: 10, processedSequence: 5 })).toBeNull();
    expect(gate.stageFrame({ streamEpoch: 4, tick: 10, processedSequence: 4 })).toBeNull();
  });

  test("new stream drops stale records but preserves an early new-stream seed", () => {
    const gate = new MovementReconciliationGate();
    gate.observeStream(3);
    gate.stageSeed(seed({ tick: 9 }));
    const next = seed({ streamEpoch: 4, tick: 1, processedSequence: 8 });
    gate.stageSeed(next);

    expect(gate.observeStream(4)).toBe(true);
    expect(gate.stageFrame({ streamEpoch: 4, tick: 1, processedSequence: 8 })).toEqual(next);
    expect(gate.stageFrame({ streamEpoch: 3, tick: 9, processedSequence: 4 })).toBeNull();
  });

  test("same-stream fresh snapshot keeps only its exact early seed", () => {
    const gate = new MovementReconciliationGate();
    gate.observeStream(3);
    gate.stageSeed(seed({ tick: 9 }));
    const exact = seed({ tick: 10 });
    gate.stageSeed(exact);
    const frame = { streamEpoch: 3, tick: 10, processedSequence: 4 };

    gate.resetForFreshSnapshot(frame);
    expect(gate.stageFrame(frame)).toEqual(exact);
    expect(gate.stageFrame({ streamEpoch: 3, tick: 9, processedSequence: 4 })).toBeNull();
  });

  test("rejects a late prior-stream seed while allowing an early successor", () => {
    const gate = new MovementReconciliationGate();
    gate.observeStream(4);

    expect(gate.acceptsSeedStream(3)).toBe(false);
    expect(gate.stageSeed(seed({ streamEpoch: 3 }))).toBeNull();
    expect(gate.acceptsSeedStream(4)).toBe(true);
    expect(gate.acceptsSeedStream(5)).toBe(true);
    expect(gate.canApplySeedImmediately(4)).toBe(true);
    expect(gate.canApplySeedImmediately(5)).toBe(false);
  });

  test("quarantines a successor seed until its exact frame establishes the stream", () => {
    const gate = new MovementReconciliationGate();
    gate.observeStream(3);
    const successor = seed({ streamEpoch: 4, tick: 1, processedSequence: 8 });

    expect(gate.canApplySeedImmediately(successor.streamEpoch)).toBe(false);
    expect(gate.stageSeed(successor)).toBeNull();
    expect(gate.observeStream(4)).toBe(true);
    expect(gate.stageFrame({ streamEpoch: 4, tick: 1, processedSequence: 8 })).toEqual(successor);
    expect(gate.canApplySeedImmediately(successor.streamEpoch)).toBe(true);
  });
});

describe("movement seed reconciliation", () => {
  test("validates and maps the wire seed", () => {
    const decoded = decodeMovementSeed(wire());
    expect(decoded?.state).toEqual({
      x: 1,
      y: 2,
      velocityX: 3,
      velocityY: 4,
      angle: 0.5,
      angularVelocity: 0.25,
      moveTarget: { active: true, x: 100, y: 200 },
    });
    expect(decoded?.params.decelerationDistance).toBe(10);
    expect(decodeMovementSeed(wire({ valid: false }))).toBeNull();
    expect(decodeMovementSeed(wire({ speedMultiplier: Number.NaN }))?.predictionTicks).toBe(0);
    expect(decodeMovementSeed(wire({ tickIntervalMs: 0 }))?.predictionTicks).toBe(0);
  });

  test("uses an unpaired seed immediately but retires inputs only after pairing", () => {
    const movementPrediction = new PredictionBuffer<MovementPredictionInput>();
    movementPrediction.push(4, { active: true, x: -100, y: 0, issuedAtMs: 1_000 });
    movementPrediction.push(5, { active: true, x: 100, y: 0, issuedAtMs: 1_000 });
    const clockSync = newClockSync();
    clockSync.initialized = true;
    clockSync.offsetMs = 1_000;
    const entity = { renderX: 0, renderY: 0, renderRot: 0 };
    const state = {
      myEntityId: 7,
      inputSeq: 5,
      movementPrediction,
      processedMovementSeq: null,
      movementSeed: null,
      movementPredictionTimeMs: null,
      clockSync,
      entities: new Map([[7, entity]]),
    } as unknown as GameState;
    const authoritative = seed({ processedSequence: 4, predictionTicks: 1 });

    expect(acceptMovementSeed(state, authoritative)).toBe(true);
    applyLocalMovementPrediction(state, 50);
    expect(entity.renderX).toBeCloseTo(0.05, 6);
    expect(movementPrediction.pendingCount).toBe(2);

    acknowledgeMovementSeed(state, authoritative);
    expect(movementPrediction.pendingCount).toBe(1);
    expect(state.processedMovementSeq).toBe(4);
  });

  test("rejects a stale authority seed and leaves interpolation in control when disabled", () => {
    const state = {
      myEntityId: 7,
      movementSeed: seed({ streamEpoch: 4 }),
      movementPredictionTimeMs: null,
      movementPrediction: new PredictionBuffer<MovementPredictionInput>(),
      clockSync: newClockSync(),
      entities: new Map([[7, { renderX: 9, renderY: 8, renderRot: 7 }]]),
    } as unknown as GameState;

    expect(acceptMovementSeed(state, seed({ streamEpoch: 3 }))).toBe(false);
    state.movementSeed = seed({ streamEpoch: 4, predictionTicks: 0 });
    applyLocalMovementPrediction(state, 10_000);
    expect(state.entities.get(7)?.renderX).toBe(9);
  });

  test("resets prediction time when a successor stream moves producer time backward", () => {
    const state = {
      myEntityId: 7,
      inputSeq: 4,
      movementSeed: seed({ streamEpoch: 3, producedAtMs: 2_000 }),
      movementPredictionTimeMs: 2_200,
      movementPrediction: new PredictionBuffer<MovementPredictionInput>(),
    } as unknown as GameState;

    const successor = seed({ streamEpoch: 4, tick: 1, producedAtMs: 1_900 });
    expect(acceptMovementSeed(state, successor)).toBe(true);
    expect(state.movementPredictionTimeMs).toBe(1_900);
  });

  test("successor clock re-anchor avoids an immediate max-horizon projection", () => {
    const playback = new AdaptivePlaybackController();
    playback.observeFrame({
      seq: 10,
      freshSnapshot: true,
      streamChanged: true,
      arrivalTimeMs: 1_000,
      producedAtMs: 2_000,
    });
    const entity = { renderX: 0, renderY: 0, renderRot: 0 };
    const state = {
      myEntityId: 7,
      inputSeq: 4,
      movementSeed: seed({ streamEpoch: 3, producedAtMs: 2_000 }),
      movementPredictionTimeMs: 2_100,
      movementPrediction: new PredictionBuffer<MovementPredictionInput>(),
      clockSync: playback.clock,
      entities: new Map([[7, entity]]),
    } as unknown as GameState;

    playback.observeFrame({
      seq: 1,
      freshSnapshot: true,
      streamChanged: true,
      arrivalTimeMs: 1_100,
      producedAtMs: 1_900,
    });
    const successor = seed({ streamEpoch: 4, tick: 1, producedAtMs: 1_900 });
    expect(acceptMovementSeed(state, successor)).toBe(true);
    applyLocalMovementPrediction(state, 1_100);

    expect(playback.clock.offsetMs).toBe(800);
    expect(state.movementPredictionTimeMs).toBe(1_900);
  });

  test("an older exact pair still ACKs after a newer pose seed arrives", () => {
    const gate = new MovementReconciliationGate();
    const movementPrediction = new PredictionBuffer<MovementPredictionInput>();
    movementPrediction.push(4, { active: true, x: 10, y: 0, issuedAtMs: 1_000 });
    movementPrediction.push(5, { active: true, x: 20, y: 0, issuedAtMs: 1_050 });
    const state = {
      myEntityId: 7,
      inputSeq: 5,
      movementSeed: null,
      movementPredictionTimeMs: null,
      movementPrediction,
      processedMovementSeq: null,
    } as unknown as GameState;
    const older = seed({ tick: 10, processedSequence: 4 });
    const newer = seed({ tick: 11, processedSequence: 5, producedAtMs: 1_050 });

    acceptMovementSeed(state, older);
    gate.stageSeed(older);
    acceptMovementSeed(state, newer);
    gate.stageSeed(newer);
    const matched = gate.stageFrame({ streamEpoch: 3, tick: 10, processedSequence: 4 });

    expect(matched).toEqual(older);
    expect(acceptMovementSeed(state, matched!)).toBe(false);
    acknowledgeMovementSeed(state, matched!);
    expect(movementPrediction.pendingInputs.map((pending) => pending.seq)).toEqual([5]);
    expect(state.movementSeed).toEqual(newer);
  });

  test("standalone reconnect seed rebases the next send without retiring it", () => {
    const movementPrediction = new PredictionBuffer<MovementPredictionInput>();
    const state = {
      myEntityId: 7,
      inputSeq: 0,
      movementSeed: null,
      movementPredictionTimeMs: null,
      movementPrediction,
      processedMovementSeq: null,
    } as unknown as GameState;
    const reconnectSeed = seed({ processedSequence: 73 });

    expect(acceptMovementSeed(state, reconnectSeed)).toBe(true);
    expect(state.inputSeq).toBe(73);
    state.inputSeq = (state.inputSeq + 1) >>> 0;
    movementPrediction.push(state.inputSeq, {
      active: true,
      x: 100,
      y: 0,
      issuedAtMs: 1_000,
    });
    expect(movementPrediction.pendingCount).toBe(1);

    acknowledgeMovementSeed(state, reconnectSeed);
    expect(movementPrediction.pendingInputs.map((pending) => pending.seq)).toEqual([74]);
  });
});
