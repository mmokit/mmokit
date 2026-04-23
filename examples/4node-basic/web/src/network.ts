import { BasicClient } from "../sdk/client.js";
import type { DeltaWorldUpdate } from "../sdk/entities.js";
import { state, setTickRate, type ClientEntity, type CellInfo } from "./state.js";
import { EntityMeshState, type SpawnedMsg, type CellTopologyMsg, type CellInfo as PbCellInfo } from "@gen/enginepb/engine_pb.js";
import { observeServerTime, } from "./clockSync.js";
import { updateEntityFromServer } from "./interpolation.js";

let showGameCallback: (() => void) | null = null;

export function onShowGame(cb: () => void): void {
  showGameCallback = cb;
}

function setStatus(msg: string): void {
  document.getElementById("status")!.textContent = msg;
}

function showDebugToggle(): void {
  const btn = document.createElement("button");
  btn.id = "debugToggle";
  btn.textContent = "Show Debug";
  btn.style.cssText = "position:fixed;top:8px;right:8px;z-index:10;padding:4px 10px;font:11px monospace;background:#222;color:#aaa;border:1px solid #444;border-radius:3px;cursor:pointer;opacity:0.7";
  btn.addEventListener("click", () => {
    state.debugVisible = !state.debugVisible;
    btn.textContent = state.debugVisible ? "Hide Debug" : "Show Debug";
    btn.style.opacity = state.debugVisible ? "1" : "0.7";
  });
  document.body.appendChild(btn);
}

export function connect(name: string): void {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const client = new BasicClient({
    url: `${proto}//${location.host}/ws`,
    onOpen: () => {
      setStatus("connected — logging in...");
      client.sendLogin({ name });
    },
    onClose: () => setStatus("disconnected"),
    onError: () => setStatus("connection error"),
  });

  client.onPlayerSpawned((msg: SpawnedMsg) => {
    state.playerNetID = msg.entityNetId;
    setStatus("");
    showGameCallback?.();
  });

  client.onServerConfig((msg) => {
    setTickRate(msg.tickRate);
  });

  client.onCellTopology((msg: CellTopologyMsg) => {
    state.cells = msg.cells.map((c: PbCellInfo): CellInfo => ({
      cellX: c.cellX, cellY: c.cellY,
      depth: c.depth, size: c.size,
      originX: c.originX, originY: c.originY,
      nodeId: c.nodeId,
    }));
    if (!state.debugAvailable) {
      state.debugAvailable = true;
      showDebugToggle();
    }
  });

  client.onDeltaWorldUpdate(applyWorldUpdate);

  client.connect();
  state.client = client;
}

export function sendMoveTarget(): void {
  if (!state.playerNetID || !state.client) return;
  state.inputSeq++;
  state.client.sendMoveTarget({
    targetX: state.moveTargetX,
    targetY: state.moveTargetY,
    sequence: state.inputSeq,
  });
}

function applyWorldUpdate(update: DeltaWorldUpdate): void {
  state.tick = update.tick;
  state.lastTickTime = performance.now();

  // TODO(K1): clockSync + interpolation will be rewired to the
  // per-entity producedAtMs stamp that the decoder already attaches
  // to each entity. For now, synthesize a frame-level "serverTimeMs"
  // from the newest producedAtMs across the frame so the existing
  // clockSync / interpolation APIs keep working end-to-end.
  const fresh = [...update.entered, ...update.updated];
  let frameStampMs = 0;
  for (const raw of fresh) {
    if (raw.producedAtMs > frameStampMs) frameStampMs = raw.producedAtMs;
  }
  if (frameStampMs > 0) {
    observeServerTime(state.clockSync, frameStampMs, performance.now());
  }

  // Merge entered + updated: both flow through updateEntityFromServer which
  // creates a ClientEntity on first sight or appends a sample to the ring.
  for (const raw of fresh) {
    updateEntityFromServer(state.entities, raw, raw.producedAtMs);
    // Stamp isReplica/isGhost from meshState (present on all AnyEntity).
    const ent = state.entities.get(raw.netID)!;
    ent.isReplica = raw.meshState === EntityMeshState.EMS_REPLICA;
    ent.isGhost = raw.meshState === EntityMeshState.EMS_GHOST;
    // Preserve name across delta updates (delta may omit name after first frame).
    if (!raw.name && ent.name) {
      // name already retained from the spread in updateEntityFromServer
    } else if (raw.name) {
      ent.name = raw.name;
    }
  }

  // removed = entity died/despawned; exited = left our AoI. Both drop from local map.
  for (const netID of update.removed) {
    state.entities.delete(netID);
  }
  for (const netID of update.exited) {
    state.entities.delete(netID);
  }

  // Check player arrival (stop prediction when we stop moving).
  const player = state.playerNetID ? state.entities.get(state.playerNetID) : null;
  if (player) checkPlayerArrival(player);
}

function checkPlayerArrival(ent: ClientEntity): void {
  if (Math.abs(ent.velX) < 1 && Math.abs(ent.velY) < 1 && state.moveTargetActive) {
    state.moveTargetActive = false;
    state.predictionActive = false;
  }
}
