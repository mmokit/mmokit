# 4node-basic Vite + TypeScript Client

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert the 4node-basic inline-JS debug client to a Vite+TypeScript project using the generated SDK.

**Architecture:** Six TypeScript modules (state, network, input, interpolation, renderer, main) replace ~1050 lines of inline JS. The generated SDK (`web/sdk/`) handles all protobuf encoding/decoding and binary delta world updates. Raw Canvas2D rendering, no framework.

**Tech Stack:** Vite 6, TypeScript 5, @bufbuild/protobuf 2.11, Canvas2D

**Spec:** `docs/superpowers/specs/2026-04-02-4node-basic-vite-ts-design.md`

---

### Task 1: Vite project scaffold — config files

**Files:**
- Create: `examples/4node-basic/web/package.json`
- Create: `examples/4node-basic/web/tsconfig.json`
- Create: `examples/4node-basic/web/vite.config.ts`
- Create: `examples/4node-basic/Makefile`

- [ ] **Step 1: Create `package.json`**

```json
{
  "name": "4node-basic-web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build"
  },
  "dependencies": {
    "@bufbuild/protobuf": "^2.11.0"
  },
  "devDependencies": {
    "typescript": "^5.7.0",
    "vite": "^6.0.0"
  }
}
```

- [ ] **Step 2: Create `tsconfig.json`**

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "paths": {
      "@gen/enginepb/engine_pb.js": ["../../../gen/es/enginepb/engine_pb"],
      "@gen/basicpb/basic_pb.js": ["../../../gen/es/basicpb/basic_pb"]
    }
  },
  "include": ["src", "sdk"]
}
```

- [ ] **Step 3: Create `vite.config.ts`**

```ts
import { defineConfig } from "vite";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  server: {
    proxy: {
      "/ws": {
        target: "ws://localhost:8080",
        ws: true,
      },
    },
    fs: {
      allow: ["../../.."],
    },
  },
  resolve: {
    alias: {
      "@gen/enginepb/engine_pb.js": path.resolve(__dirname, "../../../gen/es/enginepb/engine_pb.js"),
      "@gen/basicpb/basic_pb.js": path.resolve(__dirname, "../../../gen/es/basicpb/basic_pb.js"),
    },
    dedupe: ["@bufbuild/protobuf"],
  },
});
```

- [ ] **Step 4: Create `examples/4node-basic/Makefile`**

```makefile
ROOT := $(shell git rev-parse --show-toplevel)

.PHONY: build run dev clean

build:
	cd $(ROOT) && go build -o bin/4node-basic ./examples/4node-basic

run: build
	cd $(ROOT) && ./bin/4node-basic

dev: build
	trap 'kill 0' INT TERM EXIT; \
	(cd web && exec bun run dev) & \
	cd $(ROOT) && exec ./bin/4node-basic

clean:
	rm -f $(ROOT)/bin/4node-basic
```

- [ ] **Step 5: Install dependencies**

Run: `cd examples/4node-basic/web && bun install`
Expected: lockfile created, node_modules populated

- [ ] **Step 6: Commit**

```bash
git add examples/4node-basic/web/package.json examples/4node-basic/web/tsconfig.json examples/4node-basic/web/vite.config.ts examples/4node-basic/Makefile examples/4node-basic/web/bun.lockb
git commit -m "feat(4node-basic): add Vite + TypeScript project scaffold"
```

---

### Task 2: HTML shell and CSS

**Files:**
- Create: `examples/4node-basic/web/style.css`
- Rewrite: `examples/4node-basic/web/index.html`

- [ ] **Step 1: Create `style.css`**

Extract CSS from the current `index.html` inline styles:

```css
* { box-sizing: border-box; margin: 0; padding: 0; }

body {
  background: #111;
  color: #ccc;
  font-family: 'Courier New', monospace;
  height: 100vh;
  overflow: hidden;
  display: flex;
  align-items: center;
  justify-content: center;
}

#login {
  background: #1a1a2e;
  border: 1px solid #333;
  border-radius: 6px;
  padding: 32px 40px;
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 300px;
  box-shadow: 0 0 30px rgba(100,150,255,0.15);
}

#login h1 {
  color: #7af;
  font-size: 18px;
  letter-spacing: 2px;
  text-align: center;
  text-transform: uppercase;
}

#login p {
  color: #666;
  font-size: 11px;
  text-align: center;
}

#login input {
  background: #0d0d1a;
  border: 1px solid #334;
  border-radius: 3px;
  color: #eee;
  font-family: inherit;
  font-size: 14px;
  padding: 8px 12px;
  outline: none;
  transition: border-color 0.2s;
}

#login input:focus { border-color: #5588cc; }

#login button {
  background: #2a3a5a;
  border: 1px solid #5588cc;
  border-radius: 3px;
  color: #7af;
  cursor: pointer;
  font-family: inherit;
  font-size: 13px;
  letter-spacing: 1px;
  padding: 9px;
  text-transform: uppercase;
  transition: background 0.2s;
}

#login button:hover { background: #3a4a6a; }

#status { color: #f84; font-size: 11px; text-align: center; min-height: 14px; }

#game { display: none; width: 100vw; height: 100vh; }

canvas { display: block; width: 100%; height: 100%; cursor: crosshair; }
```

- [ ] **Step 2: Rewrite `index.html`**

Replace the entire file (removing all inline `<script>` and `<style>`) with the Vite HTML shell:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>4node-basic | MMO Debug Client</title>
  <link rel="stylesheet" href="/style.css">
</head>
<body>
  <div id="login">
    <h1>4node-basic</h1>
    <p>4-node grid &bull; 2000u cells &bull; AoI 1500u</p>
    <input id="nameInput" type="text" placeholder="enter name" maxlength="20" autocomplete="off" />
    <button id="connectBtn">Connect</button>
    <div id="status"></div>
  </div>
  <div id="game">
    <canvas id="canvas"></canvas>
  </div>
  <script type="module" src="/src/main.ts"></script>
</body>
</html>
```

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/web/style.css examples/4node-basic/web/index.html
git commit -m "feat(4node-basic): extract CSS, rewrite index.html as Vite shell"
```

---

### Task 3: Game state module

**Files:**
- Create: `examples/4node-basic/web/src/state.ts`

- [ ] **Step 1: Create `state.ts`**

```ts
import type { PlayerEntity } from "../sdk/entities.js";

/** Entity with interpolation fields added on top of the SDK type. */
export interface ClientEntity extends PlayerEntity {
  prevX: number;
  prevY: number;
  isReplica: boolean;
  isGhost: boolean;
}

export interface GameState {
  client: import("../sdk/client.js").BasicClient | null;
  playerNetID: number;
  entities: Map<number, ClientEntity>;
  tick: number;
  lastTickTime: number;
  viewerX: number;
  viewerY: number;

  // Grid metadata (from spawn message).
  gridW: number;
  gridH: number;
  cellSize: number;
  aoiRadius: number;

  // Camera.
  camX: number;
  camY: number;

  // Input / move target.
  inputSeq: number;
  moveTargetX: number;
  moveTargetY: number;
  moveTargetActive: boolean;

  // Client prediction.
  predictedX: number;
  predictedY: number;
  predictionActive: boolean;

  // FPS counter.
  lastFrameTime: number;
  fps: number;
  frameCount: number;
  lastFpsTime: number;
}

export const state: GameState = {
  client: null,
  playerNetID: 0,
  entities: new Map(),
  tick: 0,
  lastTickTime: 0,
  viewerX: 0,
  viewerY: 0,
  gridW: 2,
  gridH: 2,
  cellSize: 2000,
  aoiRadius: 1500,
  camX: 0,
  camY: 0,
  inputSeq: 0,
  moveTargetX: 0,
  moveTargetY: 0,
  moveTargetActive: false,
  predictedX: 0,
  predictedY: 0,
  predictionActive: false,
  lastFrameTime: 0,
  fps: 0,
  frameCount: 0,
  lastFpsTime: 0,
};
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/web/src/state.ts
git commit -m "feat(4node-basic): add typed game state module"
```

---

### Task 4: Network module

**Files:**
- Create: `examples/4node-basic/web/src/network.ts`

- [ ] **Step 1: Create `network.ts`**

```ts
import { BasicClient } from "../sdk/client.js";
import type { DeltaWorldUpdate } from "../sdk/entities.js";
import type { BasicSpawnedMsg } from "@gen/basicpb/basic_pb.js";
import { state, type ClientEntity } from "./state.js";

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

  client.onPlayerSpawned((msg: BasicSpawnedMsg) => {
    state.playerNetID = msg.entityNetId;
    state.gridW = msg.gridW || 2;
    state.gridH = msg.gridH || 2;
    state.cellSize = msg.cellSize || 2000;
    state.aoiRadius = msg.aoiRadius || 1500;
    setStatus("");
    showGameCallback?.();
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

  const DT = 1 / 20;

  // Advance existing entities: current → prev, dead-reckon position.
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
      isReplica: raw.state === 1,
      isGhost: raw.state === 2,
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
      isReplica: raw.state === 1,
      isGhost: raw.state === 2,
      name: raw.name || (prev ? prev.name : ""),
    };
    state.entities.set(raw.netID, ent);

    if (raw.netID === state.playerNetID) {
      checkPlayerArrival(ent);
    }
  }

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
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/web/src/network.ts
git commit -m "feat(4node-basic): add network module using generated SDK"
```

---

### Task 5: Interpolation module

**Files:**
- Create: `examples/4node-basic/web/src/interpolation.ts`

- [ ] **Step 1: Create `interpolation.ts`**

```ts
import { state } from "./state.js";

const TICK_MS = 1000 / 20;
const DT = TICK_MS / 1000;
const MOVE_SPEED = 300;
const DECEL_DIST = 100;
const MIN_SPEED = 30;

/** Interpolate between two known positions, extrapolate with velocity past interp=1. */
export function interpPos(prevX: number, currX: number, vx: number, interp: number): number {
  if (interp <= 1.0) {
    return prevX + (currX - prevX) * interp;
  }
  return currX + vx * (interp - 1.0) * DT;
}

/** Returns the current interpolation factor (0-2) based on time since last tick. */
export function getInterp(): number {
  return Math.min((performance.now() - state.lastTickTime) / TICK_MS, 2.0);
}

/** Advance client prediction toward move target, blend with server position. */
export function updatePrediction(now: number): void {
  const frameDt = state.lastFrameTime > 0 ? (now - state.lastFrameTime) / 1000 : 1 / 60;

  if (!state.predictionActive || !state.moveTargetActive) return;

  const pdx = state.moveTargetX - state.predictedX;
  const pdy = state.moveTargetY - state.predictedY;
  const pdist = Math.sqrt(pdx * pdx + pdy * pdy);

  if (pdist < 5) {
    state.predictionActive = false;
    return;
  }

  let speed = MOVE_SPEED;
  if (pdist < DECEL_DIST) speed *= pdist / DECEL_DIST;
  if (speed < MIN_SPEED) speed = MIN_SPEED;
  const step = speed * frameDt;
  state.predictedX += (pdx / pdist) * Math.min(step, pdist);
  state.predictedY += (pdy / pdist) * Math.min(step, pdist);

  // Blend toward server position to correct drift.
  const player = state.entities.get(state.playerNetID);
  if (player) {
    const interp = getInterp();
    const serverX = interpPos(player.prevX, player.worldX, player.velX, interp);
    const serverY = interpPos(player.prevY, player.worldY, player.velY, interp);
    const blend = 0.15;
    state.predictedX += (serverX - state.predictedX) * blend;
    state.predictedY += (serverY - state.predictedY) * blend;
  }
}
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/web/src/interpolation.ts
git commit -m "feat(4node-basic): add interpolation and client prediction module"
```

---

### Task 6: Input module

**Files:**
- Create: `examples/4node-basic/web/src/input.ts`

- [ ] **Step 1: Create `input.ts`**

```ts
import { state } from "./state.js";
import { sendMoveTarget } from "./network.js";

export function setupInput(canvas: HTMLCanvasElement): void {
  let mouseHeld = false;

  function worldCoords(e: MouseEvent): [number, number] {
    const rect = canvas.getBoundingClientRect();
    const scaleX = canvas.width / rect.width;
    const scaleY = canvas.height / rect.height;
    const canvasX = (e.clientX - rect.left) * scaleX;
    const canvasY = (e.clientY - rect.top) * scaleY;
    const scale = Math.min(canvas.width, canvas.height) / 3500;
    const wx = (canvasX - canvas.width / 2) / scale + state.camX;
    const wy = (canvasY - canvas.height / 2) / scale + state.camY;
    return [wx, wy];
  }

  function setMoveTarget(e: MouseEvent): void {
    const [wx, wy] = worldCoords(e);
    state.moveTargetX = wx;
    state.moveTargetY = wy;
    state.moveTargetActive = true;
    const player = state.entities.get(state.playerNetID);
    if (player && !state.predictionActive) {
      state.predictedX = player.worldX;
      state.predictedY = player.worldY;
      state.predictionActive = true;
    }
    sendMoveTarget();
  }

  canvas.addEventListener("mousedown", (e) => { mouseHeld = true; setMoveTarget(e); });
  canvas.addEventListener("mousemove", (e) => { if (mouseHeld) setMoveTarget(e); });
  canvas.addEventListener("mouseup", () => { mouseHeld = false; });
  canvas.addEventListener("mouseleave", () => { mouseHeld = false; });
  canvas.addEventListener("contextmenu", (e) => e.preventDefault());
}
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/web/src/input.ts
git commit -m "feat(4node-basic): add click-to-move input module"
```

---

### Task 7: Renderer module

**Files:**
- Create: `examples/4node-basic/web/src/renderer.ts`

- [ ] **Step 1: Create `renderer.ts`**

This is the largest module — a direct port of the Canvas2D render loop from the old `index.html` lines 633-1027.

```ts
import { state, type ClientEntity } from "./state.js";
import { interpPos, getInterp, updatePrediction } from "./interpolation.js";

const NODE_COLORS = [
  { bg: "rgba(100,150,255,0.07)", fill: "#5588cc", stroke: "#6496FF", label: "node_0_0" },
  { bg: "rgba(255,150,100,0.07)", fill: "#cc8855", stroke: "#FF9664", label: "node_1_0" },
  { bg: "rgba(100,255,150,0.07)", fill: "#55cc88", stroke: "#64FF96", label: "node_0_1" },
  { bg: "rgba(255,100,255,0.07)", fill: "#cc55cc", stroke: "#FF64FF", label: "node_1_1" },
];

export function startRenderLoop(): void {
  requestAnimationFrame(renderLoop);
}

function renderLoop(now: number): void {
  requestAnimationFrame(renderLoop);

  // FPS counter.
  state.frameCount++;
  if (now - state.lastFpsTime >= 1000) {
    state.fps = Math.round((state.frameCount * 1000) / (now - state.lastFpsTime));
    state.frameCount = 0;
    state.lastFpsTime = now;
  }

  const canvas = document.getElementById("canvas") as HTMLCanvasElement;
  const ctx = canvas.getContext("2d")!;
  const W = canvas.width;
  const H = canvas.height;

  ctx.fillStyle = "#0a0a14";
  ctx.fillRect(0, 0, W, H);

  if (!state.playerNetID) return;

  const interp = getInterp();
  const DT = (1000 / 20) / 1000;

  // Update client prediction.
  updatePrediction(now);
  state.lastFrameTime = now;

  // Camera position.
  const player = state.entities.get(state.playerNetID);
  let camX: number, camY: number;
  if (player) {
    if (state.predictionActive) {
      camX = state.predictedX;
      camY = state.predictedY;
    } else {
      camX = interpPos(player.prevX, player.worldX, player.velX, interp);
      camY = interpPos(player.prevY, player.worldY, player.velY, interp);
    }
  } else {
    camX = state.viewerX;
    camY = state.viewerY;
  }
  state.camX = camX;
  state.camY = camY;

  const scale = Math.min(W, H) / 3500;

  function worldToScreen(wx: number, wy: number): [number, number] {
    return [(wx - camX) * scale + W / 2, (wy - camY) * scale + H / 2];
  }

  // ── 1. Cell background tints & boundaries ──
  const cs = state.cellSize;
  const gw = state.gridW;
  const gh = state.gridH;

  for (let cy = 0; cy < gh; cy++) {
    for (let cx = 0; cx < gw; cx++) {
      const nodeIdx = cy * gw + cx;
      const nc = NODE_COLORS[nodeIdx % NODE_COLORS.length];
      const [sx0, sy0] = worldToScreen(cx * cs, cy * cs);
      const [sx1, sy1] = worldToScreen(cx * cs + cs, cy * cs + cs);
      ctx.fillStyle = nc.bg;
      ctx.fillRect(sx0, sy0, sx1 - sx0, sy1 - sy0);
    }
  }

  ctx.save();
  ctx.setLineDash([6, 4]);
  ctx.strokeStyle = "rgba(180,180,255,0.25)";
  ctx.lineWidth = 1;
  for (let cx = 0; cx <= gw; cx++) {
    const [sx] = worldToScreen(cx * cs, 0);
    const [, sy0] = worldToScreen(0, 0);
    const [, sy1] = worldToScreen(0, gh * cs);
    ctx.beginPath(); ctx.moveTo(sx, sy0); ctx.lineTo(sx, sy1); ctx.stroke();
  }
  for (let cy = 0; cy <= gh; cy++) {
    const [sx0] = worldToScreen(0, 0);
    const [sx1] = worldToScreen(gw * cs, 0);
    const [, sy] = worldToScreen(0, cy * cs);
    ctx.beginPath(); ctx.moveTo(sx0, sy); ctx.lineTo(sx1, sy); ctx.stroke();
  }
  ctx.restore();

  // ── 2. Cell labels ──
  for (let cy = 0; cy < gh; cy++) {
    for (let cx = 0; cx < gw; cx++) {
      const nodeIdx = cy * gw + cx;
      const nc = NODE_COLORS[nodeIdx % NODE_COLORS.length];
      const [sx0] = worldToScreen(cx * cs, cy * cs);
      const [sx1] = worldToScreen(cx * cs + cs, cy * cs);
      const [, sy0] = worldToScreen(cx * cs, cy * cs);
      const [, sy1] = worldToScreen(cx * cs, cy * cs + cs);
      ctx.save();
      ctx.font = `${Math.max(11, cs * scale * 0.04)}px 'Courier New', monospace`;
      ctx.fillStyle = "rgba(200,200,255,0.15)";
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      ctx.fillText(nc.label, (sx0 + sx1) / 2, (sy0 + sy1) / 2);
      ctx.restore();
    }
  }

  // ── 3. AoI radius ring ──
  if (player) {
    const aoiX = interpPos(player.prevX, player.worldX, player.velX, interp);
    const aoiY = interpPos(player.prevY, player.worldY, player.velY, interp);
    const [px, py] = worldToScreen(aoiX, aoiY);
    ctx.save();
    ctx.setLineDash([8, 5]);
    ctx.strokeStyle = "rgba(255,255,0,0.35)";
    ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.arc(px, py, state.aoiRadius * scale, 0, Math.PI * 2); ctx.stroke();
    ctx.restore();
  }

  // ── 3b. Move target crosshair ──
  if (state.moveTargetActive) {
    const [tx, ty] = worldToScreen(state.moveTargetX, state.moveTargetY);
    const sz = 8;
    ctx.save();
    ctx.strokeStyle = "rgba(0,255,180,0.7)";
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(tx - sz, ty - sz); ctx.lineTo(tx + sz, ty + sz);
    ctx.moveTo(tx + sz, ty - sz); ctx.lineTo(tx - sz, ty + sz);
    ctx.stroke();
    ctx.strokeStyle = "rgba(0,255,180,0.35)";
    ctx.lineWidth = 1;
    ctx.beginPath(); ctx.arc(tx, ty, sz + 4, 0, Math.PI * 2); ctx.stroke();
    ctx.restore();
  }

  // ── 4. Entities ──
  for (const [netID, ent] of state.entities) {
    let rx = interpPos(ent.prevX, ent.worldX, ent.velX, interp);
    let ry = interpPos(ent.prevY, ent.worldY, ent.velY, interp);

    if (netID === state.playerNetID && state.predictionActive) {
      rx = state.predictedX;
      ry = state.predictedY;
    }

    const [sx, sy] = worldToScreen(rx, ry);
    const r = Math.max(4, Math.abs(ent.radius) * scale);
    const isPlayer = netID === state.playerNetID;
    const nc = NODE_COLORS[(ent.ownerNode || 0) % NODE_COLORS.length];

    // Main circle.
    ctx.save();
    ctx.beginPath(); ctx.arc(sx, sy, r, 0, Math.PI * 2);
    ctx.fillStyle = nc.fill; ctx.fill();

    ctx.strokeStyle = isPlayer ? "#ffffff" : nc.stroke;
    ctx.lineWidth = isPlayer ? 2.5 : 1;
    if (ent.isReplica) {
      ctx.save(); ctx.setLineDash([4, 3]); ctx.lineWidth = 1.5; ctx.stroke(); ctx.restore();
    } else if (ent.isGhost) {
      ctx.save(); ctx.setLineDash([2, 2]); ctx.globalAlpha = 0.5; ctx.stroke(); ctx.restore();
    } else {
      ctx.stroke();
    }
    ctx.restore();

    // Velocity arrow.
    if (ent.velX !== 0 || ent.velY !== 0) {
      const speed = Math.sqrt(ent.velX * ent.velX + ent.velY * ent.velY);
      const arrowLen = Math.min(speed * scale * 0.08, 40);
      const nx = ent.velX / speed;
      const ny = ent.velY / speed;
      const ex = sx + nx * arrowLen;
      const ey = sy + ny * arrowLen;
      const angle = Math.atan2(ny, nx);
      const hl = 5;
      ctx.save();
      ctx.globalAlpha = 0.6; ctx.strokeStyle = nc.stroke; ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(sx, sy); ctx.lineTo(ex, ey);
      ctx.lineTo(ex - hl * Math.cos(angle - 0.4), ey - hl * Math.sin(angle - 0.4));
      ctx.moveTo(ex, ey);
      ctx.lineTo(ex - hl * Math.cos(angle + 0.4), ey - hl * Math.sin(angle + 0.4));
      ctx.stroke();
      ctx.restore();
    }

    // NetID label.
    ctx.save();
    ctx.font = "9px Courier New, monospace"; ctx.fillStyle = "#aaa";
    ctx.textAlign = "center"; ctx.textBaseline = "bottom";
    ctx.fillText(`#${netID}`, sx, sy - r - 2);
    ctx.restore();

    // Replica/Ghost badge.
    if (ent.isReplica || ent.isGhost) {
      const badge = ent.isGhost ? "G" : "R";
      const badgeColor = ent.isGhost ? "#ff8800" : "#00ccff";
      ctx.save();
      ctx.font = "bold 8px Courier New, monospace"; ctx.fillStyle = badgeColor;
      ctx.textAlign = "left"; ctx.textBaseline = "middle";
      ctx.fillText(badge, sx + r + 3, sy);
      ctx.restore();
    }

    // Player name.
    if (ent.name) {
      ctx.save();
      ctx.font = isPlayer ? "bold 11px Courier New, monospace" : "10px Courier New, monospace";
      ctx.fillStyle = isPlayer ? "#7af" : "#999";
      ctx.textAlign = "center"; ctx.textBaseline = "top";
      ctx.fillText(ent.name, sx, sy + r + 2);
      ctx.restore();
    }
  }

  // ── 5. HUD: Tick + FPS ──
  ctx.save();
  ctx.fillStyle = "rgba(0,0,0,0.55)"; ctx.fillRect(8, 8, 160, 61);
  ctx.font = "11px Courier New, monospace"; ctx.fillStyle = "#7af";
  ctx.textAlign = "left"; ctx.textBaseline = "top";
  ctx.fillText(`TICK  ${state.tick}`, 14, 13);
  ctx.fillText(`FPS   ${state.fps}`, 14, 28);
  ctx.fillText(`NET   #${state.playerNetID || "?"}`, 14, 43);
  ctx.restore();

  // ── 6. Grid info ──
  const panelW = 200;
  ctx.save();
  ctx.fillStyle = "rgba(0,0,0,0.55)"; ctx.fillRect(W - panelW - 8, 8, panelW, 38);
  ctx.font = "11px Courier New, monospace"; ctx.textAlign = "left"; ctx.textBaseline = "top";
  ctx.fillStyle = "#aaa";
  ctx.fillText(`GRID      ${state.gridW}x${state.gridH}`, W - panelW - 2, 13);
  ctx.fillText(`ENTITIES  ${state.entities.size}`, W - panelW - 2, 28);
  ctx.restore();

  // ── 7. Legend ──
  const rows = [
    ...NODE_COLORS.slice(0, gw * gh).map((nc) => ({ color: nc.fill, label: nc.label, dash: false })),
    { color: "#ffdd00", label: "AoI radius", dash: true },
    { color: "#00ccff", label: "R = replica", dash: true },
    { color: "#ff8800", label: "G = ghost", dash: true },
    { color: "#00ffb4", label: "move target (X)", dash: false },
  ];
  const rowH = 16;
  const legendH = rows.length * rowH + 12;
  const legendW = 170;
  const legendY = H - legendH - 8;

  ctx.save();
  ctx.fillStyle = "rgba(0,0,0,0.55)"; ctx.fillRect(8, legendY, legendW, legendH);
  ctx.font = "10px Courier New, monospace"; ctx.textBaseline = "middle";
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i];
    const ry = legendY + 6 + i * rowH + rowH / 2;
    ctx.save();
    if (row.dash) {
      ctx.setLineDash([3, 2]); ctx.strokeStyle = row.color; ctx.lineWidth = 1.5;
      ctx.beginPath(); ctx.arc(20, ry, 5, 0, Math.PI * 2); ctx.stroke();
    } else {
      ctx.fillStyle = row.color;
      ctx.beginPath(); ctx.arc(20, ry, 5, 0, Math.PI * 2); ctx.fill();
    }
    ctx.restore();
    ctx.fillStyle = "#bbb"; ctx.textAlign = "left";
    ctx.fillText(row.label, 32, ry);
  }
  ctx.restore();
}
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/web/src/renderer.ts
git commit -m "feat(4node-basic): add Canvas2D renderer module"
```

---

### Task 8: Main entry point

**Files:**
- Create: `examples/4node-basic/web/src/main.ts`

- [ ] **Step 1: Create `main.ts`**

```ts
import { connect, onShowGame } from "./network.js";
import { setupInput } from "./input.js";
import { startRenderLoop } from "./renderer.js";

function resizeCanvas(): void {
  const canvas = document.getElementById("canvas") as HTMLCanvasElement;
  canvas.width = window.innerWidth;
  canvas.height = window.innerHeight;
}

function showGame(): void {
  document.getElementById("login")!.style.display = "none";
  document.getElementById("game")!.style.display = "block";
  resizeCanvas();
  setupInput(document.getElementById("canvas") as HTMLCanvasElement);
  startRenderLoop();
}

onShowGame(showGame);
window.addEventListener("resize", resizeCanvas);

const connectBtn = document.getElementById("connectBtn")!;
const nameInput = document.getElementById("nameInput") as HTMLInputElement;

connectBtn.addEventListener("click", () => {
  const name = nameInput.value.trim();
  if (!name) {
    document.getElementById("status")!.textContent = "enter a name";
    return;
  }
  document.getElementById("status")!.textContent = "connecting...";
  connect(name);
});

nameInput.addEventListener("keydown", (e) => {
  if (e.key === "Enter") connectBtn.click();
});

nameInput.focus();
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/web/src/main.ts
git commit -m "feat(4node-basic): add main entry point wiring login and game"
```

---

### Task 9: Verify end-to-end

- [ ] **Step 1: Check TypeScript compiles**

Run: `cd examples/4node-basic/web && npx tsc --noEmit`
Expected: no errors

- [ ] **Step 2: Start dev server and game server**

Run: `cd examples/4node-basic && make dev`
Expected: Vite dev server starts on port 5173, Go server starts on port 8080

- [ ] **Step 3: Manual smoke test**

Open `http://localhost:5173` in browser. Verify:
- Login form appears with existing styling
- Enter name, click Connect — canvas appears
- Entities render with correct node colors (blue/orange/green/purple per cell)
- Click to move — player moves, crosshair appears at target
- Cell boundaries (dashed) and AoI radius ring (yellow dashed) visible
- HUD shows tick, FPS, entity count
- Open second tab — both players visible to each other
- Replica badges ("R") appear for cross-node entities

- [ ] **Step 4: Final commit**

```bash
git add -A examples/4node-basic/
git commit -m "feat(4node-basic): complete Vite + TypeScript client migration"
```
