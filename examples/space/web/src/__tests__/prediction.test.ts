import { describe, expect, test } from "bun:test";
import {
  projectShipPrediction,
  type ShipPredictionInput,
  type ShipPredictionParams,
  type ShipPredictionState,
} from "../prediction";

const params: ShipPredictionParams = {
  thrust: 20,
  maxSpeed: 68,
  turnRate: 8,
  turnAccel: 8,
  dragCoefficient: 0,
  arrivalDistance: 2.7,
  decelerationDistance: 10,
};

function seed(overrides: Partial<ShipPredictionState> = {}): ShipPredictionState {
  return {
    x: 0,
    y: 0,
    velocityX: 0,
    velocityY: 0,
    angle: 0,
    angularVelocity: 0,
    moveTarget: { active: false, x: 0, y: 0 },
    ...overrides,
  };
}

function move(
  sequence: number,
  issuedAtMs: number,
  overrides: Partial<ShipPredictionInput> = {},
): ShipPredictionInput {
  return { sequence, issuedAtMs, active: true, x: 100, y: 0, ...overrides };
}

describe("projectShipPrediction", () => {
  test("responds to a newly issued target in the fractional first tick", () => {
    const predicted = projectShipPrediction({
      seedState: seed(),
      params,
      seedServerTimeMs: 1_000,
      targetServerTimeMs: 1_010,
      pendingInputs: [move(1, 1_000)],
    });

    expect(predicted.moveTarget.active).toBe(true);
    expect(predicted.velocityX).toBeCloseTo(0.2, 6);
    expect(predicted.x).toBeCloseTo(0.01, 6);
  });

  test("replays only the inputs left pending after cumulative ACK retirement", () => {
    // Sequence 10 was acknowledged and is deliberately absent. The caller's
    // PredictionBuffer supplies only 11 and 12 to this pure physics layer.
    const pending = [
      move(11, 2_000, { x: 0, y: 100 }),
      move(12, 2_025, { active: false, x: 999, y: 999 }),
    ];
    const predicted = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: 50, y: 0 } }),
      params,
      seedServerTimeMs: 2_000,
      targetServerTimeMs: 2_050,
      pendingInputs: pending,
    });

    expect(predicted.moveTarget).toEqual({ active: false, x: 0, y: 100 });
    expect(predicted.x).toBe(0);
  });

  test("applies inactive drag, angular bleed, and downstream position integration", () => {
    const predicted = projectShipPrediction({
      seedState: seed({ velocityX: 10, angularVelocity: 1 }),
      params: { ...params, dragCoefficient: 1.5 },
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });

    const expectedVelocity = 10 * Math.exp(-1.5 * 0.05);
    expect(predicted.velocityX).toBeCloseTo(expectedVelocity, 5);
    expect(predicted.angularVelocity).toBeCloseTo(0.6, 6);
    expect(predicted.angle).toBeCloseTo(0.03, 6);
    expect(predicted.x).toBeCloseTo(expectedVelocity * 0.05, 5);

    const stopped = projectShipPrediction({
      seedState: seed({ velocityX: 0.49 }),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });
    expect(stopped.velocityX).toBe(0);
    expect(stopped.x).toBe(0);
  });

  test("quantizes commands to server steps and batches a step before dynamics", () => {
    const immediate = projectShipPrediction({
      seedState: seed(),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 100,
      pendingInputs: [move(1, 0)],
    });
    const sameStep = projectShipPrediction({
      seedState: seed(),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 100,
      pendingInputs: [move(1, 25)],
    });
    const nextStep = projectShipPrediction({
      seedState: seed(),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 100,
      pendingInputs: [move(1, 51)],
    });

    expect(immediate.x).toBeCloseTo(0.15, 6);
    expect(sameStep).toEqual(immediate);
    expect(nextStep.x).toBeCloseTo(0.05, 6);
    expect(nextStep.x).toBeLessThan(immediate.x);
  });

  test("caps projection at the default 250ms horizon", () => {
    const longProjection = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: 100, y: 0 } }),
      params,
      seedServerTimeMs: 5_000,
      targetServerTimeMs: 6_000,
    });
    const exactlyAtCap = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: 100, y: 0 } }),
      params,
      seedServerTimeMs: 5_000,
      targetServerTimeMs: 5_250,
    });

    expect(longProjection).toEqual(exactlyAtCap);
  });

  test("honors a configurable horizon", () => {
    const predicted = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: 100, y: 0 } }),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 1_000,
      maxHorizonMs: 50,
    });
    const oneTick = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: 100, y: 0 } }),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });
    expect(predicted).toEqual(oneTick);
  });

  test("conditionally clamps status-modified speed", () => {
    const predicted = projectShipPrediction({
      seedState: seed({
        velocityX: 10,
        moveTarget: { active: true, x: 100, y: 0 },
      }),
      params: { ...params, thrust: 0, maxSpeed: 10, speedMultiplier: 0.5 },
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });

    expect(predicted.velocityX).toBeCloseTo(5, 6);
    expect(predicted.x).toBeCloseTo(0.25, 6);
  });

  test("stops thrusting inside the arrival radius but keeps this tick's coast", () => {
    const predicted = projectShipPrediction({
      seedState: seed({
        velocityX: 2,
        moveTarget: { active: true, x: 1, y: 0 },
      }),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });

    expect(predicted.moveTarget.active).toBe(false);
    expect(predicted.velocityX).toBe(2);
    expect(predicted.x).toBeCloseTo(0.1, 6);
  });

  test("rate-limits angular acceleration and snaps a crossing turn", () => {
    const accelerating = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: 0, y: 100 } }),
      params: { ...params, thrust: 0 },
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });
    expect(accelerating.angularVelocity).toBeCloseTo(0.4, 6);
    expect(accelerating.angle).toBeCloseTo(0.02, 6);

    const crossingAngle = 0.02;
    const crossing = projectShipPrediction({
      seedState: seed({
        angularVelocity: 1,
        moveTarget: {
          active: true,
          x: 100 * Math.cos(crossingAngle),
          y: 100 * Math.sin(crossingAngle),
        },
      }),
      params: { ...params, thrust: 0 },
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });
    expect(crossing.angle).toBeCloseTo(crossingAngle, 5);
    expect(crossing.angularVelocity).toBe(0);
  });

  test("matches Go float32 normalization at the positive-pi boundary", () => {
    const predicted = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: -100, y: 0 } }),
      params: { ...params, thrust: 0 },
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
    });

    expect(predicted.angularVelocity).toBeGreaterThan(0);
    expect(predicted.angle).toBeGreaterThan(0);
  });

  test("rejects coordinates that overflow when encoded as float32", () => {
    const predicted = projectShipPrediction({
      seedState: seed({ moveTarget: { active: true, x: 10, y: 20 } }),
      params: { ...params, thrust: 0 },
      seedServerTimeMs: 0,
      targetServerTimeMs: 50,
      pendingInputs: [move(1, 0, { x: 1e39, y: 0 })],
    });

    expect(predicted.moveTarget).toEqual({ active: true, x: 10, y: 20 });
  });

  test("is deterministic and never mutates seed or pending command order", () => {
    const authoritative = seed({
      velocityX: 3,
      velocityY: -2,
      angle: 0.4,
      angularVelocity: -0.2,
    });
    const originalSeed = structuredClone(authoritative);
    const pending = [
      move(3, 75, { x: 80, y: 30 }),
      move(2, 25, { x: 40, y: -20 }),
    ];
    const originalPending = structuredClone(pending);
    const request = {
      seedState: authoritative,
      params: { ...params, dragCoefficient: 1.5 },
      seedServerTimeMs: 0,
      targetServerTimeMs: 173,
      pendingInputs: pending,
    };

    const first = projectShipPrediction(request);
    const second = projectShipPrediction(request);
    expect(second).toEqual(first);
    expect(authoritative).toEqual(originalSeed);
    expect(pending).toEqual(originalPending);
  });

  test("preserves send order when estimated issue time moves backward", () => {
    const predicted = projectShipPrediction({
      seedState: seed(),
      params,
      seedServerTimeMs: 0,
      targetServerTimeMs: 100,
      pendingInputs: [
        move(1, 75, { x: 100, y: 0 }),
        move(2, 25, { active: false }),
      ],
    });

    // Re-sorting by timestamp would apply the cancel first and then re-enable
    // movement. Server/WebSocket order applies move then cancel in tick two.
    expect(predicted.moveTarget.active).toBe(false);
    expect(predicted.x).toBe(0);
  });
});
