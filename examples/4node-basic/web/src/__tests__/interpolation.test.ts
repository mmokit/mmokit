import { describe, test, expect, beforeEach } from "bun:test";
import { updateEntityFromServer, interpolateEntities } from "../interpolation";
import { newClockSync, observeServerTime } from "../../sdk/_core/clock-sync.js";
import { InterpolationBuffer } from "../../sdk/_core/interpolation-buffer.js";
import type { ClientEntity, EntitySample } from "../state";
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
    netID: 1,
    entityType: 1,
    producedAtMs: firstT,
    worldX: firstX,
    worldY: 0,
    velX: 10,
    velY: 0,
    radius: 10,
    width: 20,
    height: 20,
    name: "",
    prevX: firstX,
    prevY: 0,
    isReplica: false,
    isGhost: false,
    buffer,
    renderX: firstX,
    renderY: 0,
    renderRot: 0,
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
    // clientNow=0 => serverNow=1000, renderTime=900 — before oldest, holds at oldest.
    interpolateEntities(entities, clock, 0);
    expect(ent.renderX).toBe(50);
  });

  test("two samples with renderTime between them: lerps", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100));
    entities.set(1, ent);
    // We want renderTime = 1050 (halfway). serverNow = clientNow + 1000, so clientNow = 50 + RENDER_DELAY.
    const clientNow = 50 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);
    expect(ent.renderX).toBeCloseTo(50, 1);
  });

  test("renderTime past newest: extrapolates with velocity (capped)", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100)); // velX=10
    entities.set(1, ent);
    // Force renderTime = 1100 + 40ms, well past newest but inside cap.
    // clientNow => serverNow = 1140 => clientNow = 140 + RENDER_DELAY
    const clientNow = 140 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);
    // newest worldX 100 + velX 10 * 0.04s = 100.4
    expect(ent.renderX).toBeCloseTo(100.4, 1);
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
  test("UPDATE for unknown netID synthesizes a new entry", () => {
    const entities = new Map<number, ClientEntity>();
    const incoming = {
      netID: 777,
      entityType: 1 as const,
      producedAtMs: 1000,
      worldX: 50,
      worldY: 60,
      velX: 0,
      velY: 0,
      radius: 10,
      width: 20,
      height: 20,
      name: "",
    };
    updateEntityFromServer(entities, incoming, 1000);
    const ent = entities.get(777);
    expect(ent).toBeDefined();
    expect(ent!.renderX).toBe(50);
    expect(ent!.renderY).toBe(60);
    expect(ent!.buffer.samples.length).toBe(1);
  });

  test("second update for known netID appends to ring (preserves interp state)", () => {
    const entities = new Map<number, ClientEntity>();
    const base = {
      entityType: 1 as const,
      radius: 10,
      width: 20,
      height: 20,
      name: "",
    };
    // First frame seeds the ring.
    updateEntityFromServer(entities, { netID: 555, producedAtMs: 1000, worldX: 100, worldY: 200, velX: 0, velY: 0, ...base }, 1000);
    // Second frame appends.
    updateEntityFromServer(entities, { netID: 555, producedAtMs: 1100, worldX: 110, worldY: 210, velX: 10, velY: 10, ...base }, 1100);
    const ent = entities.get(555)!;
    expect(ent.buffer.samples.length).toBe(2);
    expect(ent.buffer.samples[0].worldX).toBe(100);
    expect(ent.buffer.samples[1].worldX).toBe(110);
  });

  // Regression: at a cell boundary the same netID is delivered from two
  // producers (the demoting source cell and the promoting destination cell).
  // The ex-authority's final in-flight frame can arrive AFTER the new
  // authority's frame. The sample ring already drops such stale frames for
  // position (interpolation-core pushSample), but non-interpolated fields
  // (radius/size, and anything snapped via Object.assign) must not be applied
  // from a stale frame either — otherwise radius snaps backward then forward,
  // flickering between a few sizes while position stays smooth. This is the
  // WASM-pulse "size jumps at cell lines" bug.
  test("stale out-of-order frame does not snap non-interpolated fields backward", () => {
    const entities = new Map<number, ClientEntity>();
    const base = { entityType: 1 as const, width: 20, height: 20, name: "" };
    // Seed, then a fresh newer frame sets radius=20 at t=1100.
    updateEntityFromServer(entities, { netID: 9, producedAtMs: 1000, worldX: 100, worldY: 0, velX: 0, velY: 0, radius: 10, ...base }, 1000);
    updateEntityFromServer(entities, { netID: 9, producedAtMs: 1100, worldX: 110, worldY: 0, velX: 0, velY: 0, radius: 20, ...base }, 1100);

    // Ex-authority's last frame races in late: older stamp (1050), radius=5.
    updateEntityFromServer(entities, { netID: 9, producedAtMs: 1050, worldX: 105, worldY: 0, velX: 0, velY: 0, radius: 5, ...base }, 1050);

    const ent = entities.get(9)!;
    // Stale frame must be ignored wholesale — radius and current pos unchanged.
    expect(ent.radius).toBe(20);
    expect(ent.worldX).toBe(110);
    // Ring tip stays the newest sample; stale sample not appended.
    expect(ent.buffer.samples.length).toBe(2);
    expect(ent.buffer.samples[ent.buffer.samples.length - 1].producedAtMs).toBe(1100);
  });
});

describe("interpolation smoothness — gap preserves baseline (handoff regression)", () => {
  test("one-tick update gap preserves interp ring; no baseline reset", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100));
    // Simulate a dropped tick at t=1200 (no sample pushed).
    ent.buffer.push(mkSample(300, 1300));

    const entities = new Map<number, ClientEntity>();
    entities.set(1, ent);

    const clock = newClockSync();
    observeServerTime(clock, 1000, 0); // offset = 1000

    // Render at t = 1250 (between samples at 1100 and 1300).
    const clientNow = 250 + RENDER_DELAY;
    interpolateEntities(entities, clock, clientNow);

    // Interp lerps between the two real samples (not a reset to newest).
    expect(ent.renderX).toBeGreaterThan(150);
    expect(ent.renderX).toBeLessThanOrEqual(250);
  });
});
