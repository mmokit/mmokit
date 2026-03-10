import { EntityType } from "@gen/game_pb.js";
import { MAX_CHAT_DISPLAY } from "./constants";
import { updateEntityFromServer } from "./interpolation";
import { spawnExplosion } from "./particles";
import { decodeServerMessage, encodeLogin } from "./protocol";
import type { GameState } from "./state";
import { WSTransport } from "./transport";

export function connect(
  state: GameState,
  startRender: () => void,
  startLoginScreen: () => void,
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
      if (!state.loginError) state.loginError = "Connection lost";
      state.loggedIn = false;
      state.loginActive = true;
      state.myEntityId = 0;
      state.entities.clear();
      startLoginScreen();
      return;
    }
    statusEl.textContent = "Disconnected - Reconnecting...";
    statusEl.style.color = "#f00";
    state.myEntityId = 0;
    state.entities.clear();
    setTimeout(() => connect(state, startRender, startLoginScreen), 2000);
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
        if (spawned.sellPrices && spawned.sellPrices.length === 4) {
          state.sellPrices = [...spawned.sellPrices];
        }
        state.isDead = false;
        state.playerFlux = 0;
        state.spawnedOnce = true;
        state.entities.clear();
        statusEl.textContent = `Connected (ID: ${state.myEntityId})`;
        break;
      }

      case "worldUpdate": {
        const update = inner.value;
        state.tickCount = update.tick;
        state.lastTickTime = performance.now();

        for (const e of update.entities) {
          updateEntityFromServer(state.entities, e);
        }
        for (const id of update.removedIds) {
          const removed = state.entities.get(id);
          if (removed && removed.curr.entityType === EntityType.SHIP) {
            spawnExplosion(
              state.explosions,
              removed.renderX,
              removed.renderY,
              removed.curr.width,
              removed.curr.height,
              id === state.myEntityId,
            );
          }
          state.entities.delete(id);
          if (id === state.targetId) state.targetId = 0;
        }
        // Display chat messages
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

      case "sellResult": {
        const result = inner.value;
        state.playerFlux = result.totalFlux;
        state.toasts.push({
          text: `+${result.fluxEarned.toFixed(0)} FLUX`,
          time: performance.now(),
        });
        break;
      }

      case "playerDied": {
        const died = inner.value;
        state.isDead = true;
        state.deathTime = performance.now();
        state.killerEntityId = died.killerId;
        state.targetId = 0;
        state.cargoPanelOpen = false;
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
        }
        state.entities.delete(state.myEntityId);
        state.myEntityId = 0;
        break;
      }

      case "loginRejected": {
        const rejected = inner.value;
        state.loginError = rejected.reason || "Login rejected";
        if (state.ws) state.ws.close();
        break;
      }
    }
  });
}
