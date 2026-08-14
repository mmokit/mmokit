package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/replication"
	"github.com/zenion/mmokit/pkg/spatial"
)

// ---------------------------------------------------------------------------
// Blink detector tests (E3 + E4)
// ---------------------------------------------------------------------------

// blinkTestHarness wires up a minimal ReplicationSystem for blink-detector
// tests. It reuses testReplicator, fixedViewerSource, and stubFrameWriter
// from replication_test.go (same package).
type blinkTestHarness struct {
	world       *ecs.World
	grid        *spatial.HashGrid
	em          *testEntityMapper
	fw          *stubFrameWriter
	viewers     *fixedViewerSource
	sys         *ReplicationSystem
	currentTick uint32
	removedQ    []uint32
	blinkCalls  []blinkCall
}

type blinkCall struct {
	connID, netID    uint32
	ticksSinceRemove uint64
}

func newBlinkHarness(t *testing.T, windowTicks uint64) *blinkTestHarness {
	t.Helper()
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}

	// Spawn a dummy viewer entity so the ViewerInfo Entity is valid.
	viewerEnt := em.spawn(0, 0, 999, 0)

	h := &blinkTestHarness{
		world:   world,
		grid:    grid,
		em:      em,
		fw:      fw,
		viewers: &fixedViewerSource{viewers: []ViewerInfo{{ConnID: 1, Entity: viewerEnt, X: 0, Y: 0}}},
	}
	h.currentTick = 1

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 1})

	h.sys = NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     h.viewers,
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   500,
		AckMode:     replication.AckReliable,
		GetTick:     func() uint32 { return h.currentTick },
		RemovedIDs:  func() []uint32 { q := h.removedQ; h.removedQ = nil; return q },

		BlinkDetectorTicks: windowTicks,
		OnBlinkDetected: func(connID, netID uint32, delta uint64) {
			h.blinkCalls = append(h.blinkCalls, blinkCall{
				connID:           connID,
				netID:            netID,
				ticksSinceRemove: delta,
			})
		},
	})
	return h
}

// spawnVisible creates an entity at (10,10) with the given netID and registers
// it in the spatial grid so the viewer will see it.
func (h *blinkTestHarness) spawnVisible(netID uint32) ecs.Entity {
	ent := h.em.spawn(10, 10, netID, 1)
	h.grid.Register(spatial.Entry{Entity: ent, X: 10, Y: 10, Radius: 5})
	return ent
}

// removeEntity removes the entity from the grid and ECS, queuing it for the
// SE_ENTITY_REMOVED path via RemovedIDs.
func (h *blinkTestHarness) removeEntity(ent ecs.Entity, netID uint32) {
	h.grid.Deregister(ent)
	h.world.RemoveEntity(ent)
	h.removedQ = append(h.removedQ, netID)
}

func (h *blinkTestHarness) tick() {
	h.sys.Update(0.05)
}

// ---------------------------------------------------------------------------

// TestReplicationSystem_BlinkDetector_RemoveThenSpawnFires verifies the
// detector fires OnBlinkDetected when an entity is removed and then
// respawned for the same conn within BlinkDetectorTicks.
func TestReplicationSystem_BlinkDetector_RemoveThenSpawnFires(t *testing.T) {
	h := newBlinkHarness(t, 10)

	// Tick 1: spawn entity netID=42 visible to conn 1.
	ent := h.spawnVisible(42)
	h.tick()

	if len(h.blinkCalls) != 0 {
		t.Fatalf("unexpected blink on initial spawn: %d calls", len(h.blinkCalls))
	}

	// Tick 2: remove entity via RemovedIDs.
	h.currentTick = 2
	h.removeEntity(ent, 42)
	h.tick()

	if len(h.blinkCalls) != 0 {
		t.Fatalf("unexpected blink on removal: %d calls", len(h.blinkCalls))
	}

	// Tick 5 (within window=10): respawn same netID.
	h.currentTick = 5
	_ = h.spawnVisible(42)
	h.tick()

	if len(h.blinkCalls) != 1 {
		t.Fatalf("blink calls = %d, want 1", len(h.blinkCalls))
	}
	got := h.blinkCalls[0]
	if got.connID != 1 {
		t.Errorf("connID = %d, want 1", got.connID)
	}
	if got.netID != 42 {
		t.Errorf("netID = %d, want 42", got.netID)
	}
	if got.ticksSinceRemove != 3 { // 5 - 2
		t.Errorf("ticksSinceRemove = %d, want 3", got.ticksSinceRemove)
	}
}

// TestReplicationSystem_BlinkDetector_OutsideWindowNoFire verifies the
// detector does NOT fire if the respawn arrives outside the window.
func TestReplicationSystem_BlinkDetector_OutsideWindowNoFire(t *testing.T) {
	h := newBlinkHarness(t, 10)

	// Tick 1: spawn and let the viewer see it.
	ent := h.spawnVisible(42)
	h.tick()

	// Tick 2: remove.
	h.currentTick = 2
	h.removeEntity(ent, 42)
	h.tick()

	// Tick 20 (outside window=10, delta=18): respawn.
	h.currentTick = 20
	_ = h.spawnVisible(42)
	h.tick()

	if len(h.blinkCalls) != 0 {
		t.Fatalf("expected no blink outside window, got %d calls", len(h.blinkCalls))
	}
}

// TestReplicationSystem_BlinkDetector_DisabledWhenZero verifies that setting
// BlinkDetectorTicks=0 fully disables detection (no panic or map access).
func TestReplicationSystem_BlinkDetector_DisabledWhenZero(t *testing.T) {
	// Build a harness with window=0 (detector off). Reuse infrastructure but
	// wire manually so we can pass windowTicks=0 cleanly.
	world := ecs.NewWorld()
	grid := spatial.NewHashGrid(100)
	em := newTestEntityMapper(world)
	fw := &stubFrameWriter{}
	viewerEnt := em.spawn(0, 0, 999, 0)

	reg := NewReplicatorRegistry()
	reg.Register(&testReplicator{entityType: 1})

	tick := uint32(1)
	var removedQ []uint32
	var calls int

	sys := NewReplicationSystem(ReplicationConfig{
		World:       world,
		SpatialGrid: grid,
		Viewers:     &fixedViewerSource{viewers: []ViewerInfo{{ConnID: 1, Entity: viewerEnt, X: 0, Y: 0}}},
		Frame:       fw,
		Replicators: reg,
		AoIRadius:   500,
		AckMode:     replication.AckReliable,
		GetTick:     func() uint32 { return tick },
		RemovedIDs:  func() []uint32 { q := removedQ; removedQ = nil; return q },

		BlinkDetectorTicks: 0, // disabled
		OnBlinkDetected: func(connID, netID uint32, delta uint64) {
			calls++
		},
	})

	// Spawn, remove, respawn — detector should be silent.
	ent := em.spawn(10, 10, 42, 1)
	grid.Register(spatial.Entry{Entity: ent, X: 10, Y: 10, Radius: 5})
	sys.Update(0.05)

	tick = 2
	grid.Deregister(ent)
	world.RemoveEntity(ent)
	removedQ = []uint32{42}
	sys.Update(0.05)

	tick = 3
	ent2 := em.spawn(10, 10, 42, 1)
	grid.Register(spatial.Entry{Entity: ent2, X: 10, Y: 10, Radius: 5})
	sys.Update(0.05)

	if calls != 0 {
		t.Fatalf("expected 0 blink calls with detector disabled, got %d", calls)
	}
}

// TestReplicationSystem_BlinkDetector_GCStaleEntries verifies that entries
// older than the window are GC'd and do not fire on a much-later respawn.
func TestReplicationSystem_BlinkDetector_GCStaleEntries(t *testing.T) {
	h := newBlinkHarness(t, 5)

	// Tick 1: spawn.
	ent := h.spawnVisible(77)
	h.tick()

	// Tick 2: remove.
	h.currentTick = 2
	h.removeEntity(ent, 77)
	h.tick()

	// Tick 10: GC pass should drop the entry (10-2=8 > window=5).
	h.currentTick = 10
	h.tick() // no visible entity — just a GC pass

	// Tick 11: respawn. Entry should be gone → no blink.
	h.currentTick = 11
	_ = h.spawnVisible(77)
	h.tick()

	if len(h.blinkCalls) != 0 {
		t.Fatalf("expected 0 blink calls after GC, got %d", len(h.blinkCalls))
	}
}
