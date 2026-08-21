/**
 * Fly-camera input, as pure functions.
 *
 * This module deliberately imports NOTHING. It is the only part of the client
 * a test reaches, and CI runs the frontend suites with no `bun install` — a
 * bare specifier anywhere in a test-reachable import chain would force an
 * install step into a required check. The three.js scene lives in render3d.ts,
 * which no test imports.
 *
 * Keeping the maths here rather than in the renderer is also what makes it
 * assertable at all: a rotation hidden inside a scene graph is only checkable
 * by looking at pixels.
 */

/** Which movement keys are currently held. */
export interface KeyState {
  forward: boolean;
  back: boolean;
  left: boolean;
  right: boolean;
  up: boolean;
  down: boolean;
}

export const NO_KEYS: KeyState = {
  forward: false,
  back: false,
  left: false,
  right: false,
  up: false,
  down: false,
};

/** The axis triple sent to the server, each in [-1, 1]. */
export interface Axes {
  forward: number;
  strafe: number;
  lift: number;
}

/**
 * Collapse held keys into axes. Opposing keys cancel rather than one winning,
 * which is what makes releasing one of a held pair behave predictably.
 */
export function axesFromKeys(keys: KeyState): Axes {
  return {
    forward: (keys.forward ? 1 : 0) - (keys.back ? 1 : 0),
    strafe: (keys.right ? 1 : 0) - (keys.left ? 1 : 0),
    lift: (keys.up ? 1 : 0) - (keys.down ? 1 : 0),
  };
}

/** Look angles in radians. */
export interface Look {
  yaw: number;
  pitch: number;
}

/** How far the camera may pitch, just short of straight up or down. */
export const PITCH_LIMIT = Math.PI / 2 - 0.01;

/** Radians of rotation per pixel of pointer movement. */
export const LOOK_SENSITIVITY = 0.0025;

/**
 * Apply a pointer delta to the current look angles.
 *
 * Pitch CLAMPS and yaw WRAPS, and the asymmetry is deliberate: an unclamped
 * pitch flips the camera past vertical, while a clamped yaw would stop the
 * player turning around. Yaw is wrapped rather than left to grow so the value
 * the server receives stays bounded over a long session — the wire carries an
 * absolute angle, not a delta.
 */
export function applyLook(look: Look, dxPixels: number, dyPixels: number): Look {
  let yaw = look.yaw - dxPixels * LOOK_SENSITIVITY;
  let pitch = look.pitch - dyPixels * LOOK_SENSITIVITY;

  if (pitch > PITCH_LIMIT) pitch = PITCH_LIMIT;
  if (pitch < -PITCH_LIMIT) pitch = -PITCH_LIMIT;

  const twoPi = Math.PI * 2;
  yaw = ((yaw % twoPi) + twoPi) % twoPi;
  if (yaw > Math.PI) yaw -= twoPi;

  return { yaw, pitch };
}

/**
 * The world-space direction the camera is facing.
 *
 * Z-up, matching the engine: yaw turns about Z and pitch lifts the nose. A
 * renderer that assumed Y-up would produce a plausible-looking but wrong
 * picture, which is why this is here and tested rather than left to the
 * scene graph's conventions.
 */
export function forwardVector(look: Look): { x: number; y: number; z: number } {
  const cp = Math.cos(look.pitch);
  return {
    x: Math.cos(look.yaw) * cp,
    y: Math.sin(look.yaw) * cp,
    z: Math.sin(look.pitch),
  };
}
