import type { WSTransport } from "./transport";
import type { ClientEntity, Explosion, Toast } from "./types";

export interface GameState {
  // Login
  playerUsername: string;
  loggedIn: boolean;

  // Connection
  ws: WSTransport | null;
  connected: boolean;
  spawnedOnce: boolean;

  // Game
  myEntityId: number;
  worldWidth: number;
  worldHeight: number;
  inputSeq: number;
  tickCount: number;
  fps: number;
  lastFpsTime: number;
  frameCount: number;

  // Entities
  entities: Map<number, ClientEntity>;
  lastTickTime: number;

  // Death
  isDead: boolean;
  deathTime: number;
  killerEntityId: number;

  // Targeting/Input
  targetId: number;
  mouseX: number;
  mouseY: number;
  keys: Record<string, boolean>;
  chatMode: boolean;

  // Cargo/Economy
  cargoPanelOpen: boolean;
  jettisonRequest: number;
  sellRequest: boolean;
  sellPrices: number[];
  playerFlux: number;
  toasts: Toast[];

  // Particles
  explosions: Explosion[];
}

export function createInitialState(): GameState {
  return {
    playerUsername: "",
    loggedIn: false,

    ws: null,
    connected: false,
    spawnedOnce: false,

    myEntityId: 0,
    worldWidth: 10000,
    worldHeight: 10000,
    inputSeq: 0,
    tickCount: 0,
    fps: 0,
    lastFpsTime: performance.now(),
    frameCount: 0,

    entities: new Map(),
    lastTickTime: 0,

    isDead: false,
    deathTime: 0,
    killerEntityId: 0,

    targetId: 0,
    mouseX: 0,
    mouseY: 0,
    keys: {},
    chatMode: false,

    cargoPanelOpen: false,
    jettisonRequest: 0,
    sellRequest: false,
    sellPrices: [1, 3, 2, 5],
    playerFlux: 0,
    toasts: [],

    explosions: [],
  };
}
