import {
  SpaceClient,
  type AnyEntity,
  type DeltaWorldUpdate,
  type ShipEntity,
  type NPCEntity,
} from "../sdk/index.js";
import type {
  WorldUpdateMsg,
  PlayerSpawnedMsg,
  BankContentsMsg,
  TransferResultMsg,
  EquipResultMsg,
  DockingStateMsg,
  PlayerOwnStateMsg,
  MapDataMsg,
  MapStationInfo,
  CurrencyUpdateMsg,
} from "@gen/gamepb/game_pb.js";
import type {
  PongMsg,
  LoginRejectedMsg,
  PlayerDiedMsg,
  CellChangeMsg,
  CellTopologyMsg,
  CellInfo as PbCellInfo,
} from "@gen/enginepb/engine_pb.js";
import { MAX_CHAT_DISPLAY, CELL_SIZE } from "./constants";
import { updateEntityFromServer } from "./interpolation";
import { spawnExplosion } from "./effects/explosion";
import { SETTLEMENT_CURRENCY_ID, type GameState, type CellInfo } from "./state";
import { audio } from "./audio/audio-manager";
import { SoundId } from "./audio/sounds";

export interface NetworkCallbacks {
  onSpawned(): void;
  onDisconnected(): void;
  onLoginRejected(reason: string): void;
  onOriginChanged(sx: number, sy: number): void;
  onTopologyChanged(): void;
}

/**
 * Shift all stored entity positions by (dx, dy). Used during cell rebase when
 * the coordinate system origin changes. SDK entity interfaces are plain objects
 * so we can mutate their fields in place.
 */
function shiftEntityPositions(state: GameState, dx: number, dy: number): void {
  for (const ent of state.entities.values()) {
    // Mutate the current snapshot — ClientEntity.current is an AnyEntity
    // which is structurally a plain object (no readonly enforcement).
    (ent.current as { worldX: number; worldY: number }).worldX += dx;
    (ent.current as { worldX: number; worldY: number }).worldY += dy;
    ent.prevX += dx;
    ent.prevY += dy;
    ent.renderX += dx;
    ent.renderY += dy;
  }
}

function applyDeltaUpdate(state: GameState, update: DeltaWorldUpdate): void {
  state.tickCount = update.tick;
  state.lastTickTime = performance.now();

  // Merge entered + updated into a single "fresh" list for rebase detection
  // and interpolation updates.
  const fresh: AnyEntity[] = [...update.entered, ...update.updated];

  // After a cell change (cross-node transfer), entities were cleared.
  // The first world update from the new node has all-new entities.
  // Seed the player's prev/renderX from the pre-transfer camera position
  // (converted to the new cell's coordinate frame) so interpolation
  // starts from where the camera was, avoiding a snap-back.
  if (state.pendingCellRebase) {
    state.pendingCellRebase = false;

    // Clear stale entities from the old node and repopulate with the new node's
    // data in the same tick — no blank frame between clear and repopulate.
    state.entities.clear();
    for (const e of fresh) {
      updateEntityFromServer(state.entities, e);
    }

    const myEnt = state.entities.get(state.myEntityId);
    if (myEnt) {
      const dx = (state.preTransferCellX - state.originCellX) * CELL_SIZE;
      const dy = (state.preTransferCellY - state.originCellY) * CELL_SIZE;
      const prevX = state.preTransferCamX + dx;
      const prevY = state.preTransferCamY + dy;
      myEnt.prevX = prevX;
      myEnt.prevY = prevY;
      myEnt.prevRot = state.preTransferCamRot;
      myEnt.renderX = prevX;
      myEnt.renderY = prevY;
      myEnt.renderRot = state.preTransferCamRot;
    }
  } else {
    // Detect cell transfer: if the player's position jumps by more
    // than half a cell, rebase all existing entities BEFORE processing
    // the update. Round to nearest CELL_SIZE to get the pure coordinate
    // system shift, excluding the player's actual movement.
    let didRebase = false;
    if (state.myEntityId) {
      const myEnt = state.entities.get(state.myEntityId);
      if (myEnt) {
        for (const e of fresh) {
          if (e.netID === state.myEntityId) {
            const rawDx = e.worldX - myEnt.current.worldX;
            const rawDy = e.worldY - myEnt.current.worldY;
            if (Math.abs(rawDx) > CELL_SIZE / 2 || Math.abs(rawDy) > CELL_SIZE / 2) {
              const dx = Math.round(rawDx / CELL_SIZE) * CELL_SIZE;
              const dy = Math.round(rawDy / CELL_SIZE) * CELL_SIZE;
              shiftEntityPositions(state, dx, dy);
              didRebase = true;
            }
            break;
          }
        }
      }
    }

    for (const e of fresh) {
      updateEntityFromServer(state.entities, e);
    }

    // After rebase + update: anchor prev to current visual position to avoid
    // a one-frame velocity hitch.
    if (didRebase) {
      for (const ent of state.entities.values()) {
        ent.prevX = ent.renderX;
        ent.prevY = ent.renderY;
      }
    }
  }

  // Removed entities (despawned/killed) — spawn explosion for ships/NPCs.
  for (const id of update.removed) {
    const killed = state.entities.get(id);
    if (killed) {
      const t = killed.current.entityType;
      if (t === 0 || t === 5) {
        const e = killed.current as ShipEntity | NPCEntity;
        spawnExplosion(
          state.explosions,
          killed.renderX,
          killed.renderY,
          e.width,
          e.height,
          id === state.myEntityId,
        );
        audio.play(SoundId.Explosion);
      }
    }
    state.entities.delete(id);
    if (id === state.targetId) state.targetId = 0;
    if (id === state.lootCrateId) state.lootCrateId = 0;
    if (id === state.pendingLootCrateId) state.pendingLootCrateId = 0;
    if (id === state.lockTargetId) {
      state.lockTargetId = 0;
      state.lockProgress = 0;
    }
  }
  // Exited entities left our AoI but still exist on the server — drop from
  // local map without spawning explosions.
  for (const id of update.exited) {
    state.entities.delete(id);
    if (id === state.targetId) state.targetId = 0;
    if (id === state.lootCrateId) state.lootCrateId = 0;
    if (id === state.pendingLootCrateId) state.pendingLootCrateId = 0;
    if (id === state.lockTargetId) {
      state.lockTargetId = 0;
      state.lockProgress = 0;
    }
  }
}

export function connect(state: GameState, callbacks: NetworkCallbacks): void {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  const statusEl = document.getElementById("status")!;
  const chatMessagesEl = document.getElementById("chat-messages")!;

  let pingInterval: ReturnType<typeof setInterval> | null = null;

  const client = new SpaceClient({
    url: `${proto}//${window.location.host}/ws`,
    onOpen: () => {
      state.connected = true;
      statusEl.textContent = "Connected - Logging in...";
      statusEl.style.color = "#0f0";
      client.sendLogin({ username: state.playerUsername });
      pingInterval = setInterval(() => {
        if (state.client && state.connected) {
          state.client.sendPing({ clientTime: BigInt(Date.now()) });
        }
      }, 5000);
    },
    onClose: () => {
      if (pingInterval) {
        clearInterval(pingInterval);
        pingInterval = null;
      }
      state.connected = false;
      if (!state.spawnedOnce) {
        callbacks.onLoginRejected(state.loggedIn ? "Connection lost" : "");
        return;
      }
      statusEl.textContent = "Disconnected - Reconnecting...";
      statusEl.style.color = "#f00";
      state.myEntityId = 0;
      state.entities.clear();
      state.cellMapOpen = false;
      callbacks.onDisconnected();
      setTimeout(() => connect(state, callbacks), 2000);
    },
    onError: () => {
      /* handled in onClose */
    },
  });
  state.client = client;

  // --- Spawn / life cycle ---
  client.onPlayerSpawned((spawned: PlayerSpawnedMsg) => {
    state.myEntityId = spawned.yourEntityId;
    state.originCellX = spawned.originCellX;
    state.originCellY = spawned.originCellY;
    // Reset topology — server will send SE_CELL_TOPOLOGY if debug overlay is active.
    state.cellTopology = null;
    callbacks.onOriginChanged(spawned.originCellX, spawned.originCellY);
    if (spawned.itemDefs && spawned.itemDefs.length > 0) {
      state.itemDefs.clear();
      for (const def of spawned.itemDefs) {
        state.itemDefs.set(def.id, {
          id: def.id,
          name: def.name,
          massPerUnit: def.massPerUnit,
          sellPrice: def.sellPrice,
          category: def.category,
          equipSlot: def.equipSlot,
          buyPrice: def.buyPrice,
        });
      }
    }
    if (spawned.equipment) {
      state.equipment = {
        weapon1: spawned.equipment.weapon1,
        weapon2: spawned.equipment.weapon2,
        shield: spawned.equipment.shield,
        thruster: spawned.equipment.thruster,
      };
    }
    state.isDead = false;
    state.isDocked = false;
    state.isDockingInProgress = false;
    state.dockingProgress = 0;
    state.bankPanelOpen = false;
    state.marketPanelOpen = false;
    state.spawnedOnce = true;
    state.entities.clear();
    statusEl.textContent = `Connected (ID: ${state.myEntityId})`;
    callbacks.onSpawned();
  });

  client.onPlayerDied((died: PlayerDiedMsg) => {
    state.isDead = true;
    state.deathTime = performance.now();
    state.killerEntityId = died.killerId;
    state.targetId = 0;
    state.lockTargetId = 0;
    state.lockProgress = 0;
    state.beingLockedById = 0;
    state.beingLockedProgress = 0;
    state.cargoPanelOpen = false;
    state.bankPanelOpen = false;
    state.marketPanelOpen = false;
    state.cellMapOpen = false;
    state.lootCrateId = 0;
    state.pendingLootCrateId = 0;
    const myEnt = state.entities.get(state.myEntityId);
    if (myEnt) {
      const e = myEnt.current as ShipEntity;
      spawnExplosion(
        state.explosions,
        myEnt.renderX,
        myEnt.renderY,
        e.width,
        e.height,
        true,
      );
      audio.play(SoundId.Explosion);
    }
    audio.stopAllLoops();
    state.entities.delete(state.myEntityId);
    state.myEntityId = 0;
  });

  client.onLoginRejected((rejected: LoginRejectedMsg) => {
    callbacks.onLoginRejected(rejected.reason || "Login rejected");
    client.disconnect();
  });

  // --- World state ---
  client.onDeltaWorldUpdate((update: DeltaWorldUpdate) => {
    applyDeltaUpdate(state, update);
  });

  // WorldUpdateMsg on SE_WORLD_UPDATE carries chat messages and ability events,
  // NOT entity state. Entity state comes via onDeltaWorldUpdate.
  client.onWorldUpdate((msg: WorldUpdateMsg) => {
    if (msg.abilityEvents) {
      for (const evt of msg.abilityEvents) {
        if (evt.success) {
          state.abilityEffectQueue.push({
            slot: evt.slot,
            abilityType: evt.abilityType,
            targetId: evt.targetId,
            damageDealt: evt.damageDealt,
            casterId: evt.casterId,
            time: performance.now(),
          });
        }
      }
    }
    if (msg.chatMessages) {
      for (const chat of msg.chatMessages) {
        const div = document.createElement("div");
        div.textContent = `[${chat.username}]: ${chat.text}`;
        chatMessagesEl.appendChild(div);
      }
      while (chatMessagesEl.childElementCount > MAX_CHAT_DISPLAY) {
        chatMessagesEl.removeChild(chatMessagesEl.firstChild!);
      }
      chatMessagesEl.scrollTop = chatMessagesEl.scrollHeight;
    }
  });

  // --- Per-viewer player-own state (lock/cooldowns/cargo/equipment) ---
  client.onPlayerOwnState((own: PlayerOwnStateMsg) => {
    state.lockProgress = own.lockProgress;
    if (state.serverLockTargetId !== 0 && own.lockTargetId === 0) {
      state.lockTargetId = 0;
      state.lockProgress = 0;
      state.targetId = 0;
    }
    state.serverLockTargetId = own.lockTargetId;
    state.beingLockedById = own.beingLockedById;
    state.beingLockedProgress = own.beingLockedByProgress;
    state.abilityCooldowns.clear();
    for (const cd of own.abilityCooldowns) {
      state.abilityCooldowns.set(cd.slot, {
        remaining: cd.remaining,
        total: cd.total,
      });
    }
    if (own.equipment) {
      state.equipment = {
        weapon1: own.equipment.weapon1,
        weapon2: own.equipment.weapon2,
        shield: own.equipment.shield,
        thruster: own.equipment.thruster,
      };
    }
    state.cargoItems.clear();
    for (const item of own.cargoItems) {
      if (item.quantity > 0) {
        state.cargoItems.set(item.itemId, item.quantity);
      }
    }
    state.cargoMass = own.cargoMass;
    state.maxCargoMass = own.maxCargoMass;
  });

  // --- Cell transfer signaling ---
  client.onCellChange((msg: CellChangeMsg) => {
    const myEnt = state.entities.get(state.myEntityId);
    if (myEnt) {
      state.preTransferCamX = myEnt.renderX;
      state.preTransferCamY = myEnt.renderY;
      state.preTransferCamRot = myEnt.renderRot;
    }
    state.preTransferCellX = state.originCellX;
    state.preTransferCellY = state.originCellY;
    state.originCellX = msg.cellX;
    state.originCellY = msg.cellY;
    state.pendingCellRebase = true;
    // Clear delta decoder baselines — the new node will send fresh full snapshots.
    client.clearBaselines();
    callbacks.onOriginChanged(state.originCellX, state.originCellY);
  });

  // --- Cell topology (debug overlay) ---
  client.onCellTopology((topo: CellTopologyMsg) => {
    if (topo.cells.length === 0) {
      state.cellTopology = null;
    } else {
      state.cellTopology = topo.cells.map((c: PbCellInfo): CellInfo => ({
        cellX: c.cellX,
        cellY: c.cellY,
        depth: c.depth,
        size: c.size,
        originX: c.originX,
        originY: c.originY,
        nodeId: c.nodeId,
      }));
      if (topo.gridW > 0) state.gridCellsX = topo.gridW;
      if (topo.gridH > 0) state.gridCellsY = topo.gridH;
    }
    callbacks.onTopologyChanged();
  });

  // --- Ping ---
  client.onPong((pong: PongMsg) => {
    state.pingMs = Date.now() - Number(pong.clientTime);
  });

  // --- Bank / inventory ---
  client.onBankContents((bank: BankContentsMsg) => {
    for (const cur of bank.currencies) {
      state.currencyBalances[cur.currencyId] = Number(cur.balance);
    }
    state.bankItems.clear();
    for (const item of bank.items) {
      if (item.quantity > 0) {
        state.bankItems.set(item.itemId, item.quantity);
      }
    }
    state.bankTotalMass = bank.totalMass;
    state.bankMaxMass = bank.maxMass;
    state.dockedCargoItems.clear();
    for (const item of bank.cargoItems) {
      if (item.quantity > 0) {
        state.dockedCargoItems.set(item.itemId, item.quantity);
      }
    }
    state.dockedCargoMass = bank.cargoMass;
    state.dockedMaxCargoMass = bank.maxCargoMass;
  });

  client.onTransferResult((result: TransferResultMsg) => {
    if (result.success) {
      const def = state.itemDefs.get(result.itemId);
      const name = def ? def.name : `Item #${result.itemId}`;
      const action = result.deposit ? "Deposited" : "Withdrew";
      state.toasts.push({
        text: `${action} ${result.quantity.toFixed(0)} ${name}`,
        time: performance.now(),
      });
    } else {
      state.toasts.push({
        text: result.reason || "Transfer failed",
        time: performance.now(),
      });
    }
  });

  client.onEquipResult((result: EquipResultMsg) => {
    if (result.success) {
      const isEquip = result.equippedItemId !== 0;
      const relevantId = isEquip ? result.equippedItemId : result.previousItemId;
      const def = state.itemDefs.get(relevantId);
      const name = def ? def.name : (relevantId ? `Item #${relevantId}` : "Unknown");
      const action = isEquip ? "Equipped" : "Unequipped";
      state.toasts.push({
        text: `${action} ${name}`,
        time: performance.now(),
      });
    } else {
      state.toasts.push({
        text: result.reason || "Equip failed",
        time: performance.now(),
      });
    }
  });

  // --- Docking ---
  client.onDockingState((ds: DockingStateMsg) => {
    const wasDocking = state.isDockingInProgress;
    state.isDockingInProgress = ds.docking;
    state.dockingProgress = ds.progress;
    state.dockingTotalTime = ds.totalTime;
    state.dockingStationId = ds.stationId;
    if (ds.docking && !wasDocking) {
      audio.play(SoundId.TractorBeam);
    }
  });

  client.onDocked(() => {
    state.isDocked = true;
    state.isDockingInProgress = false;
    state.dockingProgress = 0;
    state.cellMapOpen = false;
    state.bankPanelOpen = true;
    state.entities.delete(state.myEntityId);
    state.myEntityId = 0;
    client.sendBankRequest({});
  });

  // --- Map / currency ---
  client.onMapData((mapData: MapDataMsg) => {
    state.mapStations = mapData.stations.map((s: MapStationInfo) => ({
      cellX: s.cellX,
      cellY: s.cellY,
      localX: s.localX,
      localY: s.localY,
      name: s.name,
    }));
  });

  client.onCurrencyUpdate((update: CurrencyUpdateMsg) => {
    state.currencyBalances[update.currencyId] = Number(update.balance);
  });

  client.connect();
}

// Re-export SETTLEMENT_CURRENCY_ID so existing UI imports that used to
// come from network.ts still work (no-op if already imported from state).
export { SETTLEMENT_CURRENCY_ID };
