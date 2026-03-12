import { EntityType } from "@gen/game_pb.js";
import { MAX_CHAT_DISPLAY } from "./constants";
import { updateEntityFromServer } from "./interpolation";
import { spawnExplosion } from "./effects/explosion";
import { decodeServerMessage, encodeBankRequest, encodeLogin, encodePing } from "./protocol";
import type { GameState } from "./state";
import { WSTransport } from "./transport";
import { audio } from "./audio/audio-manager";
import { SoundId } from "./audio/sounds";

export interface NetworkCallbacks {
  onSpawned(): void;
  onDisconnected(): void;
  onLoginRejected(reason: string): void;
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
    state.ws!.sendReliable(encodeLogin(state.playerUsername));

    pingInterval = setInterval(() => {
      if (state.ws && state.connected) {
        state.ws.sendReliable(encodePing());
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
    callbacks.onDisconnected();
    setTimeout(() => connect(state, callbacks), 2000);
  });

  state.ws.onMessage((rawData) => {
    const msg = decodeServerMessage(rawData);
    const inner = msg.msg;
    if (!inner) return;

    switch (inner.case) {
      case "playerSpawned": {
        const spawned = inner.value;
        state.myEntityId = spawned.yourEntityId;
        state.worldWidth = spawned.worldWidth;
        state.worldHeight = spawned.worldHeight;
        // Populate item definitions from server
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
        // Load equipment state
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
        state.spawnedOnce = true;
        state.entities.clear();
        statusEl.textContent = `Connected (ID: ${state.myEntityId})`;
        callbacks.onSpawned();
        break;
      }

      case "worldUpdate": {
        const update = inner.value;
        state.tickCount = update.tick;
        state.lastTickTime = performance.now();

        for (const e of update.entities) {
          updateEntityFromServer(state.entities, e);

          // Parse combat state from own entity
          if (e.id === state.myEntityId) {
            state.lockProgress = e.lockProgress;

            // Server broke the lock (e.g. target moved out of range) — clear client lock & selection
            if (state.serverLockTargetId !== 0 && e.lockTargetId === 0) {
              state.lockTargetId = 0;
              state.lockProgress = 0;
              state.targetId = 0;
            }
            state.serverLockTargetId = e.lockTargetId;

            // Being-locked state
            state.beingLockedById = e.lockedById;
            state.beingLockedProgress = e.lockedByProgress;

            // Update ability cooldowns
            state.abilityCooldowns.clear();
            for (const cd of e.abilityCooldowns) {
              state.abilityCooldowns.set(cd.slot, {
                remaining: cd.remaining,
                total: cd.total,
              });
            }

            // Update equipment state from server
            if (e.equipment) {
              state.equipment = {
                weapon1: e.equipment.weapon1,
                weapon2: e.equipment.weapon2,
                shield: e.equipment.shield,
                thruster: e.equipment.thruster,
              };
            }
          }
        }
        // Entities that died/were destroyed — play explosion
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
        // Entities that left AoI — silent removal
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
        // Ability events (broadcast to all players in AoI)
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

      case "playerDied": {
        const died = inner.value;
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

      case "loginRejected": {
        const rejected = inner.value;
        callbacks.onLoginRejected(rejected.reason || "Login rejected");
        if (state.ws) state.ws.close();
        break;
      }

      case "bankContents": {
        const bank = inner.value;
        state.bankItems.clear();
        for (const item of bank.items) {
          if (item.quantity > 0) {
            state.bankItems.set(item.itemId, item.quantity);
          }
        }
        state.bankTotalMass = bank.totalMass;
        state.bankMaxMass = bank.maxMass;
        // Cargo data (used when docked and entity doesn't exist)
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

      case "equipResult": {
        const result = inner.value;
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

      case "transferResult": {
        const result = inner.value;
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

      case "dockingState": {
        const ds = inner.value;
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

      case "pong": {
        const pong = inner.value;
        state.pingMs = Date.now() - Number(pong.clientTime);
        break;
      }

      case "docked": {
        state.isDocked = true;
        state.isDockingInProgress = false;
        state.dockingProgress = 0;
        state.bankPanelOpen = true;
        state.entities.delete(state.myEntityId);
        state.myEntityId = 0;
        // Request bank contents now that we're docked
        if (state.ws) {
          state.ws.sendReliable(encodeBankRequest());
        }
        break;
      }
    }
  });
}
