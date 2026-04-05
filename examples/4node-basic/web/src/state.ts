import type { PlayerEntity } from "../sdk/entities.js";

/** Entity with interpolation fields added on top of the SDK type. */
export interface ClientEntity extends PlayerEntity {
  prevX: number;
  prevY: number;
  isReplica: boolean;
  isGhost: boolean;
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
  viewerX: number;
  viewerY: number;

  // Server-provided tick config (set by SE_SERVER_CONFIG).
  tickRate: number;
  tickMs: number;
  dt: number;

  // Grid metadata (from spawn message).
  gridW: number;
  gridH: number;
  cellSize: number;

  // Cell topology (from topology message).
  cells: CellInfo[];

  // Camera.
  camX: number;
  camY: number;

  // Input / move target.
  inputSeq: number;
  moveTargetX: number;
  moveTargetY: number;
  moveTargetActive: boolean;

  // Client prediction.
  predictedX: number;
  predictedY: number;
  predictionActive: boolean;
  predictionStartTime: number;

  // FPS counter.
  lastFrameTime: number;
  fps: number;
  frameCount: number;
  lastFpsTime: number;
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
  viewerX: 0,
  viewerY: 0,
  tickRate: 0,
  tickMs: 0,
  dt: 0,
  gridW: 2,
  gridH: 2,
  cellSize: 2000,
  cells: [],
  camX: 0,
  camY: 0,
  inputSeq: 0,
  moveTargetX: 0,
  moveTargetY: 0,
  moveTargetActive: false,
  predictedX: 0,
  predictedY: 0,
  predictionActive: false,
  predictionStartTime: 0,
  lastFrameTime: 0,
  fps: 0,
  frameCount: 0,
  lastFpsTime: 0,
};
