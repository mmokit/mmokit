# 4node-basic: Vite + TypeScript Client

## Context

The 4node-basic example has a monolithic `web/index.html` with ~1050 lines of inline JavaScript: hand-written protobuf encoding/decoding, binary delta world update parsing, Canvas2D rendering, input handling, interpolation, and HUD overlays. We just built an auto-generated TypeScript SDK (`web/sdk/`) that handles all network communication. This spec converts the client to a Vite+TS project that uses the generated SDK.

## Architecture

Raw Canvas2D rendering (no Pixi.js). The generated SDK replaces all hand-written protobuf and binary decoding. Modeled after the slither example's Vite setup.

### File Structure

```
examples/4node-basic/web/
├── package.json
├── tsconfig.json
├── vite.config.ts
├── index.html            # login form + canvas shell
├── style.css             # extracted from current inline styles
├── sdk/                  # generated (already exists)
│   ├── client.ts
│   ├── entities.ts
│   ├── delta-decoder.ts
│   ├── transport.ts
│   ├── index.ts
│   └── _core/delta-decoder-core.ts
└── src/
    ├── main.ts
    ├── network.ts
    ├── state.ts
    ├── input.ts
    ├── renderer.ts
    └── interpolation.ts
```

### Module Responsibilities

**`state.ts`** — Game state type and singleton.
- `GameState` interface: playerNetID, entities map, camera, grid metadata, input state, FPS counter
- Entity type extends `PlayerEntity` from SDK with interpolation fields: `prevX`, `prevY`, `isReplica`, `isGhost`
- Exported singleton `state` object

**`network.ts`** — Creates `BasicClient`, wires SDK events to game state.
- `connect(name: string)` — constructs client, calls `connect()`, sends login
- `onPlayerSpawned` handler: sets grid metadata, playerNetID, triggers `showGame()`
- `onDeltaWorldUpdate` handler: calls `applyWorldUpdate()` which updates entity map with interpolation state
- Exposes `sendMoveTarget(x, y, seq)` for input module

**`input.ts`** — Mouse click-to-move with hold-to-drag.
- `setupInput(canvas)` — attaches mousedown/mousemove/mouseup listeners
- On click/drag: computes world coordinates from screen coords, updates state, calls `sendMoveTarget`
- `worldCoords(canvasX, canvasY)` helper using current camera + scale

**`interpolation.ts`** — Position interpolation and client prediction.
- `interpPos(prevX, currX, vx, interp, dt)` — lerp for interp<=1, extrapolate with velocity for interp>1
- `updatePrediction(dt)` — advances predicted position toward move target, blends with server position
- Constants: `MOVE_SPEED=300`, `DECEL_DIST=100`, `MIN_SPEED=30` (match server)

**`renderer.ts`** — Canvas2D render loop, ported 1:1 from current inline code.
- `startRenderLoop()` — calls `requestAnimationFrame`, manages FPS counter
- Renders in order: cell background tints, cell boundaries (dashed), cell labels, AoI radius ring, move target crosshair, entities (with node-colored circles, replica/ghost styling, velocity arrows, netID labels, name labels), HUD panels (tick/FPS, grid info, legend)
- All rendering constants (NODE_COLORS, etc.) defined in this file

**`main.ts`** — Entry point.
- Wires connect button + enter key to `connect()`
- `showGame()` — hides login, shows canvas, calls `resizeCanvas()`, `setupInput()`, `startRenderLoop()`
- Window resize handler

### Config Files

**`package.json`** — Minimal deps: `@bufbuild/protobuf`, devDeps: `typescript`, `vite`.

**`tsconfig.json`** — ES2020 target, bundler moduleResolution, strict, noEmit, paths for `@gen/basicpb/*` and `@gen/enginepb/*` pointing to `../../../gen/es/`.

**`vite.config.ts`** — WebSocket proxy `/ws` to `ws://localhost:8081`, resolve aliases for `@gen/` paths, `fs.allow` up to project root, dedupe `@bufbuild/protobuf`.

**`index.html`** — Static HTML with login form and canvas div. Styles in `style.css`. Entry: `<script type="module" src="/src/main.ts">`.

### Makefile

Update `examples/4node-basic/Makefile` to match slither's pattern:
```makefile
dev: build
    trap 'kill 0' INT TERM EXIT; \
    (cd web && exec bun run dev) & \
    cd $(ROOT) && exec ./bin/4node-basic -port 8081
```

### What Gets Deleted

- All hand-written protobuf encoding (lines 87-163 of current index.html): `encodeVarint`, `encodeField`, `concatBytes`, `encodeBasicLoginMsg`, `encodeBasicMoveTargetMsg`, `encodeClientEvent`
- All hand-written protobuf decoding (lines 165-238): `decodeVarint`, `decodeServerEvent`, `decodeBasicSpawnedMsg`
- All hand-written delta decoding (lines 240-380): `baselines`, `FIELD_SIZES`, `dequantize`, `decodeSnapshot`, `decodeInitialData`, `applyDelta`, `decodeWorldUpdate`
- Event code constants (lines 385-388): replaced by SDK

### What Gets Ported 1:1

- CSS styles → `style.css`
- Game state structure → `state.ts` (typed)
- WebSocket connection + event handling → `network.ts` (using SDK)
- Input handling → `input.ts`
- Interpolation + prediction → `interpolation.ts`
- All Canvas2D rendering → `renderer.ts`
- Login form wiring → `main.ts`

## Verification

1. `cd examples/4node-basic/web && bun install`
2. `cd examples/4node-basic && make dev`
3. Open `http://localhost:5173`, enter name, verify:
   - Login succeeds, canvas appears
   - Entities render with correct node colors
   - Click-to-move works with client prediction
   - Cell boundaries, AoI ring, HUD all display correctly
   - Replica/ghost badges appear for cross-node entities
   - Multiple browser tabs can connect simultaneously
