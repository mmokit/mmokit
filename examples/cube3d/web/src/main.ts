import { Cube3dClient } from "../sdk/client.js";
import { FlyInput } from "../sdk/inputs.js";
import type { AnyEntity } from "../sdk/entities.js";
import { Scene3D, type Renderable } from "./render3d";
import { axesFromKeys, applyLook, NO_KEYS, type KeyState, type Look } from "./flycontrol";

const canvas = document.getElementById("view") as HTMLCanvasElement;
const status = document.getElementById("status") as HTMLElement;
const scene = new Scene3D(canvas);

const keys: KeyState = { ...NO_KEYS };
let look: Look = { yaw: 0, pitch: -0.2 };
let myNetID: number | null = null;

/** Latest known state per netID, keyed as the server sends it. */
const world = new Map<number, AnyEntity>();

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
addEventListener("mousemove", (e) => {
  if (document.pointerLockElement !== canvas) return;
  look = applyLook(look, e.movementX, e.movementY);
});
addEventListener("resize", () => scene.resize(innerWidth, innerHeight));
scene.resize(innerWidth, innerHeight);

const client = new Cube3dClient({
  url: `ws://${location.host}/ws`,
  onOpen: () => (status.textContent = "connected — click to look, WASD + space/shift to fly"),
  onClose: () => (status.textContent = "disconnected"),
  onSchemaMismatch: (server, mine) =>
    (status.textContent = `schema mismatch: server ${server}, client ${mine} — regenerate the SDK`),
});

client.onPlayerEntityAssigned((msg) => { myNetID = msg.netID; });

client.onWorldDelta((msg) => {
  if (msg.freshSnapshot) world.clear();
  for (const e of msg.entered) world.set(e.netID, e);
  for (const e of msg.updated) world.set(e.netID, e);
  for (const id of msg.removed) world.delete(id);
  for (const id of msg.exited) world.delete(id);
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
  const renderables: Renderable[] = [];
  for (const e of world.values()) {
    const any = e as any;
    if (any.rot === undefined) continue;
    renderables.push({
      netID: any.netID,
      worldX: any.worldX,
      worldY: any.worldY,
      worldZ: any.worldZ,
      rot: any.rot,
      size: any.width ? any.width / 2 : 10,
      isViewer: any.netID === myNetID,
    });
  }
  scene.sync(renderables);

  const me = myNetID !== null ? (world.get(myNetID) as any) : null;
  if (me) scene.setCamera(me.worldX, me.worldY, me.worldZ, look);
  scene.render();
  requestAnimationFrame(frame);
}
requestAnimationFrame(frame);
