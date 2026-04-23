package universe

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/replication"
)

func TestBorderDispatcher_TickNoCandidatesNoPanic(t *testing.T) {
	d := NewBorderDispatcher(nil, nil)
	d.Tick(0)
}

func TestBorderDispatcher_TickSkipsWithoutNeighbors(t *testing.T) {
	// Even if base is non-nil, an empty neighbor map is a no-op.
	// This test uses a nil base intentionally — the Phase 4 stub
	// must handle nil safely so Phase 5 and 6 integration tests
	// don't have to stand up a full mesh just to exercise tick code.
	d := NewBorderDispatcher(nil, map[string]*CellViewer{})
	d.Tick(42)
}

func TestBorderDispatcher_TickIgnoresNilNeighbors(t *testing.T) {
	// A nil neighbor entry should be skipped, not panic.
	viewers := map[string]*CellViewer{
		"cell_1_0": nil,
	}
	d := NewBorderDispatcher(nil, viewers)
	d.Tick(1)
}

// TestBorderDispatcher_CornerEntityReachesAllNeighbors is a regression
// test for an asymmetric visibility bug observed in the space game: an
// entity near the shared corner of cells (0,0)/(1,0)/(0,1)/(1,1) was
// only reaching the diagonal neighbor, never the two adjacent cardinal
// neighbors.
//
// Root cause: the shared replication.Dispatcher.Walk applies two
// proximity filters in series:
//  1. BorderDispatcher.entityNearNeighborEdge — "is the entity in the
//     AoI-margin strip along the shared edge?" This is correct.
//  2. replication.InsideRadius — "is the entity within tier.Radius of
//     the viewer's *point* position?" The CellViewer's position is the
//     midpoint of the shared edge, and the old default tier radius was
//     1000 units.
//
// For a cell of size 8192 and an entity near the corner at (8177, 8177)
// in cell (0,0), distance-to-edge-midpoint is:
//
//	right cardinal (1,0):   midpoint (8192, 4096) → ~4081
//	down cardinal (0,1):    midpoint (4096, 8192) → ~4081
//	diagonal (1,1):         midpoint (8192, 8192) → ~21
//
// So the 1000-unit disc filter passed only the diagonal, rejecting both
// cardinals even though the entity legitimately belongs in both their
// border strips. The fix extends CellViewer's default tier radius to
// cover the source cell's diagonal so InsideRadius never drops an
// entity that passed entityNearNeighborEdge.
// TestBorderDispatcher_DeltaCompression_UnchangedTailEmitsSentinel
// verifies that when an entity's serialized component tail is identical
// to the tail emitted on the previous tick for the same neighbor, the
// Build closure substitutes a 2-byte unchanged-sentinel (componentCount
// = 0xFFFF) for the full tail. This is the delta compression fast path
// that drops bandwidth for mostly-static entities like parked ships or
// idle NPCs.
//
// The test registers a game component with a deterministic marshaled
// form so the two ticks produce byte-identical tails, then walks the
// dispatcher twice at non-resync ticks and inspects the raw DeltaBuf.
func TestBorderDispatcher_DeltaCompression_UnchangedTailEmitsSentinel(t *testing.T) {
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	// Register a replicated component so the tail has content to dedup.
	world := base.ECSWorld()
	healthMap := ecs.NewMap1[testReplicaComponent](world)
	def := EntityKindDef{Kind: 1, Name: "TestShip"}
	KindComponent(&def, healthMap)
	base.RegisterEntityKind(def)

	// Spawn a corner entity with a non-zero component value.
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)
	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: coords.CellSize - 15, Y: coords.CellSize - 15})
	velMap.Add(ent, &component.Velocity{})
	nidMap.Add(ent, &component.NetworkID{ID: 1, Epoch: 0})
	kindMap.Add(ent, &component.EntityKind{Type: 1})
	colMap.Add(ent, &component.Collider{Radius: 5})
	healthMap.Add(ent, &testReplicaComponent{Health: 100, Shield: 50})

	bd := NewBorderDispatcher(base, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, 1, 1)
	nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, nil, nil, nil)
	nv.SetDirection(1, 1)

	// Tick 1 is a forced resync (any tick % 30 == 0), so advance past it
	// and use a tick pair that does NOT hit a resync: tick 5 → 6.
	//
	// First tick: no baseline yet, expect a full tail (component payload
	// larger than 2 bytes).
	first := bd.disp.Walk(nv, 5, bd.candidatesFor(nv, 5))
	if len(first.Entries) != 1 {
		t.Fatalf("tick 5: expected 1 entry, got %d", len(first.Entries))
	}
	firstTail := first.Entries[0].DeltaBuf[24:]
	firstCount := binary.LittleEndian.Uint16(firstTail[0:2])
	if firstCount == borderTailUnchanged {
		t.Fatal("tick 5: first-ever build should emit a full tail, got sentinel")
	}
	if len(firstTail) <= 2 {
		t.Fatalf("tick 5: expected full tail with component data, got %d bytes", len(firstTail))
	}

	// Second tick: component unchanged, tail bytes identical → sentinel.
	second := bd.disp.Walk(nv, 6, bd.candidatesFor(nv, 6))
	if len(second.Entries) != 1 {
		t.Fatalf("tick 6: expected 1 entry, got %d", len(second.Entries))
	}
	secondTail := second.Entries[0].DeltaBuf[24:]
	if len(secondTail) != 2 {
		t.Fatalf("tick 6: expected 2-byte sentinel tail, got %d bytes: %x", len(secondTail), secondTail)
	}
	secondCount := binary.LittleEndian.Uint16(secondTail[0:2])
	if secondCount != borderTailUnchanged {
		t.Fatalf("tick 6: expected sentinel 0x%X, got 0x%X", borderTailUnchanged, secondCount)
	}
}

// TestBorderDispatcher_DeltaCompression_ForceResync verifies that every
// borderFullResyncInterval ticks the sender re-emits the full tail even
// if the content is unchanged. This is the drop-recovery mechanism:
// without it, a dropped frame on the first update would leave the
// receiver permanently stale. The force-resync window bounds staleness
// at ~1.5 seconds on a dropped frame.
func TestBorderDispatcher_DeltaCompression_ForceResync(t *testing.T) {
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	world := base.ECSWorld()
	healthMap := ecs.NewMap1[testReplicaComponent](world)
	def := EntityKindDef{Kind: 1, Name: "TestShip"}
	KindComponent(&def, healthMap)
	base.RegisterEntityKind(def)

	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)
	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: coords.CellSize - 15, Y: coords.CellSize - 15})
	velMap.Add(ent, &component.Velocity{})
	nidMap.Add(ent, &component.NetworkID{ID: 1, Epoch: 0})
	kindMap.Add(ent, &component.EntityKind{Type: 1})
	colMap.Add(ent, &component.Collider{Radius: 5})
	healthMap.Add(ent, &testReplicaComponent{Health: 100, Shield: 50})

	bd := NewBorderDispatcher(base, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, 1, 1)
	nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, nil, nil, nil)
	nv.SetDirection(1, 1)

	// Prime the baseline at a non-resync tick.
	bd.disp.Walk(nv, 5, bd.candidatesFor(nv, 5))
	// Confirm the intermediate tick emits the sentinel.
	mid := bd.disp.Walk(nv, 6, bd.candidatesFor(nv, 6))
	if binary.LittleEndian.Uint16(mid.Entries[0].DeltaBuf[24:26]) != borderTailUnchanged {
		t.Fatal("tick 6 should have been a sentinel (baseline primed)")
	}
	// Force resync at tick 30 (30 % borderFullResyncInterval == 0).
	if borderFullResyncInterval != 30 {
		t.Fatalf("test assumes borderFullResyncInterval=30, got %d", borderFullResyncInterval)
	}
	resync := bd.disp.Walk(nv, 30, bd.candidatesFor(nv, 30))
	resyncTail := resync.Entries[0].DeltaBuf[24:]
	resyncCount := binary.LittleEndian.Uint16(resyncTail[0:2])
	if resyncCount == borderTailUnchanged {
		t.Fatal("tick 30 should force a full tail resync, got sentinel")
	}
	if len(resyncTail) <= 2 {
		t.Fatalf("resync tick should emit full tail, got %d bytes", len(resyncTail))
	}
}

// TestApplyBorderFrame_UnchangedSentinelNoOps verifies that the receiver
// treats a componentCount = borderTailUnchanged sentinel as a no-op —
// the replica's existing components (from the prior full-tail frame)
// stay in place, and position/velocity continue to update from the
// fixed header. This is the receive-side half of the delta compression
// contract.
func TestApplyBorderFrame_UnchangedSentinelNoOps(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	compMap := ecs.NewMap1[testReplicaComponent](base.ECSWorld())
	def := EntityKindDef{Kind: 3, Name: "Ship"}
	KindComponent(&def, compMap)
	base.RegisterEntityKind(def)

	compID := uint16(1)
	scan := func(h, s float32) []byte {
		stash := base.ECSWorld().NewEntity()
		compMap.Add(stash, &testReplicaComponent{Health: h, Shield: s})
		data := base.ReplicationRegistry().Get(ComponentID(compID)).Scan(stash)
		base.ECSWorld().RemoveEntity(stash)
		return data
	}

	// Frame 1: create the replica with Health=75, Shield=25 (full tail).
	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{{
		NetID: replication.NetID{ID: 900, Epoch: 1},
		Kind:  3,
		DeltaBuf: appendEntryWithComponents(1100, 500, 20, 10, 0, []struct {
			ID   uint16
			Data []byte
		}{
			{ID: compID, Data: scan(75, 25)},
		}),
	}}}, "source_node")

	ent := base.replicaNetIDs[900]
	before := *compMap.Get(ent)

	// Frame 2: new position, unchanged sentinel in the tail. Components
	// must stay at the Frame 1 values.
	sentinelTail := make([]byte, 26)
	binary.LittleEndian.PutUint32(sentinelTail[0:4], math.Float32bits(1200))
	binary.LittleEndian.PutUint32(sentinelTail[4:8], math.Float32bits(500))
	binary.LittleEndian.PutUint32(sentinelTail[8:12], math.Float32bits(20))
	binary.LittleEndian.PutUint16(sentinelTail[12:14], uint16(quantizeVelI16(10, 2000)))
	binary.LittleEndian.PutUint16(sentinelTail[14:16], uint16(quantizeVelI16(0, 2000)))
	binary.LittleEndian.PutUint64(sentinelTail[16:24], 0) // producedAtMs (unset)
	binary.LittleEndian.PutUint16(sentinelTail[24:26], borderTailUnchanged)

	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{{
		NetID:    replication.NetID{ID: 900, Epoch: 2},
		Kind:     3,
		DeltaBuf: sentinelTail,
	}}}, "source_node")

	after := *compMap.Get(ent)
	if before.Health != after.Health || before.Shield != after.Shield {
		t.Fatalf("sentinel frame must not mutate components: before=%+v after=%+v", before, after)
	}
	// Position must have been updated from the fixed header.
	posMap := ecs.NewMap1[component.Position](base.ECSWorld())
	pos := posMap.Get(ent)
	if pos.X != 176 { // 1200 - 1024 = 176 (cell 1_0, cellSize 1024 from newTestWorldBase)
		t.Fatalf("sentinel frame should still update position, got X=%.1f want 176", pos.X)
	}
}

func TestBorderDispatcher_CornerEntityReachesAllNeighbors(t *testing.T) {
	// Use the production cell size so the bug is reproducible. The
	// 1000-unit default tier radius only fails to reach cardinal
	// neighbors when the edge is long enough that the corner sits
	// outside a 1000-radius disc around the edge midpoint — which
	// requires cellSize > ~2000.
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024) // restore the default other tests expect

	world := base.ECSWorld()
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)

	// Asteroid sits 15 units inside the (+X, +Y) corner of cell (0,0).
	// With AoI margin 100 this passes nearRight AND nearTop, making it
	// a candidate for all three neighbors: (1,0), (0,1), and (1,1).
	cs := coords.CellSize
	corner := cs - 15
	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: corner, Y: corner})
	velMap.Add(ent, &component.Velocity{})
	nidMap.Add(ent, &component.NetworkID{ID: 1, Epoch: 0})
	kindMap.Add(ent, &component.EntityKind{Type: 1})
	colMap.Add(ent, &component.Collider{Radius: 5})

	bd := NewBorderDispatcher(base, nil)

	cases := []struct {
		name   string
		dx, dy int32
	}{
		{"right cardinal", 1, 0},
		{"down cardinal", 0, 1},
		{"diagonal", 1, 1},
	}
	for _, tc := range cases {
		bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, tc.dx, tc.dy)
		nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, nil, nil, nil)
		nv.SetDirection(tc.dx, tc.dy)

		// Drive Walk directly so we can inspect the produced frame
		// without needing a real destination node for CellViewer.Send.
		cands := bd.candidatesFor(nv, 1)
		frame := bd.disp.Walk(nv, 1, cands)

		if len(frame.Entries) == 0 {
			t.Errorf("%s neighbor: BorderDispatcher dropped the corner entity that should be visible to it", tc.name)
			continue
		}
		if got := frame.Entries[0].NetID.ID; got != 1 {
			t.Errorf("%s neighbor: unexpected netID in frame: got %d, want 1", tc.name, got)
		}
	}
}
