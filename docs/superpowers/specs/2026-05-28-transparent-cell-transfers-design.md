# Transparent Cell Transfers — Source-Side Border Context

**Status:** Design
**Date:** 2026-05-28
**Branch target:** `world-editor` (continues current work)

## 1. Overview

Splits, merges, and migrations currently produce a visible artifact on the client: entities that should remain in view "rubber-band" or flicker for ~1 second after the commit. Audit data from a single-process 500-bot reproduction showed the destination cell's first `FreshSnapshot` frame missing 83 of 223 visible entities; the client's set-wise reconcile then deletes the missing 83, and they re-enter over the next few ticks as fresh single-sample rings, producing the visual.

The root cause is that the destination cell's spatial grid is **incomplete** at the moment of the first post-commit replication tick: it contains the authoritative entities it just received (its quadrant for a split, the donor locals for a merge, the source's locals for a migrate) but **none of the cross-cell border-replica context** that the player previously saw. That context is reconstructed asynchronously over multiple ticks as neighbor cells' BorderDispatchers fire — a race the client always loses.

This design eliminates the seam by **packaging the destination cell's full visible context into the existing CellTransfer payload**. The source side already has the data (the parent's complete view for a split, donors' complete views for a merge, the source's complete view for a migrate). Destinations materialize that context as border replicas before any tick runs, so the first replication frame is complete and the client sees no discontinuity.

The approach is symmetric in single-process and distributed deployments — the wire is `meshpb.CellTransfer` in both, only the transport differs (loopback bridge vs gRPC `MeshData`). No render-delay padding, no client-side workaround, no tick-timing dependency.

## 2. Goals & Non-Goals

### Goals

- After a split, merge, or migrate, the client's first frame from the new authoritative cell contains **the full visible set** — local authoritative entities + cross-cell border replicas.
- Works uniformly in single-process and distributed deployments.
- Zero added render-delay budget. PvP-acceptable.
- Tunable via config, biased to "complete context" by default.
- Audit instrumentation we added during diagnosis is retained as a permanent diagnostic.
- All prior client-side workarounds (grace-period, cap-bypass, fresh-reconcile churn handling) and the server-side first-frame defer are reverted — the fix replaces them.

### Non-Goals

- Eliminating the *commit pause itself* — the orchestrator's split/merge/migrate commit window is unchanged. A brief pause (a few ticks) during the commit is acceptable.
- Reducing render delay below the existing default — that's a separate optimization.
- Changing the border-replication tick mechanism itself — context-seeding bootstraps the destination's view; normal border replication takes over on subsequent ticks.
- A "cross-cell entity query" RPC for arbitrary point-in-time queries — this design ships context only as part of the existing cell-transfer pipeline, not as a general-purpose query.

## 3. Problem Recap

### Observed evidence (single-process, 500 bots)

```
[repl-audit] FreshSnapshot frame received: entered+updated=140 t=9870ms
[repl-audit] post-reconcile  fresh.in=140 stateBefore=223 stateAfter=140 deleted=83
[repl-audit] settled  frame+2003ms stateNow=214 (recovered 74) re=76 ex=66
```

- Pre-split visible set: 223 entities
- First post-split fresh-frame: 140 entities (83 missing)
- Settled state ~2s later: 214 entities; 76 of the 83 deleted re-entered via subsequent border frames
- The 1s+ rubber-band corresponds to those 76 entities arriving staggered, each starting from a single-sample ring (static-then-lerp)

### Why prior attempts didn't work

| Attempt | Mechanism | Why it failed |
|---|---|---|
| Client grace-period delete | Keep entities hidden 1.5s, restore ring on re-arrival | Entities still visibly disappear and reappear → visible flicker on N entities = jitter |
| Client cap-bypass on grace-restore | Skip `effS0Stamp` clamp so lerp spans the gap | Math works but entities are hidden during grace, then "appear" at the lerped position — still discontinuous |
| Server-side first-frame defer (1 tick) | Skip first tick so border replicas land in spatial grid before FreshSnapshot | Marginal improvement (127→140 visible) but cell goroutines are independent; sibling cells don't reliably send + deliver border frames within one tick window |
| Defer by N ticks | Same as above, longer wait | Probabilistic; never deterministic; adds latency for no guarantee |

All four are patching the asynchronous border-replication mechanism rather than fixing the data flow. The source side has the data; it should ship it.

## 4. Design

### 4.1 The data flow

Three commit kinds, three source-side context-collection rules:

**SPLIT** (parent `P` → children `C0..C3`):
- Parent's spatial grid contains: P's local entities + P's existing border replicas (entities visible from outer neighbors).
- For each child `Cn`, walk the parent's grid and collect every entity (local or replica) that falls within `Cn`'s AoI margin (configurable, defaults to `ReplicationConfig.AoIRadius`).
- Entities **inside `Cn`'s quadrant** → authoritative (existing serializer behavior, unchanged).
- Entities **outside `Cn`'s quadrant but within AoI margin** → context (border-replica destined).
- Per context entity, stamp it with its true authoritative cell:
  - If the entity was local to P and falls in sibling `Cm`'s quadrant → source = `Cm`'s cell ID
  - If the entity was already a replica in P (from outer neighbor `N`) → source = `N`'s cell ID

**MERGE** (donors `D1, D2` → survivor `S`):
- Each donor's grid contains: donor locals + donor's border replicas from outer neighbors.
- Donor locals → authoritative on `S` (existing).
- Donor replicas → context entities on `S`. Source = the original outer neighbor's cell ID.
- Note: replicas of the *other donor's* locals on each donor are obsolete (they're becoming local on `S`); de-dupe by netID at populate time.

**MIGRATE** (source `P` → destination `P'` on different host):
- `P`'s grid contains: P's locals + P's border replicas.
- Locals → authoritative on `P'` (existing).
- Replicas → context entities on `P'`. Source = the original neighbor's cell ID (preserved from the replica's existing source).

### 4.2 Wire format

The existing transfer protocol is `meshpb.CellTransfer` carrying serialized entities as opaque bytes. The current shape (paraphrased):

```proto
message CellTransfer {
  CellTransferKind kind = 1;  // SPLIT | MERGE | MIGRATE
  CellID src_cell = 2;
  CellID dst_cell = 3;
  repeated bytes entities = 4;       // each is a TransferFrame (authoritative)
  repeated bytes sessions = 5;       // player session blobs
  // ...
}
```

Add one field:

```proto
message CellTransfer {
  // ... existing fields ...
  repeated BorderContextEntry context = 6;
}

message BorderContextEntry {
  bytes entity = 1;            // serialized TransferFrame, identical codec
  string source_cell_id = 2;   // mesh ID of the cell this replica should attribute to
}
```

Each `BorderContextEntry.entity` uses the **same** `TransferFrame` codec as authoritative entities — the destination unpacks via `UnmarshalTransferFrame` exactly as today. The only difference is what the destination does with it: `upsertBorderReplica` instead of `SpawnFromTransferCore`.

`source_cell_id` is necessary because border replicas are tracked per-source on the destination (so subsequent border frames from that source land on the right replica). The source side knows this from the source-side context-collection rules above.

### 4.3 Source-side: collecting context

Single function added to the cell transfer executor:

```go
// collectBorderContextFor walks the source cell's spatial grid and
// returns serialized entity blobs for everything within `radius` of
// the (lMinX, lMinY, lMaxX, lMaxY) bounding box that ISN'T already in
// `excludedNetIDs` (the authoritative entities). Per blob, returns the
// cell ID the destination should treat as the replica's source. Used
// at SPLIT/MERGE/MIGRATE serialize time to ship cross-cell visibility
// to the destination so its first frame is complete.
//
// Bounds are passed as four floats (matching the idiom used by
// BorderDispatcher.candidatesFor and Cell.LocalBounds) rather than
// introducing a new Rectangle type.
func (b *Stage) collectBorderContextFor(
    lMinX, lMinY, lMaxX, lMaxY, radius float32,
    excludedNetIDs map[uint32]bool,
    resolveSource func(entity ecs.Entity, pos component.Position) MeshCellID,
) []BorderContextEntry
```

`resolveSource` is the per-commit-kind rule — split passes a function that maps positions to sibling cell IDs; merge passes a function that reads each donor-replica's existing source; migrate passes a function that preserves the source ID through.

The serializer in `cell_transfer_executor.go` already iterates entities; we add a second pass to collect context after the authoritative pass.

### 4.4 Destination-side: materializing context

The destination's `populateCell` already iterates `proto.entities` and calls `Spawn` (for SPLIT/MIGRATE) or the appropriate spawn variant. Add a second loop after the existing one:

```go
// Materialize context entities as border replicas. Same TransferFrame
// codec as authoritative entities; the only difference is the spawn
// API. Destination's spatial grid is now complete: authoritative
// locals + cross-cell context. The next ReplicationSystem tick emits
// a FreshSnapshot containing the full visible set, no client churn.
for _, ctx := range proto.Context {
    frame, err := UnmarshalTransferFrame(ctx.Entity)
    if err != nil { /* log + skip */ continue }
    cell.Stage.upsertBorderReplicaFromTransfer(frame, ctx.SourceCellId)
}
```

`upsertBorderReplicaFromTransfer` is a thin wrapper that extracts the args `upsertBorderReplica` needs (netID, epoch, kind, position, velocity, rotation, collider, sourceCellID, producedAtMs, componentTail) from a `TransferFrame`. Existing `upsertBorderReplica` is unchanged.

### 4.5 Configuration

New fields on `PartitionConfig` (in `pkg/universe/`):

| Field | Default | Description |
|---|---|---|
| `IncludeBorderContext` | `true` | Master toggle. Set false to skip context collection entirely (legacy behavior; useful for tests and minimal-bandwidth deployments). |
| `BorderContextRadius` | `0` (= use `ReplicationConfig.AoIRadius`) | AoI margin for context collection. Tunable for games with non-default AoI. |
| `BorderContextMaxCount` | `0` (unbounded) | Safety cap on context entities per dest-cell payload. If exceeded, log a structured warning and ship the first N; the rest fall back to normal async border replication. |

These live on `PartitionConfig` because they're orchestrator-level concerns (commit-time behavior), parallel to existing fields like `AutoRebalance` and `RebalanceMinDelta`.

### 4.6 Replication ordering

After populate completes (including context), the destination cell ticks normally. On its first PostSystems, `ensureBorderDispatcher` creates fresh CellViewers for its new neighbors and starts emitting border frames — exactly as today. On its next tick (or the same tick if PreSystems drained an inbound frame), border replicas in the spatial grid get refreshed by incoming border frames.

Net effect:
- **Tick 0 (commit):** spatial grid populated with locals + context. No tick runs.
- **Tick 1:** ReplicationSystem queries grid → emits complete FreshSnapshot to viewers. Client receives full set; no churn.
- **Tick 2+:** Normal border replication overwrites seeded replicas with live border frames. Continuous interpolation.

The seeded replicas have `producedAtMs` from the source cell at commit time, so the client's per-entity ring stays monotonic across the seam.

## 5. Reversion of Prior Patches

All the workarounds we added during diagnosis come out. The audit instrumentation stays.

### Server-side (revert)

- [pkg/system/replication.go](pkg/system/replication.go): remove `firstFramePending map[uint32]bool` field, `DeferFirstFrame` config field, the defer block in `Update`, and the firstFramePending cleanup loop.
- [pkg/mmokit/mmokit.go](pkg/mmokit/mmokit.go): remove `DeferFirstFrame: true` from `DefaultReplicationConfig`.

### Client-side (already reverted in prior turns; verify clean)

- [examples/4node-basic/web/src/state.ts](examples/4node-basic/web/src/state.ts): no `graceExpiresAtMs` / `graceRestoredAtStamp` fields on `ClientEntity`. ✓
- [examples/4node-basic/web/src/interpolation.ts](examples/4node-basic/web/src/interpolation.ts): no grace-restore branch, no cap-bypass branch. ✓
- [examples/4node-basic/web/src/reconcile.ts](examples/4node-basic/web/src/reconcile.ts): plain `entities.delete(id)` reconcile, audit logging retained. ✓
- [examples/4node-basic/web/src/renderer.ts](examples/4node-basic/web/src/renderer.ts): no grace-prune at frame top, no skip-grace in draw loop. ✓

### Retained (do not revert)

- [examples/4node-basic/web/src/replicationAudit.ts](examples/4node-basic/web/src/replicationAudit.ts): full instrumentation.
- The audit HUD panel in [renderer.ts](examples/4node-basic/web/src/renderer.ts) and `window.replAudit()` helper in [main.ts](examples/4node-basic/web/src/main.ts).
- Audit hooks in [network.ts](examples/4node-basic/web/src/network.ts) and the `[repl-audit]` console logs in [reconcile.ts](examples/4node-basic/web/src/reconcile.ts).

These are the verification surface for the new design — post-fix, the same instrumentation should show `deleted ≈ 0` and `re ≈ 0` on a split, confirming the fresh frame is complete.

## 6. Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Tech Stack:** Go 1.22+, ark v0.7.1 ECS, protobuf via buf, TypeScript + Bun (audit retention only — no client changes).

The plan is decomposed into eight tasks. Tasks 1–2 revert prior workarounds (clean baseline). Tasks 3–6 build the new mechanism. Task 7 is the end-to-end integration test. Task 8 wires the config knobs.

---

### Task 1: Revert server-side first-frame defer

**Files:**
- Modify: [pkg/system/replication.go](pkg/system/replication.go)
- Modify: [pkg/mmokit/mmokit.go](pkg/mmokit/mmokit.go) — `DefaultReplicationConfig`

- [ ] **Step 1: Remove `DeferFirstFrame` from `ReplicationConfig`**

In [pkg/system/replication.go](pkg/system/replication.go), find the `DeferFirstFrame bool` field with its multi-line comment in `ReplicationConfig` (immediately after `BlinkDetectorTicks` / `OnBlinkDetected`). Delete the entire field + comment block.

- [ ] **Step 2: Remove `firstFramePending` from `ReplicationSystem` struct**

Find `firstFramePending map[uint32]bool` (with its multi-line comment) in the `ReplicationSystem` struct and delete it.

- [ ] **Step 3: Remove its initialization**

In `NewReplicationSystem`, remove the line:
```go
firstFramePending: make(map[uint32]bool),
```
Re-align the surrounding lines (`lastVisible`, `connections`) back to their pre-defer indentation.

- [ ] **Step 4: Remove the defer logic in the viewer loop**

Find this block in `Update`:
```go
if s.cfg.DeferFirstFrame {
    if !hadPriorState && !s.firstFramePending[viewer.ConnID] {
        s.firstFramePending[viewer.ConnID] = true
        continue
    }
    if s.firstFramePending[viewer.ConnID] {
        delete(s.firstFramePending, viewer.ConnID)
    }
}
```
Delete it entirely.

- [ ] **Step 5: Remove the cleanup loop**

Find this block (right after the existing `lastVisible` cleanup loop):
```go
for connID := range s.firstFramePending {
    if !activeConns[connID] {
        delete(s.firstFramePending, connID)
    }
}
```
Delete it.

- [ ] **Step 6: Remove `DeferFirstFrame: true` from `DefaultReplicationConfig`**

In [pkg/mmokit/mmokit.go](pkg/mmokit/mmokit.go) `DefaultReplicationConfig`, remove the `DeferFirstFrame: true,` line. Re-align the struct literal.

- [ ] **Step 7: Verify**

Run:
```bash
go vet ./...
go test ./pkg/system/... ./pkg/universe/... ./pkg/mmokit/...
```
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add pkg/system/replication.go pkg/mmokit/mmokit.go
git commit -m "revert: remove first-frame defer — replaced by border-context transfer"
```

---

### Task 2: Verify client-side reversion is clean (no code change)

**Files:** (no modifications, verification only)
- Check: [examples/4node-basic/web/src/state.ts](examples/4node-basic/web/src/state.ts)
- Check: [examples/4node-basic/web/src/interpolation.ts](examples/4node-basic/web/src/interpolation.ts)
- Check: [examples/4node-basic/web/src/reconcile.ts](examples/4node-basic/web/src/reconcile.ts)
- Check: [examples/4node-basic/web/src/renderer.ts](examples/4node-basic/web/src/renderer.ts)

- [ ] **Step 1: Confirm no grace fields in `ClientEntity`**

```bash
grep -n "graceExpiresAtMs\|graceRestoredAtStamp" examples/4node-basic/web/src/state.ts
```
Expected: no output.

- [ ] **Step 2: Confirm no grace branches in `updateEntityFromServer` and no cap-bypass in `interpolateEntities`**

```bash
grep -n "graceExpiresAtMs\|graceRestoredAtStamp\|NO_CAP_RENDER_DELAY" examples/4node-basic/web/src/interpolation.ts
```
Expected: no output.

- [ ] **Step 3: Confirm `pruneStaleOnFreshSnapshot` is plain immediate-delete**

```bash
grep -A 2 "if (!visible.has(id))" examples/4node-basic/web/src/reconcile.ts
```
Expected: shows `recordDeletion(...)` followed by `entities.delete(id)`. No `graceExpiresAtMs` assignment.

- [ ] **Step 4: Confirm no grace skip / pruner in renderer**

```bash
grep -n "graceExpiresAtMs" examples/4node-basic/web/src/renderer.ts
```
Expected: no output.

- [ ] **Step 5: Run web tests + typecheck to confirm clean baseline**

```bash
cd examples/4node-basic/web && bun run tsc --noEmit && bun test
```
Expected: typecheck clean, all 29 tests pass.

No commit for this task.

---

### Task 3: Add `BorderContextEntry` to `meshpb.CellTransfer` proto

**Files:**
- Modify: `proto/meshpb/mesh.proto` (search via `find proto -name '*.proto'` to confirm exact path if the layout has moved)
- Regenerate: `gen/go/meshpb/`

- [ ] **Step 1: Locate the `CellTransfer` message in the proto file**

```bash
grep -n "message CellTransfer\|message BorderContextEntry" $(find proto -name '*.proto' 2>/dev/null)
```

- [ ] **Step 2: Add the new message and field**

Inside the same `.proto` file, add:

```proto
// BorderContextEntry carries a serialized entity that the destination
// cell should materialize as a border replica (NOT authoritative). The
// `entity` blob uses the same TransferFrame codec as authoritative
// entities in `CellTransfer.entities`; the destination dispatches it
// via upsertBorderReplica instead of SpawnFromTransferCore.
// `source_cell_id` is the mesh ID of the cell whose authority this
// replica belongs to — used by the destination to route subsequent
// border frames from that cell onto this replica.
message BorderContextEntry {
  bytes entity = 1;
  string source_cell_id = 2;
}
```

In the `CellTransfer` message body, after the existing `entities` and `sessions` fields, add:

```proto
  // Cross-cell border-replica seed for transparent handoffs. Populated
  // by the source side at SPLIT/MERGE/MIGRATE serialize time so the
  // destination's spatial grid is complete (locals + cross-cell context)
  // before its first replication tick. See
  // docs/superpowers/specs/2026-05-28-transparent-cell-transfers-design.md.
  repeated BorderContextEntry context = 6;
}
```

(Use field number 6 — confirm it's unused; bump if necessary. Field numbers must be unique within the message.)

- [ ] **Step 3: Regenerate Go bindings**

```bash
just proto
```
Expected: `gen/go/meshpb/mesh.pb.go` regenerated, no errors.

- [ ] **Step 4: Confirm regen**

```bash
grep -n "BorderContextEntry\|Context.*BorderContextEntry" gen/go/meshpb/mesh.pb.go | head -5
```
Expected: matches showing the new type + field exist.

- [ ] **Step 5: Verify build**

```bash
go vet ./...
```
Expected: no errors. (Unused field is fine; we wire it in next tasks.)

- [ ] **Step 6: Commit**

```bash
git add proto/ gen/go/meshpb/
git commit -m "proto: add BorderContextEntry to CellTransfer for transparent handoffs"
```

---

### Task 4: Add `upsertBorderReplicaFromTransfer` helper on `Stage`

**Files:**
- Modify: [pkg/universe/stage.go](pkg/universe/stage.go) — add new method near existing `upsertBorderReplica` (around line 1180+, find via grep)
- Test: `pkg/universe/border_context_test.go` (new file)

- [ ] **Step 1: Locate `upsertBorderReplica` to mirror its argument extraction**

```bash
grep -n "func.*upsertBorderReplica\b" pkg/universe/stage.go
```

- [ ] **Step 2: Write the failing test first**

Create `pkg/universe/border_context_test.go`:

```go
package universe

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
)

// TestUpsertBorderReplicaFromTransfer_SeedsReplica confirms that a
// serialized TransferFrame, when fed through the context-materialize
// helper, lands in the destination stage's ECS as a Replica entity
// at the expected position and source-cell tag — exactly the state
// border replication would reach by the steady-state of cross-cell
// border-frame round trips, but synchronously at commit time.
func TestUpsertBorderReplicaFromTransfer_SeedsReplica(t *testing.T) {
	stage, _ := newTestStage(t)
	defer stage.Shutdown()

	// Build a TransferFrame as the source side would.
	frame := &TransferFrame{
		NetID:        component.NetworkID{ID: 4242, Epoch: 1},
		Kind:         component.EntityKind{Type: 7},
		Position:     component.Position{X: 100, Y: 200},
		Velocity:     component.Velocity{X: 5, Y: 0},
		Rotation:     component.Rotation{Angle: 1.5},
		Collider:     component.Collider{Radius: 10},
		ProducedAtMs: 1700,
	}

	if err := stage.upsertBorderReplicaFromTransfer(frame, "cell_1_0"); err != nil {
		t.Fatalf("upsertBorderReplicaFromTransfer: %v", err)
	}

	rep, ok := stage.netIDIndex.Find(4242)
	if !ok {
		t.Fatalf("expected netID 4242 to be present after upsert")
	}
	if rep.Presence != PresenceReplica {
		t.Errorf("Presence = %v, want PresenceReplica", rep.Presence)
	}
	if got := stage.replicaMap.Get(rep.Entity).ProducedAtMs; got != 1700 {
		t.Errorf("Replica.ProducedAtMs = %d, want 1700", got)
	}
}
```

`newTestStage` is the existing test helper used by other `pkg/universe` tests — locate via `grep -n "func newTestStage" pkg/universe/`. If it doesn't take the args we need, use whichever stage-construction helper the neighboring tests use.

- [ ] **Step 3: Run the test to confirm it fails**

```bash
go test -run TestUpsertBorderReplicaFromTransfer_SeedsReplica ./pkg/universe/
```
Expected: FAIL with `stage.upsertBorderReplicaFromTransfer undefined`.

- [ ] **Step 4: Implement the helper**

In [pkg/universe/stage.go](pkg/universe/stage.go), near the existing `upsertBorderReplica` method, add:

```go
// upsertBorderReplicaFromTransfer materializes a TransferFrame as a
// border replica on this Stage. Used at SPLIT/MERGE/MIGRATE populate
// time to seed cross-cell visibility before any tick runs, so the
// destination's first replication frame is complete (locals + context)
// and clients see no transition artifact.
//
// Mirrors the codec path that ApplyBorderFrame takes for a live border
// frame: extracts the same fields from the TransferFrame and routes
// through the unchanged upsertBorderReplica primitive. The only
// difference is the trigger (commit-time seeding vs steady-state border
// replication tick).
func (b *Stage) upsertBorderReplicaFromTransfer(frame *TransferFrame, sourceCellID string) error {
	if frame == nil {
		return errNilTransferFrame
	}
	// Convert world coords to this cell's local coords. TransferFrame
	// carries world-absolute Position; upsertBorderReplica's signature
	// takes local-x/y (consistent with ApplyBorderFrame's call site).
	cellSize := coords.CellSize
	rootCell := b.Cell()
	for rootCell.Depth > 0 {
		rootCell = rootCell.Parent()
	}
	localX := frame.Position.X - float32(rootCell.X)*cellSize
	localY := frame.Position.Y - float32(rootCell.Y)*cellSize

	return b.upsertBorderReplica(
		frame.NetID.ID,
		frame.NetID.Epoch,
		uint8(frame.Kind.Type),
		localX, localY,
		frame.Collider.Radius,
		frame.Velocity.X, frame.Velocity.Y,
		frame.Rotation.Angle,
		MeshCellID(sourceCellID),
		frame.ProducedAtMs,
		nil, // componentTail — seeded from TransferFrame fields above; live tail follows via normal border replication
	)
}

var errNilTransferFrame = errors.New("upsertBorderReplicaFromTransfer: nil frame")
```

(If `upsertBorderReplica` returns no error, drop the `error` return — match the existing signature.)

Confirm the imports section has `errors` and `coords` already imported; add if missing.

- [ ] **Step 5: Run the test to confirm it passes**

```bash
go test -run TestUpsertBorderReplicaFromTransfer_SeedsReplica ./pkg/universe/ -v
```
Expected: PASS.

- [ ] **Step 6: Run the full universe test suite**

```bash
go test ./pkg/universe/...
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/stage.go pkg/universe/border_context_test.go
git commit -m "universe: add upsertBorderReplicaFromTransfer helper"
```

---

### Task 5: Source-side context collection in `cell_transfer_executor.go`

> **Order note:** Task 5 references `PartitionConfig.IncludeBorderContext` / `BorderContextRadius` / `BorderContextMaxCount`, added in **Task 7**. Do Task 7 first, or land both together as a single commit. Both are listed before the destination-side wiring (Task 6) and the end-to-end test (Task 8) because together they make context-shipping functional.

**Files:**
- Modify: [pkg/universe/cell_transfer_executor.go](pkg/universe/cell_transfer_executor.go) — `serializeQuadrantEntities` and `serializeAllEntities` (or whichever the SPLIT / MERGE / MIGRATE serializer functions are)
- Modify: [pkg/universe/stage.go](pkg/universe/stage.go) — add `collectBorderContextFor` method
- Test: extend `pkg/universe/border_context_test.go`

- [ ] **Step 1: Add the failing test for SPLIT context collection**

Append to `pkg/universe/border_context_test.go`:

```go
// TestCollectBorderContextFor_SplitGathersSiblingsAndOuterReplicas builds
// a parent stage with: (a) two locals in the target child quadrant, (b)
// three locals in OTHER quadrants, (c) one replica from an outer
// neighbor. The collector for a target child should return (b) + (c) —
// the cross-cell context the child needs to render its first complete
// frame post-split — excluding (a) which are the child's own
// authoritative entities (collected by the existing serializer).
func TestCollectBorderContextFor_SplitGathersSiblingsAndOuterReplicas(t *testing.T) {
	// ... build stage with the 6 entities described above ...
	// ... call stage.collectBorderContextFor(...) for child 0's bounds ...
	// ... assert returned slice contains exactly the 3 sibling locals + 1 outer replica
	//     (4 entries), all with correctly-resolved source cell IDs ...
	t.Fatal("test body — fill in with the helpers in this package")
}
```

(Use this as a stub: when you reach this task, look at how neighboring tests in `border_replication_apply_test.go` build a stage with mixed local/replica entities, and use the same patterns.)

- [ ] **Step 2: Run the test to confirm it fails**

```bash
go test -run TestCollectBorderContextFor_SplitGathersSiblingsAndOuterReplicas ./pkg/universe/
```
Expected: FAIL.

- [ ] **Step 3: Implement `collectBorderContextFor` on Stage**

In [pkg/universe/stage.go](pkg/universe/stage.go), add:

```go
// BorderContextEntry is the source-side representation of one
// to-be-shipped border replica seed. Serialized into
// meshpb.BorderContextEntry by the executor.
type BorderContextEntry struct {
	Frame        *TransferFrame
	SourceCellID MeshCellID
}

// collectBorderContextFor walks the spatial grid and returns serialized
// border-replica seeds for every entity within `radius` of `destBounds`
// that ISN'T in `excludedNetIDs` (the destination's authoritative
// entities, collected separately by the serializer's existing pass).
// Per entity, `resolveSource(entity, position)` decides the source-cell
// attribution the destination should record on the replica.
//
// Used by SPLIT (children get sibling + outer-neighbor visibility),
// MERGE (survivor gets each donor's outer-neighbor replicas), and
// MIGRATE (destination gets the source's outer-neighbor replicas).
func (b *Stage) collectBorderContextFor(
	lMinX, lMinY, lMaxX, lMaxY, radius float32,
	excludedNetIDs map[uint32]bool,
	resolveSource func(entity ecs.Entity, pos component.Position) MeshCellID,
) []BorderContextEntry {
	// 1. Enumerate entities in spatial grid within (destBounds expanded by radius).
	// 2. For each:
	//    - Skip if NetID is in excludedNetIDs.
	//    - SerializeEntityCore(entity) → TransferFrame.
	//    - sourceCell := resolveSource(entity, position).
	//    - Append BorderContextEntry{frame, sourceCell}.
	// 3. Return slice.
	//
	// Implementation: mirror SerializeEntityCore's query setup but use
	// the spatial-grid bounding-box query (see HashGrid.QueryRect) for
	// the AoI-extended bounds, then filter by exact radius check.
	panic("implement based on SerializeEntityCore + HashGrid query patterns")
}
```

Fill in the body using the patterns from `SerializeEntityCore` (entity walk, component extraction) and `BorderDispatcher.candidatesFor` (spatial-grid query, AoI-band filter). The implementation references types defined in earlier tasks: `TransferFrame` (existing), `BorderContextEntry` (this task), `MeshCellID` (existing), `Rectangle` (existing helper in `pkg/universe/`).

- [ ] **Step 4: Wire context collection into the SPLIT serialize path**

Locate `serializeQuadrantEntities` in [pkg/universe/cell_transfer_executor.go](pkg/universe/cell_transfer_executor.go). After the existing per-quadrant authoritative serialization loop, add:

```go
if e.executor.cfg().IncludeBorderContext {
    radius := e.executor.cfg().BorderContextRadius
    if radius == 0 {
        radius = stage.GetAoIRadius()
    }
    lMinX, lMinY, lMaxX, lMaxY := childCell.LocalBounds(coords.CellSize)
    contextEntries := stage.collectBorderContextFor(
        lMinX, lMinY, lMaxX, lMaxY,
        radius,
        authoritativeNetIDs, // set built during the auth pass
        func(entity ecs.Entity, pos component.Position) MeshCellID {
            // SPLIT-specific resolver: locals in a sibling's quadrant
            // attribute to that sibling; replicas keep their existing
            // source-cell ID.
            if stage.replicaMap.HasAll(entity) {
                return MeshCellID(stage.replicaMap.Get(entity).SourceCellID)
            }
            return resolveSiblingByPosition(pos, children)
        },
    )
    for _, ce := range contextEntries {
        proto.Context = append(proto.Context, &meshpb.BorderContextEntry{
            Entity:       MarshalTransferFrame(ce.Frame),
            SourceCellId: string(ce.SourceCellID),
        })
    }
}
```

`resolveSiblingByPosition` is a small helper (write inline or as a private package function) that maps a world-space position to whichever of `children[]` contains it.

`e.executor.cfg()` is the accessor for the orchestrator's `PartitionConfig` — match the existing pattern (locate via `grep`).

- [ ] **Step 5: Wire into the MERGE serialize path**

In the merge serialize function (same file), after the authoritative pass:

```go
if e.executor.cfg().IncludeBorderContext {
    radius := e.executor.cfg().BorderContextRadius
    if radius == 0 {
        radius = stage.GetAoIRadius()
    }
    // Survivor bounds = donor's bounds extended by AoI (donor's locals
    // are now survivor's locals; we want context entities outside the
    // donor's territory but visible to the survivor).
    lMinX, lMinY, lMaxX, lMaxY := donorCell.LocalBounds(coords.CellSize)
    contextEntries := stage.collectBorderContextFor(
        lMinX, lMinY, lMaxX, lMaxY,
        radius,
        authoritativeNetIDs,
        func(entity ecs.Entity, pos component.Position) MeshCellID {
            // MERGE: only replicas qualify (the other donor's locals
            // become survivor-local). Replicas keep their existing source.
            return MeshCellID(stage.replicaMap.Get(entity).SourceCellID)
        },
    )
    // ... append to proto.Context as in SPLIT ...
}
```

- [ ] **Step 6: Wire into the MIGRATE serialize path**

In the migrate serialize function (same file):

```go
if e.executor.cfg().IncludeBorderContext {
    radius := e.executor.cfg().BorderContextRadius
    if radius == 0 {
        radius = stage.GetAoIRadius()
    }
    lMinX, lMinY, lMaxX, lMaxY := sourceCell.LocalBounds(coords.CellSize)
    contextEntries := stage.collectBorderContextFor(
        lMinX, lMinY, lMaxX, lMaxY,
        radius,
        authoritativeNetIDs,
        func(entity ecs.Entity, pos component.Position) MeshCellID {
            // MIGRATE: only replicas qualify. Locals all transfer
            // authoritative; replicas keep their existing source.
            return MeshCellID(stage.replicaMap.Get(entity).SourceCellID)
        },
    )
    // ... append to proto.Context as in SPLIT ...
}
```

- [ ] **Step 7: Run the failing test from Step 1 (now fill in the body using package helpers, then re-run)**

Re-run:
```bash
go test -run TestCollectBorderContextFor_SplitGathersSiblingsAndOuterReplicas ./pkg/universe/ -v
```
Expected: PASS.

- [ ] **Step 8: Verify whole-package**

```bash
go test ./pkg/universe/...
```
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add pkg/universe/stage.go pkg/universe/cell_transfer_executor.go pkg/universe/border_context_test.go
git commit -m "universe: collect border-replica context on source-side at transfer time"
```

---

### Task 6: Destination-side context materialization in `populateCell`

**Files:**
- Modify: [pkg/universe/cell_transfer_executor.go](pkg/universe/cell_transfer_executor.go) — `populateCell` (~line 443)
- Test: extend `pkg/universe/border_context_test.go`

- [ ] **Step 1: Add the failing integration test**

Append to `pkg/universe/border_context_test.go`:

```go
// TestPopulateCell_MaterializesContextEntries builds a CellTransfer
// proto with both authoritative entities and context entries, runs
// populateCell against a fresh stage, and asserts the resulting world
// contains BOTH sets — authoritative as locals (Spawn'd), context as
// PresenceReplica entries in netIDIndex. This locks in the contract
// that the executor must materialize context, not just author.
func TestPopulateCell_MaterializesContextEntries(t *testing.T) {
	// Build proto with:
	//   - 2 authoritative entities (will Spawn → PresenceLive)
	//   - 3 context entries (will upsertBorderReplica → PresenceReplica)
	// Run executor.populateCell.
	// Assert:
	//   - 5 entities in stage.netIDIndex total
	//   - 2 PresenceLive, 3 PresenceReplica
	//   - Each replica's Replica.SourceCellID matches the proto's value
	t.Fatal("test body — fill in")
}
```

- [ ] **Step 2: Run to confirm it fails**

```bash
go test -run TestPopulateCell_MaterializesContextEntries ./pkg/universe/
```
Expected: FAIL.

- [ ] **Step 3: Add the context loop in `populateCell`**

In [pkg/universe/cell_transfer_executor.go](pkg/universe/cell_transfer_executor.go), find `populateCell` (around line 443). After the existing entity-spawn loop:

```go
// Materialize cross-cell border context (sibling locals + outer-neighbor
// replicas the source bundled in for transparent transfer). Same codec
// as authoritative entities; only the spawn API differs. After this loop,
// the destination's spatial grid contains the complete visible set from
// the player's perspective — the next ReplicationSystem tick emits a
// FreshSnapshot that needs no client-side reconcile.
for _, ctx := range proto.Context {
    frame, err := UnmarshalTransferFrame(ctx.Entity)
    if err != nil {
        e.log.Log(catTransfer, "[%s] populateCell: context decode failed: %v", cell.MeshID, err)
        continue
    }
    if err := cell.Stage.upsertBorderReplicaFromTransfer(frame, ctx.SourceCellId); err != nil {
        e.log.Log(catTransfer, "[%s] populateCell: context upsert failed netID=%d: %v",
            cell.MeshID, frame.NetID.ID, err)
        continue
    }
}
```

`catTransfer` is the existing log category for transfer operations (locate via `grep`).

- [ ] **Step 4: Fill in the test body using the helpers in `border_context_test.go`**

Replace the `t.Fatal(...)` stub from Step 1 with the actual test using the patterns established in Tasks 4-5.

- [ ] **Step 5: Run the test to confirm it passes**

```bash
go test -run TestPopulateCell_MaterializesContextEntries ./pkg/universe/ -v
```
Expected: PASS.

- [ ] **Step 6: Run the full universe suite**

```bash
go test ./pkg/universe/...
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/cell_transfer_executor.go pkg/universe/border_context_test.go
git commit -m "universe: materialize border-replica context on destination-side populate"
```

---

### Task 7: Add config knobs on `PartitionConfig`

**Files:**
- Modify: [pkg/universe/coordinator.go](pkg/universe/coordinator.go) (or wherever `PartitionConfig` is defined — find via `grep -rn 'type PartitionConfig struct'`)
- Modify: [pkg/mmokit/mmokit.go](pkg/mmokit/mmokit.go) — `DefaultPartitionConfig`

- [ ] **Step 1: Locate `PartitionConfig`**

```bash
grep -rn "type PartitionConfig struct\|DefaultPartitionConfig" pkg/universe/ pkg/mmokit/
```

- [ ] **Step 2: Add the three fields**

Inside `PartitionConfig`, after the existing rebalance fields:

```go
// IncludeBorderContext enables source-side context collection at
// SPLIT/MERGE/MIGRATE commit time. When true (default), the source
// cell ships its cross-cell visible set (sibling locals + outer-
// neighbor replicas) to the destination along with the authoritative
// transfer, so the destination's first frame to clients is complete.
// Disable for tests or minimal-bandwidth scenarios. See
// docs/superpowers/specs/2026-05-28-transparent-cell-transfers-design.md.
IncludeBorderContext bool

// BorderContextRadius is the AoI margin used when collecting context
// entities. 0 means "use ReplicationConfig.AoIRadius" (the default).
// Tunable for games where the natural AoI for context-seeding differs
// from the replication AoI.
BorderContextRadius float32

// BorderContextMaxCount caps the number of context entities serialized
// per destination cell to bound transfer payload size. 0 = unbounded.
// On overflow, the first N entries ship and a structured warning logs;
// the rest fall back to normal async border replication on subsequent
// ticks.
BorderContextMaxCount int
```

- [ ] **Step 3: Default `IncludeBorderContext = true` in `DefaultPartitionConfig`**

Locate `DefaultPartitionConfig` (search command in Step 1). Add to the returned struct:

```go
IncludeBorderContext: true,
// BorderContextRadius, BorderContextMaxCount keep zero defaults
```

- [ ] **Step 4: Honor `BorderContextMaxCount` in the collector**

In `collectBorderContextFor` (added in Task 5), respect the cap:

```go
maxCount := /* read from PartitionConfig */
collected := []BorderContextEntry{}
// ... walk entities ...
collected = append(collected, BorderContextEntry{frame, sourceCell})
if maxCount > 0 && len(collected) >= maxCount {
    b.log.Log(catBorderContext,
        "[%s] context overflow: capping at %d, falling back to async border replication",
        b.Cell().MeshID(), maxCount)
    break
}
```

Wire `PartitionConfig` access from the orchestrator to the Stage if not already threaded; pass as a parameter to `collectBorderContextFor` if cleaner than struct field.

- [ ] **Step 5: Verify**

```bash
go vet ./...
go test ./pkg/universe/... ./pkg/mmokit/...
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/ pkg/mmokit/
git commit -m "universe: PartitionConfig knobs for border-context collection (default on)"
```

---

### Task 8: End-to-end integration test — split with populated cell, watching player

**Files:**
- Create: `pkg/universe/transparent_split_test.go`

- [ ] **Step 1: Write the failing integration test**

```go
package universe

import (
	"testing"
	// ... existing test imports used by other s7_*_test.go files ...
)

// TestSplit_PlayerSeesCompleteFirstFrame_NoChurn drives the full
// commit pipeline (orchestrator → executor → populateCell) on a single-
// host cluster with one cell containing N bots and a watching player.
// After the split, asserts the player's first post-split frame contains
// the same visible set as the pre-split frame (modulo entities that
// genuinely moved out of AoI). This is the regression test for the
// transparent-cell-transfer design.
//
// Failure mode pre-fix: 80+ of the visible entities are missing from
// the first post-split frame and re-appear over the next 1-2s as
// border replicas trickle in via async border replication.
func TestSplit_PlayerSeesCompleteFirstFrame_NoChurn(t *testing.T) {
	// 1. Boot a single-host coordinator with the standard test config
	//    (mirror s7_split_test.go patterns).
	// 2. Spawn 200 bots in the player's cell, distributed across
	//    quadrants so the split will move some to siblings.
	// 3. Connect a test client / capture frame writer; record the
	//    pre-split fresh-frame visible set.
	// 4. Trigger cell split on the player's cell.
	// 5. Wait for the split commit to complete.
	// 6. Capture the FIRST post-split frame emitted to the player.
	// 7. Assert: the captured frame's entered+updated set is a SUPERSET
	//    of (preSplit ∩ stillInAoI).
	// 8. Assert: there is no second frame within the next N ticks
	//    that "fills in" missing entities — the first frame is the
	//    complete one.
	t.Fatal("test body — see s7_split_test.go for cluster-bootstrap helpers")
}
```

- [ ] **Step 2: Run to confirm it fails**

```bash
go test -run TestSplit_PlayerSeesCompleteFirstFrame_NoChurn ./pkg/universe/ -v
```
Expected: FAIL (initially with `t.Fatal`; after filling in the body, FAIL because the prior code didn't ship context — though by this point context-shipping is already implemented, so verify it passes immediately upon writing the body).

- [ ] **Step 3: Fill in test body using `s7_split_test.go` patterns**

Use the cluster setup helpers in `pkg/universe/s7_split_test.go` (the existing split test) to bootstrap. Add the bot-spawn loop using `stage.Spawn` patterns from `4node-basic`. Capture frames via a custom `FrameWriter` mock injected into `ReplicationConfig.Frame` — match patterns from `pkg/system/frame_writer_test.go`.

- [ ] **Step 4: Run to confirm it passes**

```bash
go test -run TestSplit_PlayerSeesCompleteFirstFrame_NoChurn ./pkg/universe/ -v
```
Expected: PASS (the design has been fully implemented in Tasks 3-7).

- [ ] **Step 5: Run the full s7 split/merge/migrate suite to catch regressions**

```bash
go test -run TestS7 ./pkg/universe/ -v
```
Expected: PASS — none of the existing split/merge/migrate tests should regress; context-seeding is additive.

- [ ] **Step 6: Run the 4node-basic mesh end-to-end test**

```bash
go test ./examples/4node-basic/...
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/transparent_split_test.go
git commit -m "test: regression for transparent cell split — first frame includes border context"
```

---

### Task 9: Live verification via audit instrumentation

**Files:** (no code changes)

- [ ] **Step 1: Build and run dev**

```bash
just build
cd examples/4node-basic && just dev
```

- [ ] **Step 2: In a browser, login + spawn**

Login. Watch the `[repl-audit] FreshSnapshot frame received: entered+updated=1 t=...ms` line — this is the login frame (pre-split, expected).

- [ ] **Step 3: From the server console, spawn bots + trigger split**

```
bot spawn 200 cell_0_0
```

Wait a few seconds for the bots to spread out.

```
cell split 0_0
```

- [ ] **Step 4: Read the audit logs in DevTools**

Expected pattern:

```
[repl-audit] FreshSnapshot frame received: entered+updated=~200 t=...
[repl-audit] post-reconcile  fresh.in=~200 stateBefore=~200 stateAfter=~200 deleted=~0
[repl-audit] settled  frame+2000ms stateNow=~200 (recovered ~0) re=~0
```

**The critical numbers:** `deleted` near zero, `re` near zero. If both are ~0, the FreshSnapshot contained the full visible set — design is working.

If `deleted` is meaningfully nonzero, the context collection isn't shipping enough. Bump verbosity in `collectBorderContextFor`'s overflow path or extend the AoI radius and re-run.

- [ ] **Step 5: Visual smoke-test**

Repeat split several times. Confirm visually:
- No entities flicker on/off
- No rubber-band / jitter
- The player's view is stable across the commit (modulo any genuine AoI exits as the player position relative to cell boundaries changes)

No commit for this task — verification only.

---

## 7. Testing Strategy

| Layer | Test | What it pins |
|---|---|---|
| Unit | `TestUpsertBorderReplicaFromTransfer_SeedsReplica` | Codec helper correctly converts TransferFrame → Replica with correct source attribution |
| Unit | `TestCollectBorderContextFor_SplitGathersSiblingsAndOuterReplicas` | Source-side context collection picks the right entities for a given child's bounds + AoI |
| Integration | `TestPopulateCell_MaterializesContextEntries` | Destination-side correctly dispatches context blobs to upsertBorderReplica |
| End-to-end | `TestSplit_PlayerSeesCompleteFirstFrame_NoChurn` | Full pipeline: a split on a populated cell results in a complete first frame to a watching player |
| Live | Manual via [audit instrumentation](examples/4node-basic/web/src/replicationAudit.ts) | Real-world single-process repro shows `deleted ≈ 0`, `re ≈ 0` |

Existing `pkg/universe/s7_*_test.go` cluster tests continue to validate split/merge/migrate functional correctness; context-seeding doesn't change their semantics, only what's in the destination's grid post-commit.

## 8. Risks & Considerations

### Payload size

For a player with ~200 visible entities, the additional context list adds ~200 × ~32 bytes ≈ 6KB per child cell on a split. In single-process this is in-memory; in distributed it's over gRPC `MeshData`. Acceptable for normal scenes. `BorderContextMaxCount` is the safety valve for pathological cases.

### Source-cell attribution accuracy

The resolver functions in Task 5 must correctly identify each context entity's *destination* source cell. For SPLIT, this is the child-quadrant lookup (positions in known quadrants). For MERGE/MIGRATE, the source replica already has a correct `SourceCellID`, so attribution is preservation.

If attribution is wrong, the destination still renders the entity correctly (the replica state is valid), but subsequent border-frame updates from the *actual* source won't land on the seeded replica — instead they'll create a duplicate. This would manifest as a brief visual flicker and a `netIDIndex` collision flagged by `StrictNetIDIndex` mode. Mitigations:
- The unit tests in Tasks 4-5 pin attribution correctness for each commit kind.
- The `4node-basic` dev config runs with `StrictNetIDIndex: true` per CLAUDE.md, so attribution bugs surface immediately during smoke testing.

### Interaction with auto-rebalance

Auto-rebalance migrations also go through the unified `CellTransfer` codec. Migration context-shipping is symmetrical to split/merge: the source's replicas transfer with their existing `SourceCellID` preserved. No special-casing.

### Distributed-mode bandwidth

In distributed, each SPLIT now ships up to 4× the context payload (one per child). For 200-entity AoIs and 6KB per child, that's ~24KB per split, infrequent. Comparable to other commit-time bookkeeping (topology updates, peer-list broadcasts). No new ratcheted-up cost on normal-tick traffic.

### Re-entry into the existing async border replication

The seeded replicas are valid Replica components with correct `ProducedAtMs`, position, velocity, and source attribution. Once the destination cell ticks and its neighbors start emitting live border frames, those frames arrive at the existing `upsertBorderReplica` code path and find the seeded replicas already in place — the update-existing branch fires (vs the create-new branch). No special handling required for the seam.

### Future: distributed-mode read-side query

If a future feature wants "give me the entities visible from point X at the moment Y" *outside* of the cell-transfer pipeline (e.g. for spectator views or replay), that would need a separate cross-cell query RPC. Out of scope here.

---

## 9. Done When

- [ ] Single-process `cell split` on a populated cell shows `deleted ≈ 0, re ≈ 0` in the audit log.
- [ ] Visual smoke test: no flicker, no rubber-band, no jitter on split / merge / migrate.
- [ ] `go test ./...` and `examples/4node-basic web` test suites are green.
- [ ] All prior workaround code is removed; only the audit instrumentation remains as permanent diagnostic.
