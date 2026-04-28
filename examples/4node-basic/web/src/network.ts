import { BasicClient } from "../sdk/client.js";
import { type DeltaWorldUpdate } from "../sdk/entities.js";
import { state, setTickRate, type ClientEntity, type CellInfo } from "./state.js";
import { EntityMeshState, ServerEventCode, type SpawnedMsg, type CellInfo as PbCellInfo, type DebugInfoMsg, DebugInfoMsgSchema } from "@gen/enginepb/engine_pb.js";
import { fromBinary } from "@bufbuild/protobuf";
import { observeFrameStamps } from "./clockSync.js";
import { updateEntityFromServer } from "./interpolation.js";
import { pruneStaleOnFreshSnapshot } from "./reconcile.js";
import { mountEchoPanel } from "./echo_panel.js";

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
    // Mount the echo demo panel once the session is authenticated.
    // Toggled with 'e'. Hidden by default.
    mountEchoPanel(client.rawTransport);
  });

  client.onServerConfig((msg) => {
    setTickRate(msg.tickRate);
  });

  // SE_DEBUG_INFO carries per-player debug overlay data (gated by
  // DebugFlags). Currently topology + AoI radius; future debug
  // capabilities slot in as new optional fields. Listen via
  // onRawEvent so we can decode DebugInfoMsg directly — the SDK's
  // onCellTopology helper is stale (code 14 was renamed from
  // SE_CELL_TOPOLOGY to SE_DEBUG_INFO and the payload swapped to
  // DebugInfoMsg; SDK regen happens in Task 9).
  client.onRawEvent((code, data) => {
    if (code !== ServerEventCode.SE_DEBUG_INFO) return;
    const msg: DebugInfoMsg = fromBinary(DebugInfoMsgSchema, data);
    if (msg.topology) {
      state.cells = msg.topology.cells.map((c: PbCellInfo): CellInfo => ({
        cellX: c.cellX, cellY: c.cellY,
        depth: c.depth, size: c.size,
        originX: c.originX, originY: c.originY,
        nodeId: c.nodeId,
      }));
      if (!state.debugAvailable) {
        state.debugAvailable = true;
        showDebugToggle();
      }
    }
    // msg.aoiRadius is decoded but not yet consumed — the renderer
    // still reads from per-entity ent.aoIRadius. Task 9 wires this
    // into a renderer state slot when the per-entity field is
    // removed.
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

  // Every decoded entity carries its own ClusterClock-aligned
  // `producedAtMs` stamp; clockSync anchors on the freshest one
  // in the frame.
  const fresh = [...update.entered, ...update.updated];
  observeFrameStamps(state.clockSync, fresh, performance.now());

  if (update.freshSnapshot) {
    pruneStaleOnFreshSnapshot(state.entities, fresh, state.playerNetID);
  }

  // Merge entered + updated: both flow through updateEntityFromServer which
  // creates a ClientEntity on first sight or appends a sample to the ring.
  for (const raw of fresh) {
    updateEntityFromServer(state.entities, raw, raw.producedAtMs);
    // Stamp isReplica/isGhost from presence (only present on bundles that
    // include *mmokit.DebugInfo — Player has it; Bot does not).
    const ent = state.entities.get(raw.netID)!;
    const presence = (raw as { presence?: number }).presence;
    ent.isReplica = presence === EntityMeshState.EMS_REPLICA;
    ent.isGhost = presence === EntityMeshState.EMS_GHOST;
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

  // Check player arrival (clear move-target crosshair on stop).
  const player = state.playerNetID ? state.entities.get(state.playerNetID) : null;
  if (player) checkPlayerArrival(player);
}

function checkPlayerArrival(ent: ClientEntity): void {
  if (Math.abs(ent.velX) < 1 && Math.abs(ent.velY) < 1 && state.moveTargetActive) {
    state.moveTargetActive = false;
  }
}
