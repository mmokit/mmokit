import type { WSTransport } from "./transport";
import type { AbilityCastEvent, ClientEntity, Explosion, RangeRingEvent, Toast } from "./types";

export interface ItemDef {
  id: number;
  name: string;
  massPerUnit: number;
  sellPrice: number;
}

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
  targetId: number; // mining target
  lockTargetId: number; // combat lock target
  lockProgress: number; // 0-1 from server
  mouseX: number;
  mouseY: number;
  keys: Record<string, boolean>;
  chatMode: boolean;

  // Mining
  miningActive: boolean;

  // Combat
  abilityPresses: number; // bitmask of abilities pressed this frame
  abilityCooldowns: Map<number, { remaining: number; total: number }>;
  moveTarget: { x: number; y: number; active: boolean };
  beingLockedById: number; // net ID of entity locking us (most progressed)
  beingLockedProgress: number; // 0-1 lock progress

  // Cargo/Economy
  cargoPanelOpen: boolean;
  jettisonRequest: number;
  toasts: Toast[];

  // Inventory
  itemDefs: Map<number, ItemDef>;
  bankItems: Map<number, number>; // itemID -> quantity
  bankTotalMass: number;
  bankMaxMass: number;
  bankPanelOpen: boolean;

  // UI
  escMenuOpen: boolean;

  // Particles
  explosions: Explosion[];

  // Ability effects
  abilityEffectQueue: AbilityCastEvent[];
  rangeRingQueue: RangeRingEvent[];
  screenShake: { intensity: number; startTime: number; duration: number } | null;
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
    lockTargetId: 0,
    lockProgress: 0,
    mouseX: 0,
    mouseY: 0,
    keys: {},
    chatMode: false,

    miningActive: false,
    abilityPresses: 0,
    abilityCooldowns: new Map(),
    moveTarget: { x: 0, y: 0, active: false },
    beingLockedById: 0,
    beingLockedProgress: 0,

    cargoPanelOpen: false,
    jettisonRequest: 0,
    toasts: [],

    itemDefs: new Map(),
    bankItems: new Map(),
    bankTotalMass: 0,
    bankMaxMass: 0,
    bankPanelOpen: false,

    escMenuOpen: false,

    explosions: [],

    abilityEffectQueue: [],
    rangeRingQueue: [],
    screenShake: null,
  };
}
