/**
 * Binary delta world update decoder for the space game.
 *
 * Wire format matches pkg/quantize/wireformat.go.
 * All multi-byte integers are big-endian.
 */

import { EntityType } from "@gen/game_pb.js";

// Entity type constants matching server-side component.TypeXxx.
const TYPE_SHIP = 0;
const TYPE_ASTEROID = 1;
// const TYPE_PROJECTILE = 2;
const TYPE_STATION = 3;
const TYPE_LOOT_CRATE = 4;
const TYPE_NPC = 5;

// Quantization scales matching server-side nethandler_shared.go constants.
const Q_VEL_SCALE = 2000;
const Q_SIZE_SCALE = 500;

// Per-entity baseline snapshot stored on the client for delta decoding.
interface EntityBaseline {
  type: number;
  snapshot: Uint8Array;
  pilotName: string; // from InitialData (ships only, set once)
}

// Snapshot field layouts matching server SnapshotLayout() per entity type.
// Base fields (10): relX(4), relY(4), vx(2), vy(2), rotation(2), radius(2), width(2), height(2), lockedByID(4), lockedByProgress(1)
// Positions are Float32 (not quantized) to avoid cell-boundary shift artifacts.
const BASE_FIELD_SIZES = [4, 4, 2, 2, 2, 2, 2, 2, 4, 1];
const BASE_FIXED_SIZE = 25;
const BASE_FIELD_COUNT = 10;

// Ship: base + healthCur(2), healthMax(2), shieldCur(2), shieldMax(2), flags(1), miningTargetId(4), combatTargetId(4)
const SHIP_FIELD_SIZES = [...BASE_FIELD_SIZES, 2, 2, 2, 2, 1, 4, 4];
const SHIP_FIXED_SIZE = BASE_FIXED_SIZE + 17;
const SHIP_FIELD_COUNT = 17;

// Asteroid: base + itemId(4), resourceRemaining(4)
const ASTEROID_FIELD_SIZES = [...BASE_FIELD_SIZES, 4, 4];
const ASTEROID_FIXED_SIZE = BASE_FIXED_SIZE + 8;
const ASTEROID_FIELD_COUNT = 12;

// NPC: base + healthCur(2), healthMax(2), shieldCur(2), shieldMax(2)
const NPC_FIELD_SIZES = [...BASE_FIELD_SIZES, 2, 2, 2, 2];
const NPC_FIXED_SIZE = BASE_FIXED_SIZE + 8;
const NPC_FIELD_COUNT = 14;

// Station: base only
const STATION_FIELD_SIZES = [...BASE_FIELD_SIZES];
const STATION_FIXED_SIZE = BASE_FIXED_SIZE;
const STATION_FIELD_COUNT = 10;

// LootCrate: base + var tail (-1)
const LOOTCRATE_FIELD_SIZES = [...BASE_FIELD_SIZES]; // fixed portion only
const LOOTCRATE_FIXED_SIZE = BASE_FIXED_SIZE;
const LOOTCRATE_FIELD_COUNT = 11; // 10 fixed + 1 var tail

// Map entity type to EntityType enum values (same numeric values, but using the enum for type compat).
const ENTITY_TYPE_MAP: Record<number, EntityType> = {
  [TYPE_SHIP]: EntityType.SHIP,
  [TYPE_ASTEROID]: EntityType.ASTEROID,
  [TYPE_STATION]: EntityType.STATION,
  [TYPE_LOOT_CRATE]: EntityType.LOOT_CRATE,
  [TYPE_NPC]: EntityType.NPC,
};

export interface DeltaWorldUpdate {
  tick: number;
  entities: any[];       // EntityState-compatible objects
  removedIds: number[];
  killedIds: number[];   // empty (kills sent via separate SE_PLAYER_DIED event)
  /** Full-precision viewer position from frame header (float32, not quantized). */
  viewerX: number;
  viewerY: number;
}

/**
 * DeltaWorldDecoder maintains per-entity baselines and decodes binary
 * delta world update frames into DeltaWorldUpdate.
 */
export class DeltaWorldDecoder {
  private baselines = new Map<number, EntityBaseline>();

  /** Clear all baselines. Call on cell change to discard stale state from the old node. */
  clear() {
    this.baselines.clear();
  }

  /**
   * Decode a binary delta world update frame.
   */
  decode(data: Uint8Array): DeltaWorldUpdate {
    const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
    let pos = 0;

    // Header (24 bytes).
    const tick = view.getUint32(pos); pos += 4;
    const _seq = view.getUint32(pos); pos += 4;
    const viewerX = view.getFloat32(pos); pos += 4;
    const viewerY = view.getFloat32(pos); pos += 4;
    const fullCount = view.getUint16(pos); pos += 2;
    const deltaCount = view.getUint16(pos); pos += 2;
    const removedCount = view.getUint16(pos); pos += 2;
    const exitedCount = view.getUint16(pos); pos += 2;

    const entities: any[] = [];

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
      let pilotName = "";
      const existing = this.baselines.get(netID);
      if (existing) {
        pilotName = existing.pilotName; // preserve name from earlier InitialData
      }
      if (initialData) {
        pilotName = decodeLengthPrefixedString(initialData);
      }
      this.baselines.set(netID, { type: entityType, snapshot, pilotName });

      // Decode snapshot to entity state.
      const ent = decodeEntityState(netID, entityType, snapshot, pilotName);
      if (ent) entities.push(ent);
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

      const ent = decodeEntityState(netID, entityType, updated, baseline.pilotName);
      if (ent) entities.push(ent);
    }

    // Removed IDs.
    const removedIds: number[] = [];
    for (let i = 0; i < removedCount; i++) {
      const id = view.getUint32(pos); pos += 4;
      removedIds.push(id);
      this.baselines.delete(id);
    }

    // Exited IDs (client treats exits same as removals).
    for (let i = 0; i < exitedCount; i++) {
      const id = view.getUint32(pos); pos += 4;
      removedIds.push(id);
      this.baselines.delete(id);
    }

    return { tick, entities, removedIds, killedIds: [], viewerX, viewerY };
  }
}

// --- Dequantization ---

function readUint16(data: Uint8Array, off: number): number {
  return (data[off] << 8) | data[off + 1];
}

function readInt16(data: Uint8Array, off: number): number {
  const v = (data[off] << 8) | data[off + 1];
  return v > 32767 ? v - 65536 : v;
}

function readUint32(data: Uint8Array, off: number): number {
  return (data[off] << 24 | data[off + 1] << 16 | data[off + 2] << 8 | data[off + 3]) >>> 0;
}

function readFloat32(data: Uint8Array, off: number): number {
  const view = new DataView(data.buffer, data.byteOffset + off, 4);
  return view.getFloat32(0);
}

function unRel(q: number, halfRange: number): number {
  return (q / 32767) * halfRange;
}

function unVel(q: number, scale: number): number {
  return (q / 32767) * scale; // q is already signed int16
}

function unAngle(q: number): number {
  return (q / 65535) * 2 * Math.PI - Math.PI; // q is unsigned uint16
}

function unNorm(q: number): number {
  return q / 255;
}

// --- Snapshot decoding ---

/**
 * Decode base fields shared by all entity types from a snapshot.
 * Returns { off, x, y, vx, vy, rotation, radius, width, height, lockedById, lockedByProgress }.
 */
function decodeBaseFields(snap: Uint8Array) {
  let off = 0;
  // Positions are cell-relative Float32 — no quantization, no cell-boundary shift.
  const x = readFloat32(snap, off); off += 4;
  const y = readFloat32(snap, off); off += 4;
  const vx = unVel(readInt16(snap, off), Q_VEL_SCALE); off += 2;
  const vy = unVel(readInt16(snap, off), Q_VEL_SCALE); off += 2;
  const rotation = unAngle(readUint16(snap, off)); off += 2;
  const radius = unVel(readInt16(snap, off), Q_SIZE_SCALE); off += 2;
  const width = unVel(readInt16(snap, off), Q_SIZE_SCALE); off += 2;
  const height = unVel(readInt16(snap, off), Q_SIZE_SCALE); off += 2;
  const lockedById = readUint32(snap, off); off += 4;
  const lockedByProgress = unNorm(snap[off]); off += 1;
  return { off, x, y, vx, vy, rotation, radius, width, height, lockedById, lockedByProgress };
}

/**
 * Decode a full entity snapshot into an EntityState-compatible plain object.
 */
function decodeEntityState(
  netID: number,
  entityType: number,
  snapshot: Uint8Array,
  pilotName: string,
): any | null {
  const enumType = ENTITY_TYPE_MAP[entityType];
  if (enumType === undefined) return null;

  const base = decodeBaseFields(snapshot);
  let off = base.off;

  // Build the common entity state object.
  const state: any = {
    id: netID,
    entityType: enumType,
    x: base.x,
    y: base.y,
    vx: base.vx,
    vy: base.vy,
    rotation: base.rotation,
    radius: base.radius,
    width: base.width,
    height: base.height,
    lockedById: base.lockedById,
    lockedByProgress: base.lockedByProgress,
    typeData: { case: undefined, value: undefined },
  };

  switch (entityType) {
    case TYPE_SHIP: {
      const healthCur = readUint16(snapshot, off); off += 2;
      const healthMax = readUint16(snapshot, off); off += 2;
      const shieldCur = readUint16(snapshot, off); off += 2;
      const shieldMax = readUint16(snapshot, off); off += 2;
      const flags = snapshot[off]; off += 1;
      const miningTargetId = readUint32(snapshot, off); off += 4;
      const _combatTargetId = readUint32(snapshot, off); off += 4;

      const miningActive = (flags & 0x03) !== 0; // bit0 or bit1
      const miningBeamMask = flags & 0x03;

      state.typeData = {
        case: "ship" as const,
        value: {
          combat: {
            health: healthCur,
            maxHealth: healthMax,
            shield: shieldCur,
            maxShield: shieldMax,
            statusEffects: [],
          },
          pilotName,
          miningActive,
          miningTargetId,
          miningBeamMask,
        },
      };
      break;
    }

    case TYPE_ASTEROID: {
      const itemId = readUint32(snapshot, off); off += 4;
      const resourceRemaining = readFloat32(snapshot, off); off += 4;
      state.typeData = {
        case: "asteroid" as const,
        value: { itemId, resourceRemaining },
      };
      break;
    }

    case TYPE_NPC: {
      const healthCur = readUint16(snapshot, off); off += 2;
      const healthMax = readUint16(snapshot, off); off += 2;
      const shieldCur = readUint16(snapshot, off); off += 2;
      const shieldMax = readUint16(snapshot, off); off += 2;
      state.typeData = {
        case: "npc" as const,
        value: {
          combat: {
            health: healthCur,
            maxHealth: healthMax,
            shield: shieldCur,
            maxShield: shieldMax,
            statusEffects: [],
          },
        },
      };
      break;
    }

    case TYPE_STATION: {
      state.typeData = { case: "station" as const, value: {} };
      break;
    }

    case TYPE_LOOT_CRATE: {
      // Variable-length tail: uint16 byteLen, uint8 count, (uint32 itemId + uint32 qty)*count
      const cargoItems: { itemId: number; quantity: number }[] = [];
      if (off + 2 <= snapshot.length) {
        const tailByteLen = readUint16(snapshot, off); off += 2;
        if (tailByteLen > 0 && off < snapshot.length) {
          const count = snapshot[off]; off += 1;
          for (let i = 0; i < count && off + 8 <= snapshot.length; i++) {
            const itemId = readUint32(snapshot, off); off += 4;
            const quantity = readUint32(snapshot, off); off += 4;
            cargoItems.push({ itemId, quantity });
          }
        }
      }
      state.typeData = {
        case: "lootCrate" as const,
        value: { cargoItems },
      };
      break;
    }
  }

  return state;
}

// --- Delta application ---

function getFieldLayout(entityType: number): {
  fieldSizes: number[];
  fixedSize: number;
  fieldCount: number;
  hasVarTail: boolean;
} {
  switch (entityType) {
    case TYPE_SHIP:
      return { fieldSizes: SHIP_FIELD_SIZES, fixedSize: SHIP_FIXED_SIZE, fieldCount: SHIP_FIELD_COUNT, hasVarTail: false };
    case TYPE_ASTEROID:
      return { fieldSizes: ASTEROID_FIELD_SIZES, fixedSize: ASTEROID_FIXED_SIZE, fieldCount: ASTEROID_FIELD_COUNT, hasVarTail: false };
    case TYPE_NPC:
      return { fieldSizes: NPC_FIELD_SIZES, fixedSize: NPC_FIXED_SIZE, fieldCount: NPC_FIELD_COUNT, hasVarTail: false };
    case TYPE_STATION:
      return { fieldSizes: STATION_FIELD_SIZES, fixedSize: STATION_FIXED_SIZE, fieldCount: STATION_FIELD_COUNT, hasVarTail: false };
    case TYPE_LOOT_CRATE:
      return { fieldSizes: LOOTCRATE_FIELD_SIZES, fixedSize: LOOTCRATE_FIXED_SIZE, fieldCount: LOOTCRATE_FIELD_COUNT, hasVarTail: true };
    default:
      return { fieldSizes: BASE_FIELD_SIZES, fixedSize: BASE_FIXED_SIZE, fieldCount: BASE_FIELD_COUNT, hasVarTail: false };
  }
}

function applyDelta(entityType: number, baseline: Uint8Array, delta: Uint8Array): Uint8Array {
  const { fieldSizes, fixedSize, fieldCount, hasVarTail } = getFieldLayout(entityType);
  const bitmaskSize = Math.ceil(fieldCount / 8);

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

function decodeLengthPrefixedString(data: Uint8Array): string {
  if (data.length < 2) return "";
  const len = readUint16(data, 0);
  if (len === 0 || 2 + len > data.length) return "";
  return new TextDecoder().decode(data.subarray(2, 2 + len));
}
