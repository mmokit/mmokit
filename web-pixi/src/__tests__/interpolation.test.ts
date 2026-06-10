import { describe, test, expect, beforeEach } from "bun:test";
import { updateEntityFromServer, interpolateEntities } from "../interpolation";
import { newClockSync, observeServerTime } from "../../sdk/_core/clock-sync.js";
import { InterpolationBuffer } from "../../sdk/_core/interpolation-buffer.js";
import type { ClientEntity, EntitySample } from "../types";
import type { AnyEntity } from "../../sdk/index.js";
import { RING_SIZE, RENDER_DELAY, MAX_EXTRAPOLATE_MS } from "../constants";

function mkSample(x: number, t: number): EntitySample {
  return { worldX: x, worldY: 0, velX: 10, velY: 0, rotation: 0, producedAtMs: t };
}

function mkBuffer(): InterpolationBuffer {
  return new InterpolationBuffer({
    ringSize: RING_SIZE,
    renderDelayMs: RENDER_DELAY,
    maxExtrapolateMs: MAX_EXTRAPOLATE_MS,
  });
}

function mkEntity(firstX: number, firstT: number): ClientEntity {
  const buffer = mkBuffer();
  buffer.push(mkSample(firstX, firstT));
  return {
    current: { netID: 1, entityType: 0, worldX: firstX, worldY: 0, velX: 10, velY: 0 } as any,
    buffer,
    renderX: firstX, renderY: 0, renderRot: 0,
  };
}

describe("InterpolationBuffer.push", () => {
  test("appends and caps at RING_SIZE", () => {
    const ent = mkEntity(0, 1000);
    for (let i = 1; i <= RING_SIZE + 2; i++) {
      ent.buffer.push(mkSample(i * 10, 1000 + i * 50));
    }
    expect(ent.buffer.samples.length).toBe(RING_SIZE);
    // Oldest sample should be the most recently pushed-minus-(RING_SIZE-1).
    const oldestT = ent.buffer.samples[0].producedAtMs;
    const newestT = ent.buffer.samples[ent.buffer.samples.length - 1].producedAtMs;
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
    ent.buffer.push(mkSample(100, 1100));
    entities.set(1, ent);
    // We want renderTime = 1050 (halfway). serverNow = clientNow + 1000, so clientNow = 50+RENDER_DELAY.
    const clientNow = 50 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);
    expect(ent.renderX).toBeCloseTo(50, 1);
  });

  test("renderTime past newest: extrapolates with velocity (capped)", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100)); // velX=10
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
    ent.buffer.push(mkSample(100, 1100));
    entities.set(1, ent);
    // renderTime = 1100 + 500ms (way past cap)
    const clientNow = 500 + RENDER_DELAY + 100;
    interpolateEntities(entities, clock, clientNow);
    // Capped: 100 + 10 * (50/1000) = 100.5
    expect(ent.renderX).toBeCloseTo(100.5, 1);
  });

  test("renderTime before oldest: holds at oldest", () => {
    const ent = mkEntity(42, 1000);
    ent.buffer.push(mkSample(100, 1100));
    entities.set(1, ent);
    // renderTime = 900 (before oldest at 1000)
    const clientNow = -100 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);
    expect(ent.renderX).toBe(42);
  });
});

describe("updateEntityFromServer — handoff robustness", () => {
  test("UPDATE for unknown netID synthesizes a SPAWN entry", () => {
    const entities = new Map<number, ClientEntity>();
    const incoming = {
      netID: 777,
      entityType: 0,
      worldX: 50,
      worldY: 60,
      velX: 0,
      velY: 0,
    } as AnyEntity;
    // Never saw netID 777 before; the server sent a delta UPDATE for it.
    updateEntityFromServer(entities, incoming, 1000);
    const ent = entities.get(777);
    expect(ent).toBeDefined();
    expect(ent!.renderX).toBe(50);
    expect(ent!.renderY).toBe(60);
    expect(ent!.buffer.samples.length).toBe(1);
  });

  test("SPAWN for known netID appends to ring (preserves interp state)", () => {
    const entities = new Map<number, ClientEntity>();
    // First frame seeds the ring.
    updateEntityFromServer(
      entities,
      {
        netID: 555,
        entityType: 0,
        worldX: 100,
        worldY: 200,
        velX: 0,
        velY: 0,
      } as AnyEntity,
      1000,
    );
    // Drive two more samples so the ring has real content.
    updateEntityFromServer(
      entities,
      {
        netID: 555,
        entityType: 0,
        worldX: 110,
        worldY: 210,
        velX: 10,
        velY: 10,
      } as AnyEntity,
      1100,
    );
    const beforeLen = entities.get(555)!.buffer.samples.length;
    expect(beforeLen).toBe(2);
    const firstSample = entities.get(555)!.buffer.samples[0];

    // Now a frame arrives that — on the wire — is in `entered` rather
    // than `updated` (server-side bookkeeping can flip this during
    // handoff). Client's `applyDeltaUpdate` merges entered + updated
    // into a single `fresh` list, so both flow through
    // updateEntityFromServer identically. The test here proves that
    // path: even a "new SPAWN" for an already-known netID must append,
    // not reset.
    updateEntityFromServer(
      entities,
      {
        netID: 555,
        entityType: 0,
        worldX: 120,
        worldY: 220,
        velX: 10,
        velY: 10,
      } as AnyEntity,
      1200,
    );
    const ent = entities.get(555)!;
    expect(ent.buffer.samples.length).toBe(3);
    // First sample must be preserved — no reset.
    expect(ent.buffer.samples[0]).toBe(firstSample);
  });
});

describe("interpolation — gap-preserves-interp-baseline (handoff regression)", () => {
  test("one-tick update gap preserves the interp ring; no baseline reset", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100));
    // Simulate a dropped tick at t=1200 (no sample pushed).
    ent.buffer.push(mkSample(300, 1300));

    const entities = new Map<number, ClientEntity>();
    entities.set(1, ent);

    const clock = newClockSync();
    observeServerTime(clock, 1000, 0); // offset = 1000

    // Render at t = 1250 (between samples 100→300).
    const clientNow = 250 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);

    // Interp lerps between the two real samples (not a reset to newest).
    expect(ent.renderX).toBeGreaterThan(150);
    expect(ent.renderX).toBeLessThanOrEqual(250);
  });
});
