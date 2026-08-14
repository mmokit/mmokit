import { describe, expect, test } from "bun:test";
import { playbackTickInterval, rebaseInputSequence } from "../state";

describe("rebaseInputSequence", () => {
  test("seeds a fresh browser from the server movement frontier", () => {
    expect(rebaseInputSequence(0, 73)).toBe(73);
    expect(rebaseInputSequence(1, 73)).toBe(73);
  });

  test("never moves a newer local counter backwards", () => {
    expect(rebaseInputSequence(90, 73)).toBe(90);
  });

  test("advances correctly across uint32 wrap", () => {
    expect(rebaseInputSequence(0xffff_ffff, 1)).toBe(1);
    expect(rebaseInputSequence(1, 0xffff_ffff)).toBe(1);
  });

  test("treats the exact half range as ambiguous", () => {
    expect(rebaseInputSequence(1, 0x8000_0001)).toBe(1);
  });
});

describe("playbackTickInterval", () => {
  test("uses the gateway-advertised simulation rate", () => {
    expect(playbackTickInterval(30)).toBeCloseTo(1000 / 30);
  });

  test("falls back to the default for invalid rates", () => {
    expect(playbackTickInterval(0)).toBe(50);
    expect(playbackTickInterval(Number.NaN)).toBe(50);
  });
});
