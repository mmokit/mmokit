import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  projectShipPrediction,
  type ShipPredictionParams,
  type ShipPredictionState,
} from "../prediction.js";

/**
 * Go -> TS ship-dynamics parity.
 *
 * `web-pixi/src/prediction.ts` stepShip mirrors
 * `internal/game/system_ship_dynamics.go` plus `pkg/system/physics.go` line for
 * line, and until this test nothing asserted it. Ship dynamics get tuned
 * routinely; a divergence shows up in production as constant rubber-banding,
 * not as a failing build. This is the test that turns that into a build
 * failure.
 *
 * The fixture is produced by `just shipdyn-golden`, which drives the REAL
 * systems through a real Stage — not a reimplementation.
 */

interface GoldenTick {
  x: number;
  y: number;
  velocityX: number;
  velocityY: number;
  angle: number;
  angularVelocity: number;
}

interface GoldenCase {
  name: string;
  seed: GoldenTick;
  moveTarget: { active: boolean; x: number; y: number };
  params: ShipPredictionParams;
  ticks: GoldenTick[];
}

interface GoldenManifest {
  note: string;
  tickIntervalMs: number;
  cases: GoldenCase[];
}

const manifest: GoldenManifest = JSON.parse(
  readFileSync(join(import.meta.dir, "testdata", "shipdyn_golden.json"), "utf8"),
);

function seedState(c: GoldenCase): ShipPredictionState {
  return {
    x: c.seed.x,
    y: c.seed.y,
    velocityX: c.seed.velocityX,
    velocityY: c.seed.velocityY,
    angle: c.seed.angle,
    angularVelocity: c.seed.angularVelocity,
    moveTarget: { active: c.moveTarget.active, x: c.moveTarget.x, y: c.moveTarget.y },
  };
}

// 1e-4 absolute. The two implementations do the same float32 arithmetic, but
// TS rounds through Math.fround at each step while Go's float32 rounding is
// implicit, so the last bits can differ. Anything larger than this is a real
// behavioural divergence, not rounding.
const TOLERANCE = 1e-4;

describe("ship-dynamics Go/TS parity", () => {
  it("has a fixture with every scripted scenario", () => {
    expect(manifest.tickIntervalMs).toBe(50);
    expect(manifest.cases.length).toBeGreaterThanOrEqual(6);
    expect(manifest.cases.map((c) => c.name)).toEqual([
      "straight-line-accelerate",
      "wide-turn",
      "crossing-snap",
      "arrival-radius-stop",
      "drag-only-coast-with-angular-bleed",
      "afterburner-speed-clamp",
    ]);
  });

  for (const c of manifest.cases) {
    describe(c.name, () => {
      for (let tick = 1; tick <= c.ticks.length; tick++) {
        const want = c.ticks[tick - 1];
        it(`matches the server at tick ${tick}`, () => {
          const got = projectShipPrediction({
            seedState: seedState(c),
            params: c.params,
            seedServerTimeMs: 0,
            targetServerTimeMs: tick * manifest.tickIntervalMs,
            tickIntervalMs: manifest.tickIntervalMs,
            // The default horizon is 250 ms (5 ticks); the fixture runs 8, and
            // clamping would silently make later ticks pass by not simulating
            // them at all.
            maxHorizonMs: c.ticks.length * manifest.tickIntervalMs,
          });

          expect(got.x).toBeCloseTo(want.x, 4);
          expect(got.y).toBeCloseTo(want.y, 4);
          expect(got.velocityX).toBeCloseTo(want.velocityX, 4);
          expect(got.velocityY).toBeCloseTo(want.velocityY, 4);
          expect(got.angle).toBeCloseTo(want.angle, 4);
          expect(got.angularVelocity).toBeCloseTo(want.angularVelocity, 4);
        });
      }

      it("stays within tolerance across the whole horizon", () => {
        // Guards against per-tick error that is individually inside tolerance
        // but accumulates: the final pose is checked against the final
        // authoritative sample directly.
        const last = c.ticks[c.ticks.length - 1];
        const got = projectShipPrediction({
          seedState: seedState(c),
          params: c.params,
          seedServerTimeMs: 0,
          targetServerTimeMs: c.ticks.length * manifest.tickIntervalMs,
          tickIntervalMs: manifest.tickIntervalMs,
          maxHorizonMs: c.ticks.length * manifest.tickIntervalMs,
        });
        expect(Math.abs(got.x - last.x)).toBeLessThan(TOLERANCE);
        expect(Math.abs(got.y - last.y)).toBeLessThan(TOLERANCE);
        expect(Math.abs(got.velocityX - last.velocityX)).toBeLessThan(TOLERANCE);
        expect(Math.abs(got.velocityY - last.velocityY)).toBeLessThan(TOLERANCE);
      });
    });
  }
});
