/**
 * Reference delta decoder core for mmokit binary wire format.
 *
 * This provides the reusable building blocks for decoding SE_DELTA_WORLD_UPDATE
 * binary frames. Games layer their entity-type-specific snapshot decoding on top.
 *
 * Wire format: see pkg/quantize/wireformat.go for the binary frame specification.
 */

// ---------------------------------------------------------------------------
// Binary reading helpers (big-endian)
// ---------------------------------------------------------------------------

/** Read a signed 16-bit integer (big-endian) from `data` at `offset`. */
export function readInt16(data: Uint8Array, offset: number): number {
  const v = (data[offset] << 8) | data[offset + 1];
  return v > 32767 ? v - 65536 : v;
}

/** Read an unsigned 16-bit integer (big-endian) from `data` at `offset`. */
export function readUint16(data: Uint8Array, offset: number): number {
  return (data[offset] << 8) | data[offset + 1];
}

/** Read an unsigned 32-bit integer (big-endian) from `data` at `offset`. */
export function readUint32(data: Uint8Array, offset: number): number {
  return (
    (data[offset] << 24) |
    (data[offset + 1] << 16) |
    (data[offset + 2] << 8) |
    data[offset + 3]
  ) >>> 0;
}

/** Read a 32-bit IEEE 754 float (big-endian) from `data` at `offset`. */
export function readFloat32(data: Uint8Array, offset: number): number {
  const view = new DataView(data.buffer, data.byteOffset + offset, 4);
  return view.getFloat32(0);
}

// ---------------------------------------------------------------------------
// Dequantization functions
// ---------------------------------------------------------------------------

/**
 * Dequantize a uint16 angle back to radians in [-PI, PI].
 * Inverse of the server-side quantizeAngle.
 */
export function unAngle(q: number): number {
  return (q / 65535) * 2 * Math.PI - Math.PI;
}

/**
 * Dequantize a uint8 normalized value back to [0, 1].
 * Inverse of the server-side quantizeNorm.
 */
export function unNorm(q: number): number {
  return q / 255;
}

/**
 * Dequantize a signed int16 velocity/scaled value back to [-scale, scale].
 * `q` should already be sign-extended (use readInt16).
 * Inverse of the server-side quantizeVel.
 */
export function unVel(q: number, scale: number): number {
  return (q / 32767) * scale;
}

/**
 * Dequantize a signed int16 relative position back to [-halfRange, halfRange].
 * `q` should already be sign-extended (use readInt16).
 * Inverse of the server-side quantizeRel.
 */
export function unRel(q: number, halfRange: number): number {
  return (q / 32767) * halfRange;
}

// ---------------------------------------------------------------------------
// Frame header decoding
// ---------------------------------------------------------------------------

/** Decoded header from a SE_DELTA_WORLD_UPDATE binary frame (24 bytes). */
export interface FrameHeader {
  tick: number;
  seq: number;
  viewerX: number;
  viewerY: number;
  fullCount: number;
  deltaCount: number;
  removedCount: number;
  exitedCount: number;
}

/** Header size in bytes. */
export const FRAME_HEADER_SIZE = 24;

/**
 * Decode the 24-byte frame header from the beginning of a delta world update frame.
 * Returns the parsed header and the byte offset immediately after the header.
 */
export function decodeFrameHeader(
  data: Uint8Array,
  offset: number,
): { header: FrameHeader; offset: number } {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  let pos = offset;

  const tick = view.getUint32(pos); pos += 4;
  const seq = view.getUint32(pos); pos += 4;
  const viewerX = view.getFloat32(pos); pos += 4;
  const viewerY = view.getFloat32(pos); pos += 4;
  const fullCount = view.getUint16(pos); pos += 2;
  const deltaCount = view.getUint16(pos); pos += 2;
  const removedCount = view.getUint16(pos); pos += 2;
  const exitedCount = view.getUint16(pos); pos += 2;

  return {
    header: { tick, seq, viewerX, viewerY, fullCount, deltaCount, removedCount, exitedCount },
    offset: pos,
  };
}

// ---------------------------------------------------------------------------
// Full / delta entry parsing
// ---------------------------------------------------------------------------

/**
 * A full entity entry read from the frame.
 * `snapshot` is the complete field data; `initialData` contains one-time info
 * (e.g. name) or null if the entry has none.
 */
export interface FullEntryHeader {
  netID: number;
  entityType: number;
  snapshot: Uint8Array;
  initialData: Uint8Array | null;
}

/**
 * A delta entity entry read from the frame.
 * `deltaData` is the bitmask + changed-field payload.
 */
export interface DeltaEntryHeader {
  netID: number;
  entityType: number;
  deltaData: Uint8Array;
}

/**
 * Decode one full entity entry starting at `pos`.
 * Wire layout: netID(4) entityType(1) snapLen(2) snapshot(snapLen) initLen(2) [initialData(initLen)]
 */
export function decodeFullEntry(
  data: Uint8Array,
  pos: number,
): { entry: FullEntryHeader; offset: number } {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);

  const netID = view.getUint32(pos); pos += 4;
  const entityType = data[pos]; pos += 1;
  const snapLen = view.getUint16(pos); pos += 2;
  const snapshot = data.slice(pos, pos + snapLen); pos += snapLen;
  const initLen = view.getUint16(pos); pos += 2;
  let initialData: Uint8Array | null = null;
  if (initLen > 0) {
    initialData = data.slice(pos, pos + initLen);
    pos += initLen;
  }

  return { entry: { netID, entityType, snapshot, initialData }, offset: pos };
}

/**
 * Decode one delta entity entry starting at `pos`.
 * Wire layout: netID(4) entityType(1) deltaLen(2) deltaData(deltaLen)
 */
export function decodeDeltaEntry(
  data: Uint8Array,
  pos: number,
): { entry: DeltaEntryHeader; offset: number } {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);

  const netID = view.getUint32(pos); pos += 4;
  const entityType = data[pos]; pos += 1;
  const deltaLen = view.getUint16(pos); pos += 2;
  const deltaData = data.subarray(pos, pos + deltaLen); pos += deltaLen;

  return { entry: { netID, entityType, deltaData }, offset: pos };
}

/**
 * Read removed/exited netIDs from the frame. Each is a uint32 (4 bytes).
 * Returns the list of IDs and the offset after reading.
 */
export function decodeRemovedIDs(
  data: Uint8Array,
  pos: number,
  count: number,
): { ids: number[]; offset: number } {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const ids: number[] = [];
  for (let i = 0; i < count; i++) {
    ids.push(view.getUint32(pos));
    pos += 4;
  }
  return { ids, offset: pos };
}

// ---------------------------------------------------------------------------
// Generic delta application
// ---------------------------------------------------------------------------

/**
 * Apply a delta payload to a baseline snapshot, producing an updated snapshot.
 *
 * @param fieldSizes  Array of byte sizes for each fixed field in the snapshot.
 *                    The length of this array is the number of fixed fields.
 * @param hasVarTail  Whether the snapshot has a variable-length tail field after
 *                    the fixed fields. If true, the tail is at logical field index
 *                    `fieldSizes.length` in the bitmask.
 * @param baseline    The current baseline snapshot bytes.
 * @param delta       The delta payload: bitmask + changed field values.
 * @returns           The new snapshot (a fresh Uint8Array).
 */
export function applyDelta(
  fieldSizes: number[],
  hasVarTail: boolean,
  baseline: Uint8Array,
  delta: Uint8Array,
): Uint8Array {
  const totalLogicalFields = fieldSizes.length + (hasVarTail ? 1 : 0);
  const bitmaskSize = Math.ceil(totalLogicalFields / 8);

  // Compute fixed size from field sizes.
  let fixedSize = 0;
  for (let i = 0; i < fieldSizes.length; i++) {
    fixedSize += fieldSizes[i];
  }

  // Delta read cursor starts past bitmask.
  let dpos = bitmaskSize;

  // Clone baseline for mutation.
  let result = new Uint8Array(baseline);

  // Apply fixed fields.
  let baseOff = 0;
  for (let i = 0; i < fieldSizes.length; i++) {
    const bit = (delta[Math.floor(i / 8)] >> (i % 8)) & 1;
    if (bit) {
      const sz = fieldSizes[i];
      result.set(delta.subarray(dpos, dpos + sz), baseOff);
      dpos += sz;
    }
    baseOff += fieldSizes[i];
  }

  // Apply variable-length tail if present and changed.
  if (hasVarTail) {
    const tailIdx = fieldSizes.length; // logical field index for the var tail
    const bit = (delta[Math.floor(tailIdx / 8)] >> (tailIdx % 8)) & 1;
    if (bit) {
      // Read new tail: uint16 byte-length + data.
      const newLen = readUint16(delta, dpos); dpos += 2;
      const newTail = delta.subarray(dpos, dpos + newLen);

      // Rebuild: fixed portion + uint16 len + tail data.
      const rebuilt = new Uint8Array(fixedSize + 2 + newLen);
      rebuilt.set(result.subarray(0, fixedSize));
      rebuilt[fixedSize] = (newLen >> 8) & 0xFF;
      rebuilt[fixedSize + 1] = newLen & 0xFF;
      rebuilt.set(newTail, fixedSize + 2);
      result = rebuilt;
    }
  }

  return result;
}

// ---------------------------------------------------------------------------
// Baseline store
// ---------------------------------------------------------------------------

/**
 * Generic per-entity baseline store for delta decoding.
 *
 * Stores the latest full snapshot for each netID so that subsequent delta
 * payloads can be applied on top. The optional type parameter `T` allows
 * games to attach per-entity metadata (e.g. entity type enum, cached name).
 *
 * @typeParam T  Optional metadata type stored alongside each baseline.
 */
export class BaselineStore<T = undefined> {
  private store = new Map<number, { snapshot: Uint8Array; meta: T | undefined }>();

  /** Store or replace the baseline snapshot (and optional metadata) for an entity. */
  set(netID: number, snapshot: Uint8Array, meta?: T): void {
    this.store.set(netID, { snapshot, meta });
  }

  /** Retrieve the baseline snapshot and metadata for an entity, or undefined if not stored. */
  get(netID: number): { snapshot: Uint8Array; meta: T | undefined } | undefined {
    return this.store.get(netID);
  }

  /** Remove the baseline for an entity (e.g. on removal or AoI exit). */
  delete(netID: number): void {
    this.store.delete(netID);
  }

  /** Remove all stored baselines (e.g. on disconnect or world change). */
  clear(): void {
    this.store.clear();
  }
}

// ---------------------------------------------------------------------------
// String decoding helpers
// ---------------------------------------------------------------------------

/**
 * Decode a length-prefixed string where the length is a single uint8 byte.
 * Wire layout: len(1) data(len).
 */
export function decodeLengthPrefixedStringU8(data: Uint8Array): string {
  if (data.length < 1) return "";
  const len = data[0];
  if (len === 0 || 1 + len > data.length) return "";
  return new TextDecoder().decode(data.subarray(1, 1 + len));
}

/**
 * Decode a length-prefixed string where the length is a uint16 (big-endian).
 * Wire layout: len(2) data(len).
 */
export function decodeLengthPrefixedStringU16(data: Uint8Array): string {
  if (data.length < 2) return "";
  const len = readUint16(data, 0);
  if (len === 0 || 2 + len > data.length) return "";
  return new TextDecoder().decode(data.subarray(2, 2 + len));
}
