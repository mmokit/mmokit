/**
 * Binary delta world update decoder for the slither game.
 *
 * Wire format matches pkg/quantize/wireformat.go.
 * All multi-byte integers are big-endian.
 */

import type { SegmentPos } from "./state";
import type { WorldUpdateData } from "./network";

// Entity type constants matching server-side KindPlayerSnake, KindBotSnake, KindNaturalFood, KindDeathFood.
const KIND_PLAYER_SNAKE = 0;
const KIND_BOT_SNAKE = 1;
const KIND_NATURAL_FOOD = 2;
const KIND_DEATH_FOOD = 3;

// Quantization scales matching server-side replicator constants.
const VEL_SPEED_SCALE = 500.0;
const VEL_MASS_SCALE = 10000.0;

// Per-entity baseline snapshot stored on the client for delta decoding.
interface EntityBaseline {
  type: number;
  snapshot: Uint8Array;
  name: string; // from InitialData (only set once)
}

// Snake snapshot field layout: {4, 4, 2, 2, 2, 1, 1, -1}
// Food snapshot field layout:  {4, 4, 1, 1}
const SNAKE_FIELD_SIZES = [4, 4, 2, 2, 2, 1, 1]; // 7 fixed fields + var tail
const SNAKE_FIXED_SIZE = 16;
const SNAKE_FIELD_COUNT = 8; // 7 fixed + 1 var tail
const SNAKE_BITMASK_SIZE = 1; // ceil(8 / 8)

const FOOD_FIELD_SIZES = [4, 4, 1, 1];
const FOOD_FIXED_SIZE = 10;
const FOOD_FIELD_COUNT = 4;
const FOOD_BITMASK_SIZE = 1;

/**
 * DeltaWorldDecoder maintains per-entity baselines and decodes binary
 * delta world update frames into WorldUpdateData.
 */
export class DeltaWorldDecoder {
  private baselines = new Map<number, EntityBaseline>();

  /**
   * Decode a binary delta world update frame.
   */
  decode(data: Uint8Array): WorldUpdateData {
    const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
    let pos = 0;

    // Header (16 bytes).
    const tick = view.getUint32(pos); pos += 4;
    const _seq = view.getUint32(pos); pos += 4;
    const fullCount = view.getUint16(pos); pos += 2;
    const deltaCount = view.getUint16(pos); pos += 2;
    const removedCount = view.getUint16(pos); pos += 2;
    const exitedCount = view.getUint16(pos); pos += 2;

    const snakes: WorldUpdateData["snakes"] = [];
    const foods: WorldUpdateData["foods"] = [];

    // Full entities.
    for (let i = 0; i < fullCount; i++) {
      const netID = view.getUint32(pos); pos += 4;
      const entityType = data[pos]; pos += 1;
      const snapLen = view.getUint16(pos); pos += 2;
      const snapshot = data.slice(pos, pos + snapLen); pos += snapLen;
      const initLen = view.getUint16(pos); pos += 2;
      let initialData: Uint8Array | null = null;
      if (initLen > 0) {
        initialData = data.slice(pos, pos + initLen); pos += initLen;
      }

      // Store baseline.
      let name = "";
      const existing = this.baselines.get(netID);
      if (existing) {
        name = existing.name; // preserve name from earlier InitialData
      }
      if (initialData) {
        name = decodeLengthPrefixedString(initialData);
      }
      this.baselines.set(netID, { type: entityType, snapshot, name });

      // Decode snapshot to game data.
      this.decodeEntity(netID, entityType, snapshot, name, snakes, foods);
    }

    // Delta entities.
    for (let i = 0; i < deltaCount; i++) {
      const netID = view.getUint32(pos); pos += 4;
      const entityType = data[pos]; pos += 1;
      const deltaLen = view.getUint16(pos); pos += 2;
      const deltaData = data.subarray(pos, pos + deltaLen); pos += deltaLen;

      const baseline = this.baselines.get(netID);
      if (!baseline) {
        continue;
      }

      // Apply delta to baseline.
      const updated = applyDelta(entityType, baseline.snapshot, deltaData);
      baseline.snapshot = updated;

      this.decodeEntity(netID, entityType, updated, baseline.name, snakes, foods);
    }

    // Removed IDs.
    const removed: number[] = [];
    for (let i = 0; i < removedCount; i++) {
      const id = view.getUint32(pos); pos += 4;
      removed.push(id);
      this.baselines.delete(id);
    }

    // Exited IDs.
    for (let i = 0; i < exitedCount; i++) {
      const id = view.getUint32(pos); pos += 4;
      removed.push(id); // client treats exits same as removals
      this.baselines.delete(id);
    }

    return { tick, snakes, foods, removed };
  }

  private decodeEntity(
    netID: number, entityType: number, snapshot: Uint8Array, name: string,
    snakes: WorldUpdateData["snakes"], foods: WorldUpdateData["foods"],
  ) {
    if (entityType === KIND_PLAYER_SNAKE || entityType === KIND_BOT_SNAKE) {
      snakes.push(decodeSnakeSnapshot(netID, snapshot, name));
    } else if (entityType === KIND_NATURAL_FOOD || entityType === KIND_DEATH_FOOD) {
      foods.push(decodeFoodSnapshot(netID, snapshot));
    }
  }
}

// --- Dequantization ---

function unAngle(q: number): number {
  return (q / 65535) * 2 * Math.PI - Math.PI;
}

function unVel(q: number, scale: number): number {
  // q is int16 stored as uint16 in big-endian
  if (q > 32767) q -= 65536; // convert to signed
  return (q / 32767) * scale;
}

function unNorm(q: number): number {
  return q / 255;
}

// --- Snapshot readers ---

function readUint16(data: Uint8Array, offset: number): number {
  return (data[offset] << 8) | data[offset + 1];
}

function readFloat32(data: Uint8Array, offset: number): number {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  return view.getFloat32(offset);
}

function decodeSnakeSnapshot(netID: number, snap: Uint8Array, name: string): WorldUpdateData["snakes"][0] {
  let off = 0;
  const headX = readFloat32(snap, off); off += 4;
  const headY = readFloat32(snap, off); off += 4;
  const angle = unAngle(readUint16(snap, off)); off += 2;
  const speed = unVel(readUint16(snap, off), VEL_SPEED_SCALE); off += 2;
  const mass = unVel(readUint16(snap, off), VEL_MASS_SCALE); off += 2;
  const skinID = snap[off]; off += 1;
  const flags = snap[off]; off += 1;
  const boosting = (flags & 1) !== 0;

  // Variable tail: uint16 byte-length prefix, then segment data (8 bytes each).
  const segments: SegmentPos[] = [];
  let length = 0;
  if (off + 2 <= snap.length) {
    const tailByteLen = readUint16(snap, off); off += 2;
    const segCount = Math.floor(tailByteLen / 8); // 8 bytes per segment (2x float32)
    length = segCount;
    for (let i = 0; i < segCount && off + 8 <= snap.length; i++) {
      segments.push({
        x: readFloat32(snap, off),
        y: readFloat32(snap, off + 4),
      });
      off += 8;
    }
  }

  return { id: netID, headX, headY, angle, speed, mass, skinID, length, boosting, name, segments };
}

function decodeFoodSnapshot(netID: number, snap: Uint8Array): WorldUpdateData["foods"][0] {
  const x = readFloat32(snap, 0);
  const y = readFloat32(snap, 4);
  const value = unNorm(snap[8]);
  const color = snap[9];
  return { id: netID, x, y, value, color };
}

// --- Delta application ---

function applyDelta(entityType: number, baseline: Uint8Array, delta: Uint8Array): Uint8Array {
  const isSnake = entityType === KIND_PLAYER_SNAKE || entityType === KIND_BOT_SNAKE;
  const fieldSizes = isSnake ? SNAKE_FIELD_SIZES : FOOD_FIELD_SIZES;
  const fixedSize = isSnake ? SNAKE_FIXED_SIZE : FOOD_FIXED_SIZE;
  const fieldCount = isSnake ? SNAKE_FIELD_COUNT : FOOD_FIELD_COUNT;
  const bitmaskSize = isSnake ? SNAKE_BITMASK_SIZE : FOOD_BITMASK_SIZE;
  const hasVarTail = isSnake;

  // Read bitmask.
  let dpos = bitmaskSize; // delta read cursor past bitmask

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
    const tailIdx = fieldSizes.length; // logical field index
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

function decodeLengthPrefixedString(data: Uint8Array): string {
  if (data.length < 1) return "";
  const len = data[0];
  if (len === 0 || 1 + len > data.length) return "";
  return new TextDecoder().decode(data.subarray(1, 1 + len));
}
