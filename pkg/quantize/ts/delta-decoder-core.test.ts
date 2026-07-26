import { describe, expect, test } from "bun:test";
import {
  decodeInputAck,
  FRAME_FLAG_INPUT_ACK,
} from "./delta-decoder-core";

describe("decodeInputAck", () => {
  test("leaves the cursor untouched for legacy frames", () => {
    const decoded = decodeInputAck(new Uint8Array([1, 2, 3]), 2, 0);
    expect(decoded).toEqual({ sequence: null, offset: 2 });
  });

  test("decodes the optional uint32 trailer and advances the cursor", () => {
    const decoded = decodeInputAck(
      new Uint8Array([0xaa, 0xff, 0xff, 0xff, 0xff, 0xbb]),
      1,
      FRAME_FLAG_INPUT_ACK,
    );
    expect(decoded).toEqual({ sequence: 0xffff_ffff, offset: 5 });
  });

  test("rejects a flagged frame with a truncated trailer", () => {
    expect(() =>
      decodeInputAck(new Uint8Array([0, 0, 0]), 0, FRAME_FLAG_INPUT_ACK),
    ).toThrow(RangeError);
  });
});
