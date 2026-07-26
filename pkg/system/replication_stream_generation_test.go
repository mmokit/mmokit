package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
)

type livenessViewerSource struct {
	viewers  []ViewerInfo
	existing map[uint32]bool
}

func (s *livenessViewerSource) ActiveViewers() []ViewerInfo { return s.viewers }
func (s *livenessViewerSource) ViewerExists(connID uint32) bool {
	return s.existing[connID]
}

func TestReplicationSystem_TemporaryViewerOmissionRetainsSequenceAndForcesFresh(t *testing.T) {
	world := ecsWorldForReplicationTest()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0})

	viewer := ViewerInfo{ConnID: 1, Entity: viewerEntity, X: 0, Y: 0}
	viewers := &livenessViewerSource{
		viewers:  []ViewerInfo{viewer},
		existing: map[uint32]bool{1: true},
	}
	writer := &scriptedFrameWriter{}
	registry := NewReplicatorRegistry()
	registry.Register(&testReplicator{entityType: 0})
	tick := uint32(1)

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     viewers,
		Frame:       writer,
		Replicators: registry,
		AoIRadius:   1000,
		AckMode:     replication.AckExplicit,
		GetTick:     func() uint32 { return tick },
		StreamGeneration: func(*ViewerInfo) (uint32, bool) {
			return 7, true
		},
	})

	sys.Update(0.05)
	if len(writer.frames) != 1 || writer.frames[0].Seq != 1 {
		t.Fatalf("initial frames = %+v, want one seq=1 frame", writer.frames)
	}
	if sys.connections[1].pending == nil {
		t.Fatal("initial explicit-ack frame should be pending")
	}

	// The session still exists but its lifecycle state is not currently part
	// of ActiveViewers. Its ambiguous pending attempt is abandoned without
	// throwing away the stream sequence or committed replication state.
	viewers.viewers = nil
	tick++
	sys.Update(0.05)
	retained := sys.connections[1]
	if retained == nil {
		t.Fatal("temporarily omitted viewer state was deleted")
	}
	if retained.nextSeq != 1 {
		t.Fatalf("nextSeq after omission = %d, want retained value 1", retained.nextSeq)
	}
	if retained.pending != nil {
		t.Fatal("pending attempt survived temporary omission")
	}
	if !retained.forceFresh {
		t.Fatal("temporary omission did not force a fresh reactivation frame")
	}

	viewers.viewers = []ViewerInfo{viewer}
	tick++
	sys.Update(0.05)
	if len(writer.frames) != 2 {
		t.Fatalf("frames after reactivation = %d, want 2", len(writer.frames))
	}
	reactivated := writer.frames[1]
	if reactivated.Seq != 2 {
		t.Fatalf("reactivation seq = %d, want continuous seq 2", reactivated.Seq)
	}
	if reactivated.StreamEpoch != 7 {
		t.Fatalf("reactivation stream epoch = %d, want 7", reactivated.StreamEpoch)
	}
	if reactivated.Flags&quantize.FrameFlagFreshSnapshot == 0 {
		t.Fatal("reactivation frame is not a fresh snapshot")
	}
	if len(reactivated.Full) == 0 {
		t.Fatal("reactivation fresh snapshot contains no full entity payload")
	}

	// Once liveness says the session is truly gone, automatic cleanup deletes
	// the retained state. LastVisible remains an explicit unconditional delete.
	viewers.viewers = nil
	viewers.existing[1] = false
	tick++
	sys.Update(0.05)
	if _, ok := sys.connections[1]; ok {
		t.Fatal("truly gone viewer state was retained")
	}
}

func TestReplicationSystem_StreamGenerationAdvanceResetsStateAndRejectsStale(t *testing.T) {
	world := ecsWorldForReplicationTest()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	viewerEntity := em.spawn(0, 0, 100, 0)
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0})

	writer := &scriptedFrameWriter{}
	registry := NewReplicatorRegistry()
	registry.Register(&testReplicator{entityType: 0})
	generation := uint32(41)
	tick := uint32(1)
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{{
			ConnID: 1, Entity: viewerEntity, X: 0, Y: 0,
		}}},
		Frame:       writer,
		Replicators: registry,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return tick },
		StreamGeneration: func(*ViewerInfo) (uint32, bool) {
			return generation, true
		},
	})

	sys.Update(0.05)
	firstConn := sys.connections[1]
	tick++
	sys.Update(0.05)
	if len(writer.frames) != 2 || writer.frames[1].Seq != 2 {
		t.Fatalf("equal generation did not continue stream: frames=%+v", writer.frames)
	}

	generation = 42
	tick++
	sys.Update(0.05)
	if len(writer.frames) != 3 {
		t.Fatalf("frames after generation advance = %d, want 3", len(writer.frames))
	}
	advanced := writer.frames[2]
	if advanced.StreamEpoch != 42 || advanced.Seq != 1 {
		t.Fatalf("advanced frame = epoch %d seq %d, want epoch 42 seq 1", advanced.StreamEpoch, advanced.Seq)
	}
	if advanced.Flags&quantize.FrameFlagFreshSnapshot == 0 || len(advanced.Full) == 0 {
		t.Fatalf("advanced frame is not a complete fresh snapshot: flags=%#x full=%d", advanced.Flags, len(advanced.Full))
	}
	if sys.connections[1] == firstConn {
		t.Fatal("generation advance retained the prior connection baseline state")
	}

	// A delayed value from the prior generation cannot move the stream back.
	generation = 41
	tick++
	sys.Update(0.05)
	stale := writer.frames[3]
	if stale.StreamEpoch != 42 || stale.Seq != 2 {
		t.Fatalf("stale generation regressed stream to epoch %d seq %d; want epoch 42 seq 2", stale.StreamEpoch, stale.Seq)
	}
}

func TestReplicationSystem_UnavailableGenerationUsesPinnedNetworkEpoch(t *testing.T) {
	world := ecsWorldForReplicationTest()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	viewerEntity := em.spawn(0, 0, 100, 0)
	_, networkID, _ := em.mapper.Get(viewerEntity)
	networkID.Epoch = 19
	grid.Register(spatial.Entry{Entity: viewerEntity, X: 0, Y: 0})

	writer := &scriptedFrameWriter{}
	registry := NewReplicatorRegistry()
	registry.Register(&testReplicator{entityType: 0})
	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers: &fixedViewerSource{viewers: []ViewerInfo{{
			ConnID: 1, Entity: viewerEntity, X: 0, Y: 0,
		}}},
		Frame:       writer,
		Replicators: registry,
		AoIRadius:   1000,
		GetTick:     func() uint32 { return 1 },
		StreamGeneration: func(*ViewerInfo) (uint32, bool) {
			return 0, false
		},
	})

	sys.Update(0.05)
	networkID.Epoch = 20
	sys.Update(0.05)
	if len(writer.frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(writer.frames))
	}
	for i, frame := range writer.frames {
		if frame.StreamEpoch != 19 {
			t.Fatalf("frame %d stream epoch = %d, want pinned NetworkID epoch 19", i, frame.StreamEpoch)
		}
	}
}

func TestStreamGenerationAfter_WrapSafe(t *testing.T) {
	if !streamGenerationAfter(0, ^uint32(0)) {
		t.Fatal("MaxUint32 -> 0 should be a forward generation change")
	}
	if streamGenerationAfter(^uint32(0), 0) {
		t.Fatal("MaxUint32 should be stale after generation 0")
	}
	if streamGenerationAfter(9, 10) {
		t.Fatal("lower prior generation should be stale")
	}
}

// ecsWorldForReplicationTest keeps the fixture construction compact while
// returning the exact pointer type required by ReplicationConfig.
func ecsWorldForReplicationTest() *ecs.World {
	world := ecs.NewWorld()
	return world
}
