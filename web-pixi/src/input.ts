import { px } from "./view";
import type { GameState } from "./state";
import { abilityParamsForSlot, getAbilityRange, TargetingMode } from "./ui/ability-bar";
import { devOverlay } from "./ui/dev-overlay";
import { estimatedServerNow } from "../sdk/_core/clock-sync.js";
import {
  CastAbility,
  ChannelAim,
  Dock,
  EntityType,
  JettisonItem,
  Respawn,
  SelectTarget,
  SetMoveTarget,
  ToggleSuperCruise,
  Undock,
} from "../sdk/index.js";

function sendMoveTarget(
  state: GameState,
  command: { active: boolean; x: number; y: number },
): void {
  if (!state.client) return;
  state.inputSeq = (state.inputSeq + 1) >>> 0;
  if (state.inputSeq === 0) state.inputSeq = 1; // zero is legacy/unsequenced
  const sequence = state.inputSeq;
  const clientNowMs = performance.now();
  const issuedAtMs = state.clockSync.initialized
    ? estimatedServerNow(state.clockSync, clientNowMs)
    : 0;
  // The wire codec sends float32 coordinates. Replay those exact values so
  // reconciliation does not accumulate a JS-double versus Go-float32 drift.
  const wireCommand = {
    active: command.active,
    x: Math.fround(command.x),
    y: Math.fround(command.y),
  };
  state.movementPrediction.push(sequence, { ...wireCommand, issuedAtMs });
  state.client.send(new SetMoveTarget({ sequence, ...wireCommand }));
}

// handleAbilityPress drives the aim-state machine for one slot.
//   - Self / LockOn / on-cooldown / quickcast-mode → fire immediately
//   - Skillshot mode → enter aim-state, render preview (Task 19)
//   - Same-slot press while aiming → fire (release equivalent)
//   - Different-slot press while aiming → swap aim to new slot
// state.aimingSlot is 1-indexed (slot+1) so 0 means "not aiming".
function handleAbilityPress(state: GameState, slot: number): void {
  const params = abilityParamsForSlot(state, slot);
  if (!params) return;

  // Cooldown gate (visual handled by ability-bar.ts; server enforces).
  const cd = state.abilityCooldowns.get(slot);
  if (cd && cd.remaining > 0) return;

  const isSkillshot =
    params.mode === TargetingMode.SkillshotLine ||
    params.mode === TargetingMode.SkillshotGround ||
    params.mode === TargetingMode.SkillshotChannel ||
    params.mode === TargetingMode.CursorPick;
  const quickcast = (state.quickcastMask & (1 << slot)) !== 0;

  // Same-slot press while aiming → confirm and fire (release equivalent).
  if (state.aimingSlot === slot + 1) {
    fireSkillshot(state, slot);
    state.aimingSlot = 0;
    return;
  }

  // Self / LockOn / quickcast → immediate fire, no aim state.
  if (!isSkillshot || quickcast) {
    fireSkillshot(state, slot);
    state.aimingSlot = 0;
    return;
  }

  // Skillshot + not quickcast: enter (or swap to) aim-state.
  state.aimingSlot = slot + 1;
}

// fireSkillshot sends a CastAbility with the live cursor world coords.
// LockOn/Self abilities ignore the aim coords server-side; passing them
// uniformly keeps the wire path uniform.
//
// For SkillshotChannel abilities, also primes the per-frame ChannelAim
// streamer (tickChannelAim) by setting state.channelingSlot + an
// end-time deadline so the client stops sending once the channel window
// elapses. Server independently enforces its own ChannelDuration —
// these timers should agree but the server is authoritative.
function fireSkillshot(state: GameState, slot: number): void {
  if (!state.connected || !state.client) return;
  const params = abilityParamsForSlot(state, slot);
  state.inputSeq++;
  state.client.send(new CastAbility({
    sequence: state.inputSeq,
    slot,
    aimX: state.cursorWorldX,
    aimY: state.cursorWorldY,
  }));
  if (params?.mode === TargetingMode.SkillshotChannel) {
    state.channelingSlot = slot + 1;
    state.channelEndsAt = performance.now() + (params.channelDuration ?? 3000);
  }
}

// tickChannelAim streams the live cursor coords to the server while a
// SkillshotChannel ability is active. The server's tickChannels reads
// the latest aim each tick to update the beam direction. Called from
// the main render loop on a 50ms throttle (~20 Hz). Self-clears when
// the client-side channel window expires; server's own timeout is the
// authoritative end of the channel.
export function tickChannelAim(state: GameState): void {
  if (!state.channelingSlot || !state.connected || !state.client) return;
  if (performance.now() >= state.channelEndsAt) {
    state.channelingSlot = 0;
    return;
  }
  state.inputSeq++;
  state.client.send(new ChannelAim({
    sequence: state.inputSeq,
    slot: state.channelingSlot - 1,
    aimX: state.cursorWorldX,
    aimY: state.cursorWorldY,
  }));
}

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
    if (ent.current.entityType !== EntityType.Station) continue;
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

  // Selection helpers — dispatch SelectTarget(netID) on lock/activate and
  // SelectTarget(0) on clear. state.selectedNetID is updated optimistically;
  // the server's authoritative Selection component drives final UI state.
  function tryLockOrActivate(netID: number): void {
    if (!state.client || netID === 0) return;
    state.inputSeq++;
    state.client.send(new SelectTarget({
      sequence: state.inputSeq,
      netID,
    }));
    state.selectedNetID = netID; // optimistic
  }

  function tryUnlock(_netID: number): void {
    if (state.selectedNetID === 0) return; // already empty
    if (state.client) {
      state.inputSeq++;
      state.client.send(new SelectTarget({
        sequence: state.inputSeq,
        netID: 0,
      }));
    }
    state.selectedNetID = 0;
  }

  // Tab: cycle through visible asteroid/NPC entities in selection order
  // (sorted by netID for stable cycling). Skips self; wraps at the end.
  function cycleEnemyTarget(): void {
    if (!state.client) return;
    const candidates: number[] = [];
    for (const [netID, ent] of state.entities) {
      if (netID === state.myEntityId) continue;
      const kind = ent.current?.entityType;
      if (kind === EntityType.Asteroid || kind === EntityType.NPC) {
        candidates.push(netID);
      }
    }
    if (candidates.length === 0) return;
    candidates.sort((a, b) => a - b); // stable order
    let nextIdx = 0;
    if (state.selectedNetID !== 0) {
      const idx = candidates.indexOf(state.selectedNetID);
      nextIdx = idx === -1 ? 0 : (idx + 1) % candidates.length;
    }
    const next = candidates[nextIdx];
    state.inputSeq++;
    state.client.send(new SelectTarget({
      sequence: state.inputSeq,
      netID: next,
    }));
    state.selectedNetID = next;
  }

  function issueMove(clientX: number, clientY: number) {
    if (!state.loggedIn || state.isDead) return;
    const world = screenToWorld(clientX, clientY);
    state.pendingLootCrateId = 0; // cancel auto-approach
    state.moveTarget = { x: world.x, y: world.y, active: true };
    onMoveCommand?.(world.x, world.y);
  }

  window.addEventListener("keydown", (e) => {
    // Dev overlay toggle (Backquote / ~). Available pre-login so the
    // panel can be left enabled across reconnects.
    if (e.code === "Backquote") {
      devOverlay.toggle();
      e.preventDefault();
      return;
    }

    if (!state.loggedIn) return;

    // Chat mode handling — server-side chat decommissioned in Plan 1
    // Phase 6; the input box UI is preserved as a shell so the future
    // chat service can wire up its own send handler. Enter/Escape just
    // close + clear the box.
    if (state.chatMode) {
      if (e.code === "Escape" || e.code === "Enter") {
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

    // Tab: cycle through visible enemy targets (MMO-standard targeting).
    // Suppressed while the cell map is open so Tab doesn't accidentally
    // cycle targets through the map overlay; Escape or M close the map.
    if (e.code === "Tab" && !state.isDead && !state.cellMapOpen) {
      e.preventDefault();
      cycleEnemyTarget();
      return;
    }

    // M (when not docked): toggle cell map. The docked branch below
    // routes M to the marketplace — docked/undocked are mutually
    // exclusive so the bindings don't collide.
    if (e.code === "KeyM" && !state.isDead && !state.isDocked) {
      e.preventDefault();
      state.cellMapOpen = !state.cellMapOpen;
      return;
    }

    // Block all game input while cell map is open (Escape/M close it; the
    // M-toggle above handles that path before this gate).
    if (state.cellMapOpen) {
      if (e.code === "Escape") {
        state.cellMapOpen = false;
        e.preventDefault();
      }
      return;
    }

    // Escape: close panels in priority order, or open esc menu.
    // Aim-state takes highest priority — Escape during a skillshot aim
    // cancels the aim without disturbing any panel state below it.
    if (e.code === "Escape" && !state.isDead) {
      if (state.aimingSlot) {
        state.aimingSlot = 0;
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

    // Space: select current target (ships, NPCs, or asteroids). Shift+Space
    // clears the current selection. The select/unlock helpers handle the
    // SelectTarget dispatch + select-blip sound internally.
    if (e.code === "Space" && !state.isDead) {
      if (e.shiftKey) {
        if (state.selectedNetID !== 0) {
          tryUnlock(state.selectedNetID);
        }
      } else if (state.targetId) {
        const tgt = state.entities.get(state.targetId);
        if (
          tgt &&
          (tgt.current.entityType === EntityType.Ship ||
            tgt.current.entityType === EntityType.NPC ||
            tgt.current.entityType === EntityType.Asteroid)
        ) {
          tryLockOrActivate(state.targetId);
        }
      }
    }

    // Ability keys (press, not hold). The aim-state machine in
    // handleAbilityPress decides whether to fire immediately or enter
    // an aim-confirm state (skillshot abilities without quickcast).
    if (!state.isDead && ABILITY_KEYS[e.code] !== undefined) {
      const bit = ABILITY_KEYS[e.code];
      const slot = Math.log2(bit);
      e.preventDefault();

      // Out-of-range visual feedback for lock-on abilities — kept from
      // the legacy path so players see the red range ring before the
      // cast attempt. Skillshots don't need this (preview ring renders
      // in aim-state, Task 19).
      const range = getAbilityRange(state, slot);
      if (range > 0) {
        const me = state.entities.get(state.myEntityId);
        const lockEnt = state.selectedNetID ? state.entities.get(state.selectedNetID) : null;
        if (me && lockEnt) {
          const dx = lockEnt.renderX - me.renderX;
          const dy = lockEnt.renderY - me.renderY;
          const dist = Math.sqrt(dx * dx + dy * dy);
          if (dist > range) {
            state.rangeRingQueue.push({ slot, range });
          }
        }
      }

      handleAbilityPress(state, slot);
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

    // S: stop movement. Clears server-side MoveTarget (server-side drag
    // handles the slow-down naturally — same coast-to-stop the player
    // sees at the end of a normal click-to-move). Also clears the local
    // move pin so the indicator disappears immediately.
    if (e.code === "KeyS" && !state.isDead && !state.isDocked && state.connected && state.client) {
      sendMoveTarget(state, { active: false, x: 0, y: 0 });
      state.moveTarget = { x: 0, y: 0, active: false };
      state.pendingLootCrateId = 0;
    }

    // Z: toggle supercruise. Server owns the state machine + channel
    // timing + lockout enforcement — client just sends the intent.
    if (e.code === "KeyZ" && !state.isDead && !state.isDocked && state.connected && state.client) {
      state.inputSeq++;
      state.client.send(new ToggleSuperCruise({ sequence: state.inputSeq }));
    }

  });

  window.addEventListener("keyup", (e) => {
    if (state.loggedIn && !state.chatMode) state.keys[e.code] = false;

    // Aim-confirm-on-release: tap-and-hold gesture. While aiming a
    // skillshot, releasing the aim key fires immediately with the
    // current cursor coords. Same-key re-press also fires (handled in
    // handleAbilityPress) — this branch covers the more natural
    // "press-aim-release" cadence.
    if (state.loggedIn && !state.chatMode && state.aimingSlot && ABILITY_KEYS[e.code] !== undefined) {
      const bit = ABILITY_KEYS[e.code];
      const slot = Math.log2(bit);
      if (state.aimingSlot === slot + 1) {
        fireSkillshot(state, slot);
        state.aimingSlot = 0;
      }
    }
  });

  // Mouse tracking + right-click drag movement.
  // NOTE: cursorWorldX/Y is NOT computed here — the screen→world transform
  // also depends on the camera, which moves with the ship. If we only
  // updated on mousemove, holding the cursor still while the ship flew
  // would leave the aim indicator "locked" at a stale world point.
  // main.ts re-derives cursorWorldX/Y every frame from state.mouseX/Y.
  window.addEventListener("mousemove", (e) => {
    state.mouseX = e.clientX;
    state.mouseY = e.clientY;
    if (state.rightMouseDown) {
      issueMove(e.clientX, e.clientY);
    }
  });

  window.addEventListener("mousedown", (e) => {
    if (e.button === 2 && !state.cellMapOpen) {
      // Right-click priority 1: cancel active skillshot aim. Mirrors the
      // EVE / SC2 convention: aim+right-click escapes the targeting
      // cursor.
      if (state.aimingSlot) {
        state.aimingSlot = 0;
        return;
      }
      // Right-click is the move verb — it must NOT clear the selection.
      // Selection persists across move commands so the UI keeps showing
      // info on the picked target (asteroid, NPC, ship). Use shift+click
      // or click-empty-space to clear the selection.
      state.rightMouseDown = true;
      issueMove(e.clientX, e.clientY);
      // Start local prediction and send the first target immediately. Held
      // cursor refresh remains on the 20 Hz sender below to avoid flooding.
      sendInput(state);
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
      // Skip stations and POI markers — neither is click-lockable.
      if (ent.current.entityType === EntityType.Station) continue;
      if (ent.current.entityType === EntityType.POI) continue;
      // Skip leashed NPCs — they're returning to anchor and shouldn't be
      // re-engageable until they re-aggro. Mirrors the server's targeting
      // gate so click + Space don't queue a lock the server will reject.
      if (
        ent.current.entityType === EntityType.NPC &&
        ent.current.state === 3 /* AIStateLeash */
      ) continue;
      const dx = ent.renderX - world.x;
      const dy = ent.renderY - world.y;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const hitRadius = (ent.current.radius || 0.7) + px(10);
      if (dist < hitRadius && dist < bestDist) {
        bestDist = dist;
        bestId = id;
      }
    }

    // Selection (plain click): set the player's selected target. Asteroids,
    // NPCs, and ships are all selectable. Shift+click clears the selection;
    // right-click (handled above in button === 2) also clears.
    if (bestId !== 0) {
      const ent = state.entities.get(bestId);
      const kind = ent?.current.entityType;
      const selectable = kind === EntityType.Ship || kind === EntityType.NPC || kind === EntityType.Asteroid;
      if (selectable) {
        if (e.shiftKey) {
          tryUnlock(bestId);
        } else {
          tryLockOrActivate(bestId);
        }
      }
    }

    // Left-click on loot crate: open loot popup if in range, or move toward it
    if (bestId !== 0) {
      const ent = state.entities.get(bestId);
      if (ent && ent.current.entityType === EntityType.LootCrate) {
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
// Plan G into discrete typed messages (SetMoveTarget / CastAbility /
// JettisonItem). Selection inputs (Task 13) are dispatched at the
// gesture site in setupInput() — they're edge-triggered, not
// state-mirrored, so there's no per-tick reconcile step. Each piece
// sends only when its source state changes — idle players send zero
// input frames per tick.
export function sendInput(state: GameState): void {
  if (!state.connected || !state.client) return;
  if (state.isDead || state.chatMode || state.isDocked || state.cellMapOpen) return;

  // Auto-clear optimistic selection when the selected entity has left
  // the AoI delta (died, despawned, or moved out of range). The server
  // will eventually echo Selection.cleared but client-side clearing
  // here avoids stale ring UI while we wait for that echo.
  if (state.selectedNetID !== 0 && !state.entities.has(state.selectedNetID)) {
    state.selectedNetID = 0;
  }

  // Movement: send on the tick the player issues a new click. Active=false
  // is also dispatched once when the click is released.
  const mt = state.moveTarget;
  if (mt.active) {
    sendMoveTarget(state, { active: true, x: mt.x, y: mt.y });
    mt.active = false; // consume after sending (fire-and-forget)
  }

  // Ability presses: dispatched immediately by the aim-state machine in
  // handleAbilityPress / fireSkillshot — sendInput no longer batches
  // them per-tick. Skillshot aim+confirm needs zero latency, and the
  // server tolerates >1 CastAbility per tick (cooldowns gate the rate).

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
