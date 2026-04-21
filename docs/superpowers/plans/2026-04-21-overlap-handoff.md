# Overlap Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire up the Prepare → Overlap → Commit handoff protocol so cell crossings are literally invisible to clients — no blink, no position snap, no interpolation reset — for both same-process sibling handoffs and cross-host handoffs.

**Architecture:** Four coordinated server-side changes (shadows participate in border push, `upsertBorderReplica` updates shadows in place, source gets a `DemoteLiveToReplica` mirror of `PromoteShadow`, neighbors track multi-source presence), plus a destination-side watchdog and a blink-detector invariant in the ReplicationSystem. Client-side web-pixi work is verify + harden (defensive decode paths). Shadows keep a fresh `CreatedTick` so watchdog sweeps can clean orphans.

**Tech Stack:** Go (server), TypeScript/bun (web-pixi client), existing category-based logger + commit-log ring + State Integrity invariant framework, existing S7 test fixture + 4node-basic bot harness as the regression oracle.

**Source spec:** [`docs/superpowers/specs/2026-04-21-overlap-handoff-design.md`](../specs/2026-04-21-overlap-handoff-design.md)

**Rollout order (strict):** A → B → C → D → E → F → G. Each phase ends with a reviewable commit, passes the existing S7 suite, and is independently shippable — no phase leaves the tree in a broken state.

---

## Lessons carried forward

1. **Wire-format schema = runtime bytes** (`feedback_wire_format_schema_runtime_match`). Do not gate entity-kind bindings on runtime topology state. The overlap path must not introduce new conditional fields that differ between schema dump and server output.
2. **Search for parallel code paths** (lesson 3 from State Integrity). `upsertBorderReplica`, `SpawnFromTransferCore`, `SpawnShadow`, `PromoteShadow`, and `MarkForRemoval` are the spawn/despawn sites that touch `replicaNetIDs` + `netIDIdx` + `borderLastSeen`. When touching any one, verify the others. Task D6 is a literal grep step.
3. **`selfNetID` exclusion** (`feedback_farewell_excludes_self`). Handoff for the viewer's own entity must not emit `Removed` or `Exited` for their own netID. The existing replication code handles this for `exited`; verify no new farewell path is introduced in Task F.
4. **Bot-load smoke testing before declaring done.** Go tests can be green while the real bug shows up under 60 bots + split/merge churn. Phase G runs a scripted distributed session with blink detector + StrictNetIDIndex + InvariantPanic.
5. **No Cell field shadow** (`feedback_no_cell_field_shadow`). When adding methods to `WorldBase` (e.g. `DemoteLiveToReplica`), do not introduce a field named `Cell` on any GameWorld embedder — that shadows the method.
6. **Position quantization** (`feedback_position_quantization`). DemoteLiveToReplica preserves Position/Velocity untouched; do not accidentally re-quantize them during the in-place transition.

---

## Scope check

The spec covers one tightly-scoped subsystem (handoff mechanics + client-side decode robustness + one invariant). All four server changes must ship together or the blink regresses; splitting Change 1 from Change 4 leaves neighbors evicting replicas that both sources are still pushing. The client-side changes (Phase F) and the blink detector (Phase E) are defensive but small — bundling them keeps the bot-load gate meaningful.

---

## File structure

**Modified:**
- `pkg/component/shadow.go` — add `CreatedTick` field (Task A1)
- `pkg/universe/netid_index.go` — add explicit `Demote` transition (Task B1)
- `pkg/universe/world_base.go` — `DemoteLiveToReplica` method, `upsertBorderReplica` shadow fast-path, `ApplyBorderFrame` multi-source check, `SpawnShadow` sets CreatedTick (Tasks A2, B2, C1, D1)
- `pkg/universe/border_replication.go` — dispatcher filter includes Shadow (Task C2)
- `pkg/universe/handoff_driver.go` — split crossing handler into Prepare path + Promoted-walk Commit path (Task D2)
- `pkg/universe/handoff_driver_test.go` — extend with overlap tests (Tasks A3, B3, C3, D3)
- `pkg/universe/cell.go` — destination-side Shadow watchdog on tick (Task D4)
- `pkg/universe/coordinator.go` — add `Config.BlinkDetectorTicks` (Task E1)
- `pkg/universe/commit_log_categories.go` — add `CatEventsReplication` (Task E2)
- `pkg/system/replication.go` — per-conn recent-removals ring + blink detection on spawn emission (Tasks E3, E4)
- `pkg/system/replication_test.go` — unit tests for the blink detector (Task E5)
- `pkg/universe/loopback_bridge_test.go` — 2-host handoff integration test wiring (Task F1)
- `web-pixi/src/network.ts` — UPDATE synthesizes SPAWN for unknown netID; SPAWN coalesces to UPDATE (Task F2)
- `web-pixi/src/__tests__/interpolation.test.ts` — gap-preserves-interp regression test (Task F3)

**New:**
- `pkg/universe/overlap_test.go` — integration test for source+dest simultaneous broadcast (Task F1)

**No deletions.** The v1 handoff driver's `MarkForRemoval` call on the source is replaced by `DemoteLiveToReplica`; the code line is rewritten, not a separate file.

---

## Phase A — Shadow `CreatedTick` groundwork

Before splitting handoff into two phases (Prepare vs Commit), the destination needs a way to detect orphaned Shadows for the watchdog.

### Task A1: Add `CreatedTick` to `component.Shadow`

**Files:**
- Modify: `pkg/component/shadow.go`

- [ ] **Step 1: Extend the Shadow struct**

Edit `pkg/component/shadow.go` and add a `CreatedTick` field after `Epoch`:

```go
package component

type Shadow struct {
	SourceCellID string
	NetID        uint32
	Epoch        uint32
	// CreatedTick is the destination cell's game-loop tick at the moment
	// SpawnShadow inserted this component. The cell's per-tick watchdog
	// uses it to detect orphaned shadows (no matching Commit arrived
	// within MaxWarmupTicks) and clean them up.
	CreatedTick uint64
}
```

- [ ] **Step 2: Run `go vet ./...`**

Run: `cd . && go vet ./...`
Expected: PASS (the field is optional; nothing reads it yet).

- [ ] **Step 3: Commit**

```bash
cd .
git add pkg/component/shadow.go
git commit -m "feat(handoff): add Shadow.CreatedTick for watchdog sweep

Preparation for the overlap handoff protocol — destination-side watchdog
walks Shadow entities and cleans up any that have lived past
MaxWarmupTicks without a matching Commit."
```

---

### Task A2: Populate `CreatedTick` in `SpawnShadow`

**Files:**
- Modify: `pkg/universe/world_base.go:767-801` (`SpawnShadow`)

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/handoff_driver_test.go`:

```go
// TestSpawnShadow_RecordsCreatedTick verifies the destination-side
// watchdog groundwork: every Shadow spawned by SpawnShadow must carry
// the current game tick so the watchdog can age it out.
func TestSpawnShadow_RecordsCreatedTick(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})
	world := base.ECSWorld()

	// Force the engine's tick counter forward so the test proves the
	// value comes from the live tick, not a zero default.
	base.Engine().SetTick(12345)

	// Build a minimal valid transfer blob (reuse the existing helper).
	tempEntity := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(tempEntity, &component.Position{X: 0, Y: 0})
	ecs.NewMap1[component.Velocity](world).Add(tempEntity, &component.Velocity{})
	ecs.NewMap1[component.NetworkID](world).Add(tempEntity, &component.NetworkID{ID: 99})
	ecs.NewMap1[component.EntityKind](world).Add(tempEntity, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](world).Add(tempEntity, &component.Collider{Radius: 5})
	ecs.NewMap1[component.Rotation](world).Add(tempEntity, &component.Rotation{})
	ecs.NewMap1[component.CellCoord](world).Add(tempEntity, &component.CellCoord{CellX: 1, CellY: 0})
	blob, err := base.SerializeEntity(tempEntity)
	if err != nil {
		t.Fatalf("SerializeEntity: %v", err)
	}
	world.RemoveEntity(tempEntity)

	shadowEntity, err := base.SpawnShadow(&HandoffPreparePayload{
		NetID: 99, Epoch: 1, Kind: 1, TransferBlob: blob,
	})
	if err != nil {
		t.Fatalf("SpawnShadow: %v", err)
	}

	shadowMap := ecs.NewMap1[component.Shadow](world)
	sh := shadowMap.Get(shadowEntity)
	if sh.CreatedTick != 12345 {
		t.Fatalf("Shadow.CreatedTick = %d, want 12345", sh.CreatedTick)
	}
}
```

If `engine.Engine` lacks a `SetTick` helper exposed to tests, use the existing tick-advance mechanism (look at `pkg/engine/loop.go` for `Tick()` incrementation; if no direct setter exists, call `base.Engine().Tick` enough times via the loop or expose a test helper — inspect first; if a private field exists, add a `SetTickForTest` helper alongside.).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestSpawnShadow_RecordsCreatedTick -v`
Expected: FAIL — `CreatedTick = 0, want 12345`

- [ ] **Step 3: Wire the current tick into `SpawnShadow`**

In `pkg/universe/world_base.go` locate the `shadowMap.Add(entity, &component.Shadow{...})` call in `SpawnShadow`. Replace with:

```go
shadowMap.Add(entity, &component.Shadow{
	NetID:       payload.NetID,
	Epoch:       payload.Epoch,
	CreatedTick: b.eng.CurrentTick(),
})
```

If `b.eng.CurrentTick()` does not exist, use whichever accessor returns the engine tick (e.g. `b.eng.Tick`, `b.eng.GameLoop.Tick()` — inspect `pkg/engine/engine.go` first and use the existing one; do NOT add a second).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd . && go test ./pkg/universe/ -run TestSpawnShadow_RecordsCreatedTick -v`
Expected: PASS

- [ ] **Step 5: Run the full handoff test suite to verify no regression**

Run: `cd . && go test ./pkg/universe/ -run 'Handoff|Shadow' -count=1 -v`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/world_base.go pkg/universe/handoff_driver_test.go
git commit -m "feat(handoff): stamp CreatedTick on SpawnShadow

SpawnShadow now records the destination's current game-loop tick on the
Shadow component. The destination-side watchdog (landing in a later
step) reads this field to detect and clean up orphaned shadows."
```

---

## Phase B — `DemoteLiveToReplica`

With the groundwork in place, add the source-side mirror of `PromoteShadow`. This is the single most important change — it replaces `MarkForRemoval` on the source at Commit, keeping the same ECS entity alive as a Replica.

### Task B1: Add explicit `Demote` transition to `netIDIndex`

**Files:**
- Modify: `pkg/universe/netid_index.go`
- Modify: `pkg/universe/netid_index_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/netid_index_test.go`:

```go
func TestNetIDIndex_LiveToReplicaDemote(t *testing.T) {
	idx := newNetIDIndex()
	ent := ecs.Entity{} // zero is fine for the policy check
	// Install as Live.
	idx.Enter(1, ent, PresenceLive)

	// Unsolicited Enter(Replica) on a Live slot must still be rejected —
	// no silent downgrade. Only the explicit Demote path may transition.
	res := idx.Enter(1, ent, PresenceReplica)
	if res.Action != ActionRejected {
		t.Fatalf("Enter(Replica) on Live must return ActionRejected, got %d", res.Action)
	}

	// Explicit Demote flips the slot and returns ActionUpdated, keeping
	// the same entity.
	res = idx.Demote(1, ent)
	if res.Action != ActionUpdated {
		t.Fatalf("Demote on Live must return ActionUpdated, got %d", res.Action)
	}
	_, presence, ok := idx.Lookup(1)
	if !ok || presence != PresenceReplica {
		t.Fatalf("after Demote, slot presence = %v ok=%v, want PresenceReplica true", presence, ok)
	}
}

func TestNetIDIndex_DemoteNonLiveRejected(t *testing.T) {
	idx := newNetIDIndex()
	ent := ecs.Entity{}

	// Demote on an empty slot rejects.
	if idx.Demote(42, ent).Action != ActionRejected {
		t.Fatal("Demote on empty slot must reject")
	}

	// Demote on a Shadow slot rejects (Shadow is pre-authority; the
	// source cell shouldn't see a Shadow for its own netID).
	idx.Enter(42, ent, PresenceShadow)
	if idx.Demote(42, ent).Action != ActionRejected {
		t.Fatal("Demote on Shadow slot must reject")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestNetIDIndex_LiveToReplicaDemote -v`
Expected: FAIL — `Demote` method does not exist.

- [ ] **Step 3: Implement `Demote`**

Append to `pkg/universe/netid_index.go`:

```go
// Demote is the explicit Live → Replica transition used by
// DemoteLiveToReplica at handoff commit on the source cell. Unlike
// Enter(..., PresenceReplica) which rejects on a Live slot (so a stray
// border frame cannot silently downgrade a live entity), Demote is the
// sanctioned path: called by the handoff driver when the destination
// has committed and the source is converting its Live copy into a
// Replica that will be kept in sync by the destination's subsequent
// border frames.
//
// Returns ActionUpdated on success, ActionRejected if the slot is not
// currently Live for this netID.
func (idx *netIDIndex) Demote(netID uint32, entity ecs.Entity) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	cur, ok := idx.slots[netID]
	if !ok || cur.Presence != PresenceLive {
		return TransitionResult{Action: ActionRejected}
	}
	idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceReplica}
	return TransitionResult{Action: ActionUpdated, PrevEntity: cur.Entity}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd . && go test ./pkg/universe/ -run 'TestNetIDIndex_' -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
cd .
git add pkg/universe/netid_index.go pkg/universe/netid_index_test.go
git commit -m "feat(handoff): add explicit Demote transition to netIDIndex

Unsolicited Enter(Replica) on a Live slot continues to reject (so a
stray border frame cannot silently downgrade live authority). Demote
is the sanctioned caller for the new DemoteLiveToReplica handoff path."
```

---

### Task B2: Add `WorldBase.DemoteLiveToReplica`

**Files:**
- Modify: `pkg/universe/world_base.go` (append near `PromoteShadow`)

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/handoff_driver_test.go`:

```go
// TestDemoteLiveToReplica_PreservesEntityAndTransitionsSlot verifies
// the source-side mirror of PromoteShadow. The same ECS entity must
// survive (same handle, same Position/Velocity), a Replica component
// must be added, the netIDIdx slot must flip from Live to Replica, and
// replicaNetIDs must point at the entity so subsequent border frames
// from the new authoritative cell update in place.
func TestDemoteLiveToReplica_PreservesEntityAndTransitionsSlot(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	// Spawn a Live entity the normal way.
	ent, err := base.SpawnEntity(
		100, 200,
		WithVelocity(10, -5),
		WithEntityKind(1),
		WithCollider(8),
	)
	if err != nil {
		t.Fatalf("SpawnEntity: %v", err)
	}
	// Grab the allocated netID.
	netID := base.NetworkIDMap().Get(ent).ID

	// Confirm slot is Live before demote.
	_, pres, ok := base.LookupNetID(netID)
	if !ok || pres != PresenceLive {
		t.Fatalf("pre-demote presence = %v ok=%v, want PresenceLive true", pres, ok)
	}

	// Demote to replica of the destination cell.
	if err := base.DemoteLiveToReplica(netID, "cell_1_0"); err != nil {
		t.Fatalf("DemoteLiveToReplica: %v", err)
	}

	// Same entity still alive, same position, same velocity.
	if !world.Alive(ent) {
		t.Fatal("DemoteLiveToReplica must not remove the entity")
	}
	pos := base.PositionMap().Get(ent)
	if pos.X != 100 || pos.Y != 200 {
		t.Fatalf("position mutated: got (%.0f,%.0f), want (100,200)", pos.X, pos.Y)
	}
	vel := ecs.NewMap1[component.Velocity](world).Get(ent)
	if vel.X != 10 || vel.Y != -5 {
		t.Fatalf("velocity mutated: got (%.0f,%.0f), want (10,-5)", vel.X, vel.Y)
	}

	// Replica component added with correct SourceCellID.
	repMap := ecs.NewMap1[component.Replica](world)
	if !repMap.HasAll(ent) {
		t.Fatal("Replica component not added")
	}
	rep := repMap.Get(ent)
	if rep.SourceCellID != "cell_1_0" {
		t.Fatalf("Replica.SourceCellID = %q, want cell_1_0", rep.SourceCellID)
	}
	if !rep.UpdatedThisTick {
		t.Error("Replica.UpdatedThisTick must be true")
	}

	// Slot flipped to Replica.
	_, pres, ok = base.LookupNetID(netID)
	if !ok || pres != PresenceReplica {
		t.Fatalf("post-demote presence = %v ok=%v, want PresenceReplica true", pres, ok)
	}

	// replicaNetIDs now points at the entity.
	got, ok := base.ReplicaNetIDs()[netID]
	if !ok || got != ent {
		t.Fatalf("replicaNetIDs[%d] = (%v,%v), want (%v, true)", netID, got, ok, ent)
	}
}

// TestDemoteLiveToReplica_UnknownNetIDReturnsError ensures the method
// does not silently succeed for a netID that has no live entity.
func TestDemoteLiveToReplica_UnknownNetIDReturnsError(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	if err := base.DemoteLiveToReplica(9999, "cell_1_0"); err == nil {
		t.Fatal("DemoteLiveToReplica on unknown netID must return error")
	}
}
```

(If `PositionMap()` is not an exported accessor, use `ecs.NewMap1[component.Position](world).Get(ent)` directly.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestDemoteLiveToReplica -v`
Expected: FAIL — method not defined

- [ ] **Step 3: Implement `DemoteLiveToReplica`**

In `pkg/universe/world_base.go`, insert directly after `PromoteShadow` (around line 850-860):

```go
// DemoteLiveToReplica is the source-side mirror of PromoteShadow. At
// handoff commit, the source cell converts its Live entity for netID
// into a Replica of the destination cell — the SAME ECS entity, same
// Position/Velocity/Rotation/components — so downstream replication
// continues to scan the entity and emit SE_ENTITY_UPDATE frames to
// nearby clients. No SE_ENTITY_REMOVED is ever emitted, which is what
// makes the handoff client-invisible.
//
// After this call:
//   - The source's BorderDispatcher push walk skips the entity
//     (replicas aren't in the push set).
//   - The source's client-facing ReplicationSystem continues to scan
//     the entity for viewers in AoI.
//   - The destination's first post-Commit border frame flows into
//     upsertBorderReplica's existing replica-update branch and refreshes
//     Position/Velocity/component tail from the new authoritative sim.
//
// Returns an error only if no Live entity exists for netID; on a
// successful demote the error is nil.
func (b *WorldBase) DemoteLiveToReplica(netID uint32, newSourceCellID string) error {
	ent, presence, ok := b.netIDIdx.Lookup(netID)
	if !ok || presence != PresenceLive {
		return fmt.Errorf("DemoteLiveToReplica: netID=%d not live on cell %s", netID, b.cellID)
	}
	if !b.eng.ECS.Alive(ent) {
		return fmt.Errorf("DemoteLiveToReplica: entity for netID=%d not alive", netID)
	}

	// Add Replica component with a fresh TTL so the destination's
	// subsequent border frames refresh it naturally (TTL=30 = 1.5s at
	// 20Hz, enough for the first border frame post-commit to arrive).
	if !b.replicaMap.HasAll(ent) {
		b.replicaMap.Add(ent, &component.Replica{
			SourceCellID:    newSourceCellID,
			SourceNetID:     netID,
			TTL:             30,
			UpdatedThisTick: true,
		})
	} else {
		rep := b.replicaMap.Get(ent)
		rep.SourceCellID = newSourceCellID
		rep.SourceNetID = netID
		rep.TTL = 30
		rep.UpdatedThisTick = true
	}

	// Flip netIDIdx slot Live → Replica via the sanctioned Demote path.
	if res := b.netIDIdx.Demote(netID, ent); res.Action != ActionUpdated {
		return fmt.Errorf("DemoteLiveToReplica: netIDIdx.Demote returned action=%d for netID=%d",
			res.Action, netID)
	}

	// Register so subsequent border frames update this entity in place
	// instead of creating a second ECS replica.
	b.replicaNetIDs[netID] = ent

	b.eng.Log.Log(CatMeshTransfer,
		"[%s] demoted live→replica: netID=%d newSource=%s",
		b.cellID, netID, newSourceCellID)
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd . && go test ./pkg/universe/ -run TestDemoteLiveToReplica -v`
Expected: both PASS

- [ ] **Step 5: Run the full handoff + border-frame suite**

Run: `cd . && go test ./pkg/universe/ -run 'Handoff|Shadow|BorderFrame|NetIDIndex' -count=1 -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/world_base.go pkg/universe/handoff_driver_test.go
git commit -m "feat(handoff): DemoteLiveToReplica — source mirror of PromoteShadow

At handoff commit, the source cell now demotes its Live entity to a
Replica of the destination cell in place, preserving the ECS entity
handle and all component state. This is the foundation of the overlap
handoff — no SE_ENTITY_REMOVED is emitted from the source, so clients
see continuous presence through the authority flip."
```

---

### Task B3: Write failing multi-source dedup test (red bar for Phase C+D)

**Files:**
- Modify: `pkg/universe/border_replication_apply_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/border_replication_apply_test.go`:

```go
// TestApplyBorderFrame_MultiSource_SkipsEvictionWhenOtherSourcePushes
// is the core overlap invariant: when two source cells push the same
// netID (happens during the Prepare→Commit overlap window), a single
// source dropping the netID must NOT evict the replica. Only when
// BOTH sources stop pushing does the replica go away.
//
// Today the eviction fires unconditionally — this test drives the
// Phase D ApplyBorderFrame change.
func TestApplyBorderFrame_MultiSource_SkipsEvictionWhenOtherSourcePushes(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 2, Y: 2})

	// Source A pushes netID 77.
	fa1 := replication.Frame{
		Entries: []replication.FrameEntry{{
			NetID:    replication.NetID{ID: 77, Epoch: 1},
			Kind:     3,
			DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0),
		}},
	}
	base.ApplyBorderFrame(fa1, "source_A")

	// Source B ALSO pushes netID 77 (overlap window).
	fb1 := replication.Frame{
		Entries: []replication.FrameEntry{{
			NetID:    replication.NetID{ID: 77, Epoch: 1},
			Kind:     3,
			DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0),
		}},
	}
	base.ApplyBorderFrame(fb1, "source_B")

	// Tick 2: A drops 77 from its push set. Since B still pushes it,
	// the replica must survive.
	fa2 := replication.Frame{Entries: nil}
	fb2 := replication.Frame{
		Entries: []replication.FrameEntry{{
			NetID:    replication.NetID{ID: 77, Epoch: 1},
			Kind:     3,
			DeltaBuf: buildWireEntry(1105, 500, 10, 0, 0),
		}},
	}
	base.ApplyBorderFrame(fa2, "source_A")
	base.ApplyBorderFrame(fb2, "source_B")

	if _, ok := base.ReplicaNetIDs()[77]; !ok {
		t.Fatal("replica 77 evicted while source_B still pushing — overlap invariant broken")
	}

	// Tick 3: B also drops 77. Now both sources are silent → eviction.
	base.ApplyBorderFrame(replication.Frame{}, "source_A")
	base.ApplyBorderFrame(replication.Frame{}, "source_B")

	// Single tick drain: RemoveReplicaByNetID fires MarkForRemoval; the
	// replicaNetIDs map entry is deleted synchronously in
	// RemoveReplicaByNetID, so we can check it immediately.
	if _, ok := base.ReplicaNetIDs()[77]; ok {
		t.Fatal("replica 77 should have been evicted after both sources dropped it")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestApplyBorderFrame_MultiSource -v`
Expected: FAIL — replica 77 is evicted after source_A drops it, even while source_B still pushes.

**Leave the test red.** Phase D completes it. Do not commit a broken test — skip this step's commit and proceed; the fix in Task D1 turns it green and both land in the same commit.

---

## Phase C — Shadows participate in the outgoing border push

The destination's shadow must be broadcast outward so neighbors (and the source cell) learn the destination is also authoritative-in-waiting. This is Change 1 from the spec.

### Task C1: Shadow-update fast-path in `upsertBorderReplica`

**Files:**
- Modify: `pkg/universe/world_base.go:965-1046` (`upsertBorderReplica`)

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/border_replication_apply_test.go`:

```go
// TestUpsertBorderReplica_UpdatesShadowInPlace is Change 2 from the
// spec: when a border frame arrives for a netID that already has a
// Shadow on this cell, the update must be applied to the Shadow's ECS
// entity directly — no new replica entity, no netIDIdx transition.
func TestUpsertBorderReplica_UpdatesShadowInPlace(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})
	world := base.ECSWorld()

	// Plant a Shadow for netID 55 the way SpawnShadow would.
	tempEntity := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(tempEntity, &component.Position{X: 10, Y: 20})
	ecs.NewMap1[component.Velocity](world).Add(tempEntity, &component.Velocity{X: 1, Y: 2})
	ecs.NewMap1[component.NetworkID](world).Add(tempEntity, &component.NetworkID{ID: 55, Epoch: 1})
	ecs.NewMap1[component.EntityKind](world).Add(tempEntity, &component.EntityKind{Type: 3})
	ecs.NewMap1[component.Collider](world).Add(tempEntity, &component.Collider{Radius: 5})
	ecs.NewMap1[component.Rotation](world).Add(tempEntity, &component.Rotation{})
	ecs.NewMap1[component.CellCoord](world).Add(tempEntity, &component.CellCoord{CellX: 1, CellY: 0})
	blob, err := base.SerializeEntity(tempEntity)
	if err != nil {
		t.Fatalf("SerializeEntity: %v", err)
	}
	world.RemoveEntity(tempEntity)

	shadowEnt, err := base.SpawnShadow(&HandoffPreparePayload{
		NetID: 55, Epoch: 1, Kind: 3, TransferBlob: blob,
	})
	if err != nil {
		t.Fatalf("SpawnShadow: %v", err)
	}

	// Now feed a border frame for netID 55 from the source cell. This
	// simulates the source continuing to broadcast during overlap.
	f := replication.Frame{
		Entries: []replication.FrameEntry{{
			NetID:    replication.NetID{ID: 55, Epoch: 1},
			Kind:     3,
			DeltaBuf: buildWireEntry(1200, 600, 5, 15, -7),
		}},
	}
	base.ApplyBorderFrame(f, "source_A")

	// The Shadow entity must be the ONE and only entity carrying
	// netID 55 in the ECS. Scan all entities and count.
	netMap := ecs.NewMap1[component.NetworkID](world)
	filter := ecs.NewFilter1[component.NetworkID](world)
	q := filter.Query()
	count := 0
	var foundEnt ecs.Entity
	for q.Next() {
		e := q.Entity()
		if netMap.Get(e).ID == 55 {
			count++
			foundEnt = e
		}
	}
	if count != 1 {
		t.Fatalf("netID 55 has %d ECS entries after border frame, want 1", count)
	}
	if foundEnt != shadowEnt {
		t.Fatalf("netID 55 is on a different entity after border frame — shadow was replaced")
	}

	// Shadow component must still be present (not downgraded to Replica).
	shadowMap := ecs.NewMap1[component.Shadow](world)
	if !shadowMap.HasAll(foundEnt) {
		t.Fatal("Shadow component dropped from entity — overlap update must preserve shadow")
	}

	// Position must have been refreshed from the border frame.
	// cellSize=1024, receiver cell (1,0) → recvCellX=1024; localX = 1200-1024 = 176.
	pos := ecs.NewMap1[component.Position](world).Get(foundEnt)
	if pos.X != 176 || pos.Y != 600 {
		t.Errorf("shadow position not refreshed: got (%.0f,%.0f), want (176,600)", pos.X, pos.Y)
	}

	// netIDIdx must still see it as Shadow (not Replica).
	_, presence, ok := base.LookupNetID(55)
	if !ok || presence != PresenceShadow {
		t.Fatalf("netIDIdx presence = %v ok=%v, want PresenceShadow true", presence, ok)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestUpsertBorderReplica_UpdatesShadowInPlace -v`
Expected: FAIL — the shadow is replaced or a second ECS entity is created.

- [ ] **Step 3: Add Shadow fast-path at the top of `upsertBorderReplica`**

In `pkg/universe/world_base.go`, modify `upsertBorderReplica`. After the stale-epoch check and `b.highestSeenEpoch[netID] = epoch` line, add a Shadow lookup BEFORE the existing `replicaNetIDs` branch:

```go
// Shadow fast-path: if this cell already has a Shadow for netID (put
// there by SpawnShadow during a pending handoff), the border frame is
// an overlap update from the source cell. Refresh position/velocity/
// components on the Shadow's ECS entity directly — do NOT go through
// netIDIdx.Enter (which would reject Shadow→Replica) and do NOT create
// a second ECS entity (which would trip invNoDuplicatePresencePerCell).
// The Shadow stays the single representation of netID on this cell
// until PromoteShadow fires at Commit.
if ent, presence, ok := b.netIDIdx.Lookup(netID); ok && presence == PresenceShadow && b.eng.ECS.Alive(ent) {
	if b.posMap.HasAll(ent) {
		pos := b.posMap.Get(ent)
		pos.X = localX
		pos.Y = localY
	}
	if b.velMap.HasAll(ent) {
		vel := b.velMap.Get(ent)
		vel.X = vx
		vel.Y = vy
	}
	b.applyEntityComponents(ent, componentTail)
	return
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd . && go test ./pkg/universe/ -run TestUpsertBorderReplica_UpdatesShadowInPlace -v`
Expected: PASS

- [ ] **Step 5: Run the full border-frame + handoff suite**

Run: `cd . && go test ./pkg/universe/ -run 'BorderFrame|Handoff|Shadow|Replica' -count=1 -v`
Expected: all PASS (the pre-existing `TestApplyBorderFrame_MultiSource_*` test from Task B3 remains RED — Phase D fixes it).

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/world_base.go pkg/universe/border_replication_apply_test.go
git commit -m "feat(handoff): upsertBorderReplica shadow-update fast-path

During the Prepare→Commit overlap, the source cell continues broadcasting
the entity while the destination holds a Shadow for the same netID.
The fast-path refreshes the Shadow's ECS entity in place from the
border frame, avoiding a Shadow→Replica transition that would destroy
the shadow before PromoteShadow can land."
```

---

### Task C2: Include Shadow entities in the border dispatcher's push walk

**Files:**
- Modify: `pkg/universe/border_replication.go:115-117` (filter construction in `candidatesFor`)

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/border_replication_stub_test.go` (or create `pkg/universe/border_replication_shadow_test.go` if the stub file uses `package universe_test`). Using the internal-test package:

```go
// TestBorderDispatcher_IncludesShadow verifies the destination cell's
// outgoing border push includes Shadow entities. Rationale: once the
// destination holds a Shadow for an incoming handoff, downstream
// neighbors (third cells that will see this entity in AoI) need to
// learn about it from the destination as well as the source, so that
// when Commit fires and the source demotes, the neighbor already has
// another live source feeding replication frames.
func TestBorderDispatcher_IncludesShadow(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 1})
	world := base.ECSWorld()

	// Plant a Shadow at position (50, 50) in this cell — well inside
	// the AoI margin of the neighbor at (0,1) to the west.
	ent := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(ent, &component.Position{X: 50, Y: 50})
	ecs.NewMap1[component.Velocity](world).Add(ent, &component.Velocity{})
	ecs.NewMap1[component.NetworkID](world).Add(ent, &component.NetworkID{ID: 88, Epoch: 1})
	ecs.NewMap1[component.EntityKind](world).Add(ent, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](world).Add(ent, &component.Collider{Radius: 5})
	ecs.NewMap1[component.Shadow](world).Add(ent, &component.Shadow{NetID: 88, Epoch: 1})

	// Build a dispatcher with a test CellViewer pointed at neighbor (0,1).
	neighbor := newTestCellViewer(base, CellID{X: 0, Y: 1})
	bd := NewBorderDispatcher(base, map[string]*CellViewer{neighbor.ID(): neighbor})

	bd.Tick(1)

	if !neighbor.LastFrameContainsNetID(88) {
		t.Fatal("border dispatcher must include Shadow entity 88 in the outgoing frame")
	}
}
```

If `newTestCellViewer` / `LastFrameContainsNetID` don't exist yet, inspect `border_replication_stub_test.go` and `border_viewer_test.go` — these use existing stub viewers. Use whichever helper is already available; if none, add minimal helpers in a new `border_replication_shadow_test.go` that wrap `CellViewer` with an in-memory frame recorder.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestBorderDispatcher_IncludesShadow -v`
Expected: FAIL — Shadow entities are currently excluded via the default Without filter.

- [ ] **Step 3: Modify the border dispatcher's filter**

In `pkg/universe/border_replication.go`, locate the filter construction in `candidatesFor`:

```go
filter := ecs.NewFilter4[component.Position, component.NetworkID, component.EntityKind, component.Collider](world).
    Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Dormant]())
```

Keep the filter identical — the Shadow component is NOT currently in the Without clause (re-verify this), so Shadows already flow through. If and only if the RED-bar test from Step 2 shows Shadows ARE being filtered, remove the explicit Shadow exclusion. If the test still fails for a different reason, investigate — don't add Shadow to Without just to make the test green.

**Key distinction:** per the spec, the two filters intentionally diverge:
- **Commit-path serializers** (`serializeAllEntities`, `serializeQuadrantEntities` in `cell_transfer_executor.go`): exclude Shadow. Do not change.
- **Border dispatcher push walk:** includes Shadow. Ensure it does.

Grep both files, confirm, and document the invariant with a one-line comment above each filter:

```go
// Commit-path serializer: Shadow excluded (mid-handoff entities are
// handled by their own Prepare/Commit flow; shipping them in a bulk
// snapshot during split/merge double-materializes — see commit 9d664d7).
```

```go
// Border push walk: Shadow INCLUDED. During overlap the destination
// cell's Shadow must reach downstream neighbors so they see two
// simultaneous sources for the handoff netID (see overlap handoff spec).
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd . && go test ./pkg/universe/ -run TestBorderDispatcher_IncludesShadow -v`
Expected: PASS

- [ ] **Step 5: Verify commit-path serializer still excludes Shadow**

Run: `cd . && go test ./pkg/universe/ -run TestCellTransfer -count=1 -v`
Expected: all PASS. If any fail, the serializer filter was accidentally changed — revert it.

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/border_replication.go pkg/universe/cell_transfer_executor.go pkg/universe/border_replication_shadow_test.go
git commit -m "feat(handoff): shadows participate in outgoing border push

Border dispatcher's push walk includes Shadow entities so downstream
neighbors see the destination cell as a second authoritative source
during the handoff overlap window. Commit-path serializers still
exclude Shadow — split/merge snapshots must not double-materialize
handoff-in-flight entities."
```

---

## Phase D — Multi-source eviction + Prepare/Commit split

This is the phase that actually splits the handoff into two steps and changes eviction to respect multiple sources.

### Task D1: Multi-source dedup in `ApplyBorderFrame`

**Files:**
- Modify: `pkg/universe/world_base.go:940-957` (the diff-based eviction block in `ApplyBorderFrame`)

- [ ] **Step 1: Confirm the RED test from Task B3 still fails**

Run: `cd . && go test ./pkg/universe/ -run TestApplyBorderFrame_MultiSource -v`
Expected: FAIL

- [ ] **Step 2: Update the eviction loop to skip netIDs still seen by another source**

In `pkg/universe/world_base.go` locate the block:

```go
prev := b.borderLastSeen[sourceCellID]
var removed int
for netID := range prev {
    if _, stillThere := currentSet[netID]; stillThere {
        continue
    }
    b.RemoveReplicaByNetID(netID)
    removed++
}
b.borderLastSeen[sourceCellID] = currentSet
```

Replace with:

```go
prev := b.borderLastSeen[sourceCellID]
var removed int
for netID := range prev {
    if _, stillThere := currentSet[netID]; stillThere {
        continue
    }
    // Multi-source check: if any OTHER source cell is still pushing
    // this netID, skip eviction — we're in a handoff overlap window
    // (or a genuine multi-cell AoI overlap) and another source remains
    // authoritative-for-us about the entity. Only when every source
    // has dropped it does the replica go away.
    if b.netIDStillPushedByOtherSource(netID, sourceCellID) {
        continue
    }
    b.RemoveReplicaByNetID(netID)
    removed++
}
b.borderLastSeen[sourceCellID] = currentSet
```

Then, near the bottom of `world_base.go` (alongside other private helpers), add:

```go
// netIDStillPushedByOtherSource reports whether any source cell OTHER
// than excludeSource currently has netID in its borderLastSeen snapshot.
// O(neighbors) — trivial in practice (a cell has at most 8 neighbors).
func (b *WorldBase) netIDStillPushedByOtherSource(netID uint32, excludeSource string) bool {
	for src, seen := range b.borderLastSeen {
		if src == excludeSource {
			continue
		}
		if _, ok := seen[netID]; ok {
			return true
		}
	}
	return false
}
```

**Important ordering detail:** the `b.borderLastSeen[sourceCellID] = currentSet` assignment must happen AFTER the loop (as before), otherwise the excludeSource check misses netIDs that just dropped from the current frame.

- [ ] **Step 3: Run the multi-source test to verify it passes**

Run: `cd . && go test ./pkg/universe/ -run TestApplyBorderFrame_MultiSource -v`
Expected: PASS

- [ ] **Step 4: Run the full border-frame suite**

Run: `cd . && go test ./pkg/universe/ -run 'BorderFrame|ApplyBorderFrame' -count=1 -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
cd .
git add pkg/universe/world_base.go pkg/universe/border_replication_apply_test.go
git commit -m "feat(handoff): multi-source dedup in ApplyBorderFrame eviction

When a source drops a netID from its push set, skip eviction if any
OTHER source still pushes it. During a handoff overlap both source
and destination push the same netID; neighbors must keep the replica
until BOTH fall silent, otherwise the entity blinks for the third
cell's viewers at commit time."
```

---

### Task D2: Split `handleCrossing` into Prepare + Commit phases

**Files:**
- Modify: `pkg/universe/handoff_driver.go`

- [ ] **Step 1: Write the failing test for the split**

Append to `pkg/universe/handoff_driver_test.go`:

```go
// TestHandoffDriver_PrepareThenCommit verifies the two-phase handoff:
// - First tick: Prepare fires, source entity is NOT removed, state
//   machine is in Promoted.
// - After MinWarmupTicks ticks: Commit fires, source entity becomes a
//   Replica (DemoteLiveToReplica), state machine is in Handoff.
func TestHandoffDriver_PrepareThenCommit(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	// Recording bridge that captures Prepare/Commit calls.
	rec := &recordingBridge{}
	hd := NewHandoffDriver(base, rec)

	// Spawn an entity and enqueue a crossing to cell_1_0.
	ent, err := base.SpawnEntity(100, 100, WithEntityKind(1), WithCollider(5))
	if err != nil {
		t.Fatalf("SpawnEntity: %v", err)
	}
	netID := base.NetworkIDMap().Get(ent).ID
	base.EnqueueCrossingForTest(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})

	// Tick 1: Prepare fires, entity stays Live, no Commit yet.
	hd.Tick(1)
	if len(rec.prepares) != 1 {
		t.Fatalf("tick 1: prepare count = %d, want 1", len(rec.prepares))
	}
	if len(rec.commits) != 0 {
		t.Fatalf("tick 1: commit should NOT fire yet, got %d", len(rec.commits))
	}
	if !world.Alive(ent) {
		t.Fatal("tick 1: source entity must stay alive after Prepare")
	}
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceLive {
		t.Fatalf("tick 1: presence = %v, want PresenceLive", pres)
	}

	// Ticks 2..MinWarmupTicks: no crossing events, Promoted walk keeps
	// incrementing warmup but CanCommit returns false until the floor.
	for i := uint64(2); i <= MinWarmupTicks; i++ {
		hd.Tick(i)
	}
	if len(rec.commits) != 0 {
		t.Fatalf("warmup window: commit fired early (got %d)", len(rec.commits))
	}

	// Tick MinWarmupTicks+1: warmup satisfied, Commit fires, entity
	// becomes Replica (NOT removed).
	hd.Tick(MinWarmupTicks + 1)
	if len(rec.commits) != 1 {
		t.Fatalf("post-warmup: commit count = %d, want 1", len(rec.commits))
	}
	if !world.Alive(ent) {
		t.Fatal("post-commit: source entity must stay alive (demoted, not removed)")
	}
	_, pres, _ = base.LookupNetID(netID)
	if pres != PresenceReplica {
		t.Fatalf("post-commit: presence = %v, want PresenceReplica", pres)
	}
}

// recordingBridge captures SendHandoffPrepare/Commit/Cancel calls.
type recordingBridge struct {
	prepares []*HandoffPreparePayload
	commits  []*HandoffCommitPayload
	cancels  []*HandoffCancelPayload
}

func (r *recordingBridge) SendHandoffPrepare(destCellID string, p *HandoffPreparePayload) bool {
	r.prepares = append(r.prepares, p)
	return true
}
func (r *recordingBridge) SendHandoffCommit(destCellID string, p *HandoffCommitPayload) bool {
	r.commits = append(r.commits, p)
	return true
}
func (r *recordingBridge) SendHandoffCancel(destCellID string, p *HandoffCancelPayload) bool {
	r.cancels = append(r.cancels, p)
	return true
}

// Fill in the rest of the Bridge interface with no-ops. Look at the
// Bridge interface in pkg/universe/bridge.go and stub every method;
// only the three Handoff* methods need real behavior.
func (r *recordingBridge) OnPlayerTransfer(connID uint32, destCellID string)              {}
func (r *recordingBridge) SendBorderFrame(destCellID string, frameBytes []byte, from string) {}
// ... (inspect pkg/universe/bridge.go for the full list and stub each)
```

Also add a minimal test helper method on `WorldBase` if one doesn't exist yet:

```go
// EnqueueCrossingForTest is a test-only shim that adds a CrossingEvent
// to the queue the HandoffDriver drains. Tests use this to avoid
// driving a full BoundarySystem.
func (b *WorldBase) EnqueueCrossingForTest(evt CrossingEvent) {
	b.crossingQueue = append(b.crossingQueue, evt)
}
```

(Inspect `world_base.go` for `DrainCrossingQueue` / `crossingQueue` and mirror its locking convention. If the existing crossingQueue is already unlocked, the append is fine.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestHandoffDriver_PrepareThenCommit -v`
Expected: FAIL — current `handleCrossing` fires Prepare+Commit together and `MarkForRemoval`s the entity.

- [ ] **Step 3: Rewrite `handleCrossing` for the two-phase protocol**

In `pkg/universe/handoff_driver.go` modify `handleCrossing` so that on a fresh crossing it ONLY fires Prepare, transitions the state machine to Promoted, and leaves the source entity alive. Remove the Commit + `MarkForRemoval` lines from this function.

Then modify `Tick` to walk the state machine after draining crossings:

```go
func (hd *HandoffDriver) Tick(currentTick uint64) {
	if hd.base.IsDrainingForMerge() {
		hd.base.DrainCrossingQueue()
		return
	}
	events := hd.base.DrainCrossingQueue()
	for _, evt := range events {
		hd.handleCrossing(evt, currentTick)
	}
	hd.tickPromoted(currentTick)
}

// tickPromoted advances the warmup counter on every Promoted handoff
// and fires Commit + DemoteLiveToReplica on any pair whose warmup has
// satisfied MinWarmupTicks.
func (hd *HandoffDriver) tickPromoted(currentTick uint64) {
	// Snapshot keys — Commit mutates the state machine entries map.
	type promoted struct {
		key    HandoffKey
		entity ecs.Entity
	}
	var ready []promoted
	for k, e := range hd.sm.entries {
		if e.phase != HandoffPromoted {
			continue
		}
		e.warmupCount++
		if !hd.sm.CanCommit(k) {
			continue
		}
		// Find the source entity for this netID.
		ent, presence, ok := hd.base.LookupNetID(k.EntityNetID)
		if !ok || presence != PresenceLive || !hd.base.eng.ECS.Alive(ent) {
			// Entity died or already demoted — forget the pair.
			hd.sm.Forget(k)
			continue
		}
		ready = append(ready, promoted{key: k, entity: ent})
	}
	for _, r := range ready {
		hd.fireCommit(r.key, r.entity, currentTick)
	}
}

// fireCommit sends MsgHandoffCommit to the destination, demotes the
// local entity to Replica (preserving the ECS handle + all component
// state), and enters cooldown to prevent re-crossing thrash.
func (hd *HandoffDriver) fireCommit(k HandoffKey, entity ecs.Entity, currentTick uint64) {
	var newEpoch uint32
	if hd.netMap.HasAll(entity) {
		newEpoch = hd.netMap.Get(entity).Epoch
	}
	committed := hd.bridge.SendHandoffCommit(k.NeighborID, &HandoffCommitPayload{
		NetID:      k.EntityNetID,
		Epoch:      newEpoch,
		CommitTick: currentTick,
	})
	if !committed {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] commit aborted (dest %s gone): netID=%d",
			hd.base.cellID, k.NeighborID, k.EntityNetID)
		return
	}
	if err := hd.base.DemoteLiveToReplica(k.EntityNetID, k.NeighborID); err != nil {
		hd.base.eng.Log.Log(CatMeshTransfer,
			"[%s] demote failed post-commit: netID=%d err=%v",
			hd.base.cellID, k.EntityNetID, err)
		return
	}
	hd.sm.SetState(k, HandoffHandoff)
	hd.sm.EnterCooldown(k, currentTick)

	// Cancel any other Promoted destinations for this entity — it
	// committed to k.NeighborID, so stale shadows on losing neighbors
	// must be cleaned up.
	for _, other := range hd.sm.PromotedNeighborsFor(k.EntityNetID) {
		if other == k.NeighborID {
			continue
		}
		hd.bridge.SendHandoffCancel(other, &HandoffCancelPayload{
			NetID: k.EntityNetID,
			Epoch: newEpoch,
		})
		hd.sm.Forget(HandoffKey{EntityNetID: k.EntityNetID, NeighborID: other})
	}

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff committed: netID=%d -> %s tick=%d epoch=%d",
		hd.base.cellID, k.EntityNetID, k.NeighborID, currentTick, newEpoch)
}
```

And modify `handleCrossing` to stop at Prepare:

```go
func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentTick uint64) {
	k := HandoffKey{EntityNetID: evt.NetID, NeighborID: evt.DestCellID}

	// Skip if already in cooldown (anti-thrash).
	if hd.sm.InCooldown(k, currentTick) {
		return
	}
	// Skip if already Promoted on this pair — we already sent Prepare,
	// warmup is advancing.
	if hd.sm.State(k) == HandoffPromoted {
		return
	}
	if !hd.base.eng.ECS.Alive(evt.Entity) {
		return
	}

	// Bump epoch + serialize + normalize position (UNCHANGED from v1).
	// ... (keep the existing epoch bump, position normalization, and
	// SerializeEntity calls verbatim) ...

	prepared := hd.bridge.SendHandoffPrepare(evt.DestCellID, &HandoffPreparePayload{...})
	if !prepared {
		// Roll back epoch bump, stay in Unseen.
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		return
	}

	// Transition to Promoted. DO NOT fire Commit here — that happens
	// in tickPromoted once MinWarmupTicks have elapsed.
	hd.sm.SetState(k, HandoffPromoted)

	// Handle player session transfer at Prepare time so the destination
	// can wire up the session as its Shadow arrives. (UNCHANGED.)
	if evt.ConnID != 0 {
		hd.bridge.OnPlayerTransfer(evt.ConnID, evt.DestCellID)
		if sess := hd.base.eng.Players.ByConnID(evt.ConnID); sess != nil {
			_ = hd.base.eng.Players.Transition(sess, engine.StateTransferring)
			hd.base.eng.Players.Remove(sess)
		}
	}

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] prepared handoff: netID=%d -> %s tick=%d epoch=%d",
		hd.base.cellID, evt.NetID, evt.DestCellID, currentTick, newEpoch)
}
```

**Critical:** remove the `hd.base.eng.MarkForRemoval(evt.Entity)` line from `handleCrossing` — that's the bug the overlap fixes. The source entity lives on until `DemoteLiveToReplica` in `fireCommit`, and even then it stays in the ECS as a Replica.

- [ ] **Step 4: Run the two-phase test**

Run: `cd . && go test ./pkg/universe/ -run TestHandoffDriver_PrepareThenCommit -v`
Expected: PASS

- [ ] **Step 5: Run the full S7 test family**

Run: `cd . && go test ./pkg/universe/ -run 'S7|Handoff|Shadow' -count=1 -timeout 120s -v`
Expected: all PASS. If any S7 test fails due to the timing change, update the test's tick count to account for MinWarmupTicks=5 — do NOT lower MinWarmupTicks.

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/handoff_driver.go pkg/universe/handoff_driver_test.go pkg/universe/world_base.go
git commit -m "feat(handoff): split Prepare and Commit into separate phases

handleCrossing now fires only Prepare and leaves the source entity
Live. tickPromoted walks the state machine each game tick, increments
warmup, and fires Commit+DemoteLiveToReplica once MinWarmupTicks have
elapsed. This is the core of the overlap protocol — the source and
destination are both authoritative during warmup, shared neighbors
get border frames from both, and the commit flip is client-invisible."
```

---

### Task D3: Handle HandoffCommit failure and missing source entity robustly

**Files:**
- Modify: `pkg/universe/handoff_driver.go` (already modified in D2)

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/handoff_driver_test.go`:

```go
// TestHandoffDriver_CommitFailsWhenDestGone verifies that if
// SendHandoffCommit returns false (dest cell torn down mid-warmup),
// the source does NOT demote — the entity stays Live so a future
// crossing or merge can handle it, and the state machine does NOT
// enter cooldown (which would suppress the next legitimate retry).
func TestHandoffDriver_CommitFailsWhenDestGone(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	rec := &recordingBridge{}
	rec.commitFailsForDest = "cell_1_0"
	hd := NewHandoffDriver(base, rec)

	ent, _ := base.SpawnEntity(100, 100, WithEntityKind(1), WithCollider(5))
	netID := base.NetworkIDMap().Get(ent).ID
	base.EnqueueCrossingForTest(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})

	// Drive ticks past the warmup window.
	for i := uint64(1); i <= MinWarmupTicks+1; i++ {
		hd.Tick(i)
	}

	// Source entity must stay Live — commit failed, no demote.
	if !world.Alive(ent) {
		t.Fatal("source entity must stay alive on commit failure")
	}
	_, pres, _ := base.LookupNetID(netID)
	if pres != PresenceLive {
		t.Fatalf("presence after failed commit = %v, want PresenceLive", pres)
	}
}
```

Extend `recordingBridge` with the `commitFailsForDest string` field and update `SendHandoffCommit` to return false when `destCellID == commitFailsForDest`.

- [ ] **Step 2: Run the test to verify it fails or passes**

Run: `cd . && go test ./pkg/universe/ -run TestHandoffDriver_CommitFailsWhenDestGone -v`
Expected: PASS if the Task D2 code already returns early on `!committed`. If it fails because the code entered cooldown anyway, back up and fix the Task D2 implementation to never enter cooldown on failure.

- [ ] **Step 3: Commit any fix**

If the test passed directly, skip the commit step. If you fixed Task D2's code path, amend or create a new commit:

```bash
cd .
git add pkg/universe/handoff_driver.go pkg/universe/handoff_driver_test.go
git commit -m "test(handoff): commit failure keeps entity live, no cooldown"
```

---

### Task D4: Destination-side Shadow watchdog

**Files:**
- Modify: `pkg/universe/cell.go` or `pkg/universe/world_base.go`

The spec places the watchdog on the destination cell's per-tick loop. Looking at `cell.go:95-106` (`DrainInbox` and `TickGhosts`/`TickTransferCooldowns` pattern), the cleanest placement is a new `TickShadowWatchdog` on `WorldBase` called alongside `TickGhosts`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/handoff_driver_test.go`:

```go
// TestShadowWatchdog_CleansOrphans verifies that a Shadow with no
// matching Commit after MaxWarmupTicks is cleaned up and a Cancel is
// sent back to the source.
func TestShadowWatchdog_CleansOrphans(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})
	world := base.ECSWorld()
	rec := &recordingBridge{}
	base.SetBridge(rec)

	// Plant a Shadow with CreatedTick=10 via SpawnShadow.
	tempEntity := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(tempEntity, &component.Position{})
	ecs.NewMap1[component.Velocity](world).Add(tempEntity, &component.Velocity{})
	ecs.NewMap1[component.NetworkID](world).Add(tempEntity, &component.NetworkID{ID: 321})
	ecs.NewMap1[component.EntityKind](world).Add(tempEntity, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](world).Add(tempEntity, &component.Collider{Radius: 5})
	ecs.NewMap1[component.Rotation](world).Add(tempEntity, &component.Rotation{})
	ecs.NewMap1[component.CellCoord](world).Add(tempEntity, &component.CellCoord{CellX: 1, CellY: 0})
	blob, _ := base.SerializeEntity(tempEntity)
	world.RemoveEntity(tempEntity)

	base.Engine().SetTick(10)
	shadowEnt, err := base.SpawnShadow(&HandoffPreparePayload{
		NetID: 321, Epoch: 1, Kind: 1, TransferBlob: blob,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Tick just past MaxWarmupTicks.
	base.TickShadowWatchdog(10 + MaxWarmupTicks + 1)

	// Shadow must be removed.
	if world.Alive(shadowEnt) && ecs.NewMap1[component.Shadow](world).HasAll(shadowEnt) {
		// MarkForRemoval may keep alive for the current tick; verify the
		// slot is gone from netIDIdx.
	}
	_, _, ok := base.LookupNetID(321)
	if ok {
		t.Fatal("orphaned shadow netID must be removed from netIDIdx")
	}
	// Cancel message sent to the source.
	if len(rec.cancels) != 1 {
		t.Fatalf("cancel count = %d, want 1", len(rec.cancels))
	}
	if rec.cancels[0].NetID != 321 {
		t.Fatalf("cancel netID = %d, want 321", rec.cancels[0].NetID)
	}
}
```

If `SetBridge` isn't a real method on `WorldBase`, inspect `NewWorldBase` — the bridge is normally wired at cell-construction time. Either add a test-only setter or pass the bridge into a new `TickShadowWatchdog(sourceHint string, currentTick uint64)` signature. Adjust the test to match whichever interface you choose.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd . && go test ./pkg/universe/ -run TestShadowWatchdog -v`
Expected: FAIL — `TickShadowWatchdog` does not exist.

- [ ] **Step 3: Implement `TickShadowWatchdog`**

Append to `pkg/universe/world_base.go`:

```go
// TickShadowWatchdog walks every Shadow entity on this cell and, for
// any whose CreatedTick is older than MaxWarmupTicks, removes the
// Shadow and emits MsgHandoffCancel back to the source. Runs once per
// game tick alongside TickGhosts.
//
// The bridge is consulted to route the Cancel back to the source. For
// single-process / test setups the bridge may be NoopBridge, in which
// case cancels are silently dropped — the destination still cleans up.
func (b *WorldBase) TickShadowWatchdog(currentTick uint64) {
	shadowMap := ecs.NewMap1[component.Shadow](b.eng.ECS)
	netMap := ecs.NewMap1[component.NetworkID](b.eng.ECS)
	filter := ecs.NewFilter2[component.Shadow, component.NetworkID](b.eng.ECS)
	q := filter.Query()

	type stale struct {
		netID     uint32
		source    string
		epoch     uint32
	}
	var staleShadows []stale
	for q.Next() {
		sh, nid := q.Get()
		if currentTick-sh.CreatedTick <= MaxWarmupTicks {
			continue
		}
		staleShadows = append(staleShadows, stale{
			netID:  nid.ID,
			source: sh.SourceCellID,
			epoch:  sh.Epoch,
		})
	}
	_ = shadowMap
	_ = netMap

	for _, s := range staleShadows {
		b.RemoveShadowByNetID(s.netID)
		if b.bridge != nil {
			b.bridge.SendHandoffCancel(s.source, &HandoffCancelPayload{
				NetID: s.netID,
				Epoch: s.epoch,
			})
		}
		b.eng.Log.Log(CatMeshTransfer,
			"[%s] shadow watchdog: orphan netID=%d from=%s age=%d",
			b.cellID, s.netID, s.source, currentTick-s.epoch)
	}
}
```

Then wire it into the per-tick path. In `pkg/universe/cell.go` `DrainInbox` (line 95-106), alongside `TickGhosts`:

```go
func (c *Cell) DrainInbox() {
	for {
		select {
		case msg := <-c.Inbox:
			c.processMessage(msg)
		default:
			c.Base.TickGhosts()
			c.Base.TickTransferCooldowns()
			c.Base.TickShadowWatchdog(c.Engine.CurrentTick())
			return
		}
	}
}
```

(Use whichever tick accessor is already used in the file; match existing style.)

- [ ] **Step 4: Run the watchdog test**

Run: `cd . && go test ./pkg/universe/ -run TestShadowWatchdog -v`
Expected: PASS

- [ ] **Step 5: Run full handoff + cell tests**

Run: `cd . && go test ./pkg/universe/ -run 'Handoff|Shadow|Cell' -count=1 -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/world_base.go pkg/universe/cell.go pkg/universe/handoff_driver_test.go
git commit -m "feat(handoff): destination-side watchdog for orphaned shadows

TickShadowWatchdog runs every tick, ages shadows past MaxWarmupTicks,
and sends HandoffCancel back to the source. Covers the source-died-
mid-handoff case even on reliable MeshData streams."
```

---

### Task D5: MERGE drain interaction regression test

**Files:**
- Modify: `pkg/universe/handoff_driver_test.go`

- [ ] **Step 1: Write the test**

The existing `drainingForMerge` early-return in `handoff_driver.Tick` must still block both Prepare and Commit. Append:

```go
// TestHandoffDriver_DrainingForMerge_SkipsBothPhases verifies that
// when a cell is draining for a merge, neither new Prepares nor
// pending Commits fire — the state machine stays frozen so the merge
// executor can drain the cell cleanly (prior cause: commit e4ede97).
func TestHandoffDriver_DrainingForMerge_SkipsBothPhases(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &recordingBridge{}
	hd := NewHandoffDriver(base, rec)

	ent, _ := base.SpawnEntity(100, 100, WithEntityKind(1), WithCollider(5))
	netID := base.NetworkIDMap().Get(ent).ID

	// Prepare first.
	base.EnqueueCrossingForTest(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})
	hd.Tick(1) // Prepare fires.
	if len(rec.prepares) != 1 {
		t.Fatal("tick 1 should fire Prepare")
	}

	// Now drain starts.
	base.SetDrainingForMerge(true)

	// Advance past the warmup — Commit must NOT fire.
	for i := uint64(2); i <= MinWarmupTicks+5; i++ {
		hd.Tick(i)
	}
	if len(rec.commits) != 0 {
		t.Fatalf("during drain: commit fired (got %d)", len(rec.commits))
	}
}
```

- [ ] **Step 2: Run it**

Run: `cd . && go test ./pkg/universe/ -run TestHandoffDriver_DrainingForMerge_SkipsBothPhases -v`
Expected: depends on the Task D2 `Tick` implementation. If `tickPromoted` fires even when `IsDrainingForMerge` is true, the test fails. Fix: the `Tick` method already returns early when `IsDrainingForMerge`; verify `tickPromoted` is INSIDE that guard.

- [ ] **Step 3: If the test fails, update `Tick`**

```go
func (hd *HandoffDriver) Tick(currentTick uint64) {
	if hd.base.IsDrainingForMerge() {
		hd.base.DrainCrossingQueue() // drop pending events
		return                       // also skip tickPromoted
	}
	events := hd.base.DrainCrossingQueue()
	for _, evt := range events {
		hd.handleCrossing(evt, currentTick)
	}
	hd.tickPromoted(currentTick)
}
```

- [ ] **Step 4: Run it again to verify PASS**

Run: `cd . && go test ./pkg/universe/ -run TestHandoffDriver_DrainingForMerge -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd .
git add pkg/universe/handoff_driver.go pkg/universe/handoff_driver_test.go
git commit -m "test(handoff): drain-for-merge freezes both Prepare and Commit"
```

---

### Task D6: Parallel-path audit

**Files:** (read only)
- `pkg/universe/world_base.go`
- `pkg/universe/cell.go`
- `pkg/universe/cell_transfer_executor.go`
- `pkg/universe/handoff_driver.go`
- `pkg/component/shadow.go`

- [ ] **Step 1: Grep every site that touches `replicaNetIDs`, `netIDIdx`, `Shadow` component, or `MarkForRemoval` of a netID-carrying entity**

Run these and record what each call site does:

```bash
cd .
rg -n 'replicaNetIDs\[' pkg/universe/
rg -n 'netIDIdx\.(Enter|Exit|Lookup|Demote)' pkg/universe/
rg -n 'component\.Shadow' pkg/ internal/
rg -n 'MarkForRemoval' pkg/universe/
```

- [ ] **Step 2: For each site, confirm the new protocol is respected**

Expected call-site rules post-Phase-D:
- `MarkForRemoval` for a Live netID — allowed only for genuine despawn (death, TTL expiry on replicas). Not for handoff.
- `netIDIdx.Enter(netID, ent, PresenceReplica)` on a Live slot — must return `ActionRejected` (unchanged).
- `netIDIdx.Demote` — called only from `DemoteLiveToReplica`.
- `component.Shadow` — added only in `SpawnShadow`, removed only in `PromoteShadow` or `RemoveShadowByNetID`.

If any site violates these rules, write it down and fix it in this task.

- [ ] **Step 3: Commit any cleanup**

If the audit surfaced fixes, commit them:

```bash
cd .
git add <files>
git commit -m "refactor(handoff): align parallel spawn/despawn paths with overlap protocol"
```

If no cleanup needed, skip the commit.

---

## Phase E — Blink detector

Runtime check that fires if `SE_ENTITY_SPAWN(netID=X)` is emitted for a conn that saw `SE_ENTITY_REMOVED(netID=X)` within the last K ticks.

### Task E1: Config knob `BlinkDetectorTicks`

**Files:**
- Modify: `pkg/universe/coordinator.go` (add to `Config`)

- [ ] **Step 1: Add field**

In `pkg/universe/coordinator.go` `Config` struct (around line 174, next to `StrictNetIDIndex`):

```go
// BlinkDetectorTicks controls the per-connection recent-removals
// window used by invNoBlinkForConn. When the ReplicationSystem is
// about to emit SE_ENTITY_SPAWN for a netID that was removed less
// than BlinkDetectorTicks ago for the same conn, the invariant
// framework records a violation and (in InvariantPanic mode) panics.
//
// 0 = use default (30 ticks = 1.5s at 20Hz). Set higher in
// high-latency deployments where handoff overlap can legitimately
// span more ticks; set to 1 to effectively disable.
BlinkDetectorTicks uint64
```

- [ ] **Step 2: Wire defaulting in `New()`**

In `Process.New()`, around where `StrictNetIDIndex` is copied:

```go
c.strictNetIDIndex = cfg.StrictNetIDIndex
c.blinkDetectorTicks = cfg.BlinkDetectorTicks
if c.blinkDetectorTicks == 0 {
	c.blinkDetectorTicks = 30
}
```

And add `blinkDetectorTicks uint64` to the `Process` struct alongside `invariantMode`.

- [ ] **Step 3: Build**

Run: `cd . && go vet ./...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd .
git add pkg/universe/coordinator.go
git commit -m "feat(integrity): add Config.BlinkDetectorTicks knob"
```

---

### Task E2: Add `events:replication` log category

**Files:**
- Modify: `pkg/universe/commit_log_categories.go`

- [ ] **Step 1: Add category constant**

```go
const (
	CatEventsSplit       = "events:split"
	CatEventsMerge       = "events:merge"
	CatEventsMigrate     = "events:migrate"
	CatEventsInvariant   = "events:invariant"
	CatEventsHost        = "events:host"
	CatEventsSession     = "events:session"
	CatEventsReplication = "events:replication"
)

var EventCategories = []string{
	CatEventsSplit, CatEventsMerge, CatEventsMigrate,
	CatEventsInvariant, CatEventsHost, CatEventsSession,
	CatEventsReplication,
}
```

- [ ] **Step 2: Commit**

```bash
cd .
git add pkg/universe/commit_log_categories.go
git commit -m "feat(integrity): register events:replication log category"
```

---

### Task E3: Per-connection recent-removals ring in `connState`

**Files:**
- Modify: `pkg/system/replication.go`

- [ ] **Step 1: Add field to `connState`**

In `pkg/system/replication.go:214` (`connState` struct):

```go
type connState struct {
	store     *replication.BaselineStore
	ackedSeq  uint32
	nextSeq   uint32
	selfNetID uint32
	// recentRemovals maps netID to the tick at which SE_ENTITY_REMOVED
	// was most recently emitted for this connection. Consulted on every
	// subsequent SE_ENTITY_SPAWN emission by the blink-detector invariant:
	// if the SPAWN arrives within BlinkDetectorTicks ticks of the
	// removal, it is a client-visible blink. GC'd in-band by the same
	// emission pass (entries older than the window are dropped).
	recentRemovals map[uint32]uint64
}
```

Update `newConnState` to initialize the map:

```go
func newConnState(mode replication.AckMode) *connState {
	return &connState{
		store:          replication.NewBaselineStore(mode),
		recentRemovals: make(map[uint32]uint64),
	}
}
```

- [ ] **Step 2: Commit**

No test yet — field is unread. Build-only commit:

```bash
cd .
go vet ./...
git add pkg/system/replication.go
git commit -m "feat(integrity): add recentRemovals field to connState"
```

---

### Task E4: Blink detection on emission + hook into Process

**Files:**
- Modify: `pkg/system/replication.go` (add hook into ReplicationConfig + Update)
- Modify: `pkg/universe/world.go` or wherever the ReplicationSystem config is assembled

- [ ] **Step 1: Write the failing test**

Create `pkg/system/replication_test.go` (or add to existing):

```go
// TestReplicationSystem_BlinkDetectorFires verifies that when a netID
// is removed and then re-spawned within BlinkDetectorTicks, the
// system calls the configured OnBlinkDetected hook with the conn+netID.
func TestReplicationSystem_BlinkDetectorFires(t *testing.T) {
	// Minimal harness: SpatialGrid + viewer that stays put, AoI radius
	// 200. Entity at (100,100) comes and goes twice within 5 ticks.
	//
	// Assert: on the second SPAWN emission, blinkCalls is incremented.
	//
	// Implementation: set up a ReplicationConfig with
	//   - BlinkDetectorTicks: 10
	//   - OnBlinkDetected: func(connID, netID uint32) { blinkCalls++ }
	// Drive Update; verify exactly one call with connID=1, netID=42.
	t.Skip("flesh out with actual replication-system test harness")
}
```

Replace the `t.Skip` with a concrete setup by reading existing replication tests in `pkg/system/replication_test.go` (if present) or `pkg/universe/replication_test.go`. Use the same harness conventions.

- [ ] **Step 2: Add `OnBlinkDetected` to `ReplicationConfig`**

In `pkg/system/replication.go:172` (`ReplicationConfig` struct) add:

```go
// BlinkDetectorTicks controls the recent-removals window. 0 disables
// the detector entirely. Typically 30 (1.5s at 20Hz).
BlinkDetectorTicks uint64

// OnBlinkDetected is called when a SPAWN is about to be emitted for
// a (connID, netID) that was the subject of a SE_ENTITY_REMOVED
// within BlinkDetectorTicks ticks. Implementations record to the
// commit log and (in InvariantPanic mode) panic. nil disables.
OnBlinkDetected func(connID, netID uint32, ticksSinceRemove uint64)
```

- [ ] **Step 3: Record removals and check spawns in `Update`**

In `pkg/system/replication.go` `Update`, at the point where `removed` is computed per-viewer (around line 703-715), add a loop to record removals:

```go
if s.cfg.BlinkDetectorTicks > 0 {
	for _, netID := range removed {
		conn.recentRemovals[netID] = uint64(tick)
	}
}
```

And at the point where `entered` (new SPAWN) is populated (around line 566-567, after `entered = append(entered, netID)`), add the blink check:

```go
if s.cfg.BlinkDetectorTicks > 0 && s.cfg.OnBlinkDetected != nil {
	if removedTick, ok := conn.recentRemovals[netID]; ok {
		delta := uint64(tick) - removedTick
		if delta <= s.cfg.BlinkDetectorTicks {
			s.cfg.OnBlinkDetected(viewer.ConnID, netID, delta)
		}
		// Drop the entry — spawn consumed it, stale entries GC below.
		delete(conn.recentRemovals, netID)
	}
}
```

And a GC pass at the end of the per-viewer loop to drop stale entries:

```go
if s.cfg.BlinkDetectorTicks > 0 && len(conn.recentRemovals) > 0 {
	cutoff := uint64(tick) - s.cfg.BlinkDetectorTicks
	for id, t := range conn.recentRemovals {
		if t < cutoff {
			delete(conn.recentRemovals, id)
		}
	}
}
```

- [ ] **Step 4: Wire `OnBlinkDetected` from universe setup**

Find where `ReplicationConfig` is constructed for games (search `NewReplicationSystem(` + `ReplicationConfig{`). Typically this is in `pkg/mmokit` or a game's network system factory. Add:

```go
cfg.BlinkDetectorTicks = coord.BlinkDetectorTicks()
cfg.OnBlinkDetected = func(connID, netID uint32, ticksSinceRemove uint64) {
	if coord.InvariantMode() == InvariantOff {
		return
	}
	coord.Log.Log(CatEventsReplication,
		"[BLINK] conn=%d netID=%d ticksSinceRemove=%d",
		connID, netID, ticksSinceRemove)
	if coord.CommitLog() != nil {
		coord.CommitLog().Append(CommitEvent{
			Kind:      EventInvariantViolation,
			StepIndex: -1,
			Step:      "no-blink-for-conn",
			Success:   false,
			Error:     fmt.Sprintf("blink: conn=%d netID=%d ticksSinceRemove=%d",
				connID, netID, ticksSinceRemove),
			Context: map[string]string{
				"connID":           fmt.Sprintf("%d", connID),
				"netID":            fmt.Sprintf("%d", netID),
				"ticksSinceRemove": fmt.Sprintf("%d", ticksSinceRemove),
			},
		})
	}
	if coord.InvariantMode() == InvariantPanic {
		panic(fmt.Sprintf("invariant no-blink-for-conn violated: conn=%d netID=%d delta=%d",
			connID, netID, ticksSinceRemove))
	}
}
```

Add the accessor methods `BlinkDetectorTicks`, `InvariantMode`, `CommitLog` on `Process` if they don't exist:

```go
func (c *Process) BlinkDetectorTicks() uint64 { return c.blinkDetectorTicks }
func (c *Process) InvariantMode() InvariantMode { return c.invariantMode }
func (c *Process) CommitLog() *CommitLog { return c.commitLog }
```

- [ ] **Step 5: Run the test**

Run: `cd . && go test ./pkg/system/ -run TestReplicationSystem_BlinkDetectorFires -v`
Expected: PASS

- [ ] **Step 6: Full vet + test**

Run: `cd . && go vet ./... && go test ./pkg/... -count=1 -timeout 180s`
Expected: clean + all PASS

- [ ] **Step 7: Commit**

```bash
cd .
git add pkg/system/replication.go pkg/system/replication_test.go pkg/universe/coordinator.go pkg/universe/world.go
git commit -m "feat(integrity): blink detector fires on remove→spawn within window

ReplicationSystem tracks recent SE_ENTITY_REMOVED emissions per conn
and fires OnBlinkDetected when a SPAWN lands for the same netID
within BlinkDetectorTicks. The coordinator-level hook records to the
commit log and panics under InvariantPanic. Overlap handoff plus
DemoteLiveToReplica should mean the detector NEVER fires during a
normal crossing — any firing is a genuine regression signal."
```

---

## Phase F — Client-side defense + integration test

### Task F1: Two-cell overlap integration test

**Files:**
- Create: `pkg/universe/overlap_test.go`

- [ ] **Step 1: Write the test**

```go
package universe

import (
	"testing"
	// imports...
)

// TestOverlap_NeighborReplicaStableThroughHandoff drives a scripted
// handoff across a 2-host loopback harness and asserts:
//  1. Both source and destination broadcast the netID during the
//     overlap window (both wire frames contain the netID).
//  2. A shared neighbor's replica entity handle is stable through
//     the overlap — no recreation.
//  3. No (SE_ENTITY_REMOVED, SE_ENTITY_SPAWN) pair appears on the
//     neighbor's client-facing wire for the handoff netID.
//  4. With InvariantPanic mode, no panic occurs.
func TestOverlap_NeighborReplicaStableThroughHandoff(t *testing.T) {
	t.Skip("implement against LoopbackBridge 3-cell harness — scripted ticks driven through cells A, B, P")
}
```

This test needs a 3-cell harness (A=source, B=dest, P=shared neighbor). Inspect `pkg/universe/loopback_bridge_test.go` + `cluster_fixture_test.go` for existing harness conventions. Replace the `t.Skip` with actual fixture setup.

Minimum setup:
- 3 WorldBase instances, each attached to a CellID (A:{0,0}, B:{1,0}, P:{1,1}).
- LoopbackBridge wiring all three.
- A spawns an entity at (1000, 100) — close to the A/B boundary.
- Tick A and P; confirm P's replica for the netID exists with a stable ECS entity handle.
- Enqueue a crossing A → B; tick A for `MinWarmupTicks+2` ticks; tick B drains Prepare + holds Shadow; tick P throughout.
- Assert the neighbor replica's ECS entity handle is the same pre- and post-handoff.
- Assert neither A's nor P's outbound-to-client wire (instrumented FrameWriter capturing `Removed` and `Entered` netIDs per conn) contains `(Removed=[X], ..., Entered=[X])` pairs.

- [ ] **Step 2: Run the test**

Run: `cd . && go test ./pkg/universe/ -run TestOverlap_NeighborReplicaStableThroughHandoff -v`
Expected: PASS (or SKIP while you flesh out the harness — do NOT commit as skipped; Phase G's smoke test depends on this being real).

- [ ] **Step 3: Commit**

```bash
cd .
git add pkg/universe/overlap_test.go
git commit -m "test(handoff): neighbor replica stable through overlap handoff"
```

---

### Task F2: Web-pixi defensive decode

**Files:**
- Modify: `web-pixi/src/network.ts`

- [ ] **Step 1: Write the failing test**

Append to `web-pixi/src/__tests__/network.test.ts` (create if it doesn't exist — mirror `interpolation.test.ts` conventions):

```ts
import { describe, test, expect } from "bun:test";
import { applyDeltaUpdateForTest } from "../network";

describe("decoder robustness", () => {
  test("UPDATE for unknown netID synthesizes a SPAWN", () => {
    const state = makeEmptyState();
    const update = {
      tick: 1,
      serverTimeMs: 1000,
      freshSnapshot: false,
      entered: [],
      updated: [{
        netID: 777, entityType: 0, worldX: 50, worldY: 60,
        velX: 0, velY: 0,
        /* ... other required fields as zero values ... */
      }],
      removed: [],
      exited: [],
    };
    applyDeltaUpdateForTest(state, update as any);
    const ent = state.entities.get(777);
    expect(ent).toBeDefined();
    expect(ent!.renderX).toBe(50);
    expect(ent!.renderY).toBe(60);
  });

  test("SPAWN for known netID coalesces into UPDATE (no teleport)", () => {
    const state = makeEmptyState();
    // Plant an existing entity with accumulated interp state.
    state.entities.set(555, {
      current: { netID: 555, entityType: 0, worldX: 100, worldY: 200 } as any,
      samples: [/* some samples */],
      renderX: 100, renderY: 200, renderRot: 0,
    });
    const update = {
      tick: 2,
      serverTimeMs: 1050,
      freshSnapshot: false,
      entered: [{ netID: 555, entityType: 0, worldX: 110, worldY: 210, velX: 0, velY: 0 }],
      updated: [],
      removed: [],
      exited: [],
    };
    applyDeltaUpdateForTest(state, update as any);
    // Entity still in map, renderX not snapped to 110.
    const ent = state.entities.get(555);
    expect(ent).toBeDefined();
    // Interp state should have been preserved, not reset.
    // (Exact assertion depends on how the existing code handles this;
    // at minimum assert that ent.samples is non-empty — the old samples
    // were preserved.)
    expect(ent!.samples.length).toBeGreaterThan(0);
  });
});

// Helpers — implement against actual web-pixi state module.
```

Hand-roll `makeEmptyState` + fill out the placeholder fields against the real `GameState` and `DeltaWorldUpdate` types in `web-pixi/src/state.ts` and `web-pixi/sdk/index.ts`.

- [ ] **Step 2: Run the tests**

Run: `cd web-pixi && bun test src/__tests__/network.test.ts`
Expected: FAIL

- [ ] **Step 3: Implement the two defense paths**

In `web-pixi/src/network.ts` `applyDeltaUpdate`, within the `for (const e of fresh)` loop (after merging `entered + updated`), add:

```ts
for (const e of fresh) {
  const isInEntered = update.entered.some(x => x.netID === e.netID);
  if (isInEntered && state.entities.has(e.netID)) {
    // SPAWN for a netID we already know about. Preserve the existing
    // ClientEntity's interp state; treat as an UPDATE.
    updateEntityFromServer(state.entities, e, update.serverTimeMs);
    continue;
  }
  if (!isInEntered && !state.entities.has(e.netID)) {
    // UPDATE for a netID the client never saw SPAWN for. Synthesize
    // a SPAWN by creating the ClientEntity from the update payload.
    // Same code path updateEntityFromServer takes on a fresh netID —
    // delegate to it.
    updateEntityFromServer(state.entities, e, update.serverTimeMs);
    continue;
  }
  updateEntityFromServer(state.entities, e, update.serverTimeMs);
}
```

If `updateEntityFromServer` already handles both paths correctly (inspect `web-pixi/src/interpolation.ts`), this may be a no-op. Run the tests — if they pass without changes, document that the existing code is already correct and delete the test's expectation that the code was broken.

Export a `applyDeltaUpdateForTest` wrapper from `network.ts`:

```ts
export const applyDeltaUpdateForTest = applyDeltaUpdate;
```

- [ ] **Step 4: Run the tests again**

Run: `cd web-pixi && bun test src/__tests__/network.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd .
git add web-pixi/src/network.ts web-pixi/src/__tests__/network.test.ts
git commit -m "fix(web-pixi): synthesize SPAWN for unknown UPDATE; coalesce SPAWN for known netID

Defense-in-depth for the overlap handoff. Both cases shouldn't occur
under normal operation but the client must not drop entities or reset
interp state if an out-of-order frame arrives."
```

---

### Task F3: Interp-state-survives-gap regression test

**Files:**
- Modify: `web-pixi/src/__tests__/interpolation.test.ts`

- [ ] **Step 1: Write the test**

Append:

```ts
test("one-tick update gap preserves interp baseline (no reset)", () => {
  const ent = mkEntity(0, 1000);
  pushSample(ent, mkSample(100, 1100));
  // Skip tick 1200 (gap).
  pushSample(ent, mkSample(300, 1300));
  entities.set(1, ent);

  // renderTime = 1250 (between the last two samples).
  const clientNow = 250 + RENDER_DELAY;
  interpolateEntities(entities, clock, clientNow);

  // Should lerp smoothly — no teleport to the freshest sample.
  // Expected: ~halfway between 100 and 300 → ~200.
  expect(ent.renderX).toBeGreaterThan(150);
  expect(ent.renderX).toBeLessThan(250);
});
```

- [ ] **Step 2: Run**

Run: `cd web-pixi && bun test src/__tests__/interpolation.test.ts`
Expected: PASS (per `feedback_interpolation_prev_anchor` this already works; this test documents the invariant).

- [ ] **Step 3: Commit**

```bash
cd .
git add web-pixi/src/__tests__/interpolation.test.ts
git commit -m "test(web-pixi): interp baseline survives one-tick update gap"
```

---

## Phase G — Bot-load smoke validation

### Task G1: Run the 4node-basic distributed session

**Files:** (read-only; runs the bot harness)

- [ ] **Step 1: Prepare**

```bash
cd .
just build
```

Expected: binary in `bin/server`.

- [ ] **Step 2: Start the distributed cluster**

Run: `cd examples/4node-basic && just distributed`
Expected: tmux session with 4 panes (coordinator + 2 hosts + gateway). The coordinator pane's default config (per `examples/4node-basic/world.go`) already runs with `InvariantMode=InvariantPanic` and `StrictNetIDIndex=true`. Verify `BlinkDetectorTicks=30` is set (or the default).

- [ ] **Step 3: Spawn bots and split a cell**

In the coordinator's admin console:

```
bot spawn 60 cell_0_0
cell split 0_0
```

Watch all 4 panes for panics. Let bots wander for 60 seconds.

- [ ] **Step 4: Merge and repeat**

```
cell merge 0_0
bot spawn 60 cell_1_1
cell split 1_1
```

Let run another 60 seconds.

- [ ] **Step 5: Tail `/events` for any blink-detector violations**

In a fourth terminal:

```bash
curl -s 'http://localhost:9101/events?since=5m' | jq '.[] | select(.step == "no-blink-for-conn")'
```

Expected: empty (no violations).

- [ ] **Step 6: Shutdown and record outcome**

If zero panics + zero blink-detector events after ~3 minutes of split/merge churn with 60+ bots, Phase G passes. If any panic or event fires, capture the stack trace / event JSON and file it as a follow-up — do NOT mark this task complete.

- [ ] **Step 7: Commit the bot-load result (optional)**

Add a note to the plan's "Verification" section (below) with the run date + bot count:

```bash
cd .
git add docs/superpowers/plans/2026-04-21-overlap-handoff.md
git commit -m "docs(handoff): record bot-load smoke results"
```

---

## Verification

After all phases land:

```bash
cd .
go vet ./... 2>&1              # expected: clean
just build                     # expected: bin/server exists
go test ./... -count=1 -timeout 300s  # expected: all PASS
go test ./pkg/universe/ -run '^TestS7|TestHandoff|TestOverlap|TestShadow' -count=1 -v
cd web-pixi && bun test        # expected: all PASS
```

Then the Phase G bot-load gate:

- Distributed 4-node harness
- 60 bots × 2 split cycles × 1 merge cycle
- InvariantPanic + StrictNetIDIndex + BlinkDetectorTicks=30
- Zero panics, zero `no-blink-for-conn` events

**Record once completed:**
- Run date:
- Bots spawned:
- Split/merge/migrate operations:
- Outcome:
