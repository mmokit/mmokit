# Space Game Binary Wire Format Migration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the space game's protobuf bridge FrameWriter with the binary delta wire format, update the web-pixi client to decode it, and remove all legacy decode/bridge code.

**Architecture:** The server switches from `SE_WORLD_UPDATE` (protobuf `WorldUpdateMsg`) to `SE_DELTA_WORLD_UPDATE` (binary delta frames via `quantize.FrameEncoder`). Positions switch to viewer-relative `QRel` encoding. The web-pixi client gets a `delta-decoder.ts` that maintains per-entity baselines, decodes snapshots into `EntityState`-compatible objects, and feeds them to the existing interpolation/rendering pipeline unchanged.

**Tech Stack:** Go server (pkg/quantize, pkg/system), TypeScript web client (web-pixi/src), protobuf (engine.proto)

---

### Task 1: Server — Switch base fields back to quantized encoding

The base fields in `nethandler_shared.go` currently use unquantized `Float32` as a workaround. Switch to viewer-relative `QRel` for positions and `QVel`/`QAngle`/`QNorm` for other fields — matching the slither pattern.

**Files:**
- Modify: `internal/system/nethandler_shared.go`

- [ ] **Step 1: Update `snapshotBaseFields` to use viewer-relative quantized encoding**

```go
// Quantization constants for snapshot encoding.
const (
	// qPosHalfRange must cover the max viewer-relative distance.
	// AoI radius is ~3000, but cross-cell replicas can be further.
	// CellSize (8192) + AoI margin covers the worst case.
	qPosHalfRange = float32(10000.0)
	qVelScale     = float32(2000.0)
	qSizeScale    = float32(500.0)
)

func snapshotBaseFields(w *quantize.SnapshotWriter, gw *game.GameWorld, ctx *gameNetContext, viewer *system.ViewerInfo, entry spatial.Entry) {
	relX, relY := entityRelativePos(gw, viewer, entry)

	// Viewer-relative positions — handles cross-cell replicas correctly.
	w.QRel(relX-viewer.X, qPosHalfRange)
	w.QRel(relY-viewer.Y, qPosHalfRange)

	var vx, vy float32
	if gw.C.Velocity.HasAll(entry.Entity) {
		vel := gw.C.Velocity.Get(entry.Entity)
		vx = vel.X
		vy = vel.Y
	}
	w.QVel(vx, qVelScale)
	w.QVel(vy, qVelScale)

	var rotation float32
	if gw.C.Rotation.HasAll(entry.Entity) {
		rotation = gw.C.Rotation.Get(entry.Entity).Angle
	}
	w.QAngle(rotation)

	w.QVel(entry.Radius, qSizeScale)
	w.QVel(entry.Width, qSizeScale)
	w.QVel(entry.Height, qSizeScale)

	if lb, ok := ctx.lockedBy[entry.Entity]; ok {
		w.Uint32(lb.netID)
		w.QNorm(lb.progress)
	} else {
		w.Uint32(0)
		w.QNorm(0)
	}
}
```

Update `baseFieldLayout` to match:

```go
// Fields: relX(2), relY(2), vx(2), vy(2), rotation(2), radius(2), width(2), height(2), lockedByID(4), lockedByProgress(1)
// Total: 21 bytes, 10 fields.
var baseFieldLayout = []int{2, 2, 2, 2, 2, 2, 2, 2, 4, 1}
```

- [ ] **Step 2: Update all 5 handler `SnapshotLayout()` methods to match**

Each handler's first 10 fields revert to `{2, 2, 2, 2, 2, 2, 2, 2, 4, 1}`:

- `nethandler_ship.go`: `[]int{2, 2, 2, 2, 2, 2, 2, 2, 4, 1, 1, 1, 1, 4, 4}`
- `nethandler_asteroid.go`: `[]int{2, 2, 2, 2, 2, 2, 2, 2, 4, 1, 4, 4}`
- `nethandler_npc.go`: `[]int{2, 2, 2, 2, 2, 2, 2, 2, 4, 1, 1, 1}`
- `nethandler_station.go`: `[]int{2, 2, 2, 2, 2, 2, 2, 2, 4, 1}`
- `nethandler_lootcrate.go`: `[]int{2, 2, 2, 2, 2, 2, 2, 2, 4, 1, -1}`

- [ ] **Step 3: Verify compilation**

Run: `go vet ./...`

- [ ] **Step 4: Commit**

```
feat(space): switch snapshot base fields to quantized viewer-relative encoding
```

---

### Task 2: Server — Replace protobuf bridge FrameWriter with binary FrameWriter

Replace `gameFrameWriter` in `replication_adapters.go`. Delete all the decode bridge functions. Add a `quantize.FrameEncoder` and send via `MakeEventRaw(SE_DELTA_WORLD_UPDATE, ...)`.

**Files:**
- Modify: `internal/system/replication_adapters.go`

- [ ] **Step 1: Replace `gameFrameWriter` with binary frame writer**

```go
type gameFrameWriter struct {
	gw      *game.GameWorld
	encoder *quantize.FrameEncoder
}

func (w *gameFrameWriter) WriteFrame(frame *system.ReplicationFrame) {
	gw := w.gw

	full := make([]quantize.FullEntry, len(frame.Full))
	for i := range frame.Full {
		fp := &frame.Full[i]
		full[i] = quantize.FullEntry{
			NetID:       fp.NetID,
			EntityType:  fp.Type,
			Snapshot:    fp.Snapshot,
			InitialData: fp.InitialData,
		}
	}

	deltas := make([]quantize.DeltaEntry, len(frame.Deltas))
	for i := range frame.Deltas {
		dp := &frame.Deltas[i]
		deltas[i] = quantize.DeltaEntry{
			NetID:      dp.NetID,
			EntityType: dp.Type,
			Data:       dp.Data,
		}
	}

	binData := w.encoder.Encode(
		frame.Tick, frame.Seq,
		frame.Viewer.X, frame.Viewer.Y,
		full, deltas, frame.Removed, frame.Exited,
	)

	// Copy because encoder reuses buffer.
	wireData := make([]byte, len(binData))
	copy(wireData, binData)

	// Send ack input seq as a separate reliable event (PlayerOwnState already does this).
	data := mmokit.MakeEventRaw(uint32(enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE), wireData)
	if data != nil {
		gw.ConnMgr.Send(frame.Viewer.ConnID, data)
	}
}
```

- [ ] **Step 2: Delete all protobuf bridge decode functions**

Remove from `replication_adapters.go`:
- `decodeSnapshot()`
- `decodeBaseFields()` (in `nethandler_shared.go`)
- `decodeLootCrateItems()` and `lootItem` struct (in `nethandler_lootcrate.go`)
- `decodeLengthPrefixedString()` (in `nethandler_shared.go`) — keep `encodeLengthPrefixedString()` since `InitialData` still uses it

Remove unused imports: `gamepb` from `replication_adapters.go`, `gamecomp` if no longer needed.

- [ ] **Step 3: Update FrameWriter instantiation in `network.go`**

```go
Frame: &gameFrameWriter{gw: gw, encoder: quantize.NewFrameEncoder(8192)},
```

Add `"github.com/zenion/mmokit/pkg/quantize"` import to `network.go`.

- [ ] **Step 4: Set `FullRefreshInterval` to 20 (keyframe every second)**

In `internal/game/game.go`:

```go
gw.FullRefreshInterval = uint32(eng.Config.TickRate) // keyframe every ~1 second
```

- [ ] **Step 5: Verify compilation**

Run: `go vet ./...`
Run: `make build`

- [ ] **Step 6: Commit**

```
feat(space): replace protobuf bridge FrameWriter with binary delta format
```

---

### Task 3: Web client — Delta decoder

Create a delta decoder for the space game web client. It must produce objects that conform to the `EntityState` interface from `@gen/game_pb.js` so the existing interpolation, entity manager, and renderers work unchanged.

**Files:**
- Create: `web-pixi/src/delta-decoder.ts`

- [ ] **Step 1: Create the delta decoder**

The decoder must:
1. Parse the binary frame header (tick, seq, viewerX, viewerY, counts)
2. For full entities: decode snapshot, store baseline, produce `EntityState`
3. For delta entities: apply delta to baseline, produce `EntityState`
4. Return a `WorldUpdate` object matching what `network.ts` currently gets from protobuf

Entity type constants must match `internal/component/types.go`:
- TypeShip = 0, TypeAsteroid = 1, TypeProjectile = 2, TypeStation = 3, TypeLootCrate = 4, TypeNPC = 5

Snapshot layouts (must match server `SnapshotLayout()`):
- Base fields (all types): `[2,2,2,2,2,2,2,2,4,1]` = 21 bytes, 10 fields
- Ship: base + `[1,1,1,4,4]` = 32 bytes, 15 fields
- Asteroid: base + `[4,4]` = 29 bytes, 12 fields
- NPC: base + `[1,1]` = 23 bytes, 12 fields
- Station: base only = 21 bytes, 10 fields
- LootCrate: base + var tail `[-1]` = 21+ bytes, 11 fields

Quantization constants (must match server):
- `qPosHalfRange = 10000`
- `qVelScale = 2000`
- `qSizeScale = 500`

The decoder produces plain objects matching the `EntityState` shape:
```typescript
interface DecodedEntity {
  id: number;
  entityType: number;
  x: number; y: number;       // absolute (viewerX + unRel offset)
  vx: number; vy: number;
  rotation: number;
  radius: number; width: number; height: number;
  lockedById: number;
  lockedByProgress: number;
  typeData: { case: string; value: any }; // ship/npc/asteroid/station/lootCrate
}

interface DeltaWorldUpdate {
  tick: number;
  entities: DecodedEntity[];
  removedIds: number[];
  killedIds: number[];
}
```

Key implementation notes:
- Positions are viewer-relative int16 (`QRel`): `worldX = viewerX + unRel(readInt16(), halfRange)`
- `viewerX/Y` come from the frame header (float32, absolute position)
- Velocity, radius, width, height use `unVel(readInt16(), scale)` — these are NOT viewer-relative
- Ship: health/shield are `unNorm(uint8)`, flags is uint8 (bit0=beam0, bit1=beam1), miningTargetId/combatTargetId are uint32
- Asteroid: itemId is uint32, resourceRemaining is float32
- NPC: health/shield are `unNorm(uint8)` same as ship
- LootCrate: var tail starts with uint16 byte-length prefix, then `[uint8 count][uint32 itemId, uint32 qty]*count`
- InitialData for ship: length-prefixed string (uint16 len + UTF-8 bytes) for pilot name
- InitialData decoding should preserve the name across delta updates (store in baseline alongside snapshot)

Delta encoding uses the same `applyDelta` pattern as slither's decoder — per-entity-type field sizes, bitmask + changed fields.

The `DecodedEntity` objects need to be compatible with `updateEntityFromServer()` in `interpolation.ts`, which expects the `EntityState` interface shape. Use `as any` casts or a thin adapter if needed, since the protobuf-generated `EntityState` has some TypeScript-specific union types.

- [ ] **Step 2: Verify TypeScript compiles**

Run: `cd web-pixi && npx tsc --noEmit`

- [ ] **Step 3: Commit**

```
feat(web): add binary delta decoder for space game
```

---

### Task 4: Web client — Wire into network.ts

Update `network.ts` to handle `SE_DELTA_WORLD_UPDATE` using the new decoder, alongside the existing `SE_WORLD_UPDATE` handler (keep both for backward compat during transition).

**Files:**
- Modify: `web-pixi/src/network.ts`

- [ ] **Step 1: Import decoder and add handler**

Add import:
```typescript
import { DeltaWorldDecoder, type DeltaWorldUpdate } from "./delta-decoder";
```

Add constant:
```typescript
const SE_DELTA_WORLD_UPDATE = 13;
```

Add instance to the network setup (near other state):
```typescript
const deltaDecoder = new DeltaWorldDecoder();
```

Add case in the `switch(evt.code)` block, BEFORE the `SE_WORLD_UPDATE` case:

```typescript
case SE_DELTA_WORLD_UPDATE: {
  const update = deltaDecoder.decode(new Uint8Array(evt.data));
  state.tickCount = update.tick;
  state.lastTickTime = performance.now();

  // Reuse existing cell-rebase and entity update logic.
  // The decoded entities have the same shape as protobuf EntityState.
  // [paste the same rebase + update + killed + removed + ability/chat logic]
  // BUT: update.entities replaces the protobuf entities
  // AND: abilityEvents/chatMessages are NOT in the binary frame (sent separately)

  // ... (see step 2 for full integration)
  break;
}
```

- [ ] **Step 2: Refactor world update processing into a shared function**

Extract the entity update logic from the `SE_WORLD_UPDATE` case into a shared function so both handlers can use it:

```typescript
function processWorldUpdate(
  state: GameState,
  entities: EntityState[],  // or DecodedEntity[] — same shape
  killedIds: number[],
  removedIds: number[],
) {
  // Cell rebase logic (lines 171-237 of current code)
  // Entity update loop
  // Killed/removed cleanup
}
```

Both `SE_WORLD_UPDATE` and `SE_DELTA_WORLD_UPDATE` call this function. The delta handler passes `update.entities`, `update.killedIds`, `update.removedIds`. Ability events and chat remain on the protobuf path (they're sent via separate events, not in the world update frame).

- [ ] **Step 3: Verify TypeScript compiles**

Run: `cd web-pixi && npx tsc --noEmit`

- [ ] **Step 4: Commit**

```
feat(web): handle SE_DELTA_WORLD_UPDATE in space game network layer
```

---

### Task 5: Cleanup — Remove legacy code

Remove the old protobuf `SE_WORLD_UPDATE` handler from the web client (server no longer sends it for entity updates). Remove server-side bridge code remnants.

**Files:**
- Modify: `web-pixi/src/network.ts` — remove `SE_WORLD_UPDATE` entity processing (keep for non-entity events if any)
- Modify: `internal/system/replication_adapters.go` — remove any remaining bridge imports
- Modify: `internal/system/nethandler_shared.go` — remove `decodeBaseFields` and related

- [ ] **Step 1: Remove protobuf WorldUpdate entity processing from web client**

In `network.ts`, the `SE_WORLD_UPDATE` case can be removed entirely (all entity state now comes via `SE_DELTA_WORLD_UPDATE`). Ability events and chat are sent via separate server event codes, not inside WorldUpdate.

Remove imports that are no longer needed: `WorldUpdateMsgSchema`, `type WorldUpdateMsg`, `EntityType` enum (if only used there — check other files first).

- [ ] **Step 2: Remove server-side bridge remnants**

Ensure these are gone from the server (should already be deleted in Task 2, verify):
- `decodeSnapshot()` in `replication_adapters.go`
- `decodeBaseFields()` in `nethandler_shared.go`
- `decodeLootCrateItems()` and `lootItem` in `nethandler_lootcrate.go`
- Unused `gamepb` imports

- [ ] **Step 3: Verify full build**

```bash
go vet ./...
go test ./...
make build
cd web-pixi && npx tsc --noEmit
```

- [ ] **Step 4: Commit**

```
refactor(space): remove protobuf bridge and legacy world update handler
```

---

### Task 6: Verify end-to-end

- [ ] **Step 1: Run the space game server**

```bash
make run
```

- [ ] **Step 2: Connect web client, verify gameplay**

Open `http://localhost:8080`. Check:
- Ship spawns and moves correctly
- Other ships/NPCs visible with correct positions
- Asteroids, stations, loot crates render properly
- Mining laser beams appear when mining
- Health/shield bars show on ships
- Cross-cell entities render at correct positions (not on boundary lines)
- Rotation follows movement direction
- Interpolation is smooth between ticks

- [ ] **Step 3: Final commit**

```
docs: mark space game binary wire format migration complete
```
