import { describe, test, expect, beforeEach } from "bun:test";
import { updateEntityFromServer, interpolateEntities } from "../interpolation";
import { newClockSync, observeServerTime } from "../../sdk/_core/clock-sync.js";
import { AdaptivePlaybackController } from "../../sdk/_core/playback-controller.js";
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

function fixedPlayback(): AdaptivePlaybackController {
  const clock = newClockSync();
  observeServerTime(clock, 1000, 0);
  return new AdaptivePlaybackController({
    clock,
    minDelayMs: RENDER_DELAY,
    maxDelayMs: RENDER_DELAY,
  });
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
  let playback = fixedPlayback();

  beforeEach(() => {
    entities = new Map();
    playback = fixedPlayback(); // offset = 1000
  });

  test("single sample: renders at that sample's position", () => {
    const ent = mkEntity(50, 1000);
    entities.set(1, ent);
    // clientNow=0 ⇒ serverNow=1000, renderTime=900
    interpolateEntities(entities, playback, 0);
    expect(ent.renderX).toBe(50);
  });

  test("two samples with renderTime between them: lerps", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100));
    entities.set(1, ent);
    // We want renderTime = 1050 (halfway). serverNow = clientNow + 1000, so clientNow = 50+RENDER_DELAY.
    const clientNow = 50 + RENDER_DELAY;
    interpolateEntities(entities, playback, clientNow);
    expect(ent.renderX).toBeCloseTo(50, 1);
  });

  test("renderTime past newest: extrapolates with velocity (capped)", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100)); // velX=10
    entities.set(1, ent);
    // Force renderTime = 1100 + 40ms, well past newest but inside cap.
    // clientNow ⇒ serverNow = 1140 ⇒ clientNow = 140 + RENDER_DELAY
    const clientNow = 140 + RENDER_DELAY;
    interpolateEntities(entities, playback, clientNow);
    // newest worldX 100 + velX 10 * 0.04s = 100.4
    expect(ent.renderX).toBeCloseTo(100.4, 1);
  });

  test("extrapolation cap: doesn't exceed MAX_EXTRAPOLATE_MS", () => {
    const ent = mkEntity(0, 1000);
    ent.buffer.push(mkSample(100, 1100));
    entities.set(1, ent);
    // renderTime = 1100 + 500ms (way past cap)
    const clientNow = 500 + RENDER_DELAY + 100;
    interpolateEntities(entities, playback, clientNow);
    // Capped: 100 + 10 * (50/1000) = 100.5
    expect(ent.renderX).toBeCloseTo(100.5, 1);
  });

  test("renderTime before oldest: holds at oldest", () => {
    const ent = mkEntity(42, 1000);
    ent.buffer.push(mkSample(100, 1100));
    entities.set(1, ent);
    // renderTime = 900 (before oldest at 1000)
    const clientNow = -100 + RENDER_DELAY;
    interpolateEntities(entities, playback, clientNow);
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

  test("new stream snapshot can recover an older source authority after rollback", () => {
    const entities = new Map<number, ClientEntity>();
    const destination = {
      netID: 42,
      authorityEpoch: 11,
      entityType: 0,
      producedAtMs: 1100,
      worldX: 110,
      worldY: 0,
      velX: 0,
      velY: 0,
    } as AnyEntity;
    const resumedSource = {
      ...destination,
      authorityEpoch: 10,
      producedAtMs: 1000,
      worldX: 100,
    } as AnyEntity;

    updateEntityFromServer(entities, destination, destination.producedAtMs);
    updateEntityFromServer(entities, resumedSource, resumedSource.producedAtMs);
    expect(entities.get(42)!.current.authorityEpoch).toBe(11);

    updateEntityFromServer(entities, resumedSource, resumedSource.producedAtMs, true);
    const recovered = entities.get(42)!;
    expect(recovered.current.authorityEpoch).toBe(10);
    expect(recovered.current.worldX).toBe(100);
    expect(recovered.buffer.samples).toHaveLength(1);
    expect(recovered.buffer.authorityEpoch).toBe(10);
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

    const playback = fixedPlayback();

    // Render at t = 1250 (between samples 100→300).
    const clientNow = 250 + RENDER_DELAY;
    interpolateEntities(entities, playback, clientNow);

    // Interp lerps between the two real samples (not a reset to newest).
    expect(ent.renderX).toBeGreaterThan(150);
    expect(ent.renderX).toBeLessThanOrEqual(250);
  });
});

describe("prediction never feeds back into the interpolation ring", () => {
  // Repo invariant: `prev` must be the previous SERVER snapshot, never a
  // rendered pose. If a predicted renderX/renderY/renderRot ever became a ring
  // sample, interpolation would converge geometrically toward its own output
  // and a teleport would smear instead of snapping.
  //
  // applyLocalMovementPrediction writes ONLY renderX/renderY/renderRot, and
  // runs after interpolateEntities. This pins that the ring is built purely
  // from serverState.
  test("a predicted render pose does not become the prev anchor", () => {
    const entities = new Map<number, ClientEntity>();
    const playback = fixedPlayback();

    // Two server samples establish the ring.
    updateEntityFromServer(
      entities,
      { netID: 1, entityType: 0, worldX: 0, worldY: 0, velX: 10, velY: 0 } as unknown as AnyEntity,
      1000,
    );
    updateEntityFromServer(
      entities,
      { netID: 1, entityType: 0, worldX: 100, worldY: 0, velX: 10, velY: 0 } as unknown as AnyEntity,
      1100,
    );
    const ent = entities.get(1)!;
    expect(ent.buffer.samples.length).toBe(2);

    interpolateEntities(entities, playback, 50 + RENDER_DELAY);

    // Stand in for applyLocalMovementPrediction: overwrite the render pose
    // with something far from any server sample.
    ent.renderX = 99_999;
    ent.renderY = 88_888;
    ent.renderRot = 1.234;

    // A third server sample arrives.
    updateEntityFromServer(
      entities,
      { netID: 1, entityType: 0, worldX: 200, worldY: 0, velX: 10, velY: 0 } as unknown as AnyEntity,
      1200,
    );

    // The ring must contain only server-supplied positions.
    const xs = ent.buffer.samples.map((s) => s.worldX);
    expect(xs).toEqual([0, 100, 200]);
    expect(xs).not.toContain(99_999);
    for (const s of ent.buffer.samples) {
      expect(s.worldY).toBe(0);
      expect(s.rotation).not.toBe(1.234);
    }

    // And the prev anchor for the next interpolation is the SECOND server
    // sample, not the predicted pose.
    const newest = ent.buffer.samples[ent.buffer.samples.length - 1];
    const prev = ent.buffer.samples[ent.buffer.samples.length - 2];
    expect(newest.worldX).toBe(200);
    expect(prev.worldX).toBe(100);
  });

  test("the rotation fallback DOES read renderRot — safe only because ships declare angle", () => {
    // Honest pin of the one real feedback path. entityRotation falls back to
    // the previous rotation for an entity with no `angle` field that is not
    // moving, and updateEntityFromServer passes the LIVE renderRot as that
    // fallback. So for such an entity a predicted rotation would re-enter the
    // ring.
    //
    // It cannot fire for the predicted entity: prediction only ever writes
    // state.myEntityId, and ShipEntity declares `angle`, so the fallback is
    // never consulted for it. This test pins BOTH halves — that the path
    // exists, and that declaring `angle` is what closes it — so a future
    // change that drops `angle` from the ship schema fails here instead of
    // shipping geometric convergence into the interpolator.
    const entities = new Map<number, ClientEntity>();
    const noAngle = (t: number) =>
      ({ netID: 2, entityType: 0, worldX: 0, worldY: 0, velX: 0, velY: 0 }) as unknown as AnyEntity;

    updateEntityFromServer(entities, noAngle(1000), 1000);
    const ent = entities.get(2)!;
    ent.renderRot = 2.5; // as if prediction had written it
    updateEntityFromServer(entities, noAngle(1100), 1100);

    const newest = ent.buffer.samples[ent.buffer.samples.length - 1];
    expect(newest.rotation).toBe(2.5); // the feedback path is real

    // An entity that declares `angle` ignores the fallback entirely, which is
    // exactly why the local ship is safe.
    const withAngle = new Map<number, ClientEntity>();
    const angled = (angle: number) =>
      ({ netID: 3, entityType: 0, worldX: 0, worldY: 0, velX: 0, velY: 0, angle }) as unknown as AnyEntity;

    updateEntityFromServer(withAngle, angled(0.25), 1000);
    const ship = withAngle.get(3)!;
    ship.renderRot = 2.5;
    updateEntityFromServer(withAngle, angled(0.5), 1100);

    const shipNewest = ship.buffer.samples[ship.buffer.samples.length - 1];
    expect(shipNewest.rotation).toBe(0.5);
    expect(shipNewest.rotation).not.toBe(2.5);
  });
});
