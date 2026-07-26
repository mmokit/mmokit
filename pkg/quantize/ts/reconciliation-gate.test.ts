import { describe, expect, test } from "bun:test";

import { type AckedFrame, ReconciliationGate } from "./reconciliation-gate.js";

// A minimal game-neutral seed. The real consumers carry a whole pose and
// parameter set; the gate only ever reads the identity triple.
interface TestSeed extends AckedFrame {
  payload: string;
}

function seed(overrides: Partial<TestSeed> = {}): TestSeed {
  return {
    streamEpoch: 3,
    tick: 10,
    processedSequence: 4,
    payload: "seed",
    ...overrides,
  };
}

describe("ReconciliationGate", () => {
  test("pairs seed-then-frame and frame-then-seed", () => {
    const seedFirst = new ReconciliationGate<TestSeed>();
    expect(seedFirst.stageSeed(seed())).toBeNull();
    expect(seedFirst.stageFrame({ streamEpoch: 3, tick: 10, processedSequence: 4 })).toEqual(seed());

    const frameFirst = new ReconciliationGate<TestSeed>();
    expect(frameFirst.stageFrame({ streamEpoch: 3, tick: 10, processedSequence: 4 })).toBeNull();
    expect(frameFirst.stageSeed(seed())).toEqual(seed());
  });

  test("quarantines tick, sequence, and authority mismatches", () => {
    const gate = new ReconciliationGate<TestSeed>();
    gate.stageSeed(seed());
    expect(gate.stageFrame({ streamEpoch: 3, tick: 11, processedSequence: 4 })).toBeNull();
    expect(gate.stageFrame({ streamEpoch: 3, tick: 10, processedSequence: 5 })).toBeNull();
    expect(gate.stageFrame({ streamEpoch: 4, tick: 10, processedSequence: 4 })).toBeNull();
  });

  test("new stream drops stale records but preserves an early new-stream seed", () => {
    const gate = new ReconciliationGate<TestSeed>();
    gate.observeStream(3);
    gate.stageSeed(seed({ tick: 9 }));
    const next = seed({ streamEpoch: 4, tick: 1, processedSequence: 8 });
    gate.stageSeed(next);

    expect(gate.observeStream(4)).toBe(true);
    expect(gate.stageFrame({ streamEpoch: 4, tick: 1, processedSequence: 8 })).toEqual(next);
    expect(gate.stageFrame({ streamEpoch: 3, tick: 9, processedSequence: 4 })).toBeNull();
  });

  test("same-stream fresh snapshot keeps only its exact early seed", () => {
    const gate = new ReconciliationGate<TestSeed>();
    gate.observeStream(3);
    gate.stageSeed(seed({ tick: 9 }));
    const exact = seed({ tick: 10 });
    gate.stageSeed(exact);
    const frame = { streamEpoch: 3, tick: 10, processedSequence: 4 };

    gate.resetForFreshSnapshot(frame);
    expect(gate.stageFrame(frame)).toEqual(exact);
    expect(gate.stageFrame({ streamEpoch: 3, tick: 9, processedSequence: 4 })).toBeNull();
  });

  test("rejects a late prior-stream seed while allowing an early successor", () => {
    const gate = new ReconciliationGate<TestSeed>();
    gate.observeStream(4);

    expect(gate.acceptsSeedStream(3)).toBe(false);
    expect(gate.stageSeed(seed({ streamEpoch: 3 }))).toBeNull();
    expect(gate.acceptsSeedStream(4)).toBe(true);
    expect(gate.acceptsSeedStream(5)).toBe(true);
    expect(gate.canApplySeedImmediately(4)).toBe(true);
    expect(gate.canApplySeedImmediately(5)).toBe(false);
  });

  test("quarantines a successor seed until its exact frame establishes the stream", () => {
    const gate = new ReconciliationGate<TestSeed>();
    gate.observeStream(3);
    const successor = seed({ streamEpoch: 4, tick: 1, processedSequence: 8 });

    expect(gate.canApplySeedImmediately(successor.streamEpoch)).toBe(false);
    expect(gate.stageSeed(successor)).toBeNull();
    expect(gate.observeStream(4)).toBe(true);
    expect(gate.stageFrame({ streamEpoch: 4, tick: 1, processedSequence: 8 })).toEqual(successor);
    expect(gate.canApplySeedImmediately(successor.streamEpoch)).toBe(true);
  });

  test("stream epoch only advances forward", () => {
    const gate = new ReconciliationGate<TestSeed>();
    expect(gate.observeStream(10)).toBe(false); // first observation establishes
    expect(gate.observeStream(10)).toBe(false); // same epoch is not a change
    expect(gate.observeStream(9)).toBe(false); // backward is ignored
    expect(gate.canApplySeedImmediately(10)).toBe(true);
    expect(gate.observeStream(11)).toBe(true);
  });

  test("evicts oldest-first when staging exceeds the bound", () => {
    // THIS is the case a naive port breaks: eviction order depends on JS Map
    // insertion order, which a plain hash map does not preserve. It only shows
    // up under staging pressure during a handoff.
    const gate = new ReconciliationGate<TestSeed>(3);
    for (let tick = 1; tick <= 4; tick++) {
      gate.stageSeed(seed({ tick, payload: `seed-${tick}` }));
    }
    // tick 1 was evicted; ticks 2..4 survive.
    expect(gate.stageFrame({ streamEpoch: 3, tick: 1, processedSequence: 4 })).toBeNull();
    for (let tick = 2; tick <= 4; tick++) {
      expect(gate.stageFrame({ streamEpoch: 3, tick, processedSequence: 4 })?.payload)
        .toBe(`seed-${tick}`);
    }
  });

  test("re-staging an existing key refreshes its position", () => {
    const gate = new ReconciliationGate<TestSeed>(2);
    gate.stageSeed(seed({ tick: 1, payload: "a" }));
    gate.stageSeed(seed({ tick: 2, payload: "b" }));
    // Re-stage tick 1: it moves to the back, so tick 2 becomes the oldest.
    gate.stageSeed(seed({ tick: 1, payload: "a2" }));
    gate.stageSeed(seed({ tick: 3, payload: "c" }));

    expect(gate.stageFrame({ streamEpoch: 3, tick: 2, processedSequence: 4 })).toBeNull();
    expect(gate.stageFrame({ streamEpoch: 3, tick: 1, processedSequence: 4 })?.payload).toBe("a2");
    expect(gate.stageFrame({ streamEpoch: 3, tick: 3, processedSequence: 4 })?.payload).toBe("c");
  });

  test("frames are bounded the same way as seeds", () => {
    const gate = new ReconciliationGate<TestSeed>(2);
    for (let tick = 1; tick <= 3; tick++) {
      gate.stageFrame({ streamEpoch: 3, tick, processedSequence: 4 });
    }
    // The tick-1 frame was evicted, so its seed has nothing to pair with.
    expect(gate.stageSeed(seed({ tick: 1 }))).toBeNull();
    expect(gate.stageSeed(seed({ tick: 2 }))).not.toBeNull();
  });

  test("reset clears the stream and both staging maps", () => {
    const gate = new ReconciliationGate<TestSeed>();
    gate.observeStream(5);
    gate.stageSeed(seed({ streamEpoch: 5 }));
    gate.reset();

    expect(gate.canApplySeedImmediately(1)).toBe(true); // no established stream
    expect(gate.stageFrame({ streamEpoch: 5, tick: 10, processedSequence: 4 })).toBeNull();
  });

  test("rejects a non-positive or non-integer bound", () => {
    expect(() => new ReconciliationGate<TestSeed>(0)).toThrow(RangeError);
    expect(() => new ReconciliationGate<TestSeed>(-1)).toThrow(RangeError);
    expect(() => new ReconciliationGate<TestSeed>(1.5)).toThrow(RangeError);
  });
});
