import { BasicClient } from "../sdk/client.js";
import type { DeltaWorldUpdate } from "../sdk/entities.js";
import { state, setTickRate, type ClientEntity, type CellInfo } from "./state.js";
import { EntityMeshState, type SpawnedMsg, type CellTopologyMsg, type CellInfo as PbCellInfo } from "@gen/enginepb/engine_pb.js";

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
  state.viewerX = update.viewerX;
  state.viewerY = update.viewerY;

  // Advance existing entities: current -> prev, dead-reckon position.
  for (const [, ent] of state.entities) {
    ent.prevX = ent.worldX;
    ent.prevY = ent.worldY;
    ent.worldX += ent.velX * state.dt;
    ent.worldY += ent.velY * state.dt;
  }

  // Apply entered (new) entities.
  for (const raw of update.entered) {
    const prev = state.entities.get(raw.netID);
    const ent: ClientEntity = {
      ...raw,
      prevX: prev ? prev.prevX : raw.worldX,
      prevY: prev ? prev.prevY : raw.worldY,
      isReplica: raw.meshState === EntityMeshState.EMS_REPLICA,
      isGhost: raw.meshState === EntityMeshState.EMS_GHOST,
      name: raw.name || (prev ? prev.name : ""),
    };
    state.entities.set(raw.netID, ent);

    if (raw.netID === state.playerNetID) {
      checkPlayerArrival(ent);
    }
  }

  // Apply updated (delta) entities.
  for (const raw of update.updated) {
    const prev = state.entities.get(raw.netID);
    const ent: ClientEntity = {
      ...raw,
      prevX: prev ? prev.prevX : raw.worldX,
      prevY: prev ? prev.prevY : raw.worldY,
      isReplica: raw.meshState === EntityMeshState.EMS_REPLICA,
      isGhost: raw.meshState === EntityMeshState.EMS_GHOST,
      name: raw.name || (prev ? prev.name : ""),
    };
    state.entities.set(raw.netID, ent);

    if (raw.netID === state.playerNetID) {
      checkPlayerArrival(ent);
    }
  }

  // removed = entity died/despawned; exited = left our AoI. Both drop from local map.
  for (const netID of update.removed) {
    state.entities.delete(netID);
  }
  for (const netID of update.exited) {
    state.entities.delete(netID);
  }
}

function checkPlayerArrival(ent: ClientEntity): void {
  if (Math.abs(ent.velX) < 1 && Math.abs(ent.velY) < 1 && state.moveTargetActive) {
    state.moveTargetActive = false;
    state.predictionActive = false;
  }
}
