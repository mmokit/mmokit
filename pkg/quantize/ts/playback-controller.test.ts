import { describe, expect, test } from "bun:test";
import {
  AdaptivePlaybackController,
  DEFAULT_MIN_PLAYBACK_DELAY_MS,
} from "./playback-controller";

describe("AdaptivePlaybackController", () => {
  test("holds the minimum delay under steady 20 Hz delivery", () => {
    const controller = new AdaptivePlaybackController();
    let previous = -Infinity;
    for (let i = 0; i < 30; i++) {
      const producedAtMs = 1000 + i * 50;
      const arrivalTimeMs = 5000 + i * 50;
      controller.observeFrame({
        seq: i,
        freshSnapshot: i === 0,
        arrivalTimeMs,
        producedAtMs,
      });
      const cursor = controller.renderTime(arrivalTimeMs)!;
      expect(cursor).toBeGreaterThanOrEqual(previous);
      previous = cursor;
    }

    expect(controller.metrics.targetDelayMs).toBeCloseTo(
      DEFAULT_MIN_PLAYBACK_DELAY_MS,
      8,
    );
    expect(controller.metrics.currentDelayMs).toBeCloseTo(
      DEFAULT_MIN_PLAYBACK_DELAY_MS,
      8,
    );
    expect(controller.metrics.jitterMs).toBeCloseTo(0, 8);
    expect(controller.metrics.lostFrames).toBe(0);
    expect(controller.metrics.playbackRate).toBeCloseTo(1, 8);
  });

  test("attacks on a delayed burst and sequence loss, then decays", () => {
    const controller = new AdaptivePlaybackController();
    for (let i = 0; i < 5; i++) {
      controller.observeFrame({
        seq: i,
        freshSnapshot: i === 0,
        producedAtMs: 1000 + i * 50,
        arrivalTimeMs: 5000 + i * 50,
      });
    }

    controller.observeFrame({
      seq: 8,
      freshSnapshot: false,
      producedAtMs: 1400,
      arrivalTimeMs: 5550,
    });
    const attacked = controller.metrics.targetDelayMs;
    expect(attacked).toBeGreaterThan(DEFAULT_MIN_PLAYBACK_DELAY_MS);
    expect(controller.metrics.jitterMs).toBeGreaterThan(0);
    expect(controller.metrics.excessDelayMs).toBeGreaterThan(0);
    expect(controller.metrics.lostFrames).toBe(3);
    expect(controller.metrics.lossRate).toBeCloseTo(3 / 9, 8);

    // The tail of the delayed burst arrives together, then normal cadence
    // resumes. Arrival timestamps remain monotonic throughout the fixture.
    for (let i = 9; i < 12; i++) {
      controller.observeFrame({
        seq: i,
        freshSnapshot: false,
        producedAtMs: 1000 + i * 50,
        arrivalTimeMs: 5550,
      });
    }
    for (let i = 12; i < 112; i++) {
      controller.observeFrame({
        seq: i,
        freshSnapshot: false,
        producedAtMs: 1000 + i * 50,
        arrivalTimeMs: 5000 + i * 50,
      });
    }
    expect(controller.metrics.targetDelayMs).toBeLessThan(attacked);
    expect(controller.metrics.targetDelayMs).toBeGreaterThanOrEqual(
      DEFAULT_MIN_PLAYBACK_DELAY_MS,
    );
  });

  test("counts uint32 wrap gaps and accepts empty frames", () => {
    const controller = new AdaptivePlaybackController();
    controller.observeFrame({
      seq: 0xfffffffe,
      freshSnapshot: true,
      arrivalTimeMs: 5000,
      producedAtMs: 1000,
    });
    controller.observeFrame({
      seq: 0xffffffff,
      freshSnapshot: false,
      arrivalTimeMs: 5050,
    });
    controller.observeFrame({ seq: 0, freshSnapshot: false, arrivalTimeMs: 5100 });
    controller.observeFrame({ seq: 2, freshSnapshot: false, arrivalTimeMs: 5150 });

    expect(controller.metrics.receivedFrames).toBe(4);
    expect(controller.metrics.lostFrames).toBe(1);
    expect(controller.clock.initialized).toBe(true);
  });

  test("new streams reset sequence and producer clock without rewinding", () => {
    const controller = new AdaptivePlaybackController();
    controller.observeFrame({
      seq: 100,
      freshSnapshot: true,
      streamChanged: true,
      arrivalTimeMs: 5000,
      producedAtMs: 1000,
    });
    const before = controller.renderTime(5000)!;

    controller.observeFrame({
      seq: 900_000,
      freshSnapshot: true,
      streamChanged: true,
      arrivalTimeMs: 5050,
      producedAtMs: 550,
    });
    const after = controller.renderTime(5050)!;
    expect(controller.metrics.lostFrames).toBe(0);
    expect(controller.clock.offsetMs).toBe(-4500);
    expect(after).toBeGreaterThanOrEqual(before);
  });

  test("same-stream recovery snapshots still expose sequence loss", () => {
    const controller = new AdaptivePlaybackController();
    controller.observeFrame({
      seq: 10,
      freshSnapshot: true,
      streamChanged: false,
      arrivalTimeMs: 0,
    });
    controller.observeFrame({
      seq: 12,
      freshSnapshot: true,
      streamChanged: false,
      arrivalTimeMs: 50,
    });

    expect(controller.metrics.receivedFrames).toBe(2);
    expect(controller.metrics.lostFrames).toBe(1);
  });

  test("an empty stream switch invalidates the old clock until a producer stamp", () => {
    const controller = new AdaptivePlaybackController();
    controller.observeFrame({
      seq: 1,
      freshSnapshot: true,
      streamChanged: true,
      arrivalTimeMs: 1_000,
      producedAtMs: 2_000,
    });
    expect(controller.clock.offsetMs).toBe(1_000);

    controller.observeFrame({
      seq: 1,
      freshSnapshot: true,
      streamChanged: true,
      arrivalTimeMs: 1_050,
    });
    expect(controller.clock.initialized).toBe(false);
    expect(controller.renderTime(1_050)).toBeNull();

    controller.observeFrame({
      seq: 2,
      freshSnapshot: false,
      streamChanged: false,
      arrivalTimeMs: 1_100,
      producedAtMs: 1_900,
    });
    expect(controller.clock.initialized).toBe(true);
    expect(controller.clock.offsetMs).toBe(800);
  });

  test("same-stream recovery keeps the existing clock window", () => {
    const controller = new AdaptivePlaybackController();
    controller.observeFrame({
      seq: 10,
      freshSnapshot: true,
      streamChanged: true,
      arrivalTimeMs: 1_000,
      producedAtMs: 2_000,
    });
    controller.observeFrame({
      seq: 11,
      freshSnapshot: true,
      streamChanged: false,
      arrivalTimeMs: 1_100,
      producedAtMs: 1_900,
    });

    expect(controller.clock.offsetMs).toBe(1_000);
  });

  test("clamps target and playback rate without rewinding", () => {
    const controller = new AdaptivePlaybackController({
      minDelayMs: 100,
      maxDelayMs: 150,
      minPlaybackRate: 0.95,
      maxPlaybackRate: 1.05,
      convergenceWindowMs: 1,
      attackFactor: 1,
    });
    controller.observeFrame({
      seq: 0,
      freshSnapshot: true,
      arrivalTimeMs: 5000,
      producedAtMs: 1000,
    });
    const first = controller.renderTime(5000)!;

    // The empty forward frame creates loss pressure without changing ClockSync.
    controller.observeFrame({ seq: 10, freshSnapshot: false, arrivalTimeMs: 5050 });
    expect(controller.targetDelayMs).toBe(150);
    const second = controller.renderTime(5050)!;
    expect(second).toBeGreaterThanOrEqual(first);
    expect(controller.metrics.playbackRate).toBe(0.95);

    // A better clock sample moves estimated server-now forward. Catch-up is
    // capped at the other end of the configured playback-rate range.
    controller.observeFrame({
      seq: 11,
      freshSnapshot: false,
      arrivalTimeMs: 5100,
      producedAtMs: 1300,
    });
    const third = controller.renderTime(5100)!;
    expect(third).toBeGreaterThanOrEqual(second);
    expect(controller.metrics.playbackRate).toBe(1.05);
    expect(controller.metrics.targetDelayMs).toBeGreaterThanOrEqual(100);
    expect(controller.metrics.targetDelayMs).toBeLessThanOrEqual(150);
  });

  test("ignores duplicate/reordered sequence numbers for forward loss", () => {
    const controller = new AdaptivePlaybackController();
    controller.observeFrame({ seq: 10, freshSnapshot: true, arrivalTimeMs: 1 });
    controller.observeFrame({ seq: 10, freshSnapshot: false, arrivalTimeMs: 2 });
    controller.observeFrame({ seq: 9, freshSnapshot: false, arrivalTimeMs: 3 });
    controller.observeFrame({ seq: 11, freshSnapshot: false, arrivalTimeMs: 4 });
    expect(controller.metrics.receivedFrames).toBe(2);
    expect(controller.metrics.lostFrames).toBe(0);
    expect(controller.metrics.duplicateFrames).toBe(1);
    expect(controller.metrics.outOfOrderFrames).toBe(1);
  });

  test("pauses monotonically when the clock offset steps downward", () => {
    const controller = new AdaptivePlaybackController();
    controller.observeFrame({
      seq: 0,
      freshSnapshot: true,
      arrivalTimeMs: 5000,
      producedAtMs: 4000, // offset -1000ms
    });
    let cursor = controller.renderTime(5000)!;

    // Fill ClockSync's 40-sample window with a new -1600ms offset. The old
    // maximum remains authoritative until the final observation replaces it.
    for (let i = 1; i < 40; i++) {
      const arrivalTimeMs = 5000 + i * 50;
      controller.observeFrame({
        seq: i,
        freshSnapshot: false,
        arrivalTimeMs,
        producedAtMs: arrivalTimeMs - 1600,
      });
      const next = controller.renderTime(arrivalTimeMs)!;
      expect(next).toBeGreaterThanOrEqual(cursor);
      expect(controller.currentDelayMs).toBeGreaterThanOrEqual(0);
      cursor = next;
    }
    expect(controller.clock.offsetMs).toBe(-1000);

    const arrivalTimeMs = 7000;
    controller.observeFrame({
      seq: 40,
      freshSnapshot: false,
      arrivalTimeMs,
      producedAtMs: arrivalTimeMs - 1600,
    });
    expect(controller.clock.offsetMs).toBe(-1600);

    const corrected = controller.renderTime(arrivalTimeMs)!;
    expect(corrected).toBe(cursor);
    expect(controller.currentDelayMs).toBe(0);
    expect(controller.metrics.playbackRate).toBe(0);

    // The cursor remains monotonic and the effective delay remains valid while
    // the corrected producer clock catches back up.
    for (let i = 41; i <= 55; i++) {
      const nextArrivalTimeMs = 5000 + i * 50;
      controller.observeFrame({
        seq: i,
        freshSnapshot: false,
        arrivalTimeMs: nextArrivalTimeMs,
        producedAtMs: nextArrivalTimeMs - 1600,
      });
      const next = controller.renderTime(nextArrivalTimeMs)!;
      expect(next).toBeGreaterThanOrEqual(cursor);
      expect(controller.currentDelayMs).toBeGreaterThanOrEqual(0);
      cursor = next;
    }
    expect(cursor).toBeGreaterThanOrEqual(corrected);
  });
});
