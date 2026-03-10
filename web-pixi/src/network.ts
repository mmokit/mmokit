import { EntityType } from "@gen/game_pb.js";
import { MAX_CHAT_DISPLAY } from "./constants";
import { updateEntityFromServer } from "./interpolation";
import { spawnExplosion } from "./effects/explosion";
import { decodeServerMessage, encodeLogin } from "./protocol";
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

  state.ws.onOpen(() => {
    state.connected = true;
    statusEl.textContent = "Connected - Logging in...";
    statusEl.style.color = "#0f0";
    state.ws!.sendReliable(encodeLogin(state.playerUsername));
  });

  state.ws.onClose(() => {
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
            });
          }
        }
        state.isDead = false;
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
          if (id === state.lockTargetId) {
            state.lockTargetId = 0;
            state.lockProgress = 0;
          }
        }
        // Entities that left AoI — silent removal
        for (const id of update.removedIds) {
          state.entities.delete(id);
          if (id === state.targetId) state.targetId = 0;
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
    }
  });
}
