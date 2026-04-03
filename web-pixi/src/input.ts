import { EntityType } from "@gen/game_pb.js";
import { px } from "./view";
import { encodeChatMessage, encodePlayerInput, encodeRespawnRequest, encodeDockRequest, encodeUndockRequest } from "./protocol";
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
    if (Math.sqrt(dx * dx + dy * dy) < 400) return true;
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

  function issueMove(clientX: number, clientY: number) {
    if (!state.loggedIn || state.isDead) return;
    const world = screenToWorld(clientX, clientY);
    state.pendingLootCrateId = 0; // cancel auto-approach

    if (state.moveMode === 'direction') {
      // Compute normalized direction from player to cursor
      const me = state.entities.get(state.myEntityId);
      if (me) {
        const dx = world.x - me.renderX;
        const dy = world.y - me.renderY;
        const len = Math.sqrt(dx * dx + dy * dy);
        if (len > 1) {
          state.dirTarget = { x: dx / len, y: dy / len, active: true };
        }
      }
    } else {
      state.moveTarget = { x: world.x, y: world.y, active: true };
      onMoveCommand?.(world.x, world.y);
    }
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
          const chatPayload = encodeChatMessage(text);
          state.ws.sendEvent(chatPayload.code, chatPayload.data);
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
        const respawnPayload = encodeRespawnRequest();
        state.ws.sendEvent(respawnPayload.code, respawnPayload.data);
      }
    }

    // Tab: toggle cell map
    if (e.code === "Tab" && !state.isDead) {
      e.preventDefault();
      state.cellMapOpen = !state.cellMapOpen;
      return;
    }

    // Block all game input while cell map is open (only Tab/Escape pass through)
    if (state.cellMapOpen) return;

    // Escape: close panels in priority order, or open esc menu
    if (e.code === "Escape" && !state.isDead) {
      if (state.cellMapOpen) {
        state.cellMapOpen = false;
      } else if (state.marketPanelOpen) {
        state.marketPanelOpen = false;
      } else if (state.lootCrateId) {
        state.lootCrateId = 0;
      } else if (state.bankPanelOpen && !state.isDocked) {
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

    // Space: lock onto current target (ships, NPCs, or asteroids)
    if (e.code === "Space" && !state.isDead && state.targetId) {
      const tgt = state.entities.get(state.targetId);
      if (
        tgt &&
        (tgt.curr.entityType === EntityType.SHIP ||
          tgt.curr.entityType === EntityType.NPC ||
          tgt.curr.entityType === EntityType.ASTEROID)
      ) {
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

    // C: toggle cargo panel
    if (e.code === "KeyC" && !state.isDead) {
      state.cargoPanelOpen = !state.cargoPanelOpen;
    }

    // X: dock/undock at station
    if (e.code === "KeyX" && !state.isDead) {
      if (state.isDocked) {
        if (state.connected && state.ws) {
          const undockPayload = encodeUndockRequest();
          state.ws.sendEvent(undockPayload.code, undockPayload.data);
        }
      } else if (!state.isDockingInProgress && isNearStation(state)) {
        if (state.connected && state.ws) {
          const dockPayload = encodeDockRequest();
          state.ws.sendEvent(dockPayload.code, dockPayload.data);
        }
      }
    }

    // M: toggle marketplace (only when docked)
    if (e.code === "KeyM" && !state.isDead && state.isDocked) {
      state.marketPanelOpen = !state.marketPanelOpen;
    }

    // V: toggle movement mode (only when not docked)
    if (e.code === "KeyV" && !state.isDead && !state.isDocked) {
      state.moveMode = state.moveMode === 'destination' ? 'direction' : 'destination';
      state.dirTarget = { x: 0, y: 0, active: false };
    }
  });

  window.addEventListener("keyup", (e) => {
    if (state.loggedIn && !state.chatMode) state.keys[e.code] = false;
  });

  // Mouse tracking + right-click drag movement
  window.addEventListener("mousemove", (e) => {
    state.mouseX = e.clientX;
    state.mouseY = e.clientY;
    if (state.rightMouseDown) {
      issueMove(e.clientX, e.clientY);
    }
  });

  window.addEventListener("mousedown", (e) => {
    if (e.button === 2 && !state.cellMapOpen) {
      state.rightMouseDown = true;
      issueMove(e.clientX, e.clientY);
    }
  });

  window.addEventListener("mouseup", (e) => {
    if (e.button === 2) {
      state.rightMouseDown = false;
      if (state.moveMode === 'direction') {
        state.dirTarget = { ...state.dirTarget, active: false };
      }
    }
  });

  // Left-click: target selection (ships, NPCs, asteroids, loot crates)
  window.addEventListener("click", (e) => {
    if (!state.loggedIn || state.isDead || state.cellMapOpen) return;
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
      const hitRadius = (ent.curr.radius || 0.7) + px(10);
      if (dist < hitRadius && dist < bestDist) {
        bestDist = dist;
        bestId = id;
      }
    }

    // Ctrl+click: instant lock on clicked entity (if lockable)
    if (e.ctrlKey && bestId !== 0) {
      const ent = state.entities.get(bestId);
      if (
        ent &&
        (ent.curr.entityType === EntityType.SHIP ||
          ent.curr.entityType === EntityType.NPC ||
          ent.curr.entityType === EntityType.ASTEROID)
      ) {
        state.lockTargetId = bestId;
      }
    }

    // Left-click on loot crate: open loot popup if in range, or move toward it
    if (bestId !== 0) {
      const ent = state.entities.get(bestId);
      if (ent && ent.curr.entityType === EntityType.LOOT_CRATE) {
        const myEnt = state.entities.get(state.myEntityId);
        if (myEnt) {
          const dx = myEnt.renderX - ent.renderX;
          const dy = myEnt.renderY - ent.renderY;
          if (Math.sqrt(dx * dx + dy * dy) <= 60) {
            state.lootCrateId = bestId;
            state.pendingLootCrateId = 0;
          } else {
            // Out of range: move toward crate and open when in range
            state.pendingLootCrateId = bestId;
            state.moveTarget = { x: ent.renderX, y: ent.renderY, active: true };
            onMoveCommand?.(ent.renderX, ent.renderY);
          }
        }
      } else {
        state.lootCrateId = 0;
        state.pendingLootCrateId = 0;
      }
    } else {
      state.lootCrateId = 0;
      state.pendingLootCrateId = 0;
    }

    state.targetId = bestId;
  });

  // Suppress browser context menu
  window.addEventListener("contextmenu", (e) => e.preventDefault());
}

export function sendInput(state: GameState): void {
  if (!state.connected || !state.ws) return;
  if (state.isDead || state.chatMode || state.isDocked || state.cellMapOpen) return;

  state.inputSeq++;
  const jett = state.jettisonRequest;
  state.jettisonRequest = 0;

  const abilityCast = state.abilityPresses;
  state.abilityPresses = 0;

  const mt = state.moveTarget;
  const moveActive = mt.active;
  if (mt.active) mt.active = false; // consume after sending (fire-and-forget)

  const dt = state.dirTarget;
  // dirActive is NOT consumed — it stays true while mouse is held (continuous input)

  const payload = encodePlayerInput({
    sequence: state.inputSeq,
    jettison: jett,
    moveX: mt.x,
    moveY: mt.y,
    moveActive,
    abilityCast,
    lockTargetId: state.lockTargetId,
    dirX: dt.x,
    dirY: dt.y,
    dirActive: dt.active,
  });
  state.ws.sendEvent(payload.code, payload.data);
}
