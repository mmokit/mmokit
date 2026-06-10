import type { SpaceClient } from "../sdk/index.js";
import type { AbilityCastEvent, ClientEntity, Explosion, RangeRingEvent, Toast } from "./types";
import { newClockSync, type ClockSync } from "../sdk/_core/clock-sync.js";

// Settlement currency item ID — must match server GameConfig.SettlementCurrencyID
export const SETTLEMENT_CURRENCY_ID = 1;

export interface ItemDef {
  id: number;
  name: string;
  massPerUnit: number;
  category: number;
  equipSlot: number;
}

export interface MapStation {
  cellX: number;
  cellY: number;
  localX: number;
  localY: number;
  name: string;
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
  client: SpaceClient | null;
  connected: boolean;
  spawnedOnce: boolean;

  // Game
  myEntityId: number;
  gridCellsX: number;
  gridCellsY: number;
  inputSeq: number;
  tickCount: number;
  fps: number;
  lastFpsTime: number;
  frameCount: number;
  /** Server wall-clock offset estimator for snapshot interpolation. */
  clockSync: ClockSync;

  // Entities
  entities: Map<number, ClientEntity>;

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
  /**
   * Currently selected entity (0 = nothing selected). Mirrors the server's
   * Selection.EntityNetID. Set via SelectTarget input (Task 13). Drives the
   * combat range checks, ability-bar visuals, and selection-ring overlay.
   */
  selectedNetID: number;
  mouseX: number;
  mouseY: number;
  keys: Record<string, boolean>;
  chatMode: boolean;

  // Combat
  // Note: ability presses are dispatched immediately by input.ts's
  // aim-state machine (handleAbilityPress); there's no per-tick
  // batched abilityPresses bitmask anymore.
  abilityCooldowns: Map<number, { remaining: number; total: number }>;
  moveTarget: { x: number; y: number; active: boolean };
  rightMouseDown: boolean;
  aimingSlot: number; // 0 = not aiming; 1..6 = currently aiming this slot index
  quickcastMask: number; // bitmask: bit N = slot N+1 is quickcast (skip aim-confirm)
  cursorWorldX: number; // live cursor world coords (updated by input.ts each mousemove)
  cursorWorldY: number;
  /**
   * Active channel slot — set when a SkillshotChannel ability begins, cleared
   * when channelEndsAt elapses (or the server cuts the channel short). Encoded
   * as 1..6 so 0 == "not channeling", matching aimingSlot's convention.
   */
  channelingSlot: number;
  /** performance.now() ms when the active channel auto-ends client-side. */
  channelEndsAt: number;
  /**
   * LOS-clip point for the current channel beam — set by the BeamClip
   * server event when the server's LOS gate suppresses a damage tick.
   * The aim indicator clamps the beam visual to this point so the beam
   * doesn't draw through walls. Decays after BEAM_CLIP_TTL_MS without a
   * fresh event, falling back to full-range rendering.
   */
  beamClipExpiresAt: number;
  beamClipX: number;
  beamClipY: number;

  // Cargo/Economy/Equipment
  cargoPanelOpen: boolean;
  jettisonRequest: number;
  toasts: Toast[];
  equipment: EquipmentState;
  cargoItems: Map<number, number>;  // itemID -> quantity (from PlayerOwnStateMsg)
  cargoMass: number;
  maxCargoMass: number;

  // Inventory
  itemDefs: Map<number, ItemDef>;
  bankItems: Map<number, number>; // itemID -> quantity
  bankTotalMass: number;
  dockedCargoItems: Map<number, number>; // cargo from BankContentsMsg
  dockedCargoMass: number;
  dockedMaxCargoMass: number;
  bankMaxMass: number;
  bankPanelOpen: boolean;
  currencyBalances: Record<number, number>;

  // refreshBank fires the typed-op bank request and merges the response
  // into state. Wired in network.ts.connect; UI code calls it instead of
  // posting BankRequest as a fire-and-forget input. Initialized to a
  // no-op so callers don't need to null-check before connect resolves.
  refreshBank: () => void;

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

  // Cell map
  cellMapOpen: boolean;
  mapStations: MapStation[];

  // Cell topology (from SE_CELL_TOPOLOGY, null when server doesn't send it)
  cellTopology: CellInfo[] | null;

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

    client: null,
    connected: false,
    spawnedOnce: false,

    myEntityId: 0,
    gridCellsX: 0,
    gridCellsY: 0,
    inputSeq: 0,
    tickCount: 0,
    fps: 0,
    lastFpsTime: performance.now(),
    frameCount: 0,
    clockSync: newClockSync(),

    entities: new Map(),

    isDead: false,
    deathTime: 0,
    killerEntityId: 0,

    isDocked: false,
    isDockingInProgress: false,
    dockingProgress: 0,
    dockingTotalTime: 0,
    dockingStationId: 0,

    targetId: 0,
    selectedNetID: 0,
    mouseX: 0,
    mouseY: 0,
    keys: {},
    chatMode: false,

    abilityCooldowns: new Map(),
    moveTarget: { x: 0, y: 0, active: false },
    rightMouseDown: false,
    aimingSlot: 0,
    quickcastMask: parseInt(localStorage.getItem("skillshot.quickcast") ?? "0", 10),
    cursorWorldX: 0,
    cursorWorldY: 0,
    channelingSlot: 0,
    channelEndsAt: 0,
    beamClipExpiresAt: 0,
    beamClipX: 0,
    beamClipY: 0,

    cargoPanelOpen: true,
    jettisonRequest: 0,
    toasts: [],
    equipment: { weapon1: 0, weapon2: 0, shield: 0, thruster: 0 },
    cargoItems: new Map(),
    cargoMass: 0,
    maxCargoMass: 100,

    itemDefs: new Map(),
    bankItems: new Map(),
    bankTotalMass: 0,
    bankMaxMass: 0,
    dockedCargoItems: new Map(),
    dockedCargoMass: 0,
    dockedMaxCargoMass: 0,
    bankPanelOpen: false,
    currencyBalances: {},
    refreshBank: () => {}, // wired by network.ts.connect

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

    cellMapOpen: false,
    mapStations: [],

    cellTopology: null,

    explosions: [],

    abilityEffectQueue: [],
    rangeRingQueue: [],
    screenShake: null,
  };
}
