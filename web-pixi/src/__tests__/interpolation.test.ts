import { describe, test, expect, beforeEach } from "bun:test";
import { pushSample, updateEntityFromServer, interpolateEntities } from "../interpolation";
import { newClockSync, observeServerTime } from "../clockSync";
import type { ClientEntity, EntitySample } from "../types";
import { RING_SIZE, RENDER_DELAY, MAX_EXTRAPOLATE_MS } from "../constants";

function mkSample(x: number, t: number): EntitySample {
  return { worldX: x, worldY: 0, velX: 10, velY: 0, rotation: 0, serverTimeMs: t };
}

function mkEntity(firstX: number, firstT: number): ClientEntity {
  return {
    current: { netID: 1, entityType: 0, worldX: firstX, worldY: 0, velX: 10, velY: 0 } as any,
    samples: [mkSample(firstX, firstT)],
    renderX: firstX, renderY: 0, renderRot: 0,
  };
}

describe("pushSample", () => {
  test("appends and caps at RING_SIZE", () => {
    const ent = mkEntity(0, 1000);
    for (let i = 1; i <= RING_SIZE + 2; i++) {
      pushSample(ent, mkSample(i * 10, 1000 + i * 50));
    }
    expect(ent.samples.length).toBe(RING_SIZE);
    // Oldest sample should be the most recently pushed-minus-(RING_SIZE-1).
    const oldestT = ent.samples[0].serverTimeMs;
    const newestT = ent.samples[ent.samples.length - 1].serverTimeMs;
    expect(newestT - oldestT).toBe(50 * (RING_SIZE - 1));
  });
});

describe("interpolateEntities", () => {
  let entities: Map<number, ClientEntity>;
  let clock = newClockSync();

  beforeEach(() => {
    entities = new Map();
    clock = newClockSync();
    observeServerTime(clock, 1000, 0); // offset = 1000
  });

  test("single sample: renders at that sample's position", () => {
    const ent = mkEntity(50, 1000);
    entities.set(1, ent);
    // clientNow=0 ⇒ serverNow=1000, renderTime=900
    interpolateEntities(entities, clock, 0);
    expect(ent.renderX).toBe(50);
  });

  test("two samples with renderTime between them: lerps", () => {
    const ent = mkEntity(0, 1000);
    pushSample(ent, mkSample(100, 1100));
    entities.set(1, ent);
    // We want renderTime = 1050 (halfway). serverNow = clientNow + 1000, so clientNow = 50+RENDER_DELAY.
    const clientNow = 50 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);
    expect(ent.renderX).toBeCloseTo(50, 1);
  });

  test("renderTime past newest: extrapolates with velocity (capped)", () => {
    const ent = mkEntity(0, 1000);
    pushSample(ent, mkSample(100, 1100)); // velX=10
    entities.set(1, ent);
    // Force renderTime = 1100 + 40ms, well past newest but inside cap.
    // clientNow ⇒ serverNow = 1140 ⇒ clientNow = 140 + RENDER_DELAY
    const clientNow = 140 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);
    // newest worldX 100 + velX 10 * 0.04s = 100.4
    expect(ent.renderX).toBeCloseTo(100.4, 1);
  });

  test("extrapolation cap: doesn't exceed MAX_EXTRAPOLATE_MS", () => {
    const ent = mkEntity(0, 1000);
    pushSample(ent, mkSample(100, 1100));
    entities.set(1, ent);
    // renderTime = 1100 + 500ms (way past cap)
    const clientNow = 500 + RENDER_DELAY + 100;
    interpolateEntities(entities, clock, clientNow);
    // Capped: 100 + 10 * (50/1000) = 100.5
    expect(ent.renderX).toBeCloseTo(100.5, 1);
  });

  test("renderTime before oldest: holds at oldest", () => {
    const ent = mkEntity(42, 1000);
    pushSample(ent, mkSample(100, 1100));
    entities.set(1, ent);
    // renderTime = 900 (before oldest at 1000)
    const clientNow = -100 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);
    expect(ent.renderX).toBe(42);
  });
});
