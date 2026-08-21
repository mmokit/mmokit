import { Cube3dClient } from "../sdk/client.js";
import { Cube3dDeltaDecoder } from "../sdk/delta-decoder.js";
import type { WorldDelta, PlayerEntityAssigned } from "../sdk/broadcasts.js";
import { FlyInput } from "../sdk/inputs.js";
import { AdaptivePlaybackController } from "../sdk/_core/playback-controller.js";
import { classify, cellAt, type CellRect, type EntityClass, type Topology } from "./topology";
import {
  updateEntityFromServer,
  interpolateEntities,
  RENDER_DELAY_MS,
  type RenderEntity,
  type ServerEntity,
} from "./interpolation";
import { Scene3D, type Renderable } from "./render3d";
import { axesFromKeys, applyLook, NO_KEYS, type KeyState, type Look } from "./flycontrol";
import { FpsMeter, countByClass, hudRows, cellLegend, paintHud } from "./hud";

const canvas = document.getElementById("view") as HTMLCanvasElement;
const status = document.getElementById("status") as HTMLElement;
const help = document.getElementById("help") as HTMLElement;
const hud = document.getElementById("hud") as HTMLElement;
const legend = document.getElementById("cells") as HTMLElement;
const scene = new Scene3D(canvas);

const keys: KeyState = { ...NO_KEYS };
// Pitched down enough that the cube field is in frame from the spawn height
// rather than 1300 units away on the horizon.
let look: Look = { yaw: 0, pitch: -0.6 };
let myNetID: number | null = null;
let topology: Topology | null = null;
let myCell: CellRect | null = null;
let aoiRadius = 0;
let lastSeq: number | null = null;
let connected = false;
const fps = new FpsMeter();

// On by default, and the reason cube3d exists: this example is a framework
// fixture you look at, so the state of the mesh is the content. F3 matches
// what the genre trained everyone to press.
let debugVisible = true;

/** Per-entity sample ring plus its interpolated render pose. */
const world = new Map<number, RenderEntity>();

// Rendering happens at producer-clock time minus a delay, not at the newest
// snapshot: interpolation can only smooth between samples it already holds.
const playback = new AdaptivePlaybackController({
  tickIntervalMs: 50,
  minDelayMs: RENDER_DELAY_MS,
  maxDelayMs: 300,
});

const KEY_MAP: Record<string, keyof KeyState> = {
  KeyW: "forward", KeyS: "back", KeyA: "left", KeyD: "right",
  Space: "up", ShiftLeft: "down",
};

addEventListener("keydown", (e) => {
  if (e.code === "F3") {
    debugVisible = !debugVisible;
    scene.setDebugVisible(debugVisible);
    hud.classList.toggle("hidden", !debugVisible);
    legend.classList.toggle("hidden", !debugVisible);
    e.preventDefault();
    return;
  }
  const k = KEY_MAP[e.code];
  if (k) { keys[k] = true; e.preventDefault(); }
});
addEventListener("keyup", (e) => {
  const k = KEY_MAP[e.code];
  if (k) { keys[k] = false; e.preventDefault(); }
});

canvas.addEventListener("click", () => canvas.requestPointerLock());
// The legend dims until the pointer is captured, because until then the keys
// genuinely do nothing and there is no other signal saying so.
document.addEventListener("pointerlockchange", () => {
  help.classList.toggle("inactive", document.pointerLockElement !== canvas);
});
addEventListener("mousemove", (e) => {
  if (document.pointerLockElement !== canvas) return;
  look = applyLook(look, e.movementX, e.movementY);
});
addEventListener("resize", () => scene.resize(innerWidth, innerHeight));
scene.resize(innerWidth, innerHeight);

const client = new Cube3dClient({
  url: `ws://${location.host}/ws`,
  onOpen: () => { connected = true; status.textContent = "connected"; },
  onClose: () => { connected = false; status.textContent = "disconnected"; },
  onSchemaMismatch: (server, mine) =>
    (status.textContent = `schema mismatch: server ${server}, client ${mine} — regenerate the SDK`),
});

client.onPlayerEntityAssigned((msg: PlayerEntityAssigned) => {
  myNetID = msg.entityNetID;
});

// Requires the "topology" debug grant, which cube3d gives every viewer. The
// grid and the entity colouring both come from this; without it the client
// would be guessing at both.
client.onDebugInfo((msg) => {
  topology = { cells: msg.topology.cells, baseCellSize: msg.topology.baseCellSize };
  aoiRadius = msg.aoIRadius;
  scene.setTopology(topology);
  scene.setAoIRadius(aoiRadius);
});

// WorldDelta is the typed EVENT carrying raw frame bytes; the decoder owns
// the per-baseline state for this connection and turns them into entities.
const deltaDecoder = new Cube3dDeltaDecoder();
client.onWorldDelta((msg: WorldDelta) => {
  const update = deltaDecoder.decode(msg.body, msg.streamEpoch);
  if (!update) return;
  lastSeq = update.seq;

  const arriveMs = performance.now();
  let newest = 0;
  for (const e of [...update.entered, ...update.updated]) {
    if (e.producedAtMs > newest) newest = e.producedAtMs;
  }
  // Every frame is observed, including ones with no entity stamp, so
  // sequence gaps stay meaningful to the delay estimator.
  playback.observeFrame({
    seq: update.seq,
    freshSnapshot: update.freshSnapshot,
    streamChanged: update.streamChanged,
    arrivalTimeMs: arriveMs,
    producedAtMs: newest > 0 ? newest : undefined,
  });

  if (update.freshSnapshot) {
    // PRUNE, never clear. A fresh snapshot arrives on every cross-cell
    // handoff, and clearing destroys each surviving entity's interpolation
    // ring — so every entity restarts from a single sample and visibly jumps.
    // Delete only what this frame says is gone.
    const visible = new Set<number>();
    for (const e of [...update.entered, ...update.updated]) visible.add(e.netID);
    if (myNetID !== null) visible.add(myNetID);
    for (const id of [...world.keys()]) {
      if (!visible.has(id)) world.delete(id);
    }
  }
  for (const e of [...update.entered, ...update.updated]) {
    const any = e as any;
    if (any.rot === undefined) continue; // no orientation: not a 3D entity
    updateEntityFromServer(world, any as ServerEntity, e.producedAtMs, update.freshSnapshot);
  }
  for (const id of update.removed) world.delete(id);
  for (const id of update.exited) world.delete(id);
});

client.connect();

// Input is sent on a fixed cadence rather than per keystroke: the server reads
// axes as a continuous intent, and yaw/pitch are ABSOLUTE, so a dropped
// datagram costs one frame of staleness rather than a permanently wrong camera.
const INPUT_HZ = 20;
setInterval(() => {
  if (!client.connected) return;
  const axes = axesFromKeys(keys);
  client.send(new FlyInput({
    forward: axes.forward,
    strafe: axes.strafe,
    lift: axes.lift,
    yaw: look.yaw,
    pitch: look.pitch,
  }));
}, 1000 / INPUT_HZ);

function frame(): void {
  interpolateEntities(world, playback, performance.now());

  const renderables: Renderable[] = [];
  const classes: EntityClass[] = [];
  for (const ent of world.values()) {
    const any = ent.current as any;
    const kind = classify(topology, myCell, any.netID, ent.renderX, ent.renderY, myNetID);
    classes.push(kind);
    renderables.push({
      netID: any.netID,
      // The INTERPOLATED pose, not ent.current — rendering the raw snapshot
      // is what made this jitter in 50 ms steps.
      worldX: ent.renderX,
      worldY: ent.renderY,
      worldZ: ent.renderZ,
      rot: ent.renderQuat,
      size: any.width ? any.width / 2 : 10,
      kind,
    });
  }
  scene.sync(renderables);

  const me = myNetID !== null ? world.get(myNetID) : undefined;
  // Recomputed from the viewer's own interpolated position rather than from
  // CellChange, so it stays correct between handoff events.
  myCell = me && topology ? cellAt(topology, me.renderX, me.renderY) : myCell;
  // The camera follows the interpolated pose too, or your own view judders
  // while everything in it is smooth.
  if (me) scene.setCamera(me.renderX, me.renderY, me.renderZ, look);
  scene.render();

  // Sampled every frame, not only while the overlay is up: an FPS meter that
  // only counts the frames you are watching reads high for the first second
  // after you press F3.
  const rate = fps.sample(performance.now());
  if (debugVisible) {
    const m = playback.metrics;
    paintHud(hud, hudRows({
      connected,
      fps: rate,
      seq: lastSeq,
      delayMs: m.currentDelayMs,
      jitterMs: m.jitterMs,
      lossRate: m.lossRate,
      myNetID,
      myCell,
      pos: me ? { x: me.renderX, y: me.renderY, z: me.renderZ } : null,
      counts: countByClass(classes),
      topology,
      aoiRadius,
    }));
    // Repainted per frame rather than on the topology push: the ▸ marks the
    // cell YOU are in, and that changes as you fly, not as the mesh changes.
    paintHud(legend, cellLegend(topology, myCell));
  }
  requestAnimationFrame(frame);
}
requestAnimationFrame(frame);
