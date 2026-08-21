import { test, expect } from "bun:test";
import {
  axesFromKeys,
  applyLook,
  forwardVector,
  NO_KEYS,
  PITCH_LIMIT,
  LOOK_SENSITIVITY,
} from "../flycontrol";

test("opposing keys cancel rather than one winning", () => {
  expect(axesFromKeys({ ...NO_KEYS, forward: true, back: true }).forward).toBe(0);
  expect(axesFromKeys({ ...NO_KEYS, left: true, right: true }).strafe).toBe(0);
  expect(axesFromKeys({ ...NO_KEYS, up: true, down: true }).lift).toBe(0);
});

test("single keys map to unit axes", () => {
  expect(axesFromKeys({ ...NO_KEYS, forward: true })).toEqual({ forward: 1, strafe: 0, lift: 0 });
  expect(axesFromKeys({ ...NO_KEYS, left: true })).toEqual({ forward: 0, strafe: -1, lift: 0 });
  expect(axesFromKeys({ ...NO_KEYS, down: true })).toEqual({ forward: 0, strafe: 0, lift: -1 });
});

// Pitch clamps and yaw wraps. An unclamped pitch flips the camera past
// vertical; a clamped yaw would stop the player turning around.
test("pitch clamps just short of vertical", () => {
  const up = applyLook({ yaw: 0, pitch: 0 }, 0, -100000);
  expect(up.pitch).toBeCloseTo(PITCH_LIMIT, 9);
  const down = applyLook({ yaw: 0, pitch: 0 }, 0, 100000);
  expect(down.pitch).toBeCloseTo(-PITCH_LIMIT, 9);
});

test("yaw wraps into [-pi, pi] instead of growing without bound", () => {
  let look = { yaw: 0, pitch: 0 };
  // Twenty full turns' worth of pointer movement.
  const pixelsPerTurn = (Math.PI * 2) / LOOK_SENSITIVITY;
  look = applyLook(look, -pixelsPerTurn * 20, 0);
  expect(Math.abs(look.yaw)).toBeLessThanOrEqual(Math.PI + 1e-9);
});

// Z-up, matching the engine. A renderer assuming Y-up produces a plausible
// but wrong picture, which no pixel-free test would otherwise catch.
test("forward is Z-up: yaw turns about Z, pitch lifts Z", () => {
  const flat = forwardVector({ yaw: 0, pitch: 0 });
  expect(flat.x).toBeCloseTo(1, 9);
  expect(flat.y).toBeCloseTo(0, 9);
  expect(flat.z).toBeCloseTo(0, 9);

  const quarter = forwardVector({ yaw: Math.PI / 2, pitch: 0 });
  expect(quarter.x).toBeCloseTo(0, 9);
  expect(quarter.y).toBeCloseTo(1, 9);

  const up = forwardVector({ yaw: 0, pitch: PITCH_LIMIT });
  expect(up.z).toBeGreaterThan(0.99);
});

test("forward stays unit length at every pitch", () => {
  for (const pitch of [-PITCH_LIMIT, -0.7, 0, 0.7, PITCH_LIMIT]) {
    const f = forwardVector({ yaw: 1.234, pitch });
    expect(Math.hypot(f.x, f.y, f.z)).toBeCloseTo(1, 9);
  }
});
