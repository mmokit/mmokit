# S2: Handoff Protocol Wiring — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the existing `HandoffStateMachine` into production, replacing the `MsgTransfer` + `Ghost` + `MsgArrivalConfirm` protocol with a `Prepare → Promote → Commit` handoff that eliminates cold-start stalls and provides baseline continuity across cell boundaries.

**Architecture:** The handoff state machine tracks `(entity, neighbor)` pairs through four phases: Unseen → Border → Promoted → Handoff. `BorderDispatcher.Tick` drives phase transitions. `BoundarySystem` is refactored from "detect crossing → serialize → transfer" to "detect crossing → queue event." The `cellBridge.PostSystems` loop reads the crossing queue and issues commits. A new `Shadow` component marks pre-authority entities on the destination cell. `ForwardInput` handles the single-tick input routing overlap. `MsgTransfer`, `MsgArrivalConfirm`, and the Ghost-as-transfer mechanism are retired.

**Tech Stack:** Go, existing `pkg/universe/`, `pkg/component/`, `pkg/engine/`, Ark ECS v0.7.1. No new dependencies.

**Spec:** [docs/superpowers/specs/2026-04-12-distributed-mesh-design.md](../specs/2026-04-12-distributed-mesh-design.md) — Section 4 (Handoff Protocol)

---

## File Structure

### Files to create

| Path | Responsibility |
|---|---|
| `pkg/component/shadow.go` | `Shadow` component struct |
| `pkg/universe/handoff_driver.go` | `HandoffDriver` — orchestrates state machine transitions, Prepare/Commit emission |
| `pkg/universe/handoff_driver_test.go` | Integration tests for handoff flow |

### Files to modify

| Path | What changes |
|---|---|
| `pkg/query/query.go` | Add `Shadow` to default exclusion set alongside `Ghost` + `Replica` |
| `pkg/universe/cell.go` | Handle `MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgForwardInput` in `processMessage` |
| `pkg/universe/world_base.go` | `SpawnShadow()`, `PromoteShadow()`, crossing-event queue, `ForwardInput()` helper |
| `pkg/universe/cell_bridge_impl.go` | Wire `HandoffDriver` into `PostSystems`, add `SendHandoffPrepare/Commit/ForwardInput` |
| `pkg/universe/bridge.go` | Add `SendHandoffPrepare`, `SendHandoffCommit`, `SendForwardInput` to `Bridge` interface |
| `pkg/universe/boundary_system.go` | Refactor: detect crossing → queue event instead of direct SendTransfer |
| `pkg/universe/border_replication.go` | Promote detection: entity in PromoteRadius triggers state machine transition |
| `pkg/universe/coordinator.go` | `UpdatePlayerRoute` method |
| `pkg/mmokit/mmokit.go` | Export new types: `Shadow`, `HandoffDriver` |

### Files to modify (retirement of old protocol)

| Path | What changes |
|---|---|
| `pkg/universe/bridge.go` | Remove `SendTransfer`, `SendArrivalConfirm` from interface |
| `pkg/universe/cell.go` | Remove `MsgTransfer`, `MsgArrivalConfirm` handlers |
| `pkg/universe/world_base.go` | Remove `SerializeEntity` (replaced by reused TransferFrame in Prepare payload) |

---

## Task Breakdown

### Task 1: Add Shadow component + query exclusion

**Files:**
- Create: `pkg/component/shadow.go`
- Modify: `pkg/query/query.go`
- Test: `pkg/query/query_test.go` (or inline)

- [ ] **Step 1: Create Shadow component**

Create `pkg/component/shadow.go`:

```go
package component

// Shadow marks a pre-authority entity created from a HandoffPrepare
// payload. The destination cell holds the shadow while the source
// completes the warmup window. On HandoffCommit, the Shadow component
// is removed and the entity becomes a normal local entity.
//
// Game systems exclude shadows via mmokit.Query's default Without
// filter (same pattern as Ghost and Replica). The ReplicationSystem
// DOES iterate shadows so nearby players on the destination see the
// approaching entity before authority commits.
type Shadow struct {
	// SourceCellID is the cell that currently owns the entity.
	SourceCellID string
	// NetID is the entity's network ID (matches NetworkID.ID).
	NetID uint32
	// Epoch is the NEW authority epoch that will apply on commit.
	Epoch uint32
}
```

- [ ] **Step 2: Add Shadow to default query exclusions**

In `pkg/query/query.go`, find where `Ghost` and `Replica` are added to the default exclusion set. Add `Shadow` alongside them. The exact mechanism depends on how the exclusions are built — read the file and follow the pattern.

- [ ] **Step 3: Verify**

```bash
go vet ./... && go test -count=1 ./pkg/query/ ./pkg/component/
```

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat(component): add Shadow component for pre-authority handoff entities

Shadow marks entities created from HandoffPrepare payloads on the
destination cell. Excluded from default mmokit.Query alongside Ghost
and Replica. ReplicationSystem still iterates shadows for client
visibility. Removed on HandoffCommit when entity becomes local."
```

---

### Task 2: Crossing-event queue on WorldBase

Refactor `BoundarySystem` from "detect crossing → serialize → transfer" to "detect crossing → queue event." This decouples crossing detection from the transfer protocol so the handoff driver can decide how to handle each crossing.

**Files:**
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/boundary_system.go`

- [ ] **Step 1: Add crossing-event queue to WorldBase**

In `pkg/universe/world_base.go`, add a new exported type and field:

```go
// CrossingEvent records that an entity has crossed a cell boundary
// and needs to be handed off. The handoff driver reads and drains
// this queue in PostSystems.
type CrossingEvent struct {
	Entity     ecs.Entity
	NetID      uint32
	ConnID     uint32   // non-zero for player entities
	Username   string   // non-empty for player entities
	DestCellID string   // cell the entity crossed into
	WorldX     float32  // world-space position at crossing
	WorldY     float32  // world-space position at crossing
}
```

Add to WorldBase struct:
```go
crossingQueue []CrossingEvent
```

Add methods:
```go
func (b *WorldBase) QueueCrossing(evt CrossingEvent) {
	b.crossingQueue = append(b.crossingQueue, evt)
}

func (b *WorldBase) DrainCrossingQueue() []CrossingEvent {
	q := b.crossingQueue
	b.crossingQueue = b.crossingQueue[:0]
	return q
}
```

- [ ] **Step 2: Refactor BoundarySystem to use the queue**

In `pkg/universe/boundary_system.go`, find the code that currently does the transfer (it calls `Bridge.SendTransfer`, adds Ghost, etc.). Replace it:

**BEFORE** (per-entity in the transfer batch):
- Serialize entity
- Add Ghost component
- Call Bridge.SendTransfer
- Handle player session transition

**AFTER** (per-entity in the transfer batch):
- Queue a `CrossingEvent` via `WorldBase.QueueCrossing()`
- Do NOT serialize, do NOT add Ghost, do NOT call SendTransfer
- The handoff driver (Task 5) will read the queue and handle the rest

Keep the position normalization, clamping, and boundary detection logic unchanged. Only the "what to do when a crossing is detected" part changes.

**IMPORTANT**: The old code adds Ghost + sends transfer + removes player session all in one pass. The new code just queues the event. The entity stays alive and in-place until the handoff driver processes it. This means the entity may briefly exist on both sides of the boundary (for 1-2 ticks until PostSystems runs). This is fine — the handoff protocol is designed for exactly this overlap.

- [ ] **Step 3: Verify**

The existing tests will still pass because `BoundarySystem.Update()` is called in the game loop, and without a handoff driver wired up yet, the crossing queue just accumulates (entities stay alive). This is the intermediate state before Task 5 wires the driver.

```bash
go vet ./... && go test -count=1 ./...
```

Expected: ALL PASS. Entities will no longer transfer on crossing (the queue is never drained yet), but unit tests that test boundary behavior may need adjustment if they assert on Ghost/Transfer.

If tests fail because they expect Ghost to be added or Transfer to be sent, update them to assert on the crossing queue instead. Read the failing test to understand what it expected and adapt.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(universe): BoundarySystem queues crossing events instead of direct transfer

CrossingEvent queue on WorldBase replaces the direct SendTransfer +
Ghost path in BoundarySystem.Update(). The handoff driver (next commit)
drains the queue in PostSystems and decides whether to Prepare/Commit
or instant-transfer. Decouples boundary detection from transfer protocol."
```

---

### Task 3: HandoffDriver — state machine + Prepare emission

The HandoffDriver is the orchestrator. It lives on the cellBridge (or as a standalone struct) and runs each tick after systems. It manages the HandoffStateMachine, detects promote-radius entries, emits MsgHandoffPrepare, ticks warmup, and processes crossing events.

**Files:**
- Create: `pkg/universe/handoff_driver.go`
- Modify: `pkg/universe/cell_bridge_impl.go`
- Modify: `pkg/universe/bridge.go`

- [ ] **Step 1: Add handoff methods to Bridge interface**

In `pkg/universe/bridge.go`, add to the `Bridge` interface:

```go
// SendHandoffPrepare sends a handoff preparation payload to the destination cell.
SendHandoffPrepare(destCellID string, payload *HandoffPreparePayload)
// SendHandoffCommit sends a handoff commit to the destination cell.
SendHandoffCommit(destCellID string, payload *HandoffCommitPayload)
// SendForwardInput forwards a player input frame to the new owner cell.
SendForwardInput(destCellID string, payload *ForwardInputPayload)
```

Add no-op implementations to `NoopBridge`.

- [ ] **Step 2: Implement bridge methods in cellBridge**

In `pkg/universe/cell_bridge_impl.go`, implement the three methods. They construct `CellMessage` envelopes and write to the destination cell's Inbox, same pattern as `SendTransfer` but with the handoff payload fields:

```go
func (b *cellBridge) SendHandoffPrepare(destCellID string, payload *HandoffPreparePayload) {
	b.coord.mu.RLock()
	dest, ok := b.coord.Cells[destCellID]
	b.coord.mu.RUnlock()
	if !ok {
		return
	}
	dest.Inbox <- CellMessage{
		Type:           MsgHandoffPrepare,
		FromCellID:     b.cell.ID,
		HandoffPrepare: payload,
	}
}
```

Same pattern for `SendHandoffCommit` and `SendForwardInput`.

- [ ] **Step 3: Create HandoffDriver**

Create `pkg/universe/handoff_driver.go`:

```go
package universe

import (
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/mlange-42/ark/ecs"
)

// HandoffDriver orchestrates entity handoff across cell boundaries.
// It runs each tick in PostSystems (after game systems, after
// BorderDispatcher), managing the HandoffStateMachine transitions,
// emitting Prepare/Commit messages, and processing crossing events.
type HandoffDriver struct {
	base    *WorldBase
	sm      *HandoffStateMachine
	bridge  Bridge
	posMap  *ecs.Map1[component.Position]
	netMap  *ecs.Map1[component.NetworkID]
	kindMap *ecs.Map1[component.EntityKind]
}

func NewHandoffDriver(base *WorldBase, bridge Bridge) *HandoffDriver {
	w := base.ECSWorld()
	return &HandoffDriver{
		base:    base,
		sm:      NewHandoffStateMachine(),
		bridge:  bridge,
		posMap:  ecs.NewMap1[component.Position](w),
		netMap:  ecs.NewMap1[component.NetworkID](w),
		kindMap: ecs.NewMap1[component.EntityKind](w),
	}
}

// Tick runs one pass of the handoff driver. Called from cellBridge.PostSystems
// after BorderDispatcher.Tick.
func (hd *HandoffDriver) Tick(currentTick uint64) {
	// Phase 1: Process crossing events from BoundarySystem.
	// For each crossing, if the entity is in Promoted phase with
	// CanCommit, emit MsgHandoffCommit. Otherwise, do an immediate
	// commit (degenerate case for fast-moving entities or teleports).
	for _, evt := range hd.base.DrainCrossingQueue() {
		hd.handleCrossing(evt, currentTick)
	}
}

func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentTick uint64) {
	k := HandoffKey{EntityNetID: evt.NetID, NeighborID: evt.DestCellID}

	// If already in cooldown, skip (prevents thrash).
	if hd.sm.InCooldown(k, currentTick) {
		return
	}

	// If we haven't prepared yet (entity crossed before promote
	// detection — fast mover or teleport), do an immediate
	// prepare+commit in one tick.
	state := hd.sm.State(k)
	if state == HandoffUnseen || state == HandoffBorder {
		// Emergency path: prepare + immediate commit.
		hd.emitPrepare(evt.Entity, evt.NetID, evt.DestCellID, currentTick)
		hd.sm.SetState(k, HandoffPromoted)
	}

	// Commit regardless of warmup count (entity already crossed).
	hd.emitCommit(evt, currentTick)
	hd.sm.SetState(k, HandoffHandoff)
	hd.sm.EnterCooldown(k, currentTick)
}

func (hd *HandoffDriver) emitPrepare(entity ecs.Entity, netID uint32, destCellID string, currentTick uint64) {
	if !hd.base.eng.ECS.Alive(entity) {
		return
	}

	// Serialize entity using the existing TransferFrame format.
	data, err := hd.base.SerializeEntity(entity)
	if err != nil {
		return
	}

	// Collect baselines for client continuity.
	// TODO: wire baseline collection from ReplicationSystem's conn stores.
	// For now, pass empty baselines — shadow entity will work but
	// clients may see a brief delta spike on promote.

	nid := hd.netMap.Get(entity)
	var kind uint16
	if hd.kindMap.HasAll(entity) {
		kind = uint16(hd.kindMap.Get(entity).Type)
	}

	hd.bridge.SendHandoffPrepare(destCellID, &HandoffPreparePayload{
		NetID:           netID,
		Epoch:           nid.Epoch + 1,
		Kind:            kind,
		TransferBlob:    data,
		ClientBaselines: nil, // wired in a follow-up
		ExpectedTick:    currentTick + MinWarmupTicks,
		OldEpoch:        nid.Epoch,
	})
}

func (hd *HandoffDriver) emitCommit(evt CrossingEvent, currentTick uint64) {
	// Bump epoch on the source entity.
	if hd.netMap.HasAll(evt.Entity) {
		nid := hd.netMap.Get(evt.Entity)
		nid.Epoch++
	}

	hd.bridge.SendHandoffCommit(evt.DestCellID, &HandoffCommitPayload{
		NetID:      evt.NetID,
		Epoch:      hd.netMap.Get(evt.Entity).Epoch,
		CommitTick: currentTick,
	})

	// Handle player session transfer.
	if evt.ConnID != 0 {
		hd.bridge.OnPlayerTransfer(evt.ConnID, evt.DestCellID)
		// Transition session to transferring + remove from local manager.
		if sess := hd.base.eng.Players.ByConnID(evt.ConnID); sess != nil {
			hd.base.eng.Players.Transition(sess, 2) // StateTransferring
			hd.base.eng.Players.Remove(sess)
		}
	}

	// Mark entity for removal on the source (it now lives on dest).
	hd.base.eng.MarkForRemoval(evt.Entity)

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff commit: netID=%d -> %s tick=%d",
		hd.base.cellID, evt.NetID, evt.DestCellID, currentTick)
}
```

Note: This is a starting implementation. The promote-detection (entities approaching PromoteRadius) is a follow-up enhancement that adds early Prepare emission. For v1, we do the simpler "Prepare + Commit at crossing time" which is functionally equivalent to the old instant transfer but uses the new message types. The warmup window benefits come when promote-detection is added.

- [ ] **Step 4: Wire HandoffDriver into cellBridge.PostSystems**

In `pkg/universe/cell_bridge_impl.go`, add a `handoffDriver *HandoffDriver` field to `cellBridge`. Initialize it in `ensureBorderDispatcher` (alongside the BorderDispatcher):

```go
if b.handoffDriver == nil {
	b.handoffDriver = NewHandoffDriver(b.cell.Base, b)
}
```

Call it in `PostSystems` after the BorderDispatcher:

```go
func (b *cellBridge) PostSystems() {
	b.ensureBorderDispatcher()
	if b.borderDispatcher != nil {
		currentTick := uint64(b.cell.Engine.Tick)
		b.borderDispatcher.Tick(currentTick)
	}
	if b.handoffDriver != nil {
		currentTick := uint64(b.cell.Engine.Tick)
		b.handoffDriver.Tick(currentTick)
	}
	b.cell.Base.ExpireReplicas()
}
```

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(universe): HandoffDriver orchestrates Prepare/Commit emission

HandoffDriver runs in PostSystems after BorderDispatcher. Drains the
crossing-event queue from BoundarySystem, emits MsgHandoffPrepare +
MsgHandoffCommit to the destination cell, bumps epoch on the source
entity, handles player session transfer, and marks the source entity
for removal. Uses the existing HandoffStateMachine for cooldown
tracking. Bridge interface gains SendHandoffPrepare/Commit/ForwardInput."
```

---

### Task 4: Handle MsgHandoffPrepare — create Shadow entity

**Files:**
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/cell.go`

- [ ] **Step 1: Add SpawnShadow to WorldBase**

In `pkg/universe/world_base.go`, add a method that creates a shadow entity from a HandoffPrepare payload. This reuses the existing `SpawnFromTransferCore` logic but adds a `Shadow` component instead of making it a normal local entity:

```go
// SpawnShadow creates a pre-authority shadow entity from a handoff
// prepare payload. The shadow has the full component set (via
// SpawnFromTransferCore) plus a Shadow marker. Game systems exclude
// it; the ReplicationSystem includes it for client visibility.
// Returns the spawned entity and any error.
func (b *WorldBase) SpawnShadow(payload *HandoffPreparePayload) (ecs.Entity, error) {
	entity, frame, err := b.SpawnFromTransferCore(payload.TransferBlob)
	if err != nil {
		return ecs.Entity{}, err
	}

	// Add Shadow component (replaces the normal "local entity" status).
	shadowMap := ecs.NewMap1[component.Shadow](b.eng.ECS)
	shadowMap.Add(entity, &component.Shadow{
		SourceCellID: "", // filled by caller from msg.FromCellID
		NetID:        payload.NetID,
		Epoch:        payload.Epoch,
	})

	b.eng.Log.Log(CatMeshTransfer,
		"[%s] shadow created: netID=%d epoch=%d from prepare",
		b.cellID, frame.NetworkID, payload.Epoch)

	return entity, nil
}
```

- [ ] **Step 2: Handle MsgHandoffPrepare in Cell.processMessage**

In `pkg/universe/cell.go`, add a case for `MsgHandoffPrepare` in `processMessage`:

```go
case MsgHandoffPrepare:
	if msg.HandoffPrepare == nil {
		return
	}
	c.Log.Log(CatMeshMsg, "[%s] msg MsgHandoffPrepare from=%s netID=%d epoch=%d",
		c.ID, msg.FromCellID, msg.HandoffPrepare.NetID, msg.HandoffPrepare.Epoch)

	// Remove any pre-existing replica with the same NetID.
	if msg.HandoffPrepare.NetID != 0 {
		c.Base.RemoveReplicaByNetID(msg.HandoffPrepare.NetID)
	}

	entity, err := c.Base.SpawnShadow(msg.HandoffPrepare)
	if err != nil {
		c.Log.Log(CatMeshMsg, "[%s] shadow spawn failed: %v", c.ID, err)
		return
	}

	// Set the source cell ID on the shadow.
	shadowMap := ecs.NewMap1[component.Shadow](c.Engine.ECS)
	if shadowMap.HasAll(entity) {
		s := shadowMap.Get(entity)
		s.SourceCellID = msg.FromCellID
	}
```

- [ ] **Step 3: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat(universe): handle MsgHandoffPrepare by creating Shadow entity

WorldBase.SpawnShadow reuses SpawnFromTransferCore but adds a Shadow
component marker. Cell.processMessage handles MsgHandoffPrepare by
removing any pre-existing replica and spawning the shadow. Shadow
entities are visible to ReplicationSystem (client visibility) but
excluded from game systems (default query exclusion)."
```

---

### Task 5: Handle MsgHandoffCommit — promote Shadow to local

**Files:**
- Modify: `pkg/universe/world_base.go`
- Modify: `pkg/universe/cell.go`

- [ ] **Step 1: Add PromoteShadow to WorldBase**

```go
// PromoteShadow removes the Shadow component from a shadow entity,
// making it a normal local entity that game systems will process.
// Returns false if the entity is not found or not a shadow.
func (b *WorldBase) PromoteShadow(netID uint32) bool {
	shadowMap := ecs.NewMap1[component.Shadow](b.eng.ECS)
	netIDMap := ecs.NewMap1[component.NetworkID](b.eng.ECS)

	// Find the shadow entity by scanning. In production this would use
	// an index; for now a linear scan is fine (shadows are rare, <10).
	filter := ecs.NewFilter2[component.Shadow, component.NetworkID](b.eng.ECS)
	query := filter.Query()
	for query.Next() {
		_, nid := query.Get()
		if nid.ID == netID {
			entity := query.Entity()
			query.Close()

			// Remove Shadow component — entity becomes local.
			shadowMap.Remove(entity)

			// Remove TransferCooldown if present (added by SpawnFromTransferCore).
			// Shadow entities shouldn't have a transfer cooldown preventing
			// immediate re-crossing after promotion.
			cooldownMap := ecs.NewMap1[component.TransferCooldown](b.eng.ECS)
			if cooldownMap.HasAll(entity) {
				cooldownMap.Remove(entity)
			}

			b.eng.Log.Log(CatMeshTransfer,
				"[%s] shadow promoted: netID=%d", b.cellID, netID)
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Handle MsgHandoffCommit in Cell.processMessage**

```go
case MsgHandoffCommit:
	if msg.HandoffCommit == nil {
		return
	}
	c.Log.Log(CatMeshMsg, "[%s] msg MsgHandoffCommit from=%s netID=%d epoch=%d",
		c.ID, msg.FromCellID, msg.HandoffCommit.NetID, msg.HandoffCommit.Epoch)

	if !c.Base.PromoteShadow(msg.HandoffCommit.NetID) {
		c.Log.Log(CatMeshMsg, "[%s] no shadow found for netID=%d, ignoring commit",
			c.ID, msg.HandoffCommit.NetID)
		return
	}

	// If this is a player entity, register the session on this cell.
	// The player's ConnID is embedded in the TransferBlob that created
	// the shadow. We need to wire it now.
	// Player wiring is handled by SpawnFromTransferCore's onPlayerTransferReceived
	// hook which already ran during SpawnShadow. The session is already
	// registered. On promote we just need to activate it.
```

Note: Player session handling during shadow→promote needs careful attention. Read the existing `SpawnFromTransferCore` hooks and `RegisterTransferSession` to understand what was already done at Prepare time vs what needs to happen at Commit time. The shadow creation already called `SpawnFromTransferCore` which triggered player registration. On promote, the player is already wired — we just need to ensure the session transitions to Active.

- [ ] **Step 3: Handle MsgForwardInput in Cell.processMessage**

```go
case MsgForwardInput:
	if msg.ForwardInput == nil {
		return
	}
	c.Log.Log(CatMeshMsg, "[%s] msg MsgForwardInput from=%s conn=%d",
		c.ID, msg.FromCellID, msg.ForwardInput.ConnID)
	// Inject the forwarded input into this cell's input processing.
	// The input is a raw client frame that should be processed as if
	// it arrived from the player's connection directly.
	c.Engine.ConnMgr.InjectInput(msg.ForwardInput.ConnID, msg.ForwardInput.InputBlob)
```

Note: `ConnMgr.InjectInput` may not exist yet. If not, add it as a method on ConnManager that appends to the connection's input buffer. Check `pkg/net/server.go` for the input buffer API.

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(universe): handle MsgHandoffCommit + MsgForwardInput

PromoteShadow removes the Shadow component, making the entity a normal
local entity. Cell.processMessage handles MsgHandoffCommit by promoting
the shadow and MsgForwardInput by injecting the input frame into the
local ConnManager."
```

---

### Task 6: Retire MsgTransfer + MsgArrivalConfirm

**Files:**
- Modify: `pkg/universe/bridge.go`
- Modify: `pkg/universe/cell.go`
- Modify: `pkg/universe/cell_bridge_impl.go`
- Modify: `pkg/universe/boundary_system.go`

- [ ] **Step 1: Remove SendTransfer and SendArrivalConfirm from Bridge interface**

In `pkg/universe/bridge.go`, remove:
- `SendTransfer(destCellID string, data []byte, netID uint32)`
- `SendArrivalConfirm(destCellID string, confirm *ArrivalConfirmMsg)`

Remove the corresponding no-op implementations from `NoopBridge`.

- [ ] **Step 2: Remove implementations from cellBridge**

In `pkg/universe/cell_bridge_impl.go`, remove the `SendTransfer` and `SendArrivalConfirm` methods.

- [ ] **Step 3: Remove MsgTransfer + MsgArrivalConfirm handlers from Cell.processMessage**

In `pkg/universe/cell.go`, remove the `case MsgTransfer:` and `case MsgArrivalConfirm:` blocks from `processMessage`.

- [ ] **Step 4: Clean up BoundarySystem**

In `pkg/universe/boundary_system.go`, ensure the old Ghost-add + SendTransfer code is fully removed (should already be gone from Task 2, but verify). The BoundarySystem should now ONLY queue CrossingEvents.

- [ ] **Step 5: Remove Ghost-as-transfer from WorldBase**

In `pkg/universe/world_base.go`:
- `RemoveGhostByNetID` can stay (it's used for cleanup) but its "confirmed" logic is obsolete. Simplify: just mark for removal.
- The `TickGhosts` method can stay for cleanup but doesn't need the "confirmed" flag logic.

- [ ] **Step 6: Remove ArrivalConfirmMsg if unused**

In `pkg/universe/message.go`, check if `ArrivalConfirmMsg` and `MsgArrivalConfirm` are still referenced anywhere. If not, remove them. Keep `MsgTransfer` as a constant (it may be referenced in tests or docs) but add a `// Deprecated:` comment.

- [ ] **Step 7: Fix compile errors**

The removal will cause compile errors in any code that references the old Bridge methods. Fix each one:
- If it was calling `SendTransfer`: it should now be queuing a CrossingEvent
- If it was calling `SendArrivalConfirm`: remove the call entirely

- [ ] **Step 8: Verify**

```bash
go vet ./... && go test -count=1 ./...
```

Tests that relied on the old transfer protocol will need updating. The key test files are:
- `internal/game/cell_test.go` — may test transfer behavior
- `pkg/universe/universe_test.go` — may test transfer messages
- Any test that sends `MsgTransfer` or expects `MsgArrivalConfirm`

Update these tests to use the new handoff messages or remove them if they're no longer relevant.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "refactor(universe): retire MsgTransfer + MsgArrivalConfirm protocol

SendTransfer and SendArrivalConfirm removed from Bridge interface.
MsgTransfer and MsgArrivalConfirm handlers removed from Cell.processMessage.
BoundarySystem now exclusively queues CrossingEvents; the HandoffDriver
handles all entity ownership transfers via Prepare/Commit. Ghost cleanup
simplified (no more 'confirmed' flag)."
```

---

### Task 7: Integration tests with LoopbackBridge

**Files:**
- Create: `pkg/universe/handoff_driver_test.go`

- [ ] **Step 1: Write integration test**

This test sets up two cells connected via LoopbackBridge, spawns an entity on cell A, moves it across the boundary, and asserts the full handoff flow.

The test should:
1. Create two `WorldBase` instances (cell_0_0 and cell_1_0)
2. Connect them via LoopbackBridge with zero latency
3. Spawn an entity on cell_0_0 near the right boundary
4. Move the entity past the boundary (set position beyond cell bounds)
5. Run BoundarySystem.Update on cell_0_0 → should queue a CrossingEvent
6. Run HandoffDriver.Tick on cell_0_0 → should emit Prepare + Commit
7. Process messages on cell_1_0 (drain inbox)
8. Assert: shadow entity created on cell_1_0, then promoted to local
9. Assert: entity removed from cell_0_0
10. Assert: entity alive on cell_1_0 with correct position and components

Note: This test requires significant setup — two full cells with engines, player managers, etc. Read existing tests in `pkg/universe/` to see how test cells are constructed (look at `border_replication_apply_test.go` for `newTestWorldBase` pattern). You may need to extend the test infrastructure.

If the full two-cell setup is too complex for a unit test, write a simpler test that:
1. Creates one WorldBase
2. Manually calls `SpawnShadow` with a crafted payload
3. Asserts shadow entity exists with Shadow component
4. Calls `PromoteShadow`
5. Asserts Shadow component removed, entity is local

This validates the core mechanics without the full mesh infrastructure.

- [ ] **Step 2: Run tests**

```bash
go test -v -run TestHandoff ./pkg/universe/
```

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "test(universe): handoff driver integration tests

Tests cover Shadow entity creation from HandoffPrepare, promotion on
HandoffCommit, and the crossing-event → Prepare → Commit flow."
```

---

### Task 8: Smoke test + build verification

- [ ] **Step 1: Full build + test**

```bash
go vet ./... && go test -count=1 ./... && just build
```

- [ ] **Step 2: Build examples**

```bash
go build ./examples/4node-basic/ && go build ./examples/simple/
```

- [ ] **Step 3: Commit any fixes**

If smoke testing reveals issues, fix and commit.

---

## Verification Checklist

After completing all tasks, verify:

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./...` all pass
- [ ] `just build` produces `bin/server`
- [ ] `Shadow` component exists and is excluded from default queries
- [ ] `BoundarySystem` queues crossing events instead of direct transfer
- [ ] `HandoffDriver` drains crossing queue and emits Prepare + Commit
- [ ] `Cell.processMessage` handles `MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgForwardInput`
- [ ] `MsgTransfer` and `MsgArrivalConfirm` handlers are removed from `Cell.processMessage`
- [ ] `SendTransfer` and `SendArrivalConfirm` removed from `Bridge` interface
- [ ] Integration tests verify shadow creation + promotion
- [ ] Space game can be built (even if runtime behavior needs Task 10 validation)
