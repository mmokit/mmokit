import { describe, expect, test } from "bun:test";
import { PredictionBuffer } from "./prediction-buffer";

describe("PredictionBuffer", () => {
  test("cumulatively acknowledges and deterministically replays pending inputs", () => {
    const buffer = new PredictionBuffer<number>();
    buffer.push(1, 2);
    buffer.push(2, 3);
    buffer.push(3, 4);

    const result = buffer.reconcile(10, 1, (state, input) => state + input);
    expect(result).toEqual({ state: 17, pendingCount: 2, acknowledgedCount: 1 });
    expect(buffer.pendingInputs.map((entry) => entry.seq)).toEqual([2, 3]);
  });

  test("duplicate and stale ACKs cannot discard newer inputs", () => {
    const buffer = new PredictionBuffer<string>();
    buffer.push(100, "a");
    buffer.push(101, "b");
    expect(buffer.acknowledge(100).acknowledgedCount).toBe(1);
    expect(buffer.acknowledge(100).advanced).toBe(false);
    expect(buffer.acknowledge(99).advanced).toBe(false);
    expect(buffer.pendingInputs.map((entry) => entry.seq)).toEqual([101]);
  });

  test("acknowledges across uint32 wrap", () => {
    const buffer = new PredictionBuffer<number>();
    buffer.push(0xfffffffe, 1);
    buffer.push(0xffffffff, 2);
    buffer.push(0, 3);
    buffer.push(1, 4);

    const ack = buffer.acknowledge(0);
    expect(ack.acknowledgedCount).toBe(3);
    expect(buffer.pendingInputs.map((entry) => entry.seq)).toEqual([1]);
    expect(buffer.reconcile(10, 1, (state, input) => state + input)).toEqual({
      state: 10,
      pendingCount: 0,
      acknowledgedCount: 1,
    });
  });

  test("stays bounded and reports the oldest overflow", () => {
    const buffer = new PredictionBuffer<string>({ maxPending: 2 });
    buffer.push(10, "ten");
    buffer.push(11, "eleven");
    const result = buffer.push(12, "twelve");

    expect(result).toEqual({
      accepted: true,
      dropped: { seq: 10, input: "ten" },
    });
    expect(buffer.pendingInputs.map((entry) => entry.seq)).toEqual([11, 12]);
    expect(buffer.overflowCount).toBe(1);
  });

  test("rejects duplicate and backwards input sequences", () => {
    const buffer = new PredictionBuffer<number>();
    expect(buffer.push(5, 1).accepted).toBe(true);
    expect(buffer.push(5, 2)).toEqual({
      accepted: false,
      reason: "duplicate-or-stale",
    });
    expect(buffer.push(4, 3).accepted).toBe(false);
    expect(buffer.pendingCount).toBe(1);
  });

  test("leaves state cloning and mutation policy to the caller", () => {
    const buffer = new PredictionBuffer<number>();
    buffer.push(1, 2);
    const authoritative = { x: 10 };
    const result = buffer.reconcile(authoritative, 0, (state, input) => ({
      x: state.x + input,
    }));
    expect(authoritative).toEqual({ x: 10 });
    expect(result.state).toEqual({ x: 12 });
  });

  test("validates its bound and can reset sequence continuity", () => {
    expect(() => new PredictionBuffer({ maxPending: 0 })).toThrow(RangeError);
    const buffer = new PredictionBuffer<number>({ maxPending: 1 });
    buffer.push(10, 1);
    buffer.push(11, 2);
    expect(buffer.overflowCount).toBe(1);
    buffer.reset(100);
    expect(buffer.pendingCount).toBe(0);
    expect(buffer.overflowCount).toBe(0);
    expect(buffer.push(100, 2).accepted).toBe(false);
    expect(buffer.push(101, 3).accepted).toBe(true);
  });
});
