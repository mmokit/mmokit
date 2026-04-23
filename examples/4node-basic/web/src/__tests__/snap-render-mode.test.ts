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

const { updateEntityFromServer, updatePrediction } = await import("../interpolation");
const { state } = await import("../state");

type ClientEntity = import("../state").ClientEntity;
type AnyEntity = import("../../sdk/entities").AnyEntity;

afterAll(() => {
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

  test("updateEntityFromServer still pushes samples in snap mode", () => {
    // Interpolation runs in BOTH modes — snap only disables prediction,
    // not render-lag interp. The sample ring is populated so the render
    // loop can lerp smoothly between 20Hz server ticks.
    updateEntityFromServer(entities, mkServerEntity(100, 200), 1000);
    updateEntityFromServer(entities, mkServerEntity(150, 250, 10, 0), 1050);

    const ent = entities.get(1)!;
    expect(ent.samples.length).toBe(2);
    expect(ent.samples[0].worldX).toBe(100);
    expect(ent.samples[1].worldX).toBe(150);
    // Latest server state spreads onto the entity.
    expect(ent.worldX).toBe(150);
  });

  test("updatePrediction no-ops in snap mode", () => {
    // Seed predictedX + moveTargetActive so that in interpolated mode
    // the predicted position would advance. In snap mode the early-return
    // gate keeps predicted* untouched — clicks must await server confirmation
    // before the body moves.
    state.playerNetID = 1;
    state.predictedX = 0;
    state.predictedY = 0;
    state.predictionActive = true;
    state.moveTargetActive = true;
    state.moveTargetX = 500;
    state.moveTargetY = 0;
    state.predictionStartTime = performance.now();
    state.lastFrameTime = performance.now() - 16;

    const beforeX = state.predictedX;
    const beforeY = state.predictedY;
    updatePrediction(performance.now());
    expect(state.predictedX).toBe(beforeX);
    expect(state.predictedY).toBe(beforeY);

    // Reset to avoid leaking state into other tests.
    state.predictionActive = false;
    state.moveTargetActive = false;
  });
});
