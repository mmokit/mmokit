import { Cube3dClient } from "../sdk/client.js";
import { Cube3dDeltaDecoder } from "../sdk/delta-decoder.js";
import type { WorldDelta, PlayerEntityAssigned } from "../sdk/broadcasts.js";
import { FlyInput } from "../sdk/inputs.js";
import { AdaptivePlaybackController } from "../sdk/_core/playback-controller.js";
import {
  updateEntityFromServer,
  interpolateEntities,
  RENDER_DELAY_MS,
  type RenderEntity,
  type ServerEntity,
} from "./interpolation";
import { Scene3D, type Renderable } from "./render3d";
import { axesFromKeys, applyLook, NO_KEYS, type KeyState, type Look } from "./flycontrol";

const canvas = document.getElementById("view") as HTMLCanvasElement;
const status = document.getElementById("status") as HTMLElement;
const help = document.getElementById("help") as HTMLElement;
const scene = new Scene3D(canvas);

const keys: KeyState = { ...NO_KEYS };
// Pitched down enough that the cube field is in frame from the spawn height
// rather than 1300 units away on the horizon.
let look: Look = { yaw: 0, pitch: -0.6 };
let myNetID: number | null = null;

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
  onOpen: () => (status.textContent = "connected"),
  onClose: () => (status.textContent = "disconnected"),
  onSchemaMismatch: (server, mine) =>
    (status.textContent = `schema mismatch: server ${server}, client ${mine} — regenerate the SDK`),
});

client.onPlayerEntityAssigned((msg: PlayerEntityAssigned) => {
  myNetID = msg.entityNetID;
});

// WorldDelta is the typed EVENT carrying raw frame bytes; the decoder owns
// the per-baseline state for this connection and turns them into entities.
const deltaDecoder = new Cube3dDeltaDecoder();
client.onWorldDelta((msg: WorldDelta) => {
  const update = deltaDecoder.decode(msg.body, msg.streamEpoch);
  if (!update) return;

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

  if (update.freshSnapshot) world.clear();
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
  for (const ent of world.values()) {
    const any = ent.current as any;
    renderables.push({
      netID: any.netID,
      // The INTERPOLATED pose, not ent.current — rendering the raw snapshot
      // is what made this jitter in 50 ms steps.
      worldX: ent.renderX,
      worldY: ent.renderY,
      worldZ: ent.renderZ,
      rot: ent.renderQuat,
      size: any.width ? any.width / 2 : 10,
      isViewer: any.netID === myNetID,
    });
  }
  scene.sync(renderables);

  const me = myNetID !== null ? world.get(myNetID) : undefined;
  // The camera follows the interpolated pose too, or your own view judders
  // while everything in it is smooth.
  if (me) scene.setCamera(me.renderX, me.renderY, me.renderZ, look);
  scene.render();
  requestAnimationFrame(frame);
}
requestAnimationFrame(frame);
