import { describe, expect, test } from "bun:test";
import { BasicDeltaDecoder } from "../../sdk/delta-decoder.js";

function emptyFrame(sequence: number, flags = 0): Uint8Array {
  const data = new Uint8Array(20);
  const view = new DataView(data.buffer);
  view.setUint32(0, sequence);
  view.setUint32(4, sequence);
  view.setUint32(8, flags);
  return data;
}

function fullPlayerFrame(sequence: number, authorityEpoch: number, worldX: number): Uint8Array {
  const snapshotLength = 21;
  const data = new Uint8Array(20 + 4 + 4 + 1 + 8 + 2 + snapshotLength + 2);
  const view = new DataView(data.buffer);
  view.setUint32(0, sequence); // tick
  view.setUint32(4, sequence);
  view.setUint32(8, 1); // FreshSnapshot
  view.setUint16(12, 1); // fullCount

  let pos = 20;
  view.setUint32(pos, 7); pos += 4;
  view.setUint32(pos, authorityEpoch); pos += 4;
  data[pos] = 1; pos += 1; // PlayerEntity
  view.setUint32(pos, 0); pos += 4;
  view.setUint32(pos, 1000); pos += 4;
  view.setUint16(pos, snapshotLength); pos += 2;
  view.setFloat32(pos, worldX); pos += snapshotLength;
  view.setUint16(pos, 0);
  return data;
}

describe("delta stream fencing", () => {
  test("rejects stale streams and reordered frames before decode", () => {
    const decoder = new BasicDeltaDecoder();
    expect(decoder.decode(emptyFrame(100), 7)?.streamChanged).toBe(true);
    expect(decoder.decode(emptyFrame(1, 1), 8)?.streamChanged).toBe(true);

    expect(decoder.decode(emptyFrame(101, 1), 7)).toBeNull();
    expect(decoder.decode(emptyFrame(1), 8)).toBeNull();
    expect(decoder.decode(emptyFrame(0), 8)).toBeNull();
    expect(decoder.decode(emptyFrame(2), 8)?.streamChanged).toBe(false);
  });

  test("accepts uint32 sequence and stream-epoch wrap", () => {
    const decoder = new BasicDeltaDecoder();
    expect(decoder.decode(emptyFrame(0xffff_ffff), 0xffff_ffff)).not.toBeNull();
    expect(decoder.decode(emptyFrame(0), 0xffff_ffff)).not.toBeNull();
    expect(decoder.decode(emptyFrame(0), 0)).not.toBeNull();
  });

  test("new stream generation supersedes entity authority history", () => {
    const decoder = new BasicDeltaDecoder();
    const destination = decoder.decode(fullPlayerFrame(1, 11, 110), 5)!;
    expect(destination.entered[0]?.authorityEpoch).toBe(11);

    // A rollback source epoch is stale while it remains in the destination's
    // enclosing stream.
    const sameStream = decoder.decode(fullPlayerFrame(2, 10, 100), 5)!;
    expect(sameStream.entered).toHaveLength(0);

    // Advancing the session stream supersedes the speculative destination,
    // so the source's still-valid older entity epoch must be accepted.
    const resumed = decoder.decode(fullPlayerFrame(1, 10, 100), 6)!;
    expect(resumed.entered).toHaveLength(1);
    expect(resumed.entered[0]?.authorityEpoch).toBe(10);
    expect(resumed.entered[0]?.worldX).toBe(100);
  });
});
