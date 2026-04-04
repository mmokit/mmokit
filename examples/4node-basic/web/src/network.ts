import { BasicClient } from "../sdk/client.js";
import type { DeltaWorldUpdate } from "../sdk/entities.js";
import type { SpawnedMsg, CellTopologyMsg, CellInfo as PbCellInfo } from "@gen/basicpb/basic_pb.js";
import { state, type ClientEntity, type CellInfo } from "./state.js";
import { EntityMeshState } from "@gen/enginepb/engine_pb.js";
import { DT, setTickRate } from "./constants.js";

let showGameCallback: (() => void) | null = null;

export function onShowGame(cb: () => void): void {
  showGameCallback = cb;
}

function setStatus(msg: string): void {
  document.getElementById("status")!.textContent = msg;
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
    state.gridW = msg.gridW || 2;
    state.gridH = msg.gridH || 2;
    state.cellSize = msg.cellSize || 2000;
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
    state.gridW = msg.gridW || 2;
    state.gridH = msg.gridH || 2;
    state.cellSize = msg.baseCellSize || 2000;
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
    ent.worldX += ent.velX * DT;
    ent.worldY += ent.velY * DT;
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
