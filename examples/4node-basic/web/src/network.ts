import { BasicClient } from "../sdk/client.js";
import { type DeltaWorldUpdate } from "../sdk/entities.js";
import { MoveTargetMsg } from "../sdk/inputs.js";
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

export function connect(name: string): void {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const client = new BasicClient({
    url: `${proto}//${location.host}/ws`,
    onOpen: () => setStatus(`connected as ${name} — waiting for spawn...`),
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
  state.client.send(new MoveTargetMsg({
    sequence: state.inputSeq,
    targetX: state.moveTargetX,
    targetY: state.moveTargetY,
  }));
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

  // Resolve viewer's cell once per frame so presenceOf can compare
  // each remote entity's cell to ours. Cell-based (not host-based) so
  // replicas show in single-host clusters too — every cell owned by
  // the same host but the player is only "in" one of them.
  const myCell = resolveMyCell();

  // Merge entered + updated: both flow through updateEntityFromServer which
  // creates a ClientEntity on first sight or appends a sample to the ring.
  for (const raw of fresh) {
    updateEntityFromServer(state.entities, raw, raw.producedAtMs);
    const ent = state.entities.get(raw.netID)!;
    // Derive presence client-side from topology + viewer cell. LOCAL
    // when the entity sits in our cell; REPLICA when it's mirrored
    // from a neighboring cell. GHOST is no longer wire-visible —
    // kept on the client type for back-compat with the renderer's
    // branching but always false.
    //
    // The viewer's own entity is always LOCAL from their viewport: on
    // the boundary-crossing frame myCell still reflects the previous
    // tick's player position (resolveMyCell ran before this loop wrote
    // the new sample), so a naive check would briefly classify the
    // player as REPLICA — a 1-frame R marker flicker on every cross-cell
    // walk. Short-circuit before presenceOf to avoid it.
    if (raw.netID === state.playerNetID) {
      ent.isReplica = false;
    } else if (state.cells.length > 0 && myCell) {
      const p = presenceOf(
        { worldX: raw.worldX, worldY: raw.worldY },
        { cells: state.cells } as unknown as Parameters<typeof presenceOf>[1],
        myCell,
      );
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

// resolveMyCell returns the {cellX, cellY, depth} of the cell
// containing the local player, or null if topology is empty / player
// not yet spawned. Fed to presenceOf so entities outside the player's
// cell get the R marker in the debug overlay — works regardless of
// host count (single-host runs only have one nodeId, so a host-based
// derive can't distinguish replicas).
function resolveMyCell(): { cellX: number; cellY: number; depth: number } | null {
  if (state.cells.length === 0 || !state.playerNetID) return null;
  const me = state.entities.get(state.playerNetID);
  if (!me) return null;
  for (const c of state.cells) {
    const x0 = c.originX;
    const y0 = c.originY;
    const x1 = x0 + c.size;
    const y1 = y0 + c.size;
    if (me.worldX >= x0 && me.worldX < x1 && me.worldY >= y0 && me.worldY < y1) {
      return { cellX: c.cellX, cellY: c.cellY, depth: c.depth };
    }
  }
  return null;
}

function checkPlayerArrival(ent: ClientEntity): void {
  if (Math.abs(ent.velX) < 1 && Math.abs(ent.velY) < 1 && state.moveTargetActive) {
    state.moveTargetActive = false;
  }
}
