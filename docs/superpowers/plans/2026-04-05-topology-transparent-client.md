# Topology-Transparent Client Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the client protocol topology-agnostic — clients receive entities in world-space coordinates with zero knowledge of cells, nodes, or grid layout. Debug topology info is gated behind a single coordinator flag.

**Architecture:** Add `DebugTopology bool` to the coordinator config. Strip grid metadata from `SpawnedMsg`, replace with `world_x`/`world_y`. Gate `CellTopologyMsg` and `MeshState` binding behind the flag. Update 4node-basic client to work without grid state.

**Tech Stack:** Go, protobuf (buf generate), TypeScript, SDK codegen pipeline

**Spec:** `docs/superpowers/specs/2026-04-05-topology-transparent-client-design.md`

---

### Task 1: Update SpawnedMsg proto

**Files:**

- Modify: `proto/enginepb/engine.proto:121-129`

- [ ] **Step 1: Update SpawnedMsg in engine.proto**

Replace the current SpawnedMsg with the topology-free version:

```protobuf
// Payload for SE_PLAYER_SPAWNED — tells the client its entity ID and world position.
message SpawnedMsg {
    uint32 entity_net_id = 1;
    float  world_x       = 2;
    float  world_y       = 3;
}
```

- [ ] **Step 2: Regenerate protobuf**

Run: `make proto`
Expected: Clean generation, no errors.

- [ ] **Step 3: Verify Go code compiles with updated proto**

Run: `go vet ./gen/go/...`
Expected: Compilation errors in files that reference removed SpawnedMsg fields (`CellX`, `CellY`, `CellSize`, `GridW`, `GridH`). This is expected — we'll fix callers in subsequent tasks.

- [ ] **Step 4: Commit proto change**

```bash
git add proto/enginepb/engine.proto gen/
git commit -m "proto: strip grid metadata from SpawnedMsg, add world_x/world_y"
```

---

### Task 2: Add DebugTopology to Coordinator config

**Files:**

- Modify: `pkg/universe/coordinator.go:28-45`

- [ ] **Step 1: Add DebugTopology field to Config**

```go
// In Config struct, after LogCategories:
DebugTopology bool // send MeshState + CellTopology to clients (debug/visualization only)
```

- [ ] **Step 2: Add DebugTopology accessor**

After the `GridWidth()` method (line 544):

```go
// DebugTopology returns whether debug topology info is sent to clients.
func (c *Coordinator) DebugTopology() bool { return c.cfg.DebugTopology }
```

- [ ] **Step 3: Gate CellTopology sending behind DebugTopology**

In `SendCellTopology` (line 591) and `BroadcastCellTopology` (line 597), add early return:

```go
func (c *Coordinator) SendCellTopology(connID uint32) {
	if !c.cfg.DebugTopology {
		return
	}
	frame := c.buildCellTopologyFrame()
	c.cfg.ConnManager.Send(connID, frame)
}

func (c *Coordinator) BroadcastCellTopology() {
	if !c.cfg.DebugTopology {
		return
	}
	frame := c.buildCellTopologyFrame()
	for _, connID := range c.cfg.ConnManager.ActiveConnIDs() {
		c.cfg.ConnManager.Send(connID, frame)
	}
}
```

- [ ] **Step 4: Gate OnTopologyChanged default**

In the coordinator constructor (around line 164), only set the default `OnTopologyChanged` when `DebugTopology` is also true:

```go
if cfg.DynamicPartitioning.OnTopologyChanged == nil && cfg.DebugTopology {
	cfg.DynamicPartitioning.OnTopologyChanged = func() {
		c.BroadcastCellTopology()
	}
}
```

- [ ] **Step 5: Verify compilation**

Run: `go vet ./pkg/universe/...`
Expected: PASS (no callers of DebugTopology yet, and the gating is additive).

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: add DebugTopology flag to coordinator config, gate CellTopology sending"
```

---

### Task 3: Update SendSpawnedMsg to send world coordinates

**Files:**

- Modify: `pkg/universe/world_base.go:332-358`

- [ ] **Step 1: Update SendSpawnedMsg**

Replace the current implementation:

```go
func (b *WorldBase) SendSpawnedMsg(connID uint32, entity ecs.Entity) {
	netID := uint32(0)
	if b.netIDMap.HasAll(entity) {
		netID = b.netIDMap.Get(entity).ID
	}
	cell := b.rootCell()
	cs := coords.CellSize
	var worldX, worldY float32
	if b.posMap.HasAll(entity) {
		pos := b.posMap.Get(entity)
		worldX = pos.X + float32(cell.X)*cs
		worldY = pos.Y + float32(cell.Y)*cs
	}
	msg := &enginepb.SpawnedMsg{
		EntityNetId: netID,
		WorldX:      worldX,
		WorldY:      worldY,
	}
	frame := makeEventFrame(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), msg)
	b.eng.ConnMgr.Send(connID, frame)
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./pkg/universe/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/world_base.go
git commit -m "feat: SendSpawnedMsg sends world coordinates instead of grid metadata"
```

---

### Task 4: Wire DebugTopology into EngineBindings

**Files:**

- Modify: `pkg/mmokit/mmokit.go:988-1010` (BuildReplicators)
- Modify: `examples/4node-basic/world.go:33`

- [ ] **Step 1: Pass DebugTopology to IncludeMeshState in BuildReplicators**

In `BuildReplicators` (line 991), after the `EngineBindings` call, inject the coordinator's DebugTopology:

```go
func BuildReplicators(w *ecs.World, coord *universe.Coordinator, defs ...universe.EntityKindDef) *system.ReplicatorRegistry {
	replicators := system.NewReplicatorRegistry()
	debugTopology := coord != nil && coord.DebugTopology()
	for _, def := range defs {
		var bindings []system.ComponentBinding
		if def.EngineBindings != nil {
			if ebCfg, ok := def.EngineBindings.(*EngineBindingsConfig); ok {
				ebCfg.IncludeMeshState = debugTopology
				bindings = append(bindings, EngineBindings(w, coord, *ebCfg))
			}
		} else {
			bindings = append(bindings, EngineBindings(w, coord, EngineBindingsConfig{IncludeMeshState: debugTopology}))
		}
		for _, nb := range def.NetworkBindings {
			if cb, ok := nb.(system.ComponentBinding); ok {
				bindings = append(bindings, cb)
			}
		}
		replicators.Register(system.AutoReplicator(def.Kind, bindings...))
	}
	return replicators
}
```

- [ ] **Step 2: Remove IncludeMeshState from 4node-basic EntityKindDef**

In `examples/4node-basic/world.go:33`, remove the manual `IncludeMeshState`:

```go
EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500},
```

The coordinator's `DebugTopology` flag now controls this.

- [ ] **Step 3: Set DebugTopology in 4node-basic main.go**

In `examples/4node-basic/main.go`, add `DebugTopology: true` to the coordinator config (after `AoIRadius`):

```go
cfg := mmokit.Config{
	CellsX:        CellsX,
	CellsY:        CellsY,
	CellSize:      CellSize,
	TickRate:       TickRate,
	AoIRadius:     AoIRadius,
	DebugTopology: true,
	LogCategories: *logFlag,
	// ...
}
```

- [ ] **Step 4: Verify compilation**

Run: `go vet ./pkg/mmokit/... ./examples/4node-basic/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/mmokit.go examples/4node-basic/world.go examples/4node-basic/main.go
git commit -m "feat: wire DebugTopology to EngineBindings, 4node-basic opts in"
```

---

### Task 5: Fix slither and internal game compilation

**Files:**

- Modify: `examples/slither/web/src/network.ts:109-116`
- Modify: `internal/bot/bot.go:181` (if it references removed fields)

- [ ] **Step 1: Check which files reference removed SpawnedMsg fields**

Run: `go vet ./...`
Look for compilation errors referencing `CellX`, `CellY`, `CellSize`, `GridW`, `GridH` on `SpawnedMsg`.

- [ ] **Step 2: Fix internal/bot/bot.go**

The bot client uses `gamepb.PlayerSpawnedMsg` (game-specific proto), not `enginepb.SpawnedMsg`. Verify it compiles. If it references the engine SpawnedMsg fields, update to use the new `WorldX`/`WorldY` fields.

- [ ] **Step 3: Fix any other Go callers**

Fix all remaining compilation errors from the SpawnedMsg field removal. Each caller should switch from cell metadata to `WorldX`/`WorldY`.

- [ ] **Step 4: Update slither client to derive cell from world position**

In `examples/slither/web/src/network.ts`, update the `SE_PLAYER_SPAWNED` handler:

```typescript
case ServerEventCode.SE_PLAYER_SPAWNED: {
  const msg = fromBinary(SpawnedMsgSchema, evt.data) as SpawnedMsg;
  const CELL_SIZE = 8192;
  const data: SpawnedData = {
    entityID: msg.entityNetId,
    cellX: Math.floor(msg.worldX / CELL_SIZE),
    cellY: Math.floor(msg.worldY / CELL_SIZE),
  };
  this.callbacks.onSpawned(data);
  break;
}
```

- [ ] **Step 5: Verify full compilation**

Run: `go vet ./...`
Expected: PASS — all Go code compiles.

- [ ] **Step 6: Run tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "fix: update all SpawnedMsg callers for new world_x/world_y fields"
```

---

### Task 6: Update 4node-basic client

**Files:**

- Modify: `examples/4node-basic/web/src/state.ts`
- Modify: `examples/4node-basic/web/src/network.ts`
- Modify: `examples/4node-basic/web/src/renderer.ts`

- [ ] **Step 1: Clean up state.ts**

Remove grid metadata fields. Keep `cells` for debug topology (populated when CellTopologyMsg arrives):

```typescript
export interface GameState {
  client: import("../sdk/client.js").BasicClient | null;
  playerNetID: number;
  entities: Map<number, ClientEntity>;
  tick: number;
  lastTickTime: number;
  viewerX: number;
  viewerY: number;

  // Server-provided tick config (set by SE_SERVER_CONFIG).
  tickRate: number;
  tickMs: number;
  dt: number;

  // Debug cell topology (from CellTopologyMsg, empty when DebugTopology is off).
  cells: CellInfo[];

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
  predictionStartTime: number;

  // FPS counter.
  lastFrameTime: number;
  fps: number;
  frameCount: number;
  lastFpsTime: number;
}
```

Update the initial state object to remove `gridW`, `gridH`, `cellSize` defaults.

- [ ] **Step 2: Update network.ts spawn handler**

Replace the `onPlayerSpawned` handler to use world position:

```typescript
client.onPlayerSpawned((msg: SpawnedMsg) => {
  state.playerNetID = msg.entityNetId;
  setStatus("");
  showGameCallback?.();
});
```

Remove the lines that set `state.gridW`, `state.gridH`, `state.cellSize`.

Update the `onCellTopology` handler — it stays as-is (only fires when DebugTopology is enabled on server). But remove the lines that set `gridW`/`gridH`/`cellSize` from it too:

```typescript
client.onCellTopology((msg: CellTopologyMsg) => {
  state.cells = msg.cells.map((c: PbCellInfo): CellInfo => ({
    cellX: c.cellX, cellY: c.cellY,
    depth: c.depth, size: c.size,
    originX: c.originX, originY: c.originY,
    nodeId: c.nodeId,
  }));
});
```

- [ ] **Step 3: Make renderer cell visualization conditional**

In `renderer.ts`, the cell background/boundary/label rendering block (lines 95-128) is already inside `for (const c of state.cells)` — when `state.cells` is empty (DebugTopology off), it simply doesn't render. No changes needed here.

For the replica/ghost badges (lines 187-232), guard with a check for whether the SDK entity has meshState:

```typescript
// Replica/Ghost badge (only when debug topology enabled)
if ('meshState' in ent && (ent.isReplica || ent.isGhost)) {
```

If meshState is always present in the SDK (because 4node-basic has DebugTopology=true), the `isReplica`/`isGhost` fields will work as before. No further changes needed for 4node-basic specifically.

- [ ] **Step 4: Remove gridW/gridH/cellSize references from renderer**

Search renderer.ts for any references to `state.gridW`, `state.gridH`, or `state.cellSize`. If found, remove or replace them. The grid boundary rendering already uses `state.cells` data (which includes origins and sizes), so it shouldn't reference grid dimensions.

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/web/src/
git commit -m "feat(4node-basic): remove grid metadata from client state, topology-transparent"
```

---

### Task 7: Regenerate SDK and verify end-to-end

**Files:**

- Modify: `examples/4node-basic/web/sdk/` (auto-generated)

- [ ] **Step 1: Regenerate the SDK**

Run: `make client-sdk GAME=examples/4node-basic`
Expected: SDK files regenerated. Since DebugTopology=true for 4node-basic, meshState/ownerNode fields are still present in the generated SDK.

- [ ] **Step 2: Verify TypeScript compiles**

Run: `cd examples/4node-basic/web && bun run build`
Expected: Build succeeds with no type errors.

- [ ] **Step 3: Run all Go tests**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 4: Commit SDK changes**

```bash
git add examples/4node-basic/web/sdk/
git commit -m "chore: regenerate 4node-basic SDK with updated SpawnedMsg"
```

---

### Task 8: Update 4node-basic schema export

**Files:**

- Modify: `examples/4node-basic/schema.go:21-23`

- [ ] **Step 1: Make CellTopology event registration conditional**

The schema export determines which events the SDK generates handlers for. Since CellTopology is debug-only, it should only be in the schema when debug is desired. For 4node-basic (a debug tool), keep it:

No change needed — 4node-basic always has DebugTopology=true, so the schema should include CellTopology. The schema.go file stays as-is.

- [ ] **Step 2: Verify schema export works**

Run: `cd examples/4node-basic && go run . --dump-schema | head -20`
Expected: JSON schema output with entity replication layout. SpawnedMsg should show `world_x`/`world_y` fields.

- [ ] **Step 3: Commit (if any changes)**

Only commit if changes were made.

---

### Task 9: Verify end-to-end with running server

- [ ] **Step 1: Build and run the 4node-basic example**

Run: `cd examples/4node-basic && make build`
Expected: Binary builds successfully.

- [ ] **Step 2: Verify with DebugTopology=true**

Start the server and open the web client. Verify:
- Cell boundaries visible (CellTopologyMsg received)
- R/G badges on replicas (MeshState binding active)
- Entities move smoothly across cell boundaries
- No coordinate jumps or offset artifacts

- [ ] **Step 3: Verify slither still works**

Run: `cd examples/slither && go vet ./...`
Expected: Compiles cleanly. The slither client derives cellX/cellY from worldX/worldY.

- [ ] **Step 4: Final commit with any fixups**

```bash
git add -A
git commit -m "fix: address end-to-end verification issues"
```

(Skip if no issues found.)
