import { px } from "./view";
import type { GameState } from "./state";
import { audio } from "./audio/audio-manager";
import { SoundId } from "./audio/sounds";
import { abilityParamsForSlot, getAbilityRange, TargetingMode } from "./ui/ability-bar";
import {
  CastAbility,
  ChannelAim,
  Dock,
  EntityType,
  JettisonItem,
  Respawn,
  SetMoveTarget,
  Undock,
} from "../sdk/index.js";

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
    params.mode === TargetingMode.SkillshotChannel;
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

  // Selection wiring lands in Task 13. For now, lock/unlock/activate are
  // stubs — kept as named helpers so call sites below remain readable
  // until Task 13 replaces them with SelectTarget dispatch.
  function tryLockOrActivate(_netID: number): void {
    // TODO(task-13): send SelectTarget(_netID)
  }

  function tryUnlock(_netID: number): void {
    // TODO(task-13): send SelectTarget(0) to clear selection
  }

  // Tab cycle becomes a no-op until Task 13 reimplements it on top of
  // the Selection model (cycle visible enemies, dispatch SelectTarget).
  function cycleEnemyTarget(): void {
    // TODO(task-13): cycle visible enemies via SelectTarget
  }

  function issueMove(clientX: number, clientY: number) {
    if (!state.loggedIn || state.isDead) return;
    const world = screenToWorld(clientX, clientY);
    state.pendingLootCrateId = 0; // cancel auto-approach
    state.moveTarget = { x: world.x, y: world.y, active: true };
    onMoveCommand?.(world.x, world.y);
  }

  window.addEventListener("keydown", (e) => {
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
    // clears the current selection. Selection dispatch is stubbed pending
    // Task 13; the helpers below are no-ops for now.
    if (e.code === "Space" && !state.isDead) {
      if (e.shiftKey) {
        if (state.selectedNetID !== 0) {
          tryUnlock(state.selectedNetID);
          audio.play(SoundId.TargetLock);
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
          audio.play(SoundId.TargetLock);
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

  // Mouse tracking + right-click drag movement
  window.addEventListener("mousemove", (e) => {
    state.mouseX = e.clientX;
    state.mouseY = e.clientY;
    // Keep cursor world coords up to date — read by CastAbility sends for
    // skillshot aim direction/position. Uses the same screenToWorld helper
    // wired by main.ts (camera.screenToWorld) so it tracks zoom/pan exactly.
    const world = screenToWorld(e.clientX, e.clientY);
    state.cursorWorldX = world.x;
    state.cursorWorldY = world.y;
    if (state.rightMouseDown) {
      issueMove(e.clientX, e.clientY);
    }
  });

  window.addEventListener("mousedown", (e) => {
    if (e.button === 2 && !state.cellMapOpen) {
      // Right-click during an active skillshot aim cancels the aim
      // rather than initiating movement. Mirrors the EVE / SC2
      // convention: aim+right-click escapes the targeting cursor.
      if (state.aimingSlot) {
        state.aimingSlot = 0;
        return;
      }
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

    // Modifier-click behaviors. Selection dispatch is stubbed pending
    // Task 13; the calls below are no-ops for now (helpers preserved so
    // the dispatch sites stay intact for the Task 13 rewrite).
    if (bestId !== 0) {
      const ent = state.entities.get(bestId);
      const kind = ent?.current.entityType;
      const lockable = kind === EntityType.Ship || kind === EntityType.NPC || kind === EntityType.Asteroid;
      if (lockable) {
        if (e.shiftKey) {
          // Shift+click: unlock a locked target.
          tryUnlock(bestId);
        } else if (e.ctrlKey) {
          // Ctrl+click: explicit lock/activate (works for asteroids too).
          tryLockOrActivate(bestId);
        } else if (kind === EntityType.Ship || kind === EntityType.NPC) {
          // Plain click on combat target: auto-start lock. Without this,
          // new players see only the red selection ring on an NPC and
          // assume the game isn't responding to combat input — the lock
          // gesture (Space-after-select or ctrl+click) is not discoverable.
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
