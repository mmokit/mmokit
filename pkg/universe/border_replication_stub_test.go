package universe

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/replication"
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

// TestBorderDispatcher_WireMembershipMatchesBaseline verifies both sides of
// the authoritative interest-set contract: a configured update divisor cannot
// suppress membership entries, while an entity rejected by the dispatcher's
// radius gate is removed from hysteresis and loses its baseline.
func TestBorderDispatcher_WireMembershipMatchesBaseline(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()
	posMap := ecs.NewMap1[component.Position](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)

	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: coords.CellSize - 15, Y: coords.CellSize - 15})
	nidMap.Add(ent, &component.NetworkID{ID: 41, Epoch: 2})
	kindMap.Add(ent, &component.EntityKind{Type: 7})
	colMap.Add(ent, &component.Collider{Radius: 5})

	tiers := map[uint16]replication.ReplicationTier{
		7: {Radius: coords.CellSize * 2, UpdateDivisor: 5, BaseWeight: 2},
	}
	bd := NewBorderDispatcher(base, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, 1, 1)
	nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, tiers, nil, nil)
	nv.SetDirection(1, 1)

	// Tick 1 would be skipped by a generic UpdateDivisor=5 viewer. Border
	// viewers must still emit it because Entries are the complete set.
	divisorTick := bd.disp.Walk(nv, 1, bd.candidatesFor(nv, 1))
	if len(divisorTick.Entries) != 1 {
		t.Fatalf("divisor tick entry count = %d, want 1 authoritative membership entry", len(divisorTick.Entries))
	}
	nv.SwapInSet()
	if !nv.WasInSet(41) || nv.Baselines().Baseline(41) == nil {
		t.Fatal("transmitted entity missing from retained membership/baseline")
	}

	// Tighten the custom tier radius so Dispatcher rejects the candidate.
	// Because Build never runs, this tick's transmitted set is empty and
	// SwapInSet must discard both membership and baseline.
	tiers[7] = replication.ReplicationTier{Radius: 1, UpdateDivisor: 5, BaseWeight: 2}
	radiusSkipped := bd.disp.Walk(nv, 2, bd.candidatesFor(nv, 2))
	if len(radiusSkipped.Entries) != 0 {
		t.Fatalf("radius-skipped entry count = %d, want 0", len(radiusSkipped.Entries))
	}
	nv.SwapInSet()
	if nv.WasInSet(41) {
		t.Fatal("dispatcher-rejected entity remained in transmitted membership")
	}
	if got := nv.Baselines().Baseline(41); got != nil {
		t.Fatal("dispatcher-rejected entity retained a component baseline")
	}

	// Restore visibility. Re-entry must be present even on another divisor-
	// skipped tick and must carry a full tail against the dropped baseline.
	tiers[7] = replication.ReplicationTier{Radius: coords.CellSize * 2, UpdateDivisor: 5, BaseWeight: 2}
	reentered := bd.disp.Walk(nv, 3, bd.candidatesFor(nv, 3))
	if len(reentered.Entries) != 1 {
		t.Fatalf("re-entry count = %d, want 1", len(reentered.Entries))
	}
	if got := binary.LittleEndian.Uint16(reentered.Entries[0].DeltaBuf[26:28]); got == borderTailUnchanged {
		t.Fatal("re-entry after dispatcher skip emitted unchanged sentinel")
	}
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
// form, consumes the bounded new-baseline full-tail window, then verifies
// the next byte-identical tail uses the sentinel.
func TestBorderDispatcher_DeltaCompression_UnchangedTailEmitsSentinel(t *testing.T) {
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	// Register a replicated component so the tail has content to dedup.
	world := base.ECSWorld()
	healthMap := ecs.NewMap1[testReplicaComponent](world)
	def := EntityKindDef{Kind: 1, Name: "TestShip"}
	KindComponentByID(&def, world, ecs.ComponentID[testReplicaComponent](world), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
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

	// First tick: no baseline yet, so expect a full tail (component payload
	// larger than 2 bytes).
	first := bd.disp.Walk(nv, 5, bd.candidatesFor(nv, 5))
	if len(first.Entries) != 1 {
		t.Fatalf("tick 5: expected 1 entry, got %d", len(first.Entries))
	}
	firstTail := first.Entries[0].DeltaBuf[26:]
	firstCount := binary.LittleEndian.Uint16(firstTail[0:2])
	if firstCount == borderTailUnchanged {
		t.Fatal("tick 5: first-ever build should emit a full tail, got sentinel")
	}
	if len(firstTail) <= 2 {
		t.Fatalf("tick 5: expected full tail with component data, got %d bytes", len(firstTail))
	}

	// A new baseline repeats its full tail for a short bounded window so
	// one lossy send cannot strand a fresh receiver. Consume the remaining
	// repetitions, then the next identical tail may use the sentinel.
	for tick := uint64(6); tick < 5+uint64(borderLifecycleFullTailFrames); tick++ {
		repeated := bd.disp.Walk(nv, tick, bd.candidatesFor(nv, tick))
		if got := binary.LittleEndian.Uint16(repeated.Entries[0].DeltaBuf[26:28]); got == borderTailUnchanged {
			t.Fatalf("tick %d: lifecycle full-tail window emitted sentinel", tick)
		}
	}
	sentinelTick := uint64(5) + uint64(borderLifecycleFullTailFrames)
	second := bd.disp.Walk(nv, sentinelTick, bd.candidatesFor(nv, sentinelTick))
	if len(second.Entries) != 1 {
		t.Fatalf("tick %d: expected 1 entry, got %d", sentinelTick, len(second.Entries))
	}
	secondTail := second.Entries[0].DeltaBuf[26:]
	if len(secondTail) != 2 {
		t.Fatalf("tick %d: expected 2-byte sentinel tail, got %d bytes: %x", sentinelTick, len(secondTail), secondTail)
	}
	secondCount := binary.LittleEndian.Uint16(secondTail[0:2])
	if secondCount != borderTailUnchanged {
		t.Fatalf("tick %d: expected sentinel 0x%X, got 0x%X", sentinelTick, borderTailUnchanged, secondCount)
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
	KindComponentByID(&def, world, ecs.ComponentID[testReplicaComponent](world), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
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

	// Prime the baseline and consume its bounded full-tail window.
	for tick := uint64(5); tick < 5+uint64(borderLifecycleFullTailFrames); tick++ {
		bd.disp.Walk(nv, tick, bd.candidatesFor(nv, tick))
	}
	// Confirm the intermediate tick emits the sentinel.
	midTick := uint64(5) + uint64(borderLifecycleFullTailFrames)
	mid := bd.disp.Walk(nv, midTick, bd.candidatesFor(nv, midTick))
	if binary.LittleEndian.Uint16(mid.Entries[0].DeltaBuf[26:28]) != borderTailUnchanged {
		t.Fatalf("tick %d should have been a sentinel (baseline primed)", midTick)
	}
	// Force resync at tick 30 (30 % borderFullResyncInterval == 0).
	if borderFullResyncInterval != 30 {
		t.Fatalf("test assumes borderFullResyncInterval=30, got %d", borderFullResyncInterval)
	}
	resync := bd.disp.Walk(nv, 30, bd.candidatesFor(nv, 30))
	resyncTail := resync.Entries[0].DeltaBuf[26:]
	resyncCount := binary.LittleEndian.Uint16(resyncTail[0:2])
	if resyncCount == borderTailUnchanged {
		t.Fatal("tick 30 should force a full tail resync, got sentinel")
	}
	if len(resyncTail) <= 2 {
		t.Fatalf("resync tick should emit full tail, got %d bytes", len(resyncTail))
	}
}

// TestBorderDispatcher_DeltaCompression_ReentryEmitsFullTail verifies that
// leaving a neighbor's authoritative interest set invalidates the sender-side
// component baseline. The receiver removes the replica when it observes the
// empty frame, so an unchanged-sentinel on re-entry would recreate the entity
// with zero-valued game components until the next periodic full resync.
func TestBorderDispatcher_DeltaCompression_ReentryEmitsFullTail(t *testing.T) {
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	world := base.ECSWorld()
	healthMap := ecs.NewMap1[testReplicaComponent](world)
	def := EntityKindDef{Kind: 1, Name: "TestShip"}
	KindComponentByID(&def, world, ecs.ComponentID[testReplicaComponent](world), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
	base.RegisterEntityKind(def)

	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)
	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: coords.CellSize - 15, Y: coords.CellSize - 15})
	velMap.Add(ent, &component.Velocity{})
	nidMap.Add(ent, &component.NetworkID{ID: 1, Epoch: 1})
	kindMap.Add(ent, &component.EntityKind{Type: 1})
	colMap.Add(ent, &component.Collider{Radius: 5})
	healthMap.Add(ent, &testReplicaComponent{Health: 100, Shield: 50})

	bd := NewBorderDispatcher(base, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, 1, 1)
	nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, nil, nil, nil)
	nv.SetDirection(1, 1)

	first := bd.disp.Walk(nv, 5, bd.candidatesFor(nv, 5))
	if len(first.Entries) != 1 {
		t.Fatalf("initial entry count = %d, want 1", len(first.Entries))
	}
	nv.SwapInSet()

	for tick := uint64(6); tick < 5+uint64(borderLifecycleFullTailFrames); tick++ {
		bd.disp.Walk(nv, tick, bd.candidatesFor(nv, tick))
		nv.SwapInSet()
	}
	unchangedTick := uint64(5) + uint64(borderLifecycleFullTailFrames)
	unchanged := bd.disp.Walk(nv, unchangedTick, bd.candidatesFor(nv, unchangedTick))
	if got := binary.LittleEndian.Uint16(unchanged.Entries[0].DeltaBuf[26:28]); got != borderTailUnchanged {
		t.Fatalf("unchanged tail count = %#x, want sentinel %#x", got, borderTailUnchanged)
	}
	nv.SwapInSet()

	// Leave both edge margins. Rotating this empty interest set must also
	// discard the baseline that described the now-destroyed remote replica.
	pos := posMap.Get(ent)
	pos.X = coords.CellSize / 2
	pos.Y = coords.CellSize / 2
	leftTick := unchangedTick + 1
	left := bd.disp.Walk(nv, leftTick, bd.candidatesFor(nv, leftTick))
	if len(left.Entries) != 0 {
		t.Fatalf("leave entry count = %d, want 0", len(left.Entries))
	}
	nv.SwapInSet()
	if got := nv.Baselines().Baseline(1); got != nil {
		t.Fatal("baseline survived interest-set exit")
	}

	// Re-enter with byte-identical components. This must be a full tail,
	// because the receiver no longer has state for an unchanged sentinel.
	pos.X = coords.CellSize - 15
	pos.Y = coords.CellSize - 15
	reentryTick := leftTick + 1
	reentered := bd.disp.Walk(nv, reentryTick, bd.candidatesFor(nv, reentryTick))
	if len(reentered.Entries) != 1 {
		t.Fatalf("re-entry count = %d, want 1", len(reentered.Entries))
	}
	tail := reentered.Entries[0].DeltaBuf[26:]
	if got := binary.LittleEndian.Uint16(tail[:2]); got == borderTailUnchanged {
		t.Fatal("re-entry emitted unchanged sentinel instead of full component tail")
	}
	if len(tail) <= 2 {
		t.Fatalf("re-entry tail length = %d, want component data", len(tail))
	}
	nv.SwapInSet()
	// Treat the first re-entry frame as lost. The following attempts must
	// remain full for the rest of the bounded lifecycle window.
	for attempt := uint8(1); attempt < borderLifecycleFullTailFrames; attempt++ {
		tick := reentryTick + uint64(attempt)
		retry := bd.disp.Walk(nv, tick, bd.candidatesFor(nv, tick))
		if got := binary.LittleEndian.Uint16(retry.Entries[0].DeltaBuf[26:28]); got == borderTailUnchanged {
			t.Fatalf("re-entry retry %d emitted sentinel after a potentially lost full tail", attempt)
		}
		nv.SwapInSet()
	}
	afterWindowTick := reentryTick + uint64(borderLifecycleFullTailFrames)
	afterWindow := bd.disp.Walk(nv, afterWindowTick, bd.candidatesFor(nv, afterWindowTick))
	if got := binary.LittleEndian.Uint16(afterWindow.Entries[0].DeltaBuf[26:28]); got != borderTailUnchanged {
		t.Fatalf("post-reentry window tail count = %#x, want sentinel %#x", got, borderTailUnchanged)
	}
}

// TestBorderDispatcher_DeltaCompression_EpochChangeEmitsFullTail verifies
// that a reused netID under a new authority epoch cannot inherit the prior
// lifecycle's component baseline.
func TestBorderDispatcher_DeltaCompression_EpochChangeEmitsFullTail(t *testing.T) {
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	world := base.ECSWorld()
	healthMap := ecs.NewMap1[testReplicaComponent](world)
	def := EntityKindDef{Kind: 1, Name: "TestShip"}
	KindComponentByID(&def, world, ecs.ComponentID[testReplicaComponent](world), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
	base.RegisterEntityKind(def)

	posMap := ecs.NewMap1[component.Position](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)
	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: coords.CellSize - 15, Y: coords.CellSize - 15})
	nidMap.Add(ent, &component.NetworkID{ID: 1, Epoch: 4})
	kindMap.Add(ent, &component.EntityKind{Type: 1})
	colMap.Add(ent, &component.Collider{Radius: 5})
	healthMap.Add(ent, &testReplicaComponent{Health: 100, Shield: 50})

	bd := NewBorderDispatcher(base, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, 1, 1)
	nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, nil, nil, nil)
	nv.SetDirection(1, 1)

	first := bd.disp.Walk(nv, 5, bd.candidatesFor(nv, 5))
	if len(first.Entries) != 1 {
		t.Fatalf("initial entry count = %d, want 1", len(first.Entries))
	}
	nv.SwapInSet()

	for tick := uint64(6); tick < 5+uint64(borderLifecycleFullTailFrames); tick++ {
		bd.disp.Walk(nv, tick, bd.candidatesFor(nv, tick))
		nv.SwapInSet()
	}
	unchangedTick := uint64(5) + uint64(borderLifecycleFullTailFrames)
	unchanged := bd.disp.Walk(nv, unchangedTick, bd.candidatesFor(nv, unchangedTick))
	if got := binary.LittleEndian.Uint16(unchanged.Entries[0].DeltaBuf[26:28]); got != borderTailUnchanged {
		t.Fatalf("same-epoch tail count = %#x, want sentinel %#x", got, borderTailUnchanged)
	}
	nv.SwapInSet()

	// Identical bytes under a higher authority epoch describe a new
	// lifecycle baseline and therefore must be sent in full.
	nidMap.Get(ent).Epoch = 5
	epochTick := unchangedTick + 1
	advanced := bd.disp.Walk(nv, epochTick, bd.candidatesFor(nv, epochTick))
	if len(advanced.Entries) != 1 {
		t.Fatalf("advanced-epoch entry count = %d, want 1", len(advanced.Entries))
	}
	tail := advanced.Entries[0].DeltaBuf[26:]
	if got := binary.LittleEndian.Uint16(tail[:2]); got == borderTailUnchanged {
		t.Fatal("authority epoch change emitted unchanged sentinel")
	}
	if got := nv.Baselines().Baseline(1); got == nil || !got.HasAuthorityEpoch || got.AuthorityEpoch != 5 {
		t.Fatalf("baseline epoch = %+v, want authority epoch 5", got)
	}
	nv.SwapInSet()
	for attempt := uint8(1); attempt < borderLifecycleFullTailFrames; attempt++ {
		tick := epochTick + uint64(attempt)
		retry := bd.disp.Walk(nv, tick, bd.candidatesFor(nv, tick))
		if got := binary.LittleEndian.Uint16(retry.Entries[0].DeltaBuf[26:28]); got == borderTailUnchanged {
			t.Fatalf("epoch-change retry %d emitted sentinel after a potentially lost full tail", attempt)
		}
		nv.SwapInSet()
	}
	afterWindowTick := epochTick + uint64(borderLifecycleFullTailFrames)
	afterWindow := bd.disp.Walk(nv, afterWindowTick, bd.candidatesFor(nv, afterWindowTick))
	if got := binary.LittleEndian.Uint16(afterWindow.Entries[0].DeltaBuf[26:28]); got != borderTailUnchanged {
		t.Fatalf("post-epoch window tail count = %#x, want sentinel %#x", got, borderTailUnchanged)
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

	w := base.ECSWorld()
	compMap := ecs.NewMap1[testReplicaComponent](w)
	def := EntityKindDef{Kind: 3, Name: "Ship"}
	KindComponentByID(&def, w, ecs.ComponentID[testReplicaComponent](w), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
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
	sentinelTail := make([]byte, 28)
	binary.LittleEndian.PutUint32(sentinelTail[0:4], math.Float32bits(1200))
	binary.LittleEndian.PutUint32(sentinelTail[4:8], math.Float32bits(500))
	binary.LittleEndian.PutUint32(sentinelTail[8:12], math.Float32bits(20))
	binary.LittleEndian.PutUint16(sentinelTail[12:14], uint16(quantizeVelI16(10, 2000)))
	binary.LittleEndian.PutUint16(sentinelTail[14:16], uint16(quantizeVelI16(0, 2000)))
	binary.LittleEndian.PutUint16(sentinelTail[16:18], 0) // qangle (unset)
	binary.LittleEndian.PutUint64(sentinelTail[18:26], 0) // producedAtMs (unset)
	binary.LittleEndian.PutUint16(sentinelTail[26:28], borderTailUnchanged)

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
	// Size passed to the fixture, not set afterwards. A Stage captures its
	// geometry at construction, so setting it after this line would leave the
	// stage on 1024 and this regression test would stop reproducing the bug it
	// exists for — silently, since it asserts a reachability property that
	// simply holds at small cell sizes.
	base := newTestWorldBase(t, CellID{X: 0, Y: 0}, 8192)
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
