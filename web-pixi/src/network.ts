import {
  SpaceClient,
  SpaceDeltaDecoder,
  type AnyEntity,
  type DeltaWorldUpdate,
  type ShipEntity,
  type NPCEntity,
  PlayerSpawned,
  BankContents,
  TransferResult,
  EquipResult,
  DockingState,
  PlayerOwnState,
  MapData,
  CurrencyUpdate,
  BeamClip,
  PlayerDied,
  Ping,
  Pong,
  DebugInfo,
  Damage,
  MineExtract,
  Status,
  Killed,
  BeamToggle,
  BankRequest,
  WorldDelta,
  EntityType,
} from "../sdk/index.js";
import { CELL_SIZE } from "./constants";
import { updateEntityFromServer } from "./interpolation";
import { observeFrameStamps } from "./clockSync";
import { devOverlay } from "./ui/dev-overlay";
import { spawnExplosion } from "./effects/explosion";
import { SETTLEMENT_CURRENCY_ID, type GameState, type CellInfo } from "./state";

// Move the right-rail cargo-panel into / out of the docked station-panels
// flex container based on dock state. While docked the layout is
// [cargo-panel | bank-panel | marketplace-panel]; while undocked the
// cargo-panel returns to its original right-side rail.
function syncCargoPanelLocation(isDocked: boolean): void {
  const cargoPanel = document.getElementById("cargo-panel");
  if (!cargoPanel) return;
  if (isDocked) {
    const stationPanels = document.getElementById("station-panels");
    if (stationPanels && cargoPanel.parentElement !== stationPanels) {
      // Insert as the FIRST child so the order is cargo | bank | market.
      stationPanels.insertBefore(cargoPanel, stationPanels.firstChild);
    }
  } else {
    const gameUi = document.getElementById("game-ui");
    if (gameUi && cargoPanel.parentElement !== gameUi) {
      gameUi.appendChild(cargoPanel);
    }
  }
}
import { audio } from "./audio/audio-manager";
import { SoundId } from "./audio/sounds";

export interface NetworkCallbacks {
  onWSOpen(): void;
  onSpawned(): void;
  onDisconnected(): void;
  onLoginRejected(reason: string): void;
  onOriginChanged(sx: number, sy: number): void;
  onTopologyChanged(): void;
}

/**
 * Shift all stored entity positions by (dx, dy). Used during cell rebase when
 * the coordinate system origin changes. SDK entity interfaces are plain objects
 * so we can mutate their fields in place.
 */
function applyDeltaUpdate(state: GameState, update: DeltaWorldUpdate): void {
  state.tickCount = update.tick;

  // Merge entered + updated into a single "fresh" list. Every decoded
  // entity carries its own ClusterClock-aligned `producedAtMs` stamp;
  // clockSync anchors on the freshest one in the frame.
  const fresh: AnyEntity[] = [...update.entered, ...update.updated];
  const arriveMs = performance.now();
  const offsetBefore = state.clockSync.offsetMs;
  observeFrameStamps(state.clockSync, fresh, arriveMs);
  const offsetAfter = state.clockSync.offsetMs;
  let maxStamp = 0;
  for (const e of fresh) {
    if (e.producedAtMs > maxStamp) maxStamp = e.producedAtMs;
  }
  devOverlay.observeServerFrame(arriveMs, maxStamp, fresh.length, offsetBefore, offsetAfter);

  // Fresh-snapshot frames (flag set by the server on the first frame from a
  // given ReplicationSystem: login or cross-cell handoff) are authoritative
  // about the full visible set. Reconcile local state set-wise: keep any
  // entity whose netID is present in `entered`, drop everything else.
  //
  // Clearing the whole map and rebuilding from `entered` would work
  // functionally but resets every persistent entity's prev/render state,
  // producing a visible interpolation hitch on the render frame after
  // each handoff — entities snap to their current position for one tick
  // before smooth interpolation resumes. Instead, we remove only the
  // entities that truly left visibility (local-to-old-cell entities that
  // aren't in the new cell's AoI). Persistent entities (the player, any
  // asteroid/ship that's in both cells' views) keep their interpolation
  // anchor and flow smoothly through the crossing. updateEntityFromServer
  // below then samples prev→curr on every entered/updated entity
  // regardless of whether it was retained or new.
  //
  // Positions are world-absolute on the wire, so the new cell sends every
  // persistent entity at the same (worldX, worldY) the old cell was
  // sending — no rebase, no teleport. The client learns nothing about
  // cells; this branch is a codec-level baseline reset only.
  if (update.freshSnapshot) {
    const visible = new Set<number>();
    for (const e of update.entered) visible.add(e.netID);
    for (const e of update.updated) visible.add(e.netID);
    if (state.myEntityId) visible.add(state.myEntityId);
    for (const id of Array.from(state.entities.keys())) {
      if (!visible.has(id)) state.entities.delete(id);
    }
  }

  // Push one server-timestamped sample per fresh entity into its ring.
  // The render loop does the actual interpolation off (estimatedServerNow
  // − RENDER_DELAY); cross-cell tick-phase mismatches are absorbed by
  // matching on true server-time deltas rather than client arrival times.
  for (const e of fresh) {
    updateEntityFromServer(state.entities, e, e.producedAtMs);
  }

  // Removed entities (despawned/killed) — spawn explosion for ships/NPCs.
  for (const id of update.removed) {
    const killed = state.entities.get(id);
    if (killed) {
      const t = killed.current.entityType;
      if (t === EntityType.Ship || t === EntityType.NPC) {
        const e = killed.current as ShipEntity | NPCEntity;
        spawnExplosion(
          state.explosions,
          killed.renderX,
          killed.renderY,
          e.width,
          e.height,
          id === state.myEntityId,
        );
        audio.play(SoundId.Explosion);
      }
    }
    state.entities.delete(id);
    if (id === state.targetId) state.targetId = 0;
    if (id === state.lootCrateId) state.lootCrateId = 0;
    if (id === state.pendingLootCrateId) state.pendingLootCrateId = 0;
    if (id === state.selectedNetID) state.selectedNetID = 0;
  }
  // Exited entities left our AoI but still exist on the server — drop from
  // local map without spawning explosions.
  for (const id of update.exited) {
    state.entities.delete(id);
    if (id === state.targetId) state.targetId = 0;
    if (id === state.lootCrateId) state.lootCrateId = 0;
    if (id === state.pendingLootCrateId) state.pendingLootCrateId = 0;
    if (id === state.selectedNetID) state.selectedNetID = 0;
  }
}

export function connect(state: GameState, callbacks: NetworkCallbacks): void {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  const statusEl = document.getElementById("status")!;

  let pingInterval: ReturnType<typeof setInterval> | null = null;

  const client = new SpaceClient({
    url: `${proto}//${window.location.host}/ws`,
    onOpen: () => {
      state.connected = true;
      statusEl.textContent = "Connected - Authenticating...";
      statusEl.style.color = "#0f0";
      callbacks.onWSOpen();
      pingInterval = setInterval(() => {
        if (state.client && state.connected) {
          state.client.send(new Ping({ clientTime: Date.now() }));
        }
      }, 5000);
    },
    onClose: () => {
      if (pingInterval) {
        clearInterval(pingInterval);
        pingInterval = null;
      }
      state.connected = false;
      if (!state.spawnedOnce) {
        callbacks.onLoginRejected(state.loggedIn ? "Connection lost" : "");
        return;
      }
      statusEl.textContent = "Disconnected - Reconnecting...";
      statusEl.style.color = "#f00";
      state.myEntityId = 0;
      state.entities.clear();
      state.cellMapOpen = false;
      callbacks.onDisconnected();
      setTimeout(() => connect(state, callbacks), 2000);
    },
    onError: () => {
      /* handled in onClose */
    },
  });
  state.client = client;

  // --- Spawn / life cycle ---
  client.onPlayerSpawned((spawned: PlayerSpawned) => {
    state.myEntityId = spawned.yourEntityID;
    // Reset topology — server will send SE_CELL_TOPOLOGY if debug overlay is active.
    state.cellTopology = null;
    callbacks.onOriginChanged(spawned.originCellX, spawned.originCellY);
    if (spawned.itemDefs.length > 0) {
      state.itemDefs.clear();
      for (const def of spawned.itemDefs) {
        state.itemDefs.set(def.id, {
          id: def.id,
          name: def.name,
          massPerUnit: def.massPerUnit,
          category: def.category,
          equipSlot: def.equipSlot,
        });
      }
    }
    state.equipment = {
      weapon1: spawned.equipment.weapon1,
      weapon2: spawned.equipment.weapon2,
      shield: spawned.equipment.shield,
      thruster: spawned.equipment.thruster,
    };
    state.isDead = false;
    state.isDocked = false;
    state.isDockingInProgress = false;
    state.dockingProgress = 0;
    state.bankPanelOpen = false;
    state.marketPanelOpen = false;
    document.body.classList.remove("docked");
    syncCargoPanelLocation(false);
    state.spawnedOnce = true;
    // Don't clear state.entities here — the server fires SE_PLAYER_SPAWNED
    // for in-session transitions too (e.g. undock: StateDocked→StateActive
    // runs the OnPlayerJoin → reconnectPlayer chain). Wiping the entity map
    // makes the station/asteroids/NPCs blank for one tick until the next
    // delta re-emits them, which the user sees as a flicker. The genuine
    // "fresh client state" cleanup happens in onDisconnected when the WS
    // closes, and AoI Exit events (network.ts:104-105) handle ongoing
    // visibility cleanup.
    statusEl.textContent = `Connected (ID: ${state.myEntityId})`;
    callbacks.onSpawned();
  });

  client.onPlayerDied((died: PlayerDied) => {
    state.isDead = true;
    state.deathTime = performance.now();
    state.killerEntityId = died.killerID;
    state.targetId = 0;
    state.selectedNetID = 0;
    state.cargoPanelOpen = false;
    state.bankPanelOpen = false;
    state.marketPanelOpen = false;
    state.cellMapOpen = false;
    document.body.classList.remove("docked");
    syncCargoPanelLocation(false);
    state.lootCrateId = 0;
    state.pendingLootCrateId = 0;
    const myEnt = state.entities.get(state.myEntityId);
    if (myEnt) {
      const e = myEnt.current as ShipEntity;
      spawnExplosion(
        state.explosions,
        myEnt.renderX,
        myEnt.renderY,
        e.width,
        e.height,
        true,
      );
      audio.play(SoundId.Explosion);
    }
    audio.stopAllLoops();
    state.entities.delete(state.myEntityId);
    state.myEntityId = 0;
  });

  // Server-side login rejection used to ride a typed LoginRejected
  // broadcast on channel 0x00; the auth subsystem now handles login
  // failures inline at the HTTP /auth/* endpoints. The
  // callbacks.onLoginRejected hook is still invoked from onClose below
  // for transport-level disconnect + connection-lost UX.

  // --- World state ---
  // Per-tick entity-state delta arrives as a typed WorldDelta event (the
  // legacy SE_DELTA_WORLD_UPDATE protobuf envelope is gone). The reflection
  // codec hands us the raw delta-frame bytes via WorldDelta.body; decode
  // them with the SDK's SpaceDeltaDecoder, which owns the per-baseline
  // state for the local connection.
  const deltaDecoder = new SpaceDeltaDecoder();
  client.onWorldDelta((msg: WorldDelta) => {
    // Time the full decode+apply pass so the dev overlay can show
    // per-frame processing duration. If this consistently exceeds the
    // ~50ms tick interval, frame events back up on the JS event loop
    // and arrive in bursts when the loop catches up — that's a
    // client-side cause of the burst pattern, separate from any
    // network-level batching.
    const t0 = performance.now();
    applyDeltaUpdate(state, deltaDecoder.decode(msg.body));
    devOverlay.observeApplyDuration(performance.now() - t0);
  });

  // Typed broadcast events — dispatched per-class via the framework's
  // reflect-codec pipeline. Each handler pushes onto the existing
  // abilityEffectQueue so the renderer (effects/ability-effects.ts) keeps
  // its current AbilityCastEvent shape; the queue is the integration
  // point, not the wire format.
  client.typedEvents.on(Damage, (msg: Damage) => {
    state.abilityEffectQueue.push({
      slot: msg.slot,
      abilityType: msg.abilityType,
      targetId: msg.target,
      damageDealt: msg.dealt,
      casterId: msg.source,
      time: performance.now(),
    });
  });

  client.typedEvents.on(MineExtract, (msg: MineExtract) => {
    state.abilityEffectQueue.push({
      slot: msg.beam,
      abilityType: 0, // mining beam — type discriminator unused by mining renderer
      targetId: msg.asteroid,
      damageDealt: msg.extracted,
      casterId: msg.caster,
      time: performance.now(),
    });
  });

  client.typedEvents.on(Status, (msg: Status) => {
    state.abilityEffectQueue.push({
      slot: msg.slot,
      abilityType: msg.abilityType,
      targetId: msg.target,
      damageDealt: 0,
      casterId: msg.source,
      time: performance.now(),
    });
  });

  // Killed broadcasts the dying entity's NetID to AoI viewers; the
  // delta-world-update path already drives the per-entity explosion via
  // update.removed in applyDeltaUpdate, so this handler is currently a
  // no-op placeholder. Wire dedicated VFX (kill cam, score popup) here.
  client.typedEvents.on(Killed, (_msg: Killed) => {
    // intentionally empty for now
  });

  // BeamToggle is the per-press pulse VFX restored in Plan G. The mining
  // beam visual itself comes from ActiveMining replication; this event
  // just lets us emit a one-shot pulse on toggle. TODO: render a brief
  // pulse at the caster (effects/beam-pulse.ts).
  client.typedEvents.on(BeamToggle, (_msg: BeamToggle) => {
    // intentionally empty for now — server-side broadcast is wired,
    // client-side renderer is follow-up polish.
  });

  // --- Per-viewer player-own state (cooldowns/cargo/equipment) ---
  client.onPlayerOwnState((own: PlayerOwnState) => {
    state.abilityCooldowns.clear();
    for (const cd of own.abilityCooldowns) {
      state.abilityCooldowns.set(cd.slot, {
        remaining: cd.remaining,
        total: cd.total,
      });
    }
    state.equipment = {
      weapon1: own.equipment.weapon1,
      weapon2: own.equipment.weapon2,
      shield: own.equipment.shield,
      thruster: own.equipment.thruster,
    };
    state.cargoItems.clear();
    for (const item of own.cargoItems) {
      if (item.quantity > 0) {
        state.cargoItems.set(item.itemID, item.quantity);
      }
    }
    state.cargoMass = own.cargoMass;
    state.maxCargoMass = own.maxCargoMass;
  });

  // --- Debug info (per-player gated overlay payload) ---
  // SE_CELL_TOPOLOGY was folded into SE_DEBUG_INFO; topology lives at
  // msg.topology and is only populated for players with the topology
  // debug flag granted. Empty payload (topology cleared, aoiRadius
  // unset) is the sentinel sent on revoke-to-zero.
  client.onDebugInfo((msg: DebugInfo) => {
    if (msg.topology.cells.length > 0) {
      const topo = msg.topology;
      state.cellTopology = topo.cells.map((c): CellInfo => ({
        cellX: c.cellX,
        cellY: c.cellY,
        depth: c.depth,
        size: c.size,
        originX: c.originX,
        originY: c.originY,
        nodeId: c.nodeID,
      }));
      if (topo.gridW > 0) state.gridCellsX = topo.gridW;
      if (topo.gridH > 0) state.gridCellsY = topo.gridH;
    } else {
      state.cellTopology = null;
    }
    callbacks.onTopologyChanged();
  });

  // --- Ping ---
  client.onPong((pong: Pong) => {
    state.pingMs = Date.now() - Number(pong.clientTime);
  });

  // --- Bank / inventory ---
  // applyBankContents is shared between two delivery paths now:
  //   1. onBankContents — out-of-band server-initiated pushes (admin
  //      tools, future automated payouts, deposit/withdraw side effects).
  //   2. The bank() typed-op response — see refreshBank below and the
  //      consumers in ui/bank.ts / ui/market.ts / ui/hud.ts.
  const applyBankContents = (bank: BankContents) => {
    for (const cur of bank.currencies) {
      state.currencyBalances[cur.currencyID] = Number(cur.balance);
    }
    state.bankItems.clear();
    for (const item of bank.items) {
      if (item.quantity > 0) {
        state.bankItems.set(item.itemID, item.quantity);
      }
    }
    state.bankTotalMass = bank.totalMass;
    state.bankMaxMass = bank.maxMass;
    state.dockedCargoItems.clear();
    for (const item of bank.cargoItems) {
      if (item.quantity > 0) {
        state.dockedCargoItems.set(item.itemID, item.quantity);
      }
    }
    state.dockedCargoMass = bank.cargoMass;
    state.dockedMaxCargoMass = bank.maxCargoMass;
  };
  client.onBankContents(applyBankContents);

  // refreshBank is the typed-op replacement for the legacy fire-and-
  // forget BankRequest input. Every former `client.send(new BankRequest(...))`
  // site now calls state.refreshBank() instead — the typed-op promise
  // returns BankResponse, applyBankContents merges it into state, and
  // any handler error (rare — proximity check, etc.) is logged + ignored.
  state.refreshBank = () => {
    if (!state.connected) return;
    state.inputSeq++;
    void client.bank(new BankRequest({ sequence: state.inputSeq }))
      .then((resp) => {
        if (resp.error) {
          // Server-side rejection (not near station, no entity, etc.) —
          // silent for now; the UI's existing pollers retry on cadence.
          return;
        }
        applyBankContents(resp.contents);
      })
      .catch(() => { /* disconnect race */ });
  };

  client.onTransferResult((result: TransferResult) => {
    if (result.success) {
      const def = state.itemDefs.get(result.itemID);
      const name = def ? def.name : `Item #${result.itemID}`;
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
  });

  client.onEquipResult((result: EquipResult) => {
    if (result.success) {
      const isEquip = result.equippedItemID !== 0;
      const relevantId = isEquip ? result.equippedItemID : result.previousItemID;
      const def = state.itemDefs.get(relevantId);
      const name = def ? def.name : (relevantId ? `Item #${relevantId}` : "Unknown");
      const action = isEquip ? "Equipped" : "Unequipped";
      state.toasts.push({
        text: `${action} ${name}`,
        time: performance.now(),
      });

      // Apply the change to local state.equipment so the slot UI refreshes
      // immediately. PlayerOwnStateMsg (which carries equipment normally)
      // is gated server-side on StateActive, so docked equip changes don't
      // come through that channel — apply locally from the result message.
      // EquipSlot enum: 1=Weapon1, 2=Weapon2, 3=Shield, 4=Thruster.
      switch (result.slot) {
        case 1: state.equipment.weapon1 = result.equippedItemID; break;
        case 2: state.equipment.weapon2 = result.equippedItemID; break;
        case 3: state.equipment.shield = result.equippedItemID; break;
        case 4: state.equipment.thruster = result.equippedItemID; break;
      }
    } else {
      state.toasts.push({
        text: result.reason || "Equip failed",
        time: performance.now(),
      });
    }
  });

  // --- Docking ---
  client.onDockingState((ds: DockingState) => {
    const wasDocking = state.isDockingInProgress;
    state.isDockingInProgress = ds.docking;
    state.dockingProgress = ds.progress;
    state.dockingTotalTime = ds.totalTime;
    state.dockingStationId = ds.stationID;
    if (ds.docking && !wasDocking) {
      audio.play(SoundId.TractorBeam);
    }
  });

  client.onDocked(() => {
    state.isDocked = true;
    state.isDockingInProgress = false;
    state.dockingProgress = 0;
    state.cellMapOpen = false;
    state.bankPanelOpen = true;
    state.marketPanelOpen = true;
    state.cargoPanelOpen = true;
    document.body.classList.add("docked");
    syncCargoPanelLocation(true);
    // Keep myEntityId + the self-entity in state.entities. The server
    // parks the ship at station center and marks it Dormant — other
    // pilots' AoI broadcasts skip it (we vanish from the system view),
    // but the docked player's own AoI still includes it so the HUD can
    // continue to read position/cell/equipment from state.entities.get(myEntityId).
    state.refreshBank();
  });

  // --- Map / currency ---
  client.onMapData((mapData: MapData) => {
    state.mapStations = mapData.stations.map((s) => ({
      cellX: s.cellX,
      cellY: s.cellY,
      localX: s.localX,
      localY: s.localY,
      name: s.name,
    }));
  });

  client.onCurrencyUpdate((update: CurrencyUpdate) => {
    state.currencyBalances[update.currencyID] = Number(update.balance);
  });

  // BeamClip — server signals that the active beam channel is currently
  // blocked by a wall / asteroid at (hitX, hitY). The aim indicator
  // clamps the beam visual to this point so we don't draw through the
  // obstruction. Stale clip events decay after BEAM_CLIP_TTL_MS in
  // aim-indicator.ts.
  client.onBeamClip((clip: BeamClip) => {
    if (clip.caster !== state.myEntityId) return;
    state.beamClipX = clip.hitX;
    state.beamClipY = clip.hitY;
    state.beamClipExpiresAt = performance.now() + 300; // BEAM_CLIP_TTL_MS
  });

  client.connect();
}

// Re-export SETTLEMENT_CURRENCY_ID so existing UI imports that used to
// come from network.ts still work (no-op if already imported from state).
export { SETTLEMENT_CURRENCY_ID };
