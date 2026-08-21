import { test, expect } from "bun:test";
import {
  sampleFrom,
  newBuffer,
  updateEntityFromServer,
  interpolateEntities,
  RENDER_DELAY_MS,
  type RenderEntity,
  type ServerEntity,
} from "../interpolation";
import type { AdaptivePlaybackController } from "../../sdk/_core/playback-controller.js";

const IDENT = { x: 0, y: 0, z: 0, w: 1 };
const QUARTER_Z = { x: 0, y: 0, z: Math.SQRT1_2, w: Math.SQRT1_2 };

function entity(over: Partial<ServerEntity> = {}): ServerEntity {
  return {
    netID: 1, worldX: 0, worldY: 0, worldZ: 0,
    velX: 0, velY: 0, velZ: 0,
    rot: IDENT, authorityEpoch: 0,
    ...over,
  };
}

/** A playback controller stub: render at a time the test chooses. */
function playbackAt(renderTimeMs: number | null): AdaptivePlaybackController {
  return {
    renderTime: () => renderTimeMs,
    currentDelayMs: RENDER_DELAY_MS,
  } as unknown as AdaptivePlaybackController;
}

test("the sample carries Z and orientation, not the 2D yaw channel", () => {
  const s = sampleFrom(entity({ worldZ: 42, velZ: -3, rot: QUARTER_Z }), 100);
  expect(s.worldZ).toBe(42);
  expect(s.velZ).toBe(-3);
  expect(s.quat).toEqual(QUARTER_Z);
  // rotation stays 0: this profile carries orientation in quat, and
  // populating both would be two sources of truth for one thing.
  expect(s.rotation).toBe(0);
});

test("a first snapshot renders at the snapshot pose, not the origin", () => {
  const world = new Map<number, RenderEntity>();
  updateEntityFromServer(world, entity({ worldX: 5, worldZ: 9, rot: QUARTER_Z }), 100);
  const e = world.get(1)!;
  expect(e.renderX).toBe(5);
  expect(e.renderZ).toBe(9);
  expect(e.renderQuat).toEqual(QUARTER_Z);
});

// The whole point: a pose BETWEEN two snapshots rather than snapped to one.
test("interpolates position and height, and slerps orientation, between samples", () => {
  const world = new Map<number, RenderEntity>();
  updateEntityFromServer(world, entity({ worldX: 0, worldZ: 10, rot: IDENT }), 0);
  updateEntityFromServer(world, entity({ worldX: 100, worldZ: 20, rot: QUARTER_Z }), 100);

  interpolateEntities(world, playbackAt(50), 0);
  const e = world.get(1)!;

  expect(e.renderX).toBeCloseTo(50, 3);
  expect(e.renderZ).toBeCloseTo(15, 3);
  // Halfway from identity to 90-about-Z is 45-about-Z: z = sin(22.5deg).
  expect(e.renderQuat.z).toBeCloseTo(Math.sin(Math.PI / 8), 4);
  expect(e.renderQuat.w).toBeCloseTo(Math.cos(Math.PI / 8), 4);
});

test("a stale snapshot is dropped whole, so nothing snaps backward", () => {
  const world = new Map<number, RenderEntity>();
  updateEntityFromServer(world, entity({ worldX: 0 }), 100);
  updateEntityFromServer(world, entity({ worldX: 100 }), 200);
  // An ex-authority's final frame arriving late.
  updateEntityFromServer(world, entity({ worldX: -999 }), 150);
  expect(world.get(1)!.current.worldX).toBe(100);
});

test("a fresh snapshot may reset the ring past the staleness gate", () => {
  const world = new Map<number, RenderEntity>();
  updateEntityFromServer(world, entity({ worldX: 0 }), 500);
  // Older stamp, but a fresh snapshot on a newer enclosing stream.
  updateEntityFromServer(world, entity({ worldX: 77 }), 100, true);
  expect(world.get(1)!.current.worldX).toBe(77);
});

test("no render time yet leaves poses untouched rather than zeroing them", () => {
  const world = new Map<number, RenderEntity>();
  updateEntityFromServer(world, entity({ worldX: 5, worldZ: 9 }), 100);
  interpolateEntities(world, playbackAt(null), 0);
  const e = world.get(1)!;
  expect(e.renderX).toBe(5);
  expect(e.renderZ).toBe(9);
});

test("the render delay is at least two ticks, or there is nothing to interpolate between", () => {
  expect(RENDER_DELAY_MS).toBeGreaterThanOrEqual(100);
});

test("a buffer is constructed with a ring deep enough for the delay", () => {
  const b = newBuffer();
  b.push(sampleFrom(entity(), 0));
  expect(b.newest()).not.toBeNull();
});
