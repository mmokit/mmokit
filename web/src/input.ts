import { EntityType } from "@gen/game_pb.js";
import { encodeChatMessage, encodePlayerInput, encodeRespawnRequest } from "./protocol";
import type { GameState } from "./state";

export function setupInput(state: GameState, canvas: HTMLCanvasElement): void {
  const chatInputEl = document.getElementById("chat-input") as HTMLInputElement;

  window.addEventListener("keydown", (e) => {
    if (state.loginActive) return;

    // Chat mode handling
    if (state.chatMode) {
      if (e.code === "Escape") {
        state.chatMode = false;
        chatInputEl.style.display = "none";
        chatInputEl.value = "";
      } else if (e.code === "Enter") {
        const text = chatInputEl.value.trim();
        if (text && state.connected && state.ws) {
          state.ws.sendReliable(encodeChatMessage(text));
        }
        state.chatMode = false;
        chatInputEl.style.display = "none";
        chatInputEl.value = "";
      }
      return;
    }

    state.keys[e.code] = true;

    if (e.code === "Enter" && !state.isDead) {
      state.chatMode = true;
      chatInputEl.style.display = "block";
      chatInputEl.focus();
      return;
    }

    if (state.isDead && (e.code === "Space" || e.code === "Enter")) {
      if (state.connected && state.ws) {
        state.ws.sendReliable(encodeRespawnRequest());
      }
    }
    if (e.code === "KeyC" && !state.isDead) {
      state.cargoPanelOpen = !state.cargoPanelOpen;
    }
    if (e.code === "KeyE" && !state.isDead) {
      state.sellRequest = true;
    }
    // 1-4 keys jettison cargo while panel is open
    if (state.cargoPanelOpen) {
      const digit = parseInt(e.key);
      if (digit >= 1 && digit <= 4) {
        state.jettisonRequest = digit;
      }
    }
  });

  window.addEventListener("keyup", (e) => {
    if (!state.loginActive && !state.chatMode) state.keys[e.code] = false;
  });

  // Mouse tracking for targeting
  canvas.addEventListener("mousemove", (e) => {
    state.mouseX = e.clientX;
    state.mouseY = e.clientY;
  });

  canvas.addEventListener("click", (e) => {
    if (state.isDead) return;
    const clickWorldX = e.clientX + state.cameraX;
    const clickWorldY = e.clientY + state.cameraY;

    let bestId = 0;
    let bestDist = Infinity;

    for (const [id, ent] of state.entities) {
      if (
        ent.curr.entityType !== EntityType.ASTEROID &&
        ent.curr.entityType !== EntityType.LOOT_CRATE
      )
        continue;
      const dx = ent.renderX - clickWorldX;
      const dy = ent.renderY - clickWorldY;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const hitRadius = (ent.curr.radius || 20) + 10;
      if (dist < hitRadius && dist < bestDist) {
        bestDist = dist;
        bestId = id;
      }
    }

    state.targetId = bestId;
  });
}

export function sendInput(state: GameState): void {
  if (!state.connected || !state.ws) return;
  if (state.isDead || state.chatMode) return;

  let thrust = 0;
  let turn = 0;
  const fire = !!state.keys["Space"];
  const mine = !!state.keys["KeyF"];

  if (state.keys["KeyW"] || state.keys["ArrowUp"]) thrust = 1;
  if (state.keys["KeyS"] || state.keys["ArrowDown"]) thrust = -1;
  if (state.keys["KeyA"] || state.keys["ArrowLeft"]) turn = -1;
  if (state.keys["KeyD"] || state.keys["ArrowRight"]) turn = 1;

  state.inputSeq++;
  const jett = state.jettisonRequest;
  state.jettisonRequest = 0;
  const sell = state.sellRequest;
  state.sellRequest = false;
  const data = encodePlayerInput(
    thrust,
    turn,
    fire,
    mine,
    state.inputSeq,
    state.targetId,
    jett,
    sell,
  );
  state.ws.sendUnreliable(data);
}
