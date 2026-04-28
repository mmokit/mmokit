import type { AnyEntity, PlayerEntity } from "../sdk/entities.js";
import { type ClockSync, newClockSync } from "./clockSync.js";

export interface EntitySample {
  worldX: number;
  worldY: number;
  velX: number;
  velY: number;
  rotation: number;    // computed from angle or vel direction
  // Per-entity ClusterClock-aligned stamp from the server (Phase K of
  // the replication-timeline redesign). Rings lerp on this axis so
  // cross-cell handoff produces adjacent-tick samples.
  producedAtMs: number;
}

/**
 * Entity with interpolation fields added on top of the SDK type. Drops the
 * literal entityType narrowing on PlayerEntity so the same shape can hold a
 * BotEntity (or any future kind) — the structural fields are identical
 * across all kinds in this example.
 */
export interface ClientEntity extends Omit<PlayerEntity, "entityType"> {
  entityType: AnyEntity["entityType"];
  prevX: number;
  prevY: number;
  isReplica: boolean;
  isGhost: boolean;
  // Interpolation ring — authoritative snapshots with server timestamps.
  samples: EntitySample[];
  // Last rendered values. Used by drawEntity and as fallback rotation.
  renderX: number;
  renderY: number;
  renderRot: number;
}

export interface CellInfo {
  cellX: number;
  cellY: number;
  depth: number;
  size: number;
  originX: number;
  originY: number;
  nodeId: string;
}

export interface GameState {
  client: import("../sdk/client.js").BasicClient | null;
  playerNetID: number;
  entities: Map<number, ClientEntity>;
  tick: number;
  lastTickTime: number;

  // Server-provided tick config (set by SE_SERVER_CONFIG).
  tickRate: number;
  tickMs: number;
  dt: number;

  // Debug cell topology (from CellTopologyMsg, empty when DebugTopology is off).
  cells: CellInfo[];
  // AoI radius from SE_DEBUG_INFO (0 when DebugTopology is off / before
  // first SE_DEBUG_INFO arrives). Used by the renderer to draw the
  // viewer's AoI circle.
  aoiRadius: number;
  debugAvailable: boolean; // true when server sends topology data
  debugVisible: boolean; // user toggle for debug overlay

  // Camera.
  camX: number;
  camY: number;

  // Input / move target.
  inputSeq: number;
  moveTargetX: number;
  moveTargetY: number;
  moveTargetActive: boolean;

  // FPS counter.
  fps: number;
  frameCount: number;
  lastFpsTime: number;

  // Clock sync for snapshot interpolation.
  clockSync: ClockSync;
}

export function setTickRate(rate: number): void {
  state.tickRate = rate;
  state.tickMs = 1000 / rate;
  state.dt = state.tickMs / 1000;
}

export const state: GameState = {
  client: null,
  playerNetID: 0,
  entities: new Map(),
  tick: 0,
  lastTickTime: 0,
  tickRate: 0,
  tickMs: 0,
  dt: 0,
  cells: [],
  aoiRadius: 0,
  debugAvailable: false,
  debugVisible: true,
  camX: 0,
  camY: 0,
  inputSeq: 0,
  moveTargetX: 0,
  moveTargetY: 0,
  moveTargetActive: false,
  fps: 0,
  frameCount: 0,
  lastFpsTime: 0,
  clockSync: newClockSync(),
};
