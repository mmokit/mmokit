import { EntityType } from "@gen/game_pb.js";
import { encodeChatMessage, encodePlayerInput, encodeRespawnRequest } from "./protocol";
import type { GameState } from "./state";

// Ability key -> bitmask bit mapping
const ABILITY_KEYS: Record<string, number> = {
  KeyQ: 1 << 0, // Pulse Laser
  KeyW: 1 << 1, // Railgun
  KeyE: 1 << 2, // Ion Disruptor
  KeyR: 1 << 3, // Plasma Torpedo
  KeyD: 1 << 4, // Emergency Shields
  KeyF: 1 << 5, // Afterburner
};

export function setupInput(
  state: GameState,
  worldToScreen: (wx: number, wy: number) => { x: number; y: number },
  screenToWorld: (sx: number, sy: number) => { x: number; y: number },
  onMoveCommand?: (wx: number, wy: number) => void,
): void {
  const chatInputEl = document.getElementById("chat-input") as HTMLInputElement;

  window.addEventListener("keydown", (e) => {
    if (!state.loggedIn) return;

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

    // Escape: clear selection (lock is independent)
    if (e.code === "Escape" && !state.isDead) {
      state.targetId = 0;
    }

    // Space: lock onto current target (if it's a ship/NPC)
    if (e.code === "Space" && !state.isDead && state.targetId) {
      const tgt = state.entities.get(state.targetId);
      if (tgt && (tgt.curr.entityType === EntityType.SHIP || tgt.curr.entityType === EntityType.NPC)) {
        state.lockTargetId = state.targetId;
      }
    }

    // Ability keys (press, not hold)
    if (!state.isDead && ABILITY_KEYS[e.code] !== undefined) {
      state.abilityPresses |= ABILITY_KEYS[e.code];
    }

    // G: toggle mining laser
    if (e.code === "KeyG" && !state.isDead) {
      state.miningActive = !state.miningActive;
    }

    if (e.code === "KeyC" && !state.isDead) {
      state.cargoPanelOpen = !state.cargoPanelOpen;
    }
    if (e.code === "KeyX" && !state.isDead) {
      state.sellRequest = true;
    }
    if (state.cargoPanelOpen) {
      const digit = parseInt(e.key);
      if (digit >= 1 && digit <= 4) {
        state.jettisonRequest = digit;
      }
    }
  });

  window.addEventListener("keyup", (e) => {
    if (state.loggedIn && !state.chatMode) state.keys[e.code] = false;
  });

  // Mouse tracking
  window.addEventListener("mousemove", (e) => {
    state.mouseX = e.clientX;
    state.mouseY = e.clientY;
  });

  // Left-click: target selection (ships, NPCs, asteroids, loot crates)
  window.addEventListener("click", (e) => {
    if (!state.loggedIn || state.isDead) return;
    const world = screenToWorld(e.clientX, e.clientY);

    let bestId = 0;
    let bestDist = Infinity;

    for (const [id, ent] of state.entities) {
      if (id === state.myEntityId) continue; // can't target self
      // Skip stations and projectiles
      if (ent.curr.entityType === EntityType.STATION) continue;
      const dx = ent.renderX - world.x;
      const dy = ent.renderY - world.y;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const hitRadius = (ent.curr.radius || 20) + 10;
      if (dist < hitRadius && dist < bestDist) {
        bestDist = dist;
        bestId = id;
      }
    }

    // Ctrl+click: instant lock on clicked entity (if lockable)
    if (e.ctrlKey && bestId !== 0) {
      const ent = state.entities.get(bestId);
      if (ent && (ent.curr.entityType === EntityType.SHIP || ent.curr.entityType === EntityType.NPC)) {
        state.lockTargetId = bestId;
      }
    }

    state.targetId = bestId;
  });

  // Right-click: move to destination
  window.addEventListener("contextmenu", (e) => {
    e.preventDefault();
    if (!state.loggedIn || state.isDead) return;
    const world = screenToWorld(e.clientX, e.clientY);
    state.moveTarget = { x: world.x, y: world.y, active: true };
    onMoveCommand?.(world.x, world.y);
  });
}

export function sendInput(state: GameState): void {
  if (!state.connected || !state.ws) return;
  if (state.isDead || state.chatMode) return;

  const mine = state.miningActive;

  state.inputSeq++;
  const jett = state.jettisonRequest;
  state.jettisonRequest = 0;
  const sell = state.sellRequest;
  state.sellRequest = false;

  const abilityCast = state.abilityPresses;
  state.abilityPresses = 0;

  const mt = state.moveTarget;
  const moveActive = mt.active;
  if (mt.active) mt.active = false; // consume after sending

  const data = encodePlayerInput({
    mine,
    sequence: state.inputSeq,
    targetId: state.targetId,
    jettison: jett,
    sell,
    moveX: mt.x,
    moveY: mt.y,
    moveActive,
    abilityCast,
    lockTargetId: state.lockTargetId,
  });
  state.ws.sendUnreliable(data);
}
