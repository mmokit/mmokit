import { describe, test, expect, beforeEach, afterAll, mock } from "bun:test";

// Force the SDK's render-mode helpers into snap before loading any module
// that reads them. Subsequent `await import(...)` below pulls in
// interpolation.ts fresh with the mocked helpers in place.
//
// IMPORTANT: Bun's mock.module() is process-global and persists across
// test files. We restore the real module in afterAll so sibling test
// files that depend on Interpolated-mode behavior aren't affected.
mock.module("../../sdk/entities.js", () => ({
  CLIENT_RENDER_MODE: "snap",
  isSnapMode: () => true,
  isInterpolatedMode: () => false,
}));

const { updateEntityFromServer, interpolateEntities } = await import("../interpolation");
const { newClockSync, observeServerTime } = await import("../clockSync");

type ClientEntity = import("../state").ClientEntity;
type AnyEntity = import("../../sdk/entities").AnyEntity;

afterAll(() => {
  // Restore the real SDK module so other test files see the default
  // "interpolated" mode again. Re-mock with the real values.
  mock.module("../../sdk/entities.js", () => ({
    CLIENT_RENDER_MODE: "interpolated",
    isSnapMode: () => false,
    isInterpolatedMode: () => true,
  }));
});

function mkServerEntity(x: number, y: number, velX = 0, velY = 0): AnyEntity {
  return {
    netID: 1,
    entityType: 1,
    producedAtMs: 1000,
    worldX: x,
    worldY: y,
    velX,
    velY,
    radius: 10,
    width: 20,
    height: 20,
    meshState: 0,
    ownerNode: 0,
    aoIRadius: 500,
    name: "",
  };
}

describe("snap render mode", () => {
  let entities: Map<number, ClientEntity>;

  beforeEach(() => {
    entities = new Map();
  });

  test("first frame: empty samples ring, renderX/Y set to worldX/Y", () => {
    updateEntityFromServer(entities, mkServerEntity(100, 200), 1000);
    const ent = entities.get(1)!;
    expect(ent.samples.length).toBe(0);
    expect(ent.renderX).toBe(100);
    expect(ent.renderY).toBe(200);
  });

  test("subsequent frames: renderX/Y track worldX/Y directly, ring stays empty", () => {
    updateEntityFromServer(entities, mkServerEntity(100, 200), 1000);
    updateEntityFromServer(entities, mkServerEntity(150, 250, 10, 0), 1050);
    updateEntityFromServer(entities, mkServerEntity(210, 260, 10, 5), 1100);

    const ent = entities.get(1)!;
    expect(ent.samples.length).toBe(0);
    expect(ent.renderX).toBe(210);
    expect(ent.renderY).toBe(260);
    expect(ent.worldX).toBe(210);
    expect(ent.worldY).toBe(260);
  });

  test("renderRot follows velocity direction when entity is moving", () => {
    updateEntityFromServer(entities, mkServerEntity(0, 0), 1000);
    // move right → rotation = atan2(0, 10) = 0
    updateEntityFromServer(entities, mkServerEntity(10, 0, 10, 0), 1050);
    const ent = entities.get(1)!;
    expect(ent.renderRot).toBeCloseTo(0, 5);
  });

  test("interpolateEntities no-ops in snap mode (doesn't crash on empty ring)", () => {
    updateEntityFromServer(entities, mkServerEntity(100, 200), 1000);
    // clock is uninitialized in snap mode — if interpolateEntities didn't
    // early-return it would also hit `if (!clock.initialized) return`, but
    // more importantly with snap the ring is empty so an interp attempt
    // would blow up. The guard keeps renderX untouched.
    const clock = newClockSync();
    observeServerTime(clock, 1000, 0);
    const ent = entities.get(1)!;
    const rxBefore = ent.renderX;
    const ryBefore = ent.renderY;
    interpolateEntities(entities, clock, performance.now());
    expect(ent.renderX).toBe(rxBefore);
    expect(ent.renderY).toBe(ryBefore);
  });
});
