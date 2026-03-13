import type { WSTransport } from "./transport";
import type { AbilityCastEvent, ClientEntity, Explosion, RangeRingEvent, Toast } from "./types";

export interface ItemDef {
  id: number;
  name: string;
  massPerUnit: number;
  sellPrice: number;
  category: number;
  equipSlot: number;
  buyPrice: number;
}

export interface EquipmentState {
  weapon1: number;
  weapon2: number;
  shield: number;
  thruster: number;
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

  // Docking
  isDocked: boolean;
  isDockingInProgress: boolean;
  dockingProgress: number;
  dockingTotalTime: number;
  dockingStationId: number;

  // Targeting/Input
  targetId: number; // mining target
  lockTargetId: number; // combat lock target
  lockProgress: number; // 0-1 from server
  serverLockTargetId: number; // last lock target confirmed by server
  mouseX: number;
  mouseY: number;
  keys: Record<string, boolean>;
  chatMode: boolean;

  // Combat
  abilityPresses: number; // bitmask of abilities pressed this frame
  abilityCooldowns: Map<number, { remaining: number; total: number }>;
  moveTarget: { x: number; y: number; active: boolean };
  beingLockedById: number; // net ID of entity locking us (most progressed)
  beingLockedProgress: number; // 0-1 lock progress

  // Cargo/Economy/Equipment
  cargoPanelOpen: boolean;
  jettisonRequest: number;
  toasts: Toast[];
  equipment: EquipmentState;

  // Inventory
  itemDefs: Map<number, ItemDef>;
  bankItems: Map<number, number>; // itemID -> quantity
  bankTotalMass: number;
  dockedCargoItems: Map<number, number>; // cargo from BankContentsMsg
  dockedCargoMass: number;
  dockedMaxCargoMass: number;
  bankMaxMass: number;
  bankPanelOpen: boolean;

  // Loot popup
  lootCrateId: number; // net ID of crate whose popup is open (0 = closed)
  pendingLootCrateId: number; // net ID of crate we're moving toward (0 = none)

  // Ping
  pingMs: number;

  // UI
  escMenuOpen: boolean;

  // Marketplace
  marketPanelOpen: boolean;
  marketTab: "browse" | "sell" | "myorders";
  marketSelectedItemId: number;
  marketSellSelectedItemId: number;
  marketSearchQuery: string;
  marketOrderBook: {
    itemId: number;
    sellLevels: { price: number; quantity: number; orderCount: number }[];
    buyLevels: { price: number; quantity: number; orderCount: number }[];
  } | null;
  marketMyOrders: {
    orderId: number;
    itemId: number;
    isBuy: boolean;
    pricePerUnit: number;
    quantity: number;
    origQuantity: number;
    createdAt: number;
    expiresAt: number;
  }[];
  marketOrderFormSide: "buy" | "sell";
  marketOrderFormPrice: string;
  marketOrderFormQty: string;
  marketPendingRequestId: number;
  marketRequestCounter: number;

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

    isDocked: false,
    isDockingInProgress: false,
    dockingProgress: 0,
    dockingTotalTime: 0,
    dockingStationId: 0,

    targetId: 0,
    lockTargetId: 0,
    lockProgress: 0,
    serverLockTargetId: 0,
    mouseX: 0,
    mouseY: 0,
    keys: {},
    chatMode: false,

    abilityPresses: 0,
    abilityCooldowns: new Map(),
    moveTarget: { x: 0, y: 0, active: false },
    beingLockedById: 0,
    beingLockedProgress: 0,

    cargoPanelOpen: true,
    jettisonRequest: 0,
    toasts: [],
    equipment: { weapon1: 0, weapon2: 0, shield: 0, thruster: 0 },

    itemDefs: new Map(),
    bankItems: new Map(),
    bankTotalMass: 0,
    bankMaxMass: 0,
    dockedCargoItems: new Map(),
    dockedCargoMass: 0,
    dockedMaxCargoMass: 0,
    bankPanelOpen: false,

    lootCrateId: 0,
    pendingLootCrateId: 0,

    pingMs: 0,

    escMenuOpen: false,

    // Marketplace
    marketPanelOpen: false,
    marketTab: "browse" as const,
    marketSelectedItemId: 0,
    marketSellSelectedItemId: 0,
    marketSearchQuery: "",
    marketOrderBook: null,
    marketMyOrders: [],
    marketOrderFormSide: "buy" as const,
    marketOrderFormPrice: "",
    marketOrderFormQty: "",
    marketPendingRequestId: 0,
    marketRequestCounter: 0,

    explosions: [],

    abilityEffectQueue: [],
    rangeRingQueue: [],
    screenShake: null,
  };
}
