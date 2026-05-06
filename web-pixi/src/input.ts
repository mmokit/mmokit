import { px } from "./view";
import type { GameState } from "./state";
import { audio } from "./audio/audio-manager";
import { SoundId } from "./audio/sounds";
import { getAbilityRange } from "./ui/ability-bar";
import {
  CastAbility,
  Chat,
  Dock,
  JettisonItem,
  Respawn,
  SetLockTarget,
  SetMoveTarget,
  Undock,
} from "../sdk/index.js";

// Entity kind numeric literals (match server-side component.Type*).
const KIND_SHIP = 0;
const KIND_ASTEROID = 1;
const KIND_STATION = 3;
const KIND_LOOT_CRATE = 4;
const KIND_NPC = 5;

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
    if (ent.current.entityType !== KIND_STATION) continue;
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
        if (text && state.connected && state.client) {
          state.inputSeq++;
          state.client.send(new Chat({
            sequence: state.inputSeq,
            username: state.playerUsername,
            text,
          }));
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
      if (state.connected && state.client) {
        state.inputSeq++;
        state.client.send(new Respawn({ sequence: state.inputSeq }));
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
      } else if (state.bankPanelOpen) {
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
        (tgt.current.entityType === KIND_SHIP ||
          tgt.current.entityType === KIND_NPC ||
          tgt.current.entityType === KIND_ASTEROID)
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
        if (state.connected && state.client) {
          state.inputSeq++;
          state.client.send(new Undock({ sequence: state.inputSeq }));
        }
      } else if (!state.isDockingInProgress && isNearStation(state)) {
        if (state.connected && state.client) {
          state.inputSeq++;
          state.client.send(new Dock({ sequence: state.inputSeq }));
        }
      }
    }

    // M: toggle marketplace (only when docked)
    if (e.code === "KeyM" && !state.isDead && state.isDocked) {
      state.marketPanelOpen = !state.marketPanelOpen;
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
      // Skip stations
      if (ent.current.entityType === KIND_STATION) continue;
      const dx = ent.renderX - world.x;
      const dy = ent.renderY - world.y;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const hitRadius = (ent.current.radius || 0.7) + px(10);
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
        (ent.current.entityType === KIND_SHIP ||
          ent.current.entityType === KIND_NPC ||
          ent.current.entityType === KIND_ASTEROID)
      ) {
        state.lockTargetId = bestId;
      }
    }

    // Left-click on loot crate: open loot popup if in range, or move toward it
    if (bestId !== 0) {
      const ent = state.entities.get(bestId);
      if (ent && ent.current.entityType === KIND_LOOT_CRATE) {
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

// Per-tick input sender. Bundled CE_PLAYER_INPUT was decomposed in
// Plan G into discrete typed messages (SetMoveTarget / SetLockTarget /
// CastAbility / JettisonItem). Each piece sends only when its source
// state changes — idle players send zero input frames per tick.
export function sendInput(state: GameState): void {
  if (!state.connected || !state.client) return;
  if (state.isDead || state.chatMode || state.isDocked || state.cellMapOpen) return;

  // Movement: send on the tick the player issues a new click. Active=false
  // is also dispatched once when the click is released.
  const mt = state.moveTarget;
  if (mt.active) {
    state.inputSeq++;
    state.client.send(new SetMoveTarget({
      sequence: state.inputSeq,
      active: true,
      x: mt.x,
      y: mt.y,
    }));
    mt.active = false; // consume after sending (fire-and-forget)
  }

  // Lock target: send on transition. lastSentLockTargetId mirrors the
  // most recent value the server was told about.
  if (state.lockTargetId !== state.lastSentLockTargetId) {
    state.inputSeq++;
    state.client.send(new SetLockTarget({
      sequence: state.inputSeq,
      targetNetID: state.lockTargetId,
    }));
    state.lastSentLockTargetId = state.lockTargetId;
  }

  // Ability presses: one CastAbility per pressed bit.
  if (state.abilityPresses !== 0) {
    const presses = state.abilityPresses;
    state.abilityPresses = 0;
    for (let slot = 0; slot < 8; slot++) {
      if ((presses & (1 << slot)) === 0) continue;
      state.inputSeq++;
      state.client.send(new CastAbility({
        sequence: state.inputSeq,
        slot,
      }));
    }
  }

  // Jettison: discrete one-shot.
  if (state.jettisonRequest !== 0) {
    const itemID = state.jettisonRequest;
    state.jettisonRequest = 0;
    state.inputSeq++;
    state.client.send(new JettisonItem({
      sequence: state.inputSeq,
      itemID,
    }));
  }
}
