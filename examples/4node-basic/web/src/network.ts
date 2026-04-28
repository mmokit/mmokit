import { BasicClient } from "../sdk/client.js";
import { type DeltaWorldUpdate } from "../sdk/entities.js";
import { state, setTickRate, type ClientEntity, type CellInfo } from "./state.js";
import { ServerEventCode, type SpawnedMsg, type CellInfo as PbCellInfo, type DebugInfoMsg, DebugInfoMsgSchema } from "@gen/enginepb/engine_pb.js";
import { fromBinary } from "@bufbuild/protobuf";
import { observeFrameStamps } from "./clockSync.js";
import { updateEntityFromServer } from "./interpolation.js";
import { pruneStaleOnFreshSnapshot } from "./reconcile.js";
import { mountEchoPanel } from "./echo_panel.js";
import { presenceOf } from "./debug-presence.js";

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
  // capabilities slot in as new optional fields. Decoded directly via
  // fromBinary so we get the typed DebugInfoMsg shape.
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
    // aoiRadius is optional in DebugInfoMsg; only update when present.
    if (msg.aoiRadius !== undefined) {
      state.aoiRadius = msg.aoiRadius;
    }
    // Empty payload (no topology, no aoiRadius) is the sentinel sent on
    // revoke-to-zero — clear the cached topology so the overlay vanishes
    // until the player is re-granted.
    if (!msg.topology && msg.aoiRadius === undefined) {
      state.cells = [];
      state.aoiRadius = 0;
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

  // Every decoded entity carries its own ClusterClock-aligned
  // `producedAtMs` stamp; clockSync anchors on the freshest one
  // in the frame.
  const fresh = [...update.entered, ...update.updated];
  observeFrameStamps(state.clockSync, fresh, performance.now());

  if (update.freshSnapshot) {
    pruneStaleOnFreshSnapshot(state.entities, fresh, state.playerNetID);
  }

  // Resolve viewer's host once per frame: find the cell containing the
  // local player's position and read its nodeId. Fed into presenceOf for
  // every other entity so replicas (entities owned by remote hosts) get
  // the R marker in the debug overlay.
  const myHost = resolveMyHost();

  // Merge entered + updated: both flow through updateEntityFromServer which
  // creates a ClientEntity on first sight or appends a sample to the ring.
  for (const raw of fresh) {
    updateEntityFromServer(state.entities, raw, raw.producedAtMs);
    const ent = state.entities.get(raw.netID)!;
    // Derive presence client-side from topology + position. LOCAL when
    // the entity sits in a cell owned by our own host; REPLICA otherwise.
    // GHOST is no longer wire-visible — kept on the client type for
    // back-compat with the renderer's branching but always false.
    if (state.cells.length > 0 && myHost) {
      const p = presenceOf({ worldX: raw.worldX, worldY: raw.worldY }, { cells: state.cells } as unknown as Parameters<typeof presenceOf>[1], myHost);
      ent.isReplica = p === "REPLICA";
    } else {
      ent.isReplica = false;
    }
    ent.isGhost = false;
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

// resolveMyHost returns the nodeId of the cell containing the local
// player's position, or "" if topology is empty / player not yet
// spawned. Used by applyWorldUpdate to feed presenceOf for every
// remote entity so replicas get the R marker in the debug overlay.
function resolveMyHost(): string {
  if (state.cells.length === 0 || !state.playerNetID) return "";
  const me = state.entities.get(state.playerNetID);
  if (!me) return "";
  for (const c of state.cells) {
    const x0 = c.originX;
    const y0 = c.originY;
    const x1 = x0 + c.size;
    const y1 = y0 + c.size;
    if (me.worldX >= x0 && me.worldX < x1 && me.worldY >= y0 && me.worldY < y1) {
      return c.nodeId;
    }
  }
  return "";
}

function checkPlayerArrival(ent: ClientEntity): void {
  if (Math.abs(ent.velX) < 1 && Math.abs(ent.velY) < 1 && state.moveTargetActive) {
    state.moveTargetActive = false;
  }
}
