import { describe, test, expect } from "bun:test";
import {
  decodeFrameHeader,
  decodeFullEntry,
  decodeDeltaEntry,
  decodeRemovedIDs,
  decodeInputAck,
  applyDelta,
  unAngle,
  unNorm,
  unVel,
  unRel,
  FRAME_HEADER_SIZE,
} from "./delta-decoder-core";

// The golden manifest is authored by cmd/csharp-golden (Go reference).
const golden = require("../../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json");

// This file is the TypeScript half of a parity suite that previously only had a
// C# half. The manifest has carried frame/reflect/applyDelta/dequant bytes since
// it was written, and csharp/Mmokit.Sdk.Core.Tests asserted against all of them,
// but the TS side read only `clockSync` and `playback` — so the Unity client's
// delta decoder was pinned to Go's bytes and the browser's was not. That is
// backwards from where the risk sits: the browser is the reference game's only
// client. These tests close that asymmetry.
//
// Every expected value here comes from Go, not from this implementation. A
// TypeScript bug must surface as a failure here rather than as two decoders
// agreeing on the wrong answer.

function hex(s: string): Uint8Array {
  if (!s) return new Uint8Array(0);
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.substr(i * 2, 2), 16);
  return out;
}

describe("dequantizers (TS === Go === C#)", () => {
  test("every golden case", () => {
    for (const c of golden.dequant) {
      let got: number;
      switch (c.kind) {
        case "unAngle": got = unAngle(c.q); break;
        case "unNorm": got = unNorm(c.q); break;
        case "unVel": got = unVel(c.q, c.scale); break;
        case "unRel": got = unRel(c.q, c.scale); break;
        default: throw new Error(`unknown dequant kind ${c.kind}`);
      }
      expect(got).toBeCloseTo(c.expected, 4);
    }
  });
});

// One test per golden frame. `frame` has no input-ack; `inputAckFrame` does, and
// exercises the FRAME_FLAG_INPUT_ACK branch plus a non-empty exited list.
for (const name of ["frame", "inputAckFrame"] as const) {
  describe(`golden ${name} (TS === Go === C#)`, () => {
    const g = golden[name];

    test("frame header decodes to the Go-recorded values", () => {
      const frame = hex(g.hexBytes);
      const { header, offset } = decodeFrameHeader(frame, 0);
      expect(offset).toBe(FRAME_HEADER_SIZE);
      expect(header.tick).toBe(g.tick);
      expect(header.seq).toBe(g.seq);
      expect(header.flags).toBe(g.flags);
      expect(header.fullCount).toBe(g.fullCount);
      expect(header.deltaCount).toBe(g.deltaCount);
      expect(header.removedCount).toBe(g.removedCount);
      expect(header.exitedCount).toBe(g.exitedCount);
    });

    test("full, delta, removed and exited entries decode to the Go-recorded values", () => {
      const frame = hex(g.hexBytes);
      let { offset: pos } = decodeFrameHeader(frame, 0);

      for (const want of g.full ?? []) {
        const { entry, offset } = decodeFullEntry(frame, pos);
        pos = offset;
        expect(entry.netID).toBe(want.netID);
        expect(entry.epoch).toBe(want.epoch);
        expect(entry.entityType).toBe(want.entityType);
        expect(entry.producedAtMs).toBe(want.producedAtMs);
        expect(Array.from(entry.snapshot)).toEqual(Array.from(hex(want.snapshotHex)));
        expect(Array.from(entry.initialData ?? new Uint8Array(0)))
          .toEqual(Array.from(hex(want.initialHex ?? "")));
      }

      for (const want of g.delta ?? []) {
        const { entry, offset } = decodeDeltaEntry(frame, pos);
        pos = offset;
        expect(entry.netID).toBe(want.netID);
        expect(entry.producedAtMs).toBe(want.producedAtMs);
        expect(Array.from(entry.deltaData)).toEqual(Array.from(hex(want.deltaHex)));
      }

      const removed = decodeRemovedIDs(frame, pos, g.removedCount);
      pos = removed.offset;
      expect(removed.ids).toEqual(g.removedIDs ?? []);

      const exited = decodeRemovedIDs(frame, pos, g.exitedCount);
      pos = exited.offset;
      expect(exited.ids).toEqual(g.exitedIDs ?? []);

      // The input-ack trailer is read from the running offset, which is the
      // point of checking it here rather than in isolation: it only decodes
      // correctly if every preceding entry advanced by exactly the right
      // number of bytes.
      const ack = decodeInputAck(frame, pos, g.flags);
      if (g.hasInputAck) {
        expect(ack.sequence).toBe(g.expectedInputAck);
      } else {
        expect(ack.sequence).toBeNull();
      }
      pos = ack.offset;

      // Every byte accounted for. A decoder that silently stopped short would
      // pass every assertion above.
      expect(pos).toBe(frame.length);
    });
  });
}

describe("applyDelta (TS === Go === C#)", () => {
  test("every golden case", () => {
    for (const c of golden.applyDelta) {
      const got = applyDelta(c.fieldSizes, c.hasVarTail, hex(c.baselineHex), hex(c.deltaHex));
      expect(Array.from(got)).toEqual(Array.from(hex(c.expectedHex)));
    }
  });
});
