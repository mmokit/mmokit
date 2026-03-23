import { fromBinary } from "@bufbuild/protobuf";
import {
  EntityType,
  ServerEventCode,
  OperationCode,
  WorldUpdateMsgSchema,
  PlayerSpawnedMsgSchema,
  PlayerDiedMsgSchema,
  PongMsgSchema,
  LoginRejectedMsgSchema,
  BankContentsMsgSchema,
  TransferResultMsgSchema,
  EquipResultMsgSchema,
  DockingStateMsgSchema,
  DockedMsgSchema,
  MarketOrderBookResponseSchema,
  MarketOrderResultResponseSchema,
  MarketMyOrdersResponseSchema,
  MarketTradeNotificationSchema,
  PlayerOwnStateMsgSchema,
  SectorChangeMsgSchema,
  MapDataMsgSchema,
  DebugFlagsMsgSchema,
} from "@gen/game_pb.js";
import type {
  WorldUpdateMsg,
  PlayerSpawnedMsg,
  PlayerDiedMsg,
  PongMsg,
  LoginRejectedMsg,
  BankContentsMsg,
  TransferResultMsg,
  EquipResultMsg,
  DockingStateMsg,
  DockedMsg,
  MarketOrderBookResponse,
  MarketOrderResultResponse,
  MarketMyOrdersResponse,
  MarketTradeNotification,
  MarketPriceLevel,
  MarketOrderEntry,
  PlayerOwnStateMsg,
  SectorChangeMsg,
  MapDataMsg,
  MapStationInfo,
  DebugFlagsMsg,
} from "@gen/game_pb.js";
import { MAX_CHAT_DISPLAY, SECTOR_SIZE } from "./constants";
import { updateEntityFromServer } from "./interpolation";
import { spawnExplosion } from "./effects/explosion";
import { decodeServerEvent, decodeOperationResponse, encodeBankRequest, encodeLogin, encodePing } from "./protocol";
import type { GameState } from "./state";
import { WSTransport } from "./transport";
import { audio } from "./audio/audio-manager";
import { SoundId } from "./audio/sounds";

export interface NetworkCallbacks {
  onSpawned(): void;
  onDisconnected(): void;
  onLoginRejected(reason: string): void;
  onOriginChanged(sx: number, sy: number): void;
}

export function connect(
  state: GameState,
  callbacks: NetworkCallbacks,
): void {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  state.ws = new WSTransport(`${protocol}//${window.location.host}/ws`);

  const statusEl = document.getElementById("status")!;
  const chatMessagesEl = document.getElementById("chat-messages")!;

  let pingInterval: ReturnType<typeof setInterval> | null = null;

  state.ws.onOpen(() => {
    state.connected = true;
    statusEl.textContent = "Connected - Logging in...";
    statusEl.style.color = "#0f0";
    const loginPayload = encodeLogin(state.playerUsername);
    state.ws!.sendEvent(loginPayload.code, loginPayload.data);

    pingInterval = setInterval(() => {
      if (state.ws && state.connected) {
        const pingPayload = encodePing();
        state.ws.sendEvent(pingPayload.code, pingPayload.data);
      }
    }, 5000);
  });

  state.ws.onClose(() => {
    if (pingInterval) { clearInterval(pingInterval); pingInterval = null; }
    state.connected = false;
    if (!state.spawnedOnce) {
      callbacks.onLoginRejected(state.loggedIn ? "Connection lost" : "");
      return;
    }
    statusEl.textContent = "Disconnected - Reconnecting...";
    statusEl.style.color = "#f00";
    state.myEntityId = 0;
    state.entities.clear();
    state.sectorMapOpen = false;
    callbacks.onDisconnected();
    setTimeout(() => connect(state, callbacks), 2000);
  });

  // Channel 0x00: Game events
  state.ws.onEvent((rawData) => {
    const evt = decodeServerEvent(rawData);
    const code = evt.code as number;

    switch (code) {
      case ServerEventCode.SE_PLAYER_SPAWNED: {
        const spawned = fromBinary(PlayerSpawnedMsgSchema, evt.data) as PlayerSpawnedMsg;
        state.myEntityId = spawned.yourEntityId;
        state.originSectorX = spawned.originSectorX;
        state.originSectorY = spawned.originSectorY;
        callbacks.onOriginChanged(spawned.originSectorX, spawned.originSectorY);
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
        break;
      }

      case ServerEventCode.SE_WORLD_UPDATE: {
        const update = fromBinary(WorldUpdateMsgSchema, evt.data) as WorldUpdateMsg;
        state.tickCount = update.tick;
        state.lastTickTime = performance.now();

        // Detect sector transfer: if the player's position jumps by more
        // than half a sector, rebase all existing entities BEFORE processing
        // the update. Round to nearest SECTOR_SIZE to get the pure coordinate
        // system shift, excluding the player's actual movement.
        let didRebase = false;
        if (state.myEntityId) {
          const myEnt = state.entities.get(state.myEntityId);
          if (myEnt) {
            for (const e of update.entities) {
              if (e.id === state.myEntityId) {
                const rawDx = e.x - myEnt.curr.x;
                const rawDy = e.y - myEnt.curr.y;
                if (Math.abs(rawDx) > SECTOR_SIZE / 2 || Math.abs(rawDy) > SECTOR_SIZE / 2) {
                  const dx = Math.round(rawDx / SECTOR_SIZE) * SECTOR_SIZE;
                  const dy = Math.round(rawDy / SECTOR_SIZE) * SECTOR_SIZE;
                  for (const ent of state.entities.values()) {
                    ent.prev.x += dx; ent.prev.y += dy;
                    ent.curr.x += dx; ent.curr.y += dy;
                    ent.renderX += dx; ent.renderY += dy;
                  }
                  didRebase = true;
                }
                break;
              }
            }
          }
        }

        for (const e of update.entities) {
          updateEntityFromServer(state.entities, e);
        }

        // After rebase + update: anchor prev to current visual position.
        // updateEntityFromServer sets prev = rebased old curr, but renderX
        // holds the rebased EXTRAPOLATED position (where the entity visually
        // is right now). Without this, interpolation at t≈0 snaps the camera
        // from the extrapolated position to the old curr, causing a visible
        // hitch of velocity × gap_duration units.
        if (didRebase) {
          for (const ent of state.entities.values()) {
            ent.prev.x = ent.renderX;
            ent.prev.y = ent.renderY;
          }
        }
        for (const id of update.killedIds) {
          const killed = state.entities.get(id);
          if (killed && (killed.curr.entityType === EntityType.SHIP || killed.curr.entityType === EntityType.NPC)) {
            spawnExplosion(
              state.explosions,
              killed.renderX,
              killed.renderY,
              killed.curr.width,
              killed.curr.height,
              id === state.myEntityId,
            );
            audio.play(SoundId.Explosion);
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
        for (const id of update.removedIds) {
          state.entities.delete(id);
          if (id === state.targetId) state.targetId = 0;
          if (id === state.lootCrateId) state.lootCrateId = 0;
          if (id === state.pendingLootCrateId) state.pendingLootCrateId = 0;
          if (id === state.lockTargetId) {
            state.lockTargetId = 0;
            state.lockProgress = 0;
          }
        }
        if (update.abilityEvents) {
          for (const evt of update.abilityEvents) {
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
        if (update.chatMessages) {
          for (const chat of update.chatMessages) {
            const div = document.createElement("div");
            div.textContent = `[${chat.username}]: ${chat.text}`;
            chatMessagesEl.appendChild(div);
          }
          while (chatMessagesEl.childElementCount > MAX_CHAT_DISPLAY) {
            chatMessagesEl.removeChild(chatMessagesEl.firstChild!);
          }
          chatMessagesEl.scrollTop = chatMessagesEl.scrollHeight;
        }
        break;
      }

      case ServerEventCode.SE_PLAYER_DIED: {
        const died = fromBinary(PlayerDiedMsgSchema, evt.data) as PlayerDiedMsg;
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
        state.sectorMapOpen = false;
        state.lootCrateId = 0;
        state.pendingLootCrateId = 0;
        const myEnt = state.entities.get(state.myEntityId);
        if (myEnt) {
          spawnExplosion(
            state.explosions,
            myEnt.renderX,
            myEnt.renderY,
            myEnt.curr.width,
            myEnt.curr.height,
            true,
          );
          audio.play(SoundId.Explosion);
        }
        audio.stopAllLoops();
        state.entities.delete(state.myEntityId);
        state.myEntityId = 0;
        break;
      }

      case ServerEventCode.SE_LOGIN_REJECTED: {
        const rejected = fromBinary(LoginRejectedMsgSchema, evt.data) as LoginRejectedMsg;
        callbacks.onLoginRejected(rejected.reason || "Login rejected");
        if (state.ws) state.ws.close();
        break;
      }

      case ServerEventCode.SE_BANK_CONTENTS: {
        const bank = fromBinary(BankContentsMsgSchema, evt.data) as BankContentsMsg;
        state.fluxBalance = Number(bank.fluxBalance);
        state.bankItems.clear();
        for (const item of bank.items) {
          if (item.itemId === 1) continue; // Flux is tracked via fluxBalance
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
        break;
      }

      case ServerEventCode.SE_EQUIP_RESULT: {
        const result = fromBinary(EquipResultMsgSchema, evt.data) as EquipResultMsg;
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
        break;
      }

      case ServerEventCode.SE_TRANSFER_RESULT: {
        const result = fromBinary(TransferResultMsgSchema, evt.data) as TransferResultMsg;
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
        break;
      }

      case ServerEventCode.SE_DOCKING_STATE: {
        const ds = fromBinary(DockingStateMsgSchema, evt.data) as DockingStateMsg;
        const wasDocking = state.isDockingInProgress;
        state.isDockingInProgress = ds.docking;
        state.dockingProgress = ds.progress;
        state.dockingTotalTime = ds.totalTime;
        state.dockingStationId = ds.stationId;
        if (ds.docking && !wasDocking) {
          audio.play(SoundId.TractorBeam);
        }
        break;
      }

      case ServerEventCode.SE_PONG: {
        const pong = fromBinary(PongMsgSchema, evt.data) as PongMsg;
        state.pingMs = Date.now() - Number(pong.clientTime);
        break;
      }

      case ServerEventCode.SE_DOCKED: {
        fromBinary(DockedMsgSchema, evt.data) as DockedMsg; // consume
        state.isDocked = true;
        state.isDockingInProgress = false;
        state.dockingProgress = 0;
        state.sectorMapOpen = false;
        state.bankPanelOpen = true;
        state.entities.delete(state.myEntityId);
        state.myEntityId = 0;
        if (state.ws) {
          const bankReq = encodeBankRequest();
          state.ws.sendEvent(bankReq.code, bankReq.data);
        }
        break;
      }

      case ServerEventCode.SE_PLAYER_OWN_STATE: {
        const own = fromBinary(PlayerOwnStateMsgSchema, evt.data) as PlayerOwnStateMsg;
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
        break;
      }

      case ServerEventCode.SE_SECTOR_CHANGE: {
        const msg = fromBinary(SectorChangeMsgSchema, evt.data) as SectorChangeMsg;
        state.originSectorX = msg.sectorX;
        state.originSectorY = msg.sectorY;
        callbacks.onOriginChanged(state.originSectorX, state.originSectorY);
        break;
      }

      case ServerEventCode.SE_MAP_DATA: {
        const mapData = fromBinary(MapDataMsgSchema, evt.data) as MapDataMsg;
        state.mapStations = mapData.stations.map((s: MapStationInfo) => ({
          sectorX: s.sectorX,
          sectorY: s.sectorY,
          localX: s.localX,
          localY: s.localY,
          name: s.name,
        }));
        break;
      }

      case ServerEventCode.SE_DEBUG_FLAGS: {
        const flags = fromBinary(DebugFlagsMsgSchema, evt.data) as DebugFlagsMsg;
        state.showSectorGrid = flags.showSectorGrid;
        break;
      }
    }
  });

  // Channel 0x01: Operation responses
  state.ws.onOperation((rawData) => {
    const resp = decodeOperationResponse(rawData);
    const opCode = resp.code as number;
    const isResponse = resp.requestId > 0;

    // Handle errors
    if (resp.returnCode !== 0) {
      state.toasts.push({
        text: resp.errorMsg || "Operation failed",
        time: performance.now(),
      });
      return;
    }

    switch (opCode) {
      case OperationCode.OP_MARKET_BROWSE: {
        const book = fromBinary(MarketOrderBookResponseSchema, resp.data) as MarketOrderBookResponse;
        state.marketOrderBook = {
          itemId: book.itemId,
          sellLevels: book.sellLevels.map((l: MarketPriceLevel) => ({
            price: Number(l.price),
            quantity: l.quantity,
            orderCount: l.orderCount,
          })),
          buyLevels: book.buyLevels.map((l: MarketPriceLevel) => ({
            price: Number(l.price),
            quantity: l.quantity,
            orderCount: l.orderCount,
          })),
        };
        break;
      }

      case OperationCode.OP_MARKET_CREATE_ORDER: {
        const result = fromBinary(MarketOrderResultResponseSchema, resp.data) as MarketOrderResultResponse;
        if (result.filledQty > 0) {
          state.toasts.push({
            text: `Order filled: ${result.filledQty} @ avg ${Number(result.avgPrice)} FLUX`,
            time: performance.now(),
          });
        }
        if (Number(result.orderId) > 0) {
          state.toasts.push({
            text: `Order #${Number(result.orderId)} placed`,
            time: performance.now(),
          });
        }
        // Refresh bank and order book
        if (state.ws) {
          const bankReq = encodeBankRequest();
          state.ws.sendEvent(bankReq.code, bankReq.data);
        }
        break;
      }

      case OperationCode.OP_MARKET_CANCEL_ORDER: {
        state.toasts.push({
          text: "Order cancelled",
          time: performance.now(),
        });
        if (state.ws) {
          const bankReq = encodeBankRequest();
          state.ws.sendEvent(bankReq.code, bankReq.data);
        }
        break;
      }

      case OperationCode.OP_MARKET_MY_ORDERS: {
        const orders = fromBinary(MarketMyOrdersResponseSchema, resp.data) as MarketMyOrdersResponse;
        state.marketMyOrders = orders.orders.map((o: MarketOrderEntry) => ({
          orderId: Number(o.orderId),
          itemId: o.itemId,
          isBuy: o.isBuy,
          pricePerUnit: Number(o.pricePerUnit),
          quantity: o.quantity,
          origQuantity: o.origQuantity,
          createdAt: Number(o.createdAt),
          expiresAt: Number(o.expiresAt),
        }));
        break;
      }

      case OperationCode.OP_MARKET_INSTANT_TRADE: {
        if (isResponse) {
          // Instant trade response
          const result = fromBinary(MarketOrderResultResponseSchema, resp.data) as MarketOrderResultResponse;
          if (result.filledQty > 0) {
            state.toasts.push({
              text: `Trade: ${result.filledQty} @ avg ${Number(result.avgPrice)} FLUX`,
              time: performance.now(),
            });
          } else {
            state.toasts.push({
              text: "No orders available to fill",
              time: performance.now(),
            });
          }
          if (state.ws) {
            const bankReq = encodeBankRequest();
            state.ws.sendEvent(bankReq.code, bankReq.data);
          }
        } else {
          // Push notification: someone filled our resting order
          const notif = fromBinary(MarketTradeNotificationSchema, resp.data) as MarketTradeNotification;
          const def = state.itemDefs.get(notif.itemId);
          const name = def ? def.name : `Item #${notif.itemId}`;
          const action = notif.youSold ? "Sold" : "Bought";
          state.toasts.push({
            text: `${action} ${notif.filledQty} ${name} @ ${Number(notif.price)} FLUX`,
            time: performance.now(),
          });
        }
        break;
      }
    }
  });
}
