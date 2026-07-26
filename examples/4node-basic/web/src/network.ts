import { BasicClient } from "../sdk/client.js";
import { DebugInfo, PlayerEntityAssigned, WorldDelta } from "../sdk/broadcasts.js";
import { type DeltaWorldUpdate } from "../sdk/entities.js";
import { BasicDeltaDecoder } from "../sdk/delta-decoder.js";
import { MoveTargetMsg } from "../sdk/inputs.js";
import { state, setTickRate, resetNetworkTiming, type ClientEntity, type CellInfo } from "./state.js";
import { updateEntityFromServer } from "./interpolation.js";
import { pruneStaleOnFreshSnapshot } from "./reconcile.js";
import { recordDeletion } from "./replicationAudit.js";
import { mountEchoPanel } from "./echo_panel.js";
import { mountChatPanel } from "./chat_panel.js";
import { presenceOf } from "./debug-presence.js";
import { backendWsUrl } from "./config.js";

let showGameCallback: (() => void) | null = null;

export function onShowGame(cb: () => void): void {
  showGameCallback = cb;
}

function setStatus(msg: string): void {
  document.getElementById("status")!.textContent = msg;
}

export function connect(name: string): void {
  resetNetworkTiming();
  const client = new BasicClient({
    url: backendWsUrl(),
    onOpen: () => setStatus(`connected as ${name} — waiting for spawn...`),
    onClose: () => setStatus("disconnected"),
    onError: () => setStatus("connection error"),
  });

  // Mount the chat panel BEFORE connect so its server-event handlers
  // (especially onChatChannelsHydratedEvent) are registered before the
  // gateway's chat hook fires post-auth. Otherwise the hydration event
  // arrives before the client subscribes and silently drops in the
  // typed-event dispatcher.
  mountChatPanel(client);

  client.onPlayerEntityAssigned((msg: PlayerEntityAssigned) => {
    state.playerNetID = msg.entityNetID;
    setStatus("");
    showGameCallback?.();
    // Echo demo panel needs the player entity for some demo scenarios;
    // mount on spawn. Toggled with 'e'. Hidden by default.
    mountEchoPanel(client);
  });

  client.onServerConfig((msg) => {
    setTickRate(msg.tickRate);
  });

  // Typed DebugInfo carries per-player debug overlay data (gated by
  // DebugFlags). Currently topology + AoI radius; future debug
  // capabilities slot in as new typed-event fields. Empty Topology.cells
  // + AoIRadius==0 is the sentinel sent on revoke-to-zero — clear the
  // cached topology so the overlay vanishes until the player is
  // re-granted.
  client.onDebugInfo((msg: DebugInfo) => {
    if (msg.topology.cells.length > 0) {
      state.cells = msg.topology.cells.map((c): CellInfo => ({
        cellX: c.cellX, cellY: c.cellY,
        depth: c.depth, size: c.size,
        originX: c.originX, originY: c.originY,
        nodeId: c.nodeID,
      }));
    } else {
      state.cells = [];
    }
    state.aoiRadius = msg.aoIRadius;
  });

  // Per-tick entity-state delta arrives as a typed WorldDelta event (the
  // legacy SE_DELTA_WORLD_UPDATE protobuf envelope is gone). The reflection
  // codec hands us the raw delta-frame bytes via WorldDelta.body; decode
  // them with the SDK's BasicDeltaDecoder, which owns the per-baseline
  // state for the local connection.
  const deltaDecoder = new BasicDeltaDecoder();
  client.onWorldDelta((msg: WorldDelta) => {
    const update = deltaDecoder.decode(msg.body, msg.streamEpoch);
    if (update) applyWorldUpdate(update);
  });

  client.connect();
  state.client = client;
}

export function sendMoveTarget(): void {
  if (!state.playerNetID || !state.client) return;
  state.client.send(new MoveTargetMsg({
    x: state.moveTargetX,
    y: state.moveTargetY,
  }));
}

function applyWorldUpdate(update: DeltaWorldUpdate): void {
  state.tick = update.tick;
  state.lastTickTime = performance.now();

  // Every decoded entity carries its own ClusterClock-aligned
  // `producedAtMs` stamp; clockSync anchors on the freshest one
  // in the frame.
  const fresh = [...update.entered, ...update.updated];
  const arriveMs = performance.now();
  let maxStamp = 0;
  for (const entity of fresh) {
    if (entity.producedAtMs > maxStamp) maxStamp = entity.producedAtMs;
  }
  state.playback.observeFrame({
    seq: update.seq,
    freshSnapshot: update.freshSnapshot,
    streamChanged: update.streamChanged,
    arrivalTimeMs: arriveMs,
    producedAtMs: maxStamp > 0 ? maxStamp : undefined,
  });

  if (update.freshSnapshot) {
    pruneStaleOnFreshSnapshot(state.entities, fresh, state.playerNetID, state.replicationAudit, arriveMs);
  }

  // Resolve viewer's cell once per frame so presenceOf can compare
  // each remote entity's cell to ours. Cell-based (not host-based) so
  // replicas show in single-host clusters too — every cell owned by
  // the same host but the player is only "in" one of them.
  const myCell = resolveMyCell();

  // Merge entered + updated: both flow through updateEntityFromServer which
  // creates a ClientEntity on first sight or appends a sample to the ring.
  for (const raw of fresh) {
    const accepted = updateEntityFromServer(
      state.entities,
      raw,
      raw.producedAtMs,
      state.replicationAudit,
      arriveMs,
      update.streamChanged,
    );
    // A late ex-authority sample must not influence derived presence or any
    // other non-buffered state after the interpolation gate rejected it.
    if (!accepted) continue;
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
    recordDeletion(state.replicationAudit, arriveMs, netID, "removed");
    state.entities.delete(netID);
  }
  for (const netID of update.exited) {
    recordDeletion(state.replicationAudit, arriveMs, netID, "exited");
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
