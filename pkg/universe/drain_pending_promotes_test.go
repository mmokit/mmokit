package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/replication"
)

// newMinimalCell creates a minimal Cell for use in drainPendingPromotes tests.
// It wires Stage and the engine Log; other fields are left at zero.
func newMinimalCell(t *testing.T, cell CellID) *Cell {
	t.Helper()
	base := newTestWorldBase(t, cell)
	c := NewCell(cell.MeshID(), cell)
	c.Stage = base
	c.Engine = base.eng
	c.Log = logger.New()
	return c
}

// worldXOf computes the world-X for an entity: pos.X + cellCoord.CellX * cellSize.
// Used to assert correct absolute position after a handoff commit.
func worldXOf(t *testing.T, base *Stage, ent ecs.Entity, cellSize float32) float32 {
	t.Helper()
	posMap := ecs.NewMap1[component.Position](base.ECSWorld())
	ccMap := ecs.NewMap1[component.CellCoord](base.ECSWorld())
	if !posMap.HasAll(ent) {
		t.Fatal("entity missing Position")
	}
	pos := posMap.Get(ent)
	var cellX int32
	if ccMap.HasAll(ent) {
		cellX = ccMap.Get(ent).CellX
	}
	return pos.X + float32(cellX)*cellSize
}

// TestDrainPendingPromotes_BorderReplicaTip_WorldXCorrect is the regression
// test for the cross-cell position bug:
//
// Player in cell_0_0 clicks at worldX=1500, walks right and crosses the
// east boundary (worldX>1024). The border-dispatcher has been pushing the
// entity's position to cell_1_0 as a replica each tick. At the hard-cut
// commit, drainPendingPromotes must:
//
//  1. Capture the replica's tip-of-motion (pos.X = localX on cell_1_0,
//     CellX = 1) — these are the LAST border-frame values received before
//     the commit.
//  2. Remove the replica.
//  3. Spawn from the transfer blob (normalized: PosX=6, CellX=1).
//  4. Overwrite blob values with the captured tip-of-motion.
//
// The resulting entity must have worldX ≈ lastBorderFrameWorldX (< 2*cellSize)
// NOT worldX ≈ 1900 (far right of cell_1_0 or beyond).
//
// The root-cause bug: if component.Position is registered in the
// ReplicationRegistry (e.g. via RegisterKind[PlayerComponents] when the
// bundle includes a Position field), scanEntityComponents on the source
// includes the raw source-local position (X=1030) in the border-frame
// component tail. On the receiver, applyEntityComponents overwrites the
// correctly-computed localX (6) with 1030. drainPendingPromotes then
// captures recentPosX=1030 and spawns the live entity with that wrong value
// → worldX=1030+1024=2054, player walks left toward the original click.
//
// Fix: scanEntityComponents must skip IsTransferCore components (Position,
// Velocity, Rotation, CellCoord) since their correct border values are
// already carried by the fixed DeltaBuf fields.
func TestDrainPendingPromotes_BorderReplicaTip_WorldXCorrect(t *testing.T) {
	const cellSize = float32(1024)
	coords.SetCellSize(cellSize)

	// --- Source side (cell_0_0): build the transfer blob ---
	// Register Position in the src registry to exercise the scanEntityComponents
	// bug path (border frame component tail includes raw source position).
	src := newTestWorldBase(t, CellID{X: 0, Y: 0})
	srcPosMap := ecs.NewMap1[component.Position](src.ECSWorld())
	RegisterComponent(src.ReplicationRegistry(), srcPosMap)

	rec := &handoffRecordingBridge{}
	hd := newAutoAcceptHandoffDriver(src, rec)

	// Source entity at pos.X=1030 (6px past east boundary), velX=200.
	const srcPosX = float32(1030)
	const velX = float32(200)
	ent := src.Spawn(
		component.Position{X: srcPosX, Y: 500},
		component.Velocity{X: velX, Y: 0},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()
	netID := src.NetworkIDMap().Get(ent).ID
	src.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})
	hd.Tick(10) // currentClusterTick=10 → commitTick=12

	if len(rec.handoffs) != 1 {
		t.Fatalf("expected 1 handoff, got %d", len(rec.handoffs))
	}
	blob := rec.handoffs[0].TransferBlob

	// Verify the blob has normalized coords (PosX=6, CellX=1).
	frame, err := UnmarshalTransferFrame(blob)
	if err != nil {
		t.Fatalf("UnmarshalTransferFrame: %v", err)
	}
	const wantNormPosX = srcPosX - cellSize
	if frame.PosX != wantNormPosX {
		t.Errorf("blob PosX = %.1f, want %.1f", frame.PosX, wantNormPosX)
	}
	if frame.CellX != 1 {
		t.Errorf("blob CellX = %d, want 1", frame.CellX)
	}

	// --- Destination side (cell_1_0): pre-populate a border replica ---
	// Also register Position in dst registry — this is what triggers the
	// apply-side overwrite of localX with the raw source value.
	dst := newMinimalCell(t, CellID{X: 1, Y: 0})
	dstPosMap := ecs.NewMap1[component.Position](dst.Stage.ECSWorld())
	RegisterComponent(dst.Stage.ReplicationRegistry(), dstPosMap)

	// Simulate the last border frame pushed by src before the commit:
	// source entity at srcPosX → worldX=1030, localX=1030-1024=6, velX=200.
	// producedAtMs=0 so hasRecentStamp=false and the extrapolation branch in
	// drainPendingPromotes is skipped — we are testing position correctness
	// from the tip capture, not extrapolation.
	const borderWorldX = srcPosX                 // 1030.0
	const borderLocalX = borderWorldX - cellSize // 6.0
	// Build a border entry that exercises the scanEntityComponents bug: the
	// source's BorderDispatcher would append Position.X=1030 in the component
	// tail. We manually build that tail here.
	baseBorderEntry := buildWireEntryAt(borderWorldX, 500, 5, velX, 0, 0)
	// The entry from buildWireEntryAt ends with [0, 0] (count=0 component tail).
	// Replace with a real component tail carrying Position.X=1030 (raw source value).
	rawEntry := baseBorderEntry[:len(baseBorderEntry)-2] // strip count
	posData := mustMarshal(t, &component.Position{X: srcPosX, Y: 500})
	// Find the Position component's registry ID on the src registry.
	var posID ComponentID
	for _, rep := range src.ReplicationRegistry().All() {
		posID = rep.ID // only one component registered (Position)
		break
	}
	rawEntry = append(rawEntry, byte(1), byte(0)) // count=1
	rawEntry = append(rawEntry, byte(posID), byte(posID>>8))
	rawEntry = append(rawEntry, byte(len(posData)), byte(len(posData)>>8))
	rawEntry = append(rawEntry, posData...)

	borderFrame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: netID, Epoch: 1},
				Kind:     1,
				DeltaBuf: rawEntry,
			},
		},
	}
	dst.Stage.ApplyBorderFrame(borderFrame, "cell_0_0")

	// Verify the replica was created with correct pos.X and CellCoord.
	replicaEnt, ok := dst.Stage.replicaNetIDs[netID]
	if !ok {
		t.Fatal("border replica not created on dst")
	}
	repPosMap := ecs.NewMap1[component.Position](dst.Stage.ECSWorld())
	repPos := repPosMap.Get(replicaEnt)
	repCCMap := ecs.NewMap1[component.CellCoord](dst.Stage.ECSWorld())
	repCC := repCCMap.Get(replicaEnt)

	t.Logf("replica: pos.X=%.1f, CellCoord.CellX=%d → worldX=%.1f",
		repPos.X, repCC.CellX, repPos.X+float32(repCC.CellX)*cellSize)

	if repPos.X != borderLocalX {
		t.Errorf("replica pos.X = %.1f, want %.1f (localX = worldX - recvCellOriginX)",
			repPos.X, borderLocalX)
	}
	if repCC.CellX != 1 {
		t.Errorf("replica CellCoord.CellX = %d, want 1", repCC.CellX)
	}

	// --- Queue the pendingPromote on dst and drain at commitTick=12 ---
	dst.pendingPromotes = map[uint64][]pendingPromote{
		12: {{
			netID:        netID,
			epoch:        rec.handoffs[0].Epoch,
			transferBlob: blob,
		}},
	}

	// drainPendingPromotes at commitTick=12.
	dst.drainPendingPromotes(12)

	// After drain, the replica must be gone and a live entity must be present.
	if _, ok := dst.Stage.replicaNetIDs[netID]; ok {
		t.Error("replica still present after drainPendingPromotes commit")
	}

	liveEnt, pres, ok := dst.Stage.LookupNetID(netID)
	if !ok {
		t.Fatal("netID not found in dst after drain")
	}
	if pres != PresenceLive {
		t.Fatalf("entity presence = %v, want PresenceLive", pres)
	}

	// Check final pos and CellCoord separately for diagnosis.
	finalPos := repPosMap.Get(liveEnt)
	finalCC := repCCMap.Get(liveEnt)
	finalWorldX := finalPos.X + float32(finalCC.CellX)*cellSize

	t.Logf("after commit: pos.X=%.1f, CellCoord.CellX=%d → worldX=%.1f",
		finalPos.X, finalCC.CellX, finalWorldX)

	// The entity must be within cell_1_0 world bounds [1024, 2048).
	const wantMin = cellSize     // 1024
	const wantMax = 2 * cellSize // 2048

	if finalWorldX < wantMin || finalWorldX >= wantMax {
		t.Errorf("worldX = %.1f is outside cell_1_0 bounds [%.0f, %.0f)"+
			" — pos.X=%.1f, CellCoord.CellX=%d",
			finalWorldX, wantMin, wantMax, finalPos.X, finalCC.CellX)
	}

	// More specific: the entity should be near the LEFT edge of cell_1_0
	// (just inside from the west boundary), not the far right.
	// Allow up to 2 ticks of extrapolation = 2 * velX * (50ms) = 20px.
	const extrapolationMargin = float32(100) // generous margin for clock drift
	if finalWorldX >= wantMin+extrapolationMargin+cellSize/4 {
		t.Errorf("worldX = %.1f appears too far right of cell_1_0; expected near left edge (worldX < %.1f)",
			finalWorldX, wantMin+extrapolationMargin+cellSize/4)
	}
}

// TestDrainPendingPromotes_DeStalesColliderRadius is the regression test for
// the "size jumps at the cell line" bug. Collider.Radius is animated every tick
// by the WASM pulse mod. The transfer blob captures the radius at CROSSING time
// (commitTick − HandoffLeadTicks); the border-replica is refreshed every tick
// right up to the commit. drainPendingPromotes must serve the FRESH replica
// radius on promote, not the stale blob value — otherwise the player's first
// post-handoff frame carries a 2-tick-old size, then snaps forward (the visible
// dip-and-jump). Generalizes the existing motion-only de-stale to Collider.
func TestDrainPendingPromotes_DeStalesColliderRadius(t *testing.T) {
	const cellSize = float32(1024)
	coords.SetCellSize(cellSize)

	// Source: blob captures the STALE radius at crossing time.
	src := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := newAutoAcceptHandoffDriver(src, rec)

	const staleRadius = float32(5)
	ent := src.Spawn(
		component.Position{X: 1030, Y: 500},
		component.Velocity{X: 0, Y: 0},
		component.EntityKind{Type: 1},
		component.Collider{Radius: staleRadius},
	).Handle()
	netID := src.NetworkIDMap().Get(ent).ID
	src.QueueCrossing(CrossingEvent{Entity: ent, NetID: netID, DestCellID: "cell_1_0"})
	hd.Tick(10) // commitTick=12
	blob := rec.handoffs[0].TransferBlob

	// Destination: border-replica refreshed with a FRESHER radius — the entity
	// breathed between crossing and commit.
	dst := newMinimalCell(t, CellID{X: 1, Y: 0})
	const freshRadius = float32(42)
	dst.Stage.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{{
		NetID:    replication.NetID{ID: netID, Epoch: 1},
		Kind:     1,
		DeltaBuf: buildWireEntry(1030, 500, freshRadius, 0, 0),
	}}}, "cell_0_0")

	colMap := ecs.NewMap1[component.Collider](dst.Stage.ECSWorld())
	if r := colMap.Get(dst.Stage.replicaNetIDs[netID]).Radius; r != freshRadius {
		t.Fatalf("precondition: replica radius=%.1f want %.1f", r, freshRadius)
	}

	dst.pendingPromotes = map[uint64][]pendingPromote{
		12: {{netID: netID, epoch: rec.handoffs[0].Epoch, transferBlob: blob}},
	}
	dst.drainPendingPromotes(12)

	liveEnt, pres, ok := dst.Stage.LookupNetID(netID)
	if !ok || pres != PresenceLive {
		t.Fatalf("entity not live after drain (ok=%v pres=%v)", ok, pres)
	}
	if r := colMap.Get(liveEnt).Radius; r != freshRadius {
		t.Fatalf("promoted Collider.Radius=%.1f, want %.1f — served the stale blob "+
			"value instead of the fresh border-replica value", r, freshRadius)
	}
}

// TestDrainPendingPromotes_NoReplica_WorldXCorrect verifies the fast-mover
// case: no border replica exists at commit time, so the entity spawns purely
// from the transfer blob. The blob has normalized coords (PosX=6, CellX=1),
// so worldX must be 6+1024=1030 — NOT the raw source value 1030 which would
// give worldX=1030+1024=2054 (wrong; placed in cell_2_0 coordinates).
//
// This is the case that bd37aa4 was intended to fix. The test here confirms
// the fix holds for the SpawnFromTransferCore-only path.
func TestDrainPendingPromotes_NoReplica_WorldXCorrect(t *testing.T) {
	const cellSize = float32(1024)
	coords.SetCellSize(cellSize)

	src := newTestWorldBase(t, CellID{X: 0, Y: 0})
	rec := &handoffRecordingBridge{}
	hd := newAutoAcceptHandoffDriver(src, rec)

	const srcPosX = float32(1030)
	ent := src.Spawn(
		component.Position{X: srcPosX, Y: 500},
		component.Velocity{X: 200, Y: 0},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()
	netID := src.NetworkIDMap().Get(ent).ID
	src.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})
	hd.Tick(10)

	if len(rec.handoffs) != 1 {
		t.Fatalf("expected 1 handoff, got %d", len(rec.handoffs))
	}
	blob := rec.handoffs[0].TransferBlob

	// Destination: NO border replica. Pure fast-mover case.
	dst := newMinimalCell(t, CellID{X: 1, Y: 0})

	dst.pendingPromotes = map[uint64][]pendingPromote{
		12: {{
			netID:        netID,
			epoch:        rec.handoffs[0].Epoch,
			transferBlob: blob,
		}},
	}
	dst.drainPendingPromotes(12)

	liveEnt, pres, ok := dst.Stage.LookupNetID(netID)
	if !ok {
		t.Fatal("netID not found in dst after drain")
	}
	if pres != PresenceLive {
		t.Fatalf("entity presence = %v, want PresenceLive", pres)
	}

	posMap := ecs.NewMap1[component.Position](dst.Stage.ECSWorld())
	ccMap := ecs.NewMap1[component.CellCoord](dst.Stage.ECSWorld())

	finalPos := posMap.Get(liveEnt)
	finalCC := ccMap.Get(liveEnt)
	finalWorldX := finalPos.X + float32(finalCC.CellX)*cellSize

	t.Logf("no-replica fast-mover: pos.X=%.1f, CellCoord.CellX=%d → worldX=%.1f",
		finalPos.X, finalCC.CellX, finalWorldX)

	// Must be in cell_1_0 [1024, 2048).
	if finalWorldX < cellSize || finalWorldX >= 2*cellSize {
		t.Errorf("worldX = %.1f outside cell_1_0 [%.0f, %.0f); pos.X=%.1f CellX=%d",
			finalWorldX, cellSize, 2*cellSize, finalPos.X, finalCC.CellX)
	}
}

// TestDrainPendingPromotes_WithMoveTargetInRegistry_WorldXCorrect tests the
// scenario where MoveTarget (which contains CellX/LocalX fields) is registered
// in the ReplicationRegistry. This exercises the case where the player clicked
// at worldX=1500 (MoveTarget.CellX=1, MT.LocalX=476) and the component is
// serialized in the transfer blob.
//
// After transfer to cell_1_0 (CellX=1), ClickToMove must compute correct
// direction: dx = (MT.CellX - CC.CellX)*cellSize + MT.LocalX - pos.X
//
//	= (1 - 1)*1024 + 476 - posX = 476 - posX > 0 (walk right)
//
// If CC.CellX were incorrectly 2, dx = (1-2)*1024 + 476 - posX = -548 - posX < 0
// (walk LEFT) — matching the user-reported symptom.
func TestDrainPendingPromotes_WithMoveTargetInRegistry_WorldXCorrect(t *testing.T) {
	const cellSize = float32(1024)
	coords.SetCellSize(cellSize)

	// Source with MoveTarget in the registry.
	src := newTestWorldBase(t, CellID{X: 0, Y: 0})
	mtMap := ecs.NewMap1[component.MoveTarget](src.ECSWorld())
	RegisterComponent(src.ReplicationRegistry(), mtMap)

	rec := &handoffRecordingBridge{}
	hd := newAutoAcceptHandoffDriver(src, rec)

	const srcPosX = float32(1030)
	const velX = float32(200)
	ent := src.Spawn(
		component.Position{X: srcPosX, Y: 500},
		component.Velocity{X: velX, Y: 0},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()
	// Set MoveTarget to simulate clicking at worldX=1500.
	// MT.CellX = floor(1500/1024) = 1, MT.LocalX = 1500 - 1024 = 476.
	mtMap.Add(ent, &component.MoveTarget{CellX: 1, LocalX: 476, Active: true})

	netID := src.NetworkIDMap().Get(ent).ID
	src.QueueCrossing(CrossingEvent{
		Entity: ent, NetID: netID, DestCellID: "cell_1_0",
	})
	hd.Tick(10)

	if len(rec.handoffs) != 1 {
		t.Fatalf("expected 1 handoff, got %d", len(rec.handoffs))
	}
	blob := rec.handoffs[0].TransferBlob

	// Destination with border replica and MoveTarget registry.
	dst := newMinimalCell(t, CellID{X: 1, Y: 0})
	dstMTMap := ecs.NewMap1[component.MoveTarget](dst.Stage.ECSWorld())
	RegisterComponent(dst.Stage.ReplicationRegistry(), dstMTMap)

	// Pre-populate border replica.
	borderEntry := buildWireEntry(srcPosX, 500, 5, velX, 0)
	borderFrame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: netID, Epoch: 1},
				Kind:     1,
				DeltaBuf: borderEntry,
			},
		},
	}
	dst.Stage.ApplyBorderFrame(borderFrame, "cell_0_0")

	dst.pendingPromotes = map[uint64][]pendingPromote{
		12: {{
			netID:        netID,
			epoch:        rec.handoffs[0].Epoch,
			transferBlob: blob,
		}},
	}
	dst.drainPendingPromotes(12)

	liveEnt, pres, ok := dst.Stage.LookupNetID(netID)
	if !ok {
		t.Fatal("netID not found after drain")
	}
	if pres != PresenceLive {
		t.Fatalf("presence = %v, want PresenceLive", pres)
	}

	posMap := ecs.NewMap1[component.Position](dst.Stage.ECSWorld())
	ccMap := ecs.NewMap1[component.CellCoord](dst.Stage.ECSWorld())
	finalPos := posMap.Get(liveEnt)
	finalCC := ccMap.Get(liveEnt)
	finalWorldX := finalPos.X + float32(finalCC.CellX)*cellSize

	t.Logf("with MoveTarget: pos.X=%.1f, CellCoord.CellX=%d → worldX=%.1f",
		finalPos.X, finalCC.CellX, finalWorldX)

	// CellCoord.CellX must be 1 (cell_1_0), not 2.
	if finalCC.CellX != 1 {
		t.Errorf("CellCoord.CellX = %d, want 1; worldX would be %.1f instead of %.1f",
			finalCC.CellX, finalWorldX, finalPos.X+cellSize)
	}

	// worldX must be in cell_1_0.
	if finalWorldX < cellSize || finalWorldX >= 2*cellSize {
		t.Errorf("worldX = %.1f outside cell_1_0 [%.0f, %.0f)", finalWorldX, cellSize, 2*cellSize)
	}

	// MoveTarget must have been deserialized correctly: CellX=1, LocalX=476.
	if !dstMTMap.HasAll(liveEnt) {
		t.Error("MoveTarget not present on transferred entity")
	} else {
		mt := dstMTMap.Get(liveEnt)
		if mt.CellX != 1 {
			t.Errorf("MoveTarget.CellX = %d, want 1", mt.CellX)
		}
		if mt.LocalX != 476 {
			t.Errorf("MoveTarget.LocalX = %.1f, want 476", mt.LocalX)
		}

		// Simulate ClickToMove.dx: (MT.CellX - CC.CellX)*cellSize + MT.LocalX - pos.X
		dx := float32(mt.CellX-finalCC.CellX)*cellSize + mt.LocalX - finalPos.X
		t.Logf("ClickToMove dx = %.1f (positive = walk right ✓, negative = bug)", dx)
		if dx <= 0 {
			t.Errorf("ClickToMove dx = %.1f, want > 0 (player should walk RIGHT toward target, not left)",
				dx)
		}
	}
}
