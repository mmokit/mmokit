import { EntityType } from "@gen/game_pb.js";
import { encodeChatMessage, encodePlayerInput, encodeRespawnRequest, encodeBankRequest } from "./protocol";
import type { GameState } from "./state";
import { audio } from "./audio/audio-manager";
import { SoundId } from "./audio/sounds";
import { getAbilityRange } from "./ui/ability-bar";

// Ability key -> bitmask bit mapping
const ABILITY_KEYS: Record<string, number> = {
  KeyQ: 1 << 0,
  KeyW: 1 << 1,
  KeyE: 1 << 2,
  KeyR: 1 << 3,
  KeyD: 1 << 4,
  KeyF: 1 << 5,
};

// Check if player is near a station
function isNearStation(state: GameState): boolean {
  const myEntity = state.entities.get(state.myEntityId);
  if (!myEntity) return false;
  for (const [, ent] of state.entities) {
    if (ent.curr.entityType !== EntityType.STATION) continue;
    const dx = myEntity.renderX - ent.renderX;
    const dy = myEntity.renderY - ent.renderY;
    if (Math.sqrt(dx * dx + dy * dy) < 250) return true;
  }
  return false;
}

export function setupInput(
  state: GameState,
  worldToScreen: (wx: number, wy: number) => { x: number; y: number },
  screenToWorld: (sx: number, sy: number) => { x: number; y: number },
  onMoveCommand?: (wx: number, wy: number) => void,
): void {
  const chatInputEl = document.getElementById("chat-input") as HTMLInputElement;

  // Right-click drag movement
  let rightMouseDown = false;

  function issueMove(clientX: number, clientY: number) {
    if (!state.loggedIn || state.isDead) return;
    const world = screenToWorld(clientX, clientY);
    state.moveTarget = { x: world.x, y: world.y, active: true };
    onMoveCommand?.(world.x, world.y);
  }

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

    // Escape: toggle settings menu, or clear selection
    if (e.code === "Escape" && !state.isDead) {
      if (state.bankPanelOpen) {
        state.bankPanelOpen = false;
      } else if (state.cargoPanelOpen) {
        state.cargoPanelOpen = false;
      } else if (state.escMenuOpen) {
        state.escMenuOpen = false;
      } else if (state.targetId) {
        state.targetId = 0;
      } else {
        state.escMenuOpen = true;
      }
      return;
    }

    // Block game input while ESC menu is open
    if (state.escMenuOpen) return;

    // Space: lock onto current target (if it's a ship/NPC)
    if (e.code === "Space" && !state.isDead && state.targetId) {
      const tgt = state.entities.get(state.targetId);
      if (tgt && (tgt.curr.entityType === EntityType.SHIP || tgt.curr.entityType === EntityType.NPC)) {
        state.lockTargetId = state.targetId;
        audio.play(SoundId.TargetLock);
      }
    }

    // Ability keys (press, not hold)
    if (!state.isDead && ABILITY_KEYS[e.code] !== undefined) {
      state.abilityPresses |= ABILITY_KEYS[e.code];

      // Check if targeted ability is out of range → trigger range ring
      const bit = ABILITY_KEYS[e.code];
      const slot = Math.log2(bit);
      const range = getAbilityRange(state, slot);
      if (range > 0) {
        const me = state.entities.get(state.myEntityId);
        const lockEnt = state.lockTargetId ? state.entities.get(state.lockTargetId) : null;
        if (me && lockEnt) {
          const dx = lockEnt.renderX - me.renderX;
          const dy = lockEnt.renderY - me.renderY;
          const dist = Math.sqrt(dx * dx + dy * dy);
          if (dist > range) {
            state.rangeRingQueue.push({ slot, range });
          }
        }
      }
    }

    // G: toggle mining laser
    if (e.code === "KeyG" && !state.isDead) {
      state.miningActive = !state.miningActive;
    }

    // C: toggle cargo panel
    if (e.code === "KeyC" && !state.isDead) {
      state.cargoPanelOpen = !state.cargoPanelOpen;
    }

    // X: open bank panel (interact key, only near station)
    if (e.code === "KeyX" && !state.isDead) {
      if (state.bankPanelOpen) {
        state.bankPanelOpen = false;
      } else if (isNearStation(state)) {
        state.bankPanelOpen = true;
        if (state.connected && state.ws) {
          state.ws.sendReliable(encodeBankRequest());
        }
      }
    }
  });

  window.addEventListener("keyup", (e) => {
    if (state.loggedIn && !state.chatMode) state.keys[e.code] = false;
  });

  // Mouse tracking + right-click drag movement
  window.addEventListener("mousemove", (e) => {
    state.mouseX = e.clientX;
    state.mouseY = e.clientY;
    if (rightMouseDown) {
      issueMove(e.clientX, e.clientY);
    }
  });

  window.addEventListener("mousedown", (e) => {
    if (e.button === 2) {
      rightMouseDown = true;
      issueMove(e.clientX, e.clientY);
    }
  });

  window.addEventListener("mouseup", (e) => {
    if (e.button === 2) rightMouseDown = false;
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

  // Suppress browser context menu
  window.addEventListener("contextmenu", (e) => e.preventDefault());
}

export function sendInput(state: GameState): void {
  if (!state.connected || !state.ws) return;
  if (state.isDead || state.chatMode) return;

  const mine = state.miningActive;

  state.inputSeq++;
  const jett = state.jettisonRequest;
  state.jettisonRequest = 0;

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
    moveX: mt.x,
    moveY: mt.y,
    moveActive,
    abilityCast,
    lockTargetId: state.lockTargetId,
  });
  state.ws.sendUnreliable(data);
}
