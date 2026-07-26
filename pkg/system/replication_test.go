package system

import (
	"bytes"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type testReplicator struct{ entityType uint8 }

func (r *testReplicator) EntityType() uint8 { return r.entityType }
func (r *testReplicator) Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	h.Float32(entry.X)
	h.Float32(entry.Y)
}
func (r *testReplicator) Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry) {
	w.Float32(entry.X)
	w.Float32(entry.Y)
}
func (r *testReplicator) SnapshotLayout() []int                                          { return []int{4, 4} }
func (r *testReplicator) InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte     { return nil }
func (r *testReplicator) HasInitial() bool                                               { return false }
func (r *testReplicator) InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {}

type tieredReplicator struct {
	testReplicator
	tier ReplicationTier
}

func (r *tieredReplicator) ReplicationTier() ReplicationTier { return r.tier }

type stubViewerSource struct{}

func (s *stubViewerSource) ActiveViewers() []ViewerInfo { return nil }

type stubFrameWriter struct{ frames []ReplicationFrame }

func (s *stubFrameWriter) WriteFrame(frame *ReplicationFrame) net.SendResult {
	s.frames = append(s.frames, *frame)
	return net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
}

type scriptedFrameWriter struct {
	frames  []ReplicationFrame
	results []net.SendResult
	calls   int
}

func (w *scriptedFrameWriter) WriteFrame(frame *ReplicationFrame) net.SendResult {
	cloned := *frame
	cloned.Full = append([]FullPayload(nil), frame.Full...)
	cloned.Deltas = append([]DeltaPayload(nil), frame.Deltas...)
	cloned.Entered = append([]uint32(nil), frame.Entered...)
	cloned.Exited = append([]uint32(nil), frame.Exited...)
	cloned.Removed = append([]uint32(nil), frame.Removed...)
	w.frames = append(w.frames, cloned)

	result := net.SendResult{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered}
	if w.calls < len(w.results) {
		result = w.results[w.calls]
	}
	w.calls++
	return result
}

type fixedViewerSource struct{ viewers []ViewerInfo }

func (s *fixedViewerSource) ActiveViewers() []ViewerInfo { return s.viewers }

type testEntityMapper struct {
	mapper *ecs.Map3[component.Position, component.NetworkID, component.EntityKind]
}

func newTestEntityMapper(world *ecs.World) *testEntityMapper {
	return &testEntityMapper{
		mapper: ecs.NewMap3[component.Position, component.NetworkID, component.EntityKind](world),
	}
}

func (m *testEntityMapper) spawn(x, y float32, netID uint32, kind uint8) ecs.Entity {
	return m.mapper.NewEntity(
		&component.Position{X: x, Y: y},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: kind},
	)
}

// ---------------------------------------------------------------------------
// Task 2: Tier caching tests
// ---------------------------------------------------------------------------

func TestNewReplicationSystem_TierCaching(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 1},
		tier:           ReplicationTier{Radius: 500, UpdateDivisor: 3, BaseWeight: 0.3},
	})
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 2},
		tier:           ReplicationTier{Radius: 5000, UpdateDivisor: 1, BaseWeight: 1.5},
	})

	tick := uint32(0)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     &stubViewerSource{},
		Frame:       &stubFrameWriter{},
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	// queryRadius should be the largest tier radius (5000), not AoIRadius (1000).
	if sys.queryRadius() != 5000 {
		t.Errorf("queryRadius = %v, want 5000", sys.queryRadius())
	}

	// Type 0 should have default tier (no TierProvider).
	tier0 := sys.tierConfigs[0]
	if tier0.Radius != 0 || tier0.UpdateDivisor != 1 || tier0.BaseWeight != 1.0 {
		t.Errorf("tier[0] = %+v, want default {0, 1, 1.0}", tier0)
	}

	// Type 1 should have custom tier.
	tier1 := sys.tierConfigs[1]
	if tier1.Radius != 500 || tier1.UpdateDivisor != 3 || tier1.BaseWeight != 0.3 {
		t.Errorf("tier[1] = %+v, want {500, 3, 0.3}", tier1)
	}

	// Type 2 should have custom tier.
	tier2 := sys.tierConfigs[2]
	if tier2.Radius != 5000 || tier2.UpdateDivisor != 1 || tier2.BaseWeight != 1.5 {
		t.Errorf("tier[2] = %+v, want {5000, 1, 1.5}", tier2)
	}
}

func TestNewReplicationSystem_NoTiers_MaxRadiusEqualsAoI(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	reg.Register(&testReplicator{entityType: 1})

	tick := uint32(0)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     &stubViewerSource{},
		Frame:       &stubFrameWriter{},
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return tick },
	})

	if sys.queryRadius() != 3000 {
		t.Errorf("queryRadius = %v, want 3000 (AoIRadius)", sys.queryRadius())
	}
}

func TestReplicationSystem_AttachesProcessedInputSequenceToSameFrame(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0})

	const want = uint32(1234)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{{
			ConnID: 1, Entity: viewerEntity, X: 0, Y: 0,
		}}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return 1 },
		ProcessedInputSeq: func(viewer *ViewerInfo) (uint32, bool) {
			if viewer.Entity != viewerEntity {
				t.Fatalf("callback viewer entity = %v, want %v", viewer.Entity, viewerEntity)
			}
			return want, true
		},
	})
	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(fw.frames))
	}
	if !fw.frames[0].HasInputAck || fw.frames[0].ProcessedInputSeq != want {
		t.Fatalf("input ack = (%d, %v), want (%d, true)", fw.frames[0].ProcessedInputSeq, fw.frames[0].HasInputAck, want)
	}
}

func TestReplicationSystem_PinsStreamEpochAtViewerActivation(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	viewerEntity := em.spawn(0, 0, 100, 0)
	_, networkID, _ := em.mapper.Get(viewerEntity)
	const wantEpoch = uint32(17)
	networkID.Epoch = wantEpoch
	var callbackEpochs []uint32

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{{
			ConnID: 1, Entity: viewerEntity, X: 0, Y: 0,
		}}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return 1 },
		OnBeforeSend: func(viewer *ViewerInfo, _ map[uint32]bool) {
			callbackEpochs = append(callbackEpochs, viewer.StreamEpoch)
		},
	})
	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(fw.frames))
	}
	if got := fw.frames[0].StreamEpoch; got != wantEpoch {
		t.Fatalf("stream epoch = %d, want viewer authority epoch %d", got, wantEpoch)
	}
	if len(callbackEpochs) != 1 || callbackEpochs[0] != wantEpoch {
		t.Fatalf("first callback stream epochs = %v, want [%d]", callbackEpochs, wantEpoch)
	}

	// Handoff preparation advances the entity authority epoch while this source
	// cell can still emit a short tail. Its frame stream must retain the epoch it
	// started with; the destination cell's new ReplicationSystem will capture 18.
	networkID.Epoch = wantEpoch + 1
	sys.Update(0.05)
	if len(fw.frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(fw.frames))
	}
	if got := fw.frames[1].StreamEpoch; got != wantEpoch {
		t.Fatalf("source stream epoch advanced to %d during handoff, want pinned %d", got, wantEpoch)
	}
	if len(callbackEpochs) != 2 || callbackEpochs[1] != wantEpoch {
		t.Fatalf("handoff callback stream epochs = %v, want pinned tail %d", callbackEpochs, wantEpoch)
	}
}

// ---------------------------------------------------------------------------
// Task 3: Tier radius cutoff and hash-unchanged visibility fix
// ---------------------------------------------------------------------------

func TestReplicationSystem_TierRadiusCutoff(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	// Type 0: default tier (uses AoIRadius = 1000)
	// Type 1: tier with Radius=500
	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 1},
		tier:           ReplicationTier{Radius: 500, UpdateDivisor: 1, BaseWeight: 1.0},
	})

	viewerEntity := em.spawn(0, 0, 100, 0)
	_ = viewerEntity

	// Entity at distance 400 (within both default 1000 and tier 500)
	e1 := em.spawn(400, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e1, X: 400, Y: 0})

	// Entity at distance 800 (within default 1000, outside tier 500)
	e2 := em.spawn(800, 0, 2, 1)
	grid.Register(spatial.Entry{Entity: e2, X: 800, Y: 0})

	// Entity at distance 300 (within both)
	e3 := em.spawn(300, 0, 3, 1)
	grid.Register(spatial.Entry{Entity: e3, X: 300, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(fw.frames))
	}

	frame := fw.frames[0]
	// Should see netID 1 (type 0, dist 400 < 1000) and netID 3 (type 1, dist 300 < 500)
	// Should NOT see netID 2 (type 1, dist 800 > 500)
	visibleNetIDs := make(map[uint32]bool)
	for _, f := range frame.Full {
		visibleNetIDs[f.NetID] = true
	}

	if !visibleNetIDs[1] {
		t.Error("netID 1 should be visible (type 0, dist=400 < AoI=1000)")
	}
	if visibleNetIDs[2] {
		t.Error("netID 2 should NOT be visible (type 1, dist=800 > tier radius=500)")
	}
	if !visibleNetIDs[3] {
		t.Error("netID 3 should be visible (type 1, dist=300 < tier radius=500)")
	}
}

func TestReplicationSystem_HashUnchanged_StaysVisible(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerEntity := em.spawn(0, 0, 100, 0)
	e1 := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e1, X: 100, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	// Tick 1: entity enters visibility.
	sys.Update(0.05)
	if len(fw.frames) != 1 {
		t.Fatalf("tick 1: expected 1 frame, got %d", len(fw.frames))
	}
	if len(fw.frames[0].Full) != 1 {
		t.Fatalf("tick 1: expected 1 full payload, got %d", len(fw.frames[0].Full))
	}

	// Tick 2: entity unchanged, should stay visible (no exit).
	tick = 2
	fw.frames = nil
	sys.Update(0.05)
	if len(fw.frames) != 1 {
		t.Fatalf("tick 2: expected 1 frame, got %d", len(fw.frames))
	}
	frame := fw.frames[0]
	if len(frame.Exited) > 0 {
		t.Errorf("tick 2: entity should not have exited, got exited=%v", frame.Exited)
	}
	if len(frame.Removed) > 0 {
		t.Errorf("tick 2: entity should not have been removed, got removed=%v", frame.Removed)
	}

	// Verify entity is still in the visible set.
	if !sys.IsVisible(1, 1) {
		t.Error("tick 2: entity netID=1 should still be visible to connID=1")
	}
}

func TestReplicationSystem_FirstSightingCommitsOnlyReliableOrdered(t *testing.T) {
	tests := []struct {
		name   string
		result net.SendResult
	}{
		{
			name:   "backpressure",
			result: net.SendResult{Disposition: net.SendBackpressure},
		},
		{
			name: "best effort enqueue",
			result: net.SendResult{
				Disposition: net.SendQueued,
				Delivery:    net.DeliveryBestEffort,
			},
		},
		{
			name: "ordered enqueue",
			result: net.SendResult{
				Disposition: net.SendQueued,
				Delivery:    net.DeliveryOrdered,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world := ecs.NewWorld()
			grid := spatial.NewHashGrid(100)
			em := newTestEntityMapper(world)
			fw := &scriptedFrameWriter{results: []net.SendResult{tt.result}}

			rep := &initialReplicator{
				testReplicator: testReplicator{entityType: 0},
				name:           "alpha",
			}
			reg := NewReplicatorRegistry()
			reg.Register(rep)

			viewerEntity := em.spawn(0, 0, 100, 0)
			entity := em.spawn(100, 0, 1, 0)
			grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

			tick := uint32(1)
			afterSendCalls := 0
			sys := NewReplicationSystem(ReplicationConfig{
				World:       world,
				SpatialGrid: grid,
				Viewers: &fixedViewerSource{viewers: []ViewerInfo{
					{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
				}},
				Frame:       fw,
				Replicators: reg,
				AoIRadius:   1000,
				AckMode:     replication.AckReliable,
				GetTick:     func() uint32 { return tick },
				OnAfterSend: func(*ViewerInfo, map[uint32]bool) { afterSendCalls++ },
			})

			sys.Update(0.05)
			if len(fw.frames) != 1 || len(fw.frames[0].Full) != 1 || len(fw.frames[0].Entered) != 1 {
				t.Fatalf("rejected first frame = %+v, want one full entered entity", fw.frames)
			}
			if fw.frames[0].Flags&quantize.FrameFlagFreshSnapshot == 0 {
				t.Fatal("rejected first frame must carry FreshSnapshot")
			}

			conn := sys.connections[1]
			if conn.store.Baseline(1) != nil {
				t.Fatal("rejected first sighting committed a baseline")
			}
			if conn.store.HasLastHash(1) {
				t.Fatal("rejected first sighting committed LastHash")
			}
			if conn.store.ExistingPriority(1) != nil {
				t.Fatal("rejected first sighting committed priority state")
			}
			if sys.IsVisible(1, 1) {
				t.Fatal("rejected first sighting committed visibility")
			}
			if afterSendCalls != 1 {
				t.Fatalf("rejected send OnAfterSend calls = %d, want 1", afterSendCalls)
			}

			tick = 2
			sys.Update(0.05)
			if len(fw.frames) != 2 || len(fw.frames[1].Full) != 1 || len(fw.frames[1].Entered) != 1 {
				t.Fatalf("retry frame = %+v, want one full entered entity", fw.frames)
			}
			if fw.frames[1].Flags&quantize.FrameFlagFreshSnapshot == 0 {
				t.Fatal("retry after an uncommitted first sighting must remain fresh")
			}

			baseline := conn.store.Baseline(1)
			if baseline == nil || baseline.Acked == nil {
				t.Fatal("reliable ordered retry did not commit its baseline")
			}
			if !baseline.HasInitialHash {
				t.Fatal("reliable ordered retry did not commit InitialHash")
			}
			if !conn.store.HasLastHash(1) || conn.store.ExistingPriority(1) == nil {
				t.Fatal("reliable ordered retry did not commit hash/priority state")
			}
			if !sys.IsVisible(1, 1) {
				t.Fatal("reliable ordered retry did not commit visibility")
			}
			if afterSendCalls != 2 {
				t.Fatalf("retry OnAfterSend calls = %d, want 2", afterSendCalls)
			}
		})
	}
}

func TestReplicationSystem_RejectedDeltaRetriesFromCommittedBaseline(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &scriptedFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendBackpressure},
	}}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		AckMode:     replication.AckReliable,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)
	conn := sys.connections[1]
	committedSnapshot := append([]byte(nil), conn.store.Baseline(1).Acked...)
	committedHash := conn.store.LastHash(1)
	committedPriority := *conn.store.ExistingPriority(1)

	position, _, _ := em.mapper.Get(entity)
	position.X = 150
	grid.Update(spatial.Entry{Entity: entity, X: 150, Y: 0})
	tick = 2
	sys.Update(0.05)
	if len(fw.frames[1].Deltas) != 1 {
		t.Fatalf("rejected change produced %d deltas, want 1", len(fw.frames[1].Deltas))
	}
	rejectedDelta := append([]byte(nil), fw.frames[1].Deltas[0].Data...)
	if got := conn.store.Baseline(1).Acked; !bytes.Equal(got, committedSnapshot) {
		t.Fatalf("rejected delta advanced baseline: got %v want %v", got, committedSnapshot)
	}
	if got := conn.store.LastHash(1); got != committedHash {
		t.Fatalf("rejected delta advanced LastHash: got %d want %d", got, committedHash)
	}
	if got := *conn.store.ExistingPriority(1); got != committedPriority {
		t.Fatalf("rejected delta advanced priority: got %+v want %+v", got, committedPriority)
	}

	tick = 3
	sys.Update(0.05)
	if len(fw.frames[2].Deltas) != 1 {
		t.Fatalf("retry produced %d deltas, want 1", len(fw.frames[2].Deltas))
	}
	if got := fw.frames[2].Deltas[0].Data; !bytes.Equal(got, rejectedDelta) {
		t.Fatalf("retry was not encoded from the last committed baseline: got %v want %v", got, rejectedDelta)
	}
	if got := conn.store.Baseline(1).Acked; bytes.Equal(got, committedSnapshot) {
		t.Fatal("accepted retry did not advance baseline")
	}
}

func TestReplicationSystem_RejectedRemovalRemainsPending(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &scriptedFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendBackpressure},
	}}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	tick := uint32(1)
	var removedIDs []uint32
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		AckMode:     replication.AckReliable,
		GetTick:     func() uint32 { return tick },
		RemovedIDs: func() []uint32 {
			ids := removedIDs
			removedIDs = nil
			return ids
		},
		BlinkDetectorTicks: 10,
	})

	sys.Update(0.05)
	conn := sys.connections[1]
	if conn.store.Baseline(1) == nil {
		t.Fatal("initial accepted frame did not establish a baseline")
	}

	grid.Deregister(entity)
	world.RemoveEntity(entity)
	removedIDs = []uint32{1}
	tick = 2
	sys.Update(0.05)
	if len(fw.frames[1].Removed) != 1 || fw.frames[1].Removed[0] != 1 {
		t.Fatalf("rejected removal frame Removed=%v, want [1]", fw.frames[1].Removed)
	}
	if len(fw.frames[1].Exited) != 0 {
		t.Fatalf("rejected removal frame Exited=%v, want none", fw.frames[1].Exited)
	}
	if !sys.IsVisible(1, 1) {
		t.Fatal("rejected removal changed committed visibility")
	}
	if conn.store.Baseline(1) == nil {
		t.Fatal("rejected removal dropped the committed baseline")
	}
	if !conn.pendingRemoved[1] {
		t.Fatal("rejected tick-scoped removal was not retained for retry")
	}
	if _, ok := conn.recentRemovals[1]; ok {
		t.Fatal("rejected removal was recorded as client-visible")
	}

	tick = 3
	sys.Update(0.05)
	if len(fw.frames[2].Removed) != 1 || fw.frames[2].Removed[0] != 1 {
		t.Fatalf("retry removal frame Removed=%v, want [1]", fw.frames[2].Removed)
	}
	if len(fw.frames[2].Exited) != 0 {
		t.Fatalf("retry removal degraded into Exited=%v", fw.frames[2].Exited)
	}
	if sys.IsVisible(1, 1) {
		t.Fatal("accepted removal did not commit visibility")
	}
	if conn.store.Baseline(1) != nil {
		t.Fatal("accepted removal did not drop baseline state")
	}
	if conn.pendingRemoved[1] {
		t.Fatal("accepted removal remained in the retry outbox")
	}
	if got := conn.recentRemovals[1]; got != 3 {
		t.Fatalf("accepted removal tombstone tick = %d, want 3", got)
	}
}

func TestReplicationSystem_ExplicitRingRetainsQueuedUntilExactAck(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &scriptedFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendQueued, Delivery: net.DeliveryBestEffort},
	}}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})
	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:            fw,
		Replicators:      reg,
		AoIRadius:        1000,
		AckMode:          replication.AckExplicit,
		SentHistoryDepth: 4,
		GetTick:          func() uint32 { return tick },
	})

	sys.Update(0.05)
	sys.AckSequence(1, fw.frames[0].Seq)
	baseline := sys.connections[1].store.Baseline(1)
	if baseline.RingLen != 0 {
		t.Fatalf("acked initial ring length = %d, want 0", baseline.RingLen)
	}

	position, _, _ := em.mapper.Get(entity)
	position.X = 150
	grid.Update(spatial.Entry{Entity: entity, X: 150, Y: 0})
	acked := append([]byte(nil), baseline.Acked...)
	tick = 2
	sys.Update(0.05)
	if baseline.RingLen != 1 {
		t.Fatalf("best-effort queued ring length = %d, want 1", baseline.RingLen)
	}
	if !bytes.Equal(baseline.Acked, acked) {
		t.Fatalf("best-effort queue advanced Acked: got %v want %v", baseline.Acked, acked)
	}
	entryIndex := (baseline.RingHead - 1 + len(baseline.Ring)) % len(baseline.Ring)
	if got := baseline.Ring[entryIndex].Seq; got != fw.frames[1].Seq {
		t.Fatalf("attempted ring seq = %d, want queued seq %d", got, fw.frames[1].Seq)
	}

	// One causal attempt is allowed in flight; no later frame can be encoded
	// against ambiguous state while this one awaits its application ACK.
	tick = 3
	sys.Update(0.05)
	if len(fw.frames) != 2 {
		t.Fatalf("frames while explicit attempt pending = %d, want 2", len(fw.frames))
	}

	sys.AckSequence(1, fw.frames[1].Seq)
	if baseline.RingLen != 0 {
		t.Fatalf("acked ring length = %d, want 0", baseline.RingLen)
	}
	if bytes.Equal(baseline.Acked, acked) {
		t.Fatal("exact ACK did not promote queued snapshot")
	}
}

// initialReplicator carries a mutable length-prefixed name as initial-only
// data. Its combined Hash (inherited from testReplicator) covers position only,
// so a name change does NOT bust the combined hash — the resend is driven purely
// by the initial-only hash path under test.
type initialReplicator struct {
	testReplicator
	name string
}

func (r *initialReplicator) HasInitial() bool { return true }
func (r *initialReplicator) InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte {
	return append([]byte{byte(len(r.name))}, []byte(r.name)...)
}
func (r *initialReplicator) InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for i := 0; i < len(r.name); i++ {
		h.Uint8(r.name[i])
	}
}

func decodeInitialName(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) == 0 {
		t.Fatal("initialData is empty")
	}
	n := int(b[0])
	if len(b) < 1+n {
		t.Fatalf("initialData too short: len=%d want>=%d", len(b), 1+n)
	}
	return string(b[1 : 1+n])
}

func findFull(t *testing.T, frames []ReplicationFrame, netID uint32) FullPayload {
	t.Helper()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	for _, f := range frames[0].Full {
		if f.NetID == netID {
			return f
		}
	}
	t.Fatalf("no full payload for netID=%d (full=%d deltas=%d)", netID, len(frames[0].Full), len(frames[0].Deltas))
	return FullPayload{}
}

// TestReplicationSystem_InitialFieldChange_ReSends verifies that mutating a
// net:"initial" field re-sends it (as a full entry carrying fresh initialData)
// to an already-visible viewer, and that an unchanged tick does not re-send.
func TestReplicationSystem_InitialFieldChange_ReSends(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	rep := &initialReplicator{testReplicator: testReplicator{entityType: 0}, name: "alpha"}
	reg := NewReplicatorRegistry()
	reg.Register(rep)

	viewerEntity := em.spawn(0, 0, 100, 0)
	e1 := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e1, X: 100, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	// Tick 1: entity enters — full frame with initialData "alpha".
	sys.Update(0.05)
	full := findFull(t, fw.frames, 1)
	if got := decodeInitialName(t, full.InitialData); got != "alpha" {
		t.Fatalf("tick1 name = %q, want alpha", got)
	}

	// Mutate the initial field; position is unchanged.
	rep.name = "beta"
	tick = 2
	fw.frames = nil
	sys.Update(0.05)
	full = findFull(t, fw.frames, 1) // must be a FULL entry, not a delta
	if full.InitialData == nil {
		t.Fatal("tick2: expected a full entry carrying initialData for the renamed entity")
	}
	if got := decodeInitialName(t, full.InitialData); got != "beta" {
		t.Fatalf("tick2 name = %q, want beta", got)
	}

	// Tick 3: nothing changed — must NOT re-send a full+initial frame.
	tick = 3
	fw.frames = nil
	sys.Update(0.05)
	if len(fw.frames) == 1 {
		for _, f := range fw.frames[0].Full {
			if f.NetID == 1 && f.InitialData != nil {
				t.Fatal("tick3: must not re-send initial data when nothing changed")
			}
		}
	}
}

func TestReplicationSystem_RejectedInitialChangeRetries(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &scriptedFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendBackpressure},
	}}

	rep := &initialReplicator{testReplicator: testReplicator{entityType: 0}, name: "alpha"}
	reg := NewReplicatorRegistry()
	reg.Register(rep)
	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		AckMode:     replication.AckReliable,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)
	baseline := sys.connections[1].store.Baseline(1)
	alphaHash := baseline.InitialHash

	rep.name = "beta"
	tick = 2
	sys.Update(0.05)
	if len(fw.frames[1].Full) != 1 || decodeInitialName(t, fw.frames[1].Full[0].InitialData) != "beta" {
		t.Fatalf("rejected initial change frame = %+v, want beta full payload", fw.frames[1])
	}
	if got := baseline.InitialHash; got != alphaHash {
		t.Fatalf("rejected initial change committed hash %d, want %d", got, alphaHash)
	}

	tick = 3
	sys.Update(0.05)
	if len(fw.frames[2].Full) != 1 || decodeInitialName(t, fw.frames[2].Full[0].InitialData) != "beta" {
		t.Fatalf("initial change retry frame = %+v, want beta full payload", fw.frames[2])
	}
	baseline = sys.connections[1].store.Baseline(1)
	if baseline.InitialHash == alphaHash {
		t.Fatal("accepted initial change retry did not commit InitialHash")
	}
}

func TestReplicationSystem_AuthorityEpochChangeForcesFullWithInitialAndRetries(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &scriptedFrameWriter{results: []net.SendResult{
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
		{Disposition: net.SendBackpressure},
		{Disposition: net.SendQueued, Delivery: net.DeliveryReliableOrdered},
	}}

	rep := &initialReplicator{testReplicator: testReplicator{entityType: 0}, name: "alpha"}
	reg := NewReplicatorRegistry()
	reg.Register(rep)
	viewerEntity := em.spawn(0, 0, 100, 0)
	entity := em.spawn(100, 0, 1, 0)
	_, networkID, _ := em.mapper.Get(entity)
	networkID.Epoch = 7
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		AckMode:     replication.AckReliable,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)
	baseline := sys.connections[1].store.Baseline(1)
	if baseline == nil || !baseline.HasAuthorityEpoch || baseline.AuthorityEpoch != 7 {
		t.Fatalf("initial baseline epoch = %+v, want 7", baseline)
	}

	// The entity stays visible and byte-identical, but its authority lifecycle
	// advances. The rejected attempt must not advance the committed baseline.
	networkID.Epoch = 8
	tick = 2
	sys.Update(0.05)
	if len(fw.frames[1].Deltas) != 0 {
		t.Fatalf("epoch change emitted %d deltas, want a full reset", len(fw.frames[1].Deltas))
	}
	full := findFull(t, fw.frames[1:2], 1)
	if full.Epoch != 8 || decodeInitialName(t, full.InitialData) != "alpha" {
		t.Fatalf("epoch-change full = %+v, want epoch 8 with initial data", full)
	}
	if baseline.AuthorityEpoch != 7 {
		t.Fatalf("rejected epoch change committed epoch %d, want 7", baseline.AuthorityEpoch)
	}

	// Retry is independently full and commits the new epoch only after the
	// reliable ordered writer accepts it.
	tick = 3
	sys.Update(0.05)
	full = findFull(t, fw.frames[2:3], 1)
	if full.Epoch != 8 || decodeInitialName(t, full.InitialData) != "alpha" {
		t.Fatalf("epoch-change retry = %+v, want epoch 8 with initial data", full)
	}
	baseline = sys.connections[1].store.Baseline(1)
	if baseline == nil || !baseline.HasAuthorityEpoch || baseline.AuthorityEpoch != 8 {
		t.Fatalf("committed baseline epoch = %+v, want 8", baseline)
	}
}

// ---------------------------------------------------------------------------
// Task 5: PriorityProvider integration test
// ---------------------------------------------------------------------------

// priorityReplicator implements both TierProvider and PriorityProvider.
type priorityReplicator struct {
	testReplicator
	tier     ReplicationTier
	priority float32
}

func (r *priorityReplicator) ReplicationTier() ReplicationTier { return r.tier }
func (r *priorityReplicator) NetPriority(viewer *ViewerInfo, entry spatial.Entry) float32 {
	return r.priority
}

func TestReplicationSystem_PriorityProviderMultiplier(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&priorityReplicator{
		testReplicator: testReplicator{entityType: 1},
		tier:           ReplicationTier{Radius: 3000, UpdateDivisor: 2, BaseWeight: 1.0},
		priority:       2.5,
	})

	viewerEntity := em.spawn(0, 0, 100, 1)
	e1 := em.spawn(100, 0, 1, 1)
	grid.Register(spatial.Entry{Entity: e1, X: 100, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   3000,
		GetTick:     func() uint32 { return tick },
	})

	// Tick 1: entity enters (full payload, isNew bypasses divisor).
	sys.Update(0.05)
	if len(fw.frames) != 1 || len(fw.frames[0].Full) != 1 {
		t.Fatalf("tick 1: expected 1 full payload, got frames=%d", len(fw.frames))
	}

	// Tick 2: divisor=2, tick%2==0 → send tick. Move entity to change hash.
	tick = 2
	pos2, _, _ := em.mapper.Get(e1)
	pos2.X = 150
	grid.Update(spatial.Entry{Entity: e1, X: 150, Y: 0})

	fw.frames = nil
	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("tick 2: expected 1 frame, got %d", len(fw.frames))
	}

	conn := sys.connections[1]
	ps2 := conn.store.Priority(1)
	if ps2.Accumulator != 0 {
		t.Errorf("tick 2 (send tick): expected accumulator=0 after send, got %v", ps2.Accumulator)
	}
	if ps2.LastSentTick != 2 {
		t.Errorf("tick 2: expected lastSentTick=2, got %v", ps2.LastSentTick)
	}

	// Tick 3: divisor=2, tick%2!=0 → skip tick. Move entity again to change hash.
	tick = 3
	pos3, _, _ := em.mapper.Get(e1)
	pos3.X = 200
	grid.Update(spatial.Entry{Entity: e1, X: 200, Y: 0})

	fw.frames = nil
	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("tick 3: expected 1 frame, got %d", len(fw.frames))
	}

	ps3 := conn.store.Priority(1)
	// dist=200, tierRadius=3000, distFactor=1-(200/3000)≈0.9333
	// basePriority = 1.0 * 0.9333 * 2.5 ≈ 2.333
	// The priority multiplier of 2.5 should be reflected: accumulator > 2.3
	if ps3.Accumulator <= 2.3 {
		t.Errorf("tick 3 (skip tick): expected accumulator > 2.3 (includes 2.5x priority), got %v", ps3.Accumulator)
	}
	// Also verify the entity did not send (no full or delta payloads).
	frame3 := fw.frames[0]
	if len(frame3.Full) > 0 || len(frame3.Deltas) > 0 {
		t.Errorf("tick 3 (skip tick): expected no payload, got full=%d deltas=%d", len(frame3.Full), len(frame3.Deltas))
	}
}

// ---------------------------------------------------------------------------
// Task 4: Dormancy and update divisor tests
// ---------------------------------------------------------------------------

func TestReplicationSystem_UpdateDivisor(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	// Type 1 with UpdateDivisor=3: should only send on ticks divisible by 3.
	reg := NewReplicatorRegistry()
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 1},
		tier:           ReplicationTier{Radius: 1000, UpdateDivisor: 3, BaseWeight: 1.0},
	})

	viewerEntity := em.spawn(0, 0, 100, 1)
	e1 := em.spawn(100, 0, 1, 1)
	grid.Register(spatial.Entry{Entity: e1, X: 100, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	// Tick 1: entity is new, should always send (isNew bypasses divisor).
	sys.Update(0.05)
	if len(fw.frames) != 1 || len(fw.frames[0].Full) != 1 {
		t.Fatalf("tick 1: expected 1 full payload (new entity), got frames=%d", len(fw.frames))
	}

	// Ticks 2-7: entity changes position each tick so hash changes.
	// Only ticks divisible by 3 should produce a payload.
	sentTicks := []uint32{}
	for tk := uint32(2); tk <= 7; tk++ {
		tick = tk
		// Move entity so hash changes each tick.
		newX := float32(100 + tk*10)
		em.mapper.Get(e1) // ensure alive
		pos, _, _ := em.mapper.Get(e1)
		pos.X = newX
		grid.Update(spatial.Entry{Entity: e1, X: newX, Y: 0})

		fw.frames = nil
		sys.Update(0.05)

		if len(fw.frames) != 1 {
			t.Fatalf("tick %d: expected 1 frame", tk)
		}
		frame := fw.frames[0]
		hasDelta := len(frame.Full) > 0 || len(frame.Deltas) > 0
		if hasDelta {
			sentTicks = append(sentTicks, tk)
		}

		// Entity should never exit.
		if len(frame.Exited) > 0 {
			t.Errorf("tick %d: entity should not have exited", tk)
		}
	}

	// Expect sends on ticks 3 and 6 (divisible by 3).
	if len(sentTicks) != 2 || sentTicks[0] != 3 || sentTicks[1] != 6 {
		t.Errorf("expected sends on ticks [3 6], got %v", sentTicks)
	}
}

func TestReplicationSystem_UpdateDivisorRetainsOneTimeSkippedChange(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&tieredReplicator{
		testReplicator: testReplicator{entityType: 1},
		tier:           ReplicationTier{Radius: 1000, UpdateDivisor: 3, BaseWeight: 1},
	})

	viewerEntity := em.spawn(0, 0, 100, 1)
	entity := em.spawn(100, 0, 1, 1)
	grid.Register(spatial.Entry{Entity: entity, X: 100, Y: 0})

	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{
			{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
		}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		AckMode:     replication.AckReliable,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)
	committedHash := sys.connections[1].store.LastHash(1)

	// Change only on tick 2, which the divisor skips.
	position, _, _ := em.mapper.Get(entity)
	position.X = 150
	grid.Update(spatial.Entry{Entity: entity, X: 150, Y: 0})
	tick = 2
	fw.frames = nil
	sys.Update(0.05)
	if len(fw.frames) != 1 || len(fw.frames[0].Deltas) != 0 || len(fw.frames[0].Full) != 0 {
		t.Fatalf("tick 2 should skip payload, got %+v", fw.frames)
	}
	if got := sys.connections[1].store.LastHash(1); got != committedHash {
		t.Fatalf("skipped tick advanced LastHash to %d, want committed %d", got, committedHash)
	}

	// No further change occurs. The next eligible tick must still send the
	// tick-2 state instead of treating it as already replicated.
	tick = 3
	fw.frames = nil
	sys.Update(0.05)
	if len(fw.frames) != 1 || len(fw.frames[0].Deltas) != 1 {
		t.Fatalf("tick 3 should deliver retained change, got %+v", fw.frames)
	}
}

func TestReplicationSystem_Dormancy(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerEntity := em.spawn(0, 0, 100, 0)
	e1 := em.spawn(100, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e1, X: 100, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:             world,
		SpatialGrid:       grid,
		Viewers:           viewers,
		Frame:             fw,
		Replicators:       reg,
		AoIRadius:         1000,
		DormancyThreshold: 3,
		GetTick:           func() uint32 { return tick },
	})

	// Tick 1: entity enters (new).
	sys.Update(0.05)

	// Ticks 2-4: entity unchanged, unchangedTicks increments each tick.
	for tk := uint32(2); tk <= 4; tk++ {
		tick = tk
		fw.frames = nil
		sys.Update(0.05)
	}

	// After tick 4, unchangedTicks should be >= 3 (incremented on ticks 2, 3, 4).
	conn := sys.connections[1]
	ps := conn.store.Priority(1)
	if ps.UnchangedTicks < 3 {
		t.Errorf("expected unchangedTicks >= 3, got %d", ps.UnchangedTicks)
	}

	// Tick 5: entity is dormant, should be skipped but still visible.
	tick = 5
	fw.frames = nil
	sys.Update(0.05)

	if !sys.IsVisible(1, 1) {
		t.Error("dormant entity should still be visible")
	}
	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(fw.frames))
	}
	if len(fw.frames[0].Exited) > 0 {
		t.Error("dormant entity should not exit")
	}
}

// TestReplicationSystem_BorderReplicasFlowThroughDispatcher asserts that
// border replicas (entities mirrored from neighbor nodes) are sent to
// clients alongside local entities. Before the tiered-push-replication
// refactor, border replicas were replicated to clients via a separate
// MsgReplica channel from the *source* node's dispatcher. After the
// cutover, clients only receive frames from the node they're connected
// to, so the local ReplicationSystem must dispatch border replicas
// itself — they are the only way a player can see entities owned by
// neighbor nodes (e.g. an asteroid across a cell boundary).
//
// Replicas carry the full component set of their entity kind (auto-filled
// to zero values by Stage.upsertBorderReplica via
// EnsureEntityKindComponents), so the existing Hash/Snapshot path works
// unchanged — no special handling is required in the dispatcher.
//
// Regression test for the space-game teleport panic observed after Phase
// 7.6: a stale "skip replicas" guard dropped frame visibility for every
// neighbor-owned entity, making adjacent-cell asteroids invisible until
// the player physically crossed the boundary.
func TestReplicationSystem_BorderReplicasFlowThroughDispatcher(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	// Counting replicator: records every entity whose Hash is invoked so
	// we can assert both local and replica entities reach the dispatcher.
	var hashedNetIDs []uint32
	countingRep := &countingReplicator{
		entityType: 0,
		onHash: func(entry spatial.Entry) {
			nidMap := ecs.NewMap1[component.NetworkID](world)
			if nidMap.HasAll(entry.Entity) {
				hashedNetIDs = append(hashedNetIDs, nidMap.Get(entry.Entity).ID)
			}
		},
	}
	reg := NewReplicatorRegistry()
	reg.Register(countingRep)

	viewerEntity := em.spawn(0, 0, 100, 0)

	// Local entity: should be hashed and visible.
	local := em.spawn(50, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: local, X: 50, Y: 0})

	// Border replica: Replica component attached, simulates a
	// neighbor-owned entity mirrored onto this node. Must also be hashed
	// and visible — the local dispatcher is the only channel the client
	// has to hear about it.
	replica := em.spawn(60, 0, 2, 0)
	replicaMap := ecs.NewMap1[component.Replica](world)
	replicaMap.Add(replica, &component.Replica{SourceCellID: "neighbor", TTL: 30})
	grid.Register(spatial.Entry{Entity: replica, X: 60, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)

	if len(hashedNetIDs) != 2 {
		t.Fatalf("expected exactly 2 entities hashed (local + replica), got %d: %v", len(hashedNetIDs), hashedNetIDs)
	}
	seen := make(map[uint32]bool)
	for _, id := range hashedNetIDs {
		seen[id] = true
	}
	if !seen[1] {
		t.Error("netID 1 (local) should have been hashed")
	}
	if !seen[2] {
		t.Error("netID 2 (border replica) should have been hashed — replicas are the only way clients see neighbor-owned entities")
	}

	// The emitted frame should contain BOTH entities.
	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(fw.frames))
	}
	visible := make(map[uint32]bool)
	for _, f := range fw.frames[0].Full {
		visible[f.NetID] = true
	}
	if !visible[1] {
		t.Error("netID 1 (local) should be visible in the frame")
	}
	if !visible[2] {
		t.Error("netID 2 (border replica) should be visible in the frame")
	}
}

// countingReplicator wraps testReplicator with an onHash callback so tests
// can assert which entities actually reach the Hash call.
type countingReplicator struct {
	entityType uint8
	onHash     func(spatial.Entry)
}

func (r *countingReplicator) EntityType() uint8 { return r.entityType }
func (r *countingReplicator) Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	if r.onHash != nil {
		r.onHash(entry)
	}
	h.Float32(entry.X)
	h.Float32(entry.Y)
}
func (r *countingReplicator) Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry) {
	w.Float32(entry.X)
	w.Float32(entry.Y)
}
func (r *countingReplicator) SnapshotLayout() []int                                          { return []int{4, 4} }
func (r *countingReplicator) InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte     { return nil }
func (r *countingReplicator) HasInitial() bool                                               { return false }
func (r *countingReplicator) InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {}

// TestReplicationSystem_DormantSkippedInAoI verifies the Dormant component
// excludes an entity from AoI broadcast, even though it's still in the
// spatial grid. This is the mechanism that lets games keep an "in station"
// player as a viewer (they still receive WorldUpdateMsg) without showing
// them to other pilots flying past the station.
func TestReplicationSystem_DormantSkippedInAoI(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	dormantMap := ecs.NewMap1[component.Dormant](world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	viewerEntity := em.spawn(0, 0, 100, 0)

	// e1 is awake, within AoI — should be visible.
	e1 := em.spawn(50, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e1, X: 50, Y: 0})

	// e2 is Dormant, within AoI — should be FILTERED.
	e2 := em.spawn(60, 0, 2, 0)
	dormantMap.Add(e2, &component.Dormant{})
	grid.Register(spatial.Entry{Entity: e2, X: 60, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(fw.frames))
	}

	visible := make(map[uint32]bool)
	for _, f := range fw.frames[0].Full {
		visible[f.NetID] = true
	}
	if !visible[1] {
		t.Error("netID 1 (awake, in AoI) should be visible")
	}
	if visible[2] {
		t.Error("netID 2 (Dormant, in AoI) should NOT be visible — Dormant filter broken")
	}
}

// TestReplicationSystem_DormantViewerSeesSelf verifies the self-visibility
// exception. A Dormant viewer must still receive its OWN entity in the
// AoI broadcast so the client HUD can keep position/cell/equipment readouts
// alive — without this, the docked player's top-bar info would go blank
// because state.entities.get(myEntityId) would be nil.
func TestReplicationSystem_DormantViewerSeesSelf(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	dormantMap := ecs.NewMap1[component.Dormant](world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	// Viewer is Dormant AND in the grid (mimics the docked-player setup
	// where the entity is parked at station center, Dormant, but still
	// spatially registered).
	viewerEntity := em.spawn(0, 0, 100, 0)
	dormantMap.Add(viewerEntity, &component.Dormant{})
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatal("Dormant viewer should still receive its own WorldUpdateMsg")
	}
	visible := make(map[uint32]bool)
	for _, f := range fw.frames[0].Full {
		visible[f.NetID] = true
	}
	if !visible[100] {
		t.Error("Dormant viewer should see its OWN entity (netID 100) — self-visibility exception broken; HUD will lose position/cell readout")
	}
}

// TestReplicationSystem_DormantViewerStillReceivesFrames verifies the
// inverse: a Dormant *viewer* (e.g. a docked player parked at a station
// who is still listed as a viewer in PlayerViewerSource) still receives
// AoI deltas of OTHER non-Dormant entities. The Dormant filter only hides
// the entity from being broadcast TO others; it doesn't suppress its own
// inbound replication.
func TestReplicationSystem_DormantViewerStillReceivesFrames(t *testing.T) {
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	dormantMap := ecs.NewMap1[component.Dormant](world)
	fw := &stubFrameWriter{}

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 0})

	// Viewer is Dormant (e.g. docked player at station).
	viewerEntity := em.spawn(0, 0, 100, 0)
	dormantMap.Add(viewerEntity, &component.Dormant{})

	// e1 is an awake ship near the station — viewer should see it.
	e1 := em.spawn(50, 0, 1, 0)
	grid.Register(spatial.Entry{Entity: e1, X: 50, Y: 0})

	tick := uint32(1)
	viewers := &fixedViewerSource{viewers: []ViewerInfo{
		{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0},
	}}

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
	})

	sys.Update(0.05)

	if len(fw.frames) != 1 {
		t.Fatal("Dormant viewer should still receive its own WorldUpdateMsg (tick + AoI of others)")
	}
	visible := make(map[uint32]bool)
	for _, f := range fw.frames[0].Full {
		visible[f.NetID] = true
	}
	if !visible[1] {
		t.Error("Dormant viewer should see e1 (awake, in AoI) — Dormant only hides FROM others, not the viewer's own AoI inbound")
	}
}
