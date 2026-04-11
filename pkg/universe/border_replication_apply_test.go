package universe

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/replication"
)

// buildWireEntry encodes a border-frame entry's DeltaBuf in the exact
// format BorderDispatcher.Build produces (18 bytes):
//
//	[4] worldX  float32 LE
//	[4] worldY  float32 LE
//	[4] radius  float32 LE
//	[2] qvx     int16 LE
//	[2] qvy     int16 LE
//	[2] padding zero
func buildWireEntry(worldX, worldY, radius, vx, vy float32) []byte {
	buf := make([]byte, 0, 18)
	buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(worldX))
	buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(worldY))
	buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(radius))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(quantizeVelI16(vx, 2000)))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(quantizeVelI16(vy, 2000)))
	buf = append(buf, 0, 0)
	return buf
}

// newTestWorldBase creates a WorldBase at the given cell with a fresh
// engine and no bridge. Used by ApplyBorderFrame tests to assert on
// created replica entities.
func newTestWorldBase(t *testing.T, cell CellID) *WorldBase {
	t.Helper()
	coords.SetCellSize(1024)
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), log)
	return NewWorldBase(eng, cell, 300, nil)
}

func TestApplyBorderFrame_CreatesReplica(t *testing.T) {
	// Receiver is at cell (1, 0), cellSize 1024.
	// Sender encodes worldX = 1100 (just inside receiver's cell).
	// Expected local position on receiver: localX = 1100 - 1024 = 76.
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 42, Epoch: 1},
				Kind:     3,
				DeltaBuf: buildWireEntry(1100, 500, 15, 50, -25),
			},
		},
	}

	base.ApplyBorderFrame(frame, "source_node")

	// Replica must now exist in the replica map.
	ent, ok := base.replicaNetIDs[42]
	if !ok {
		t.Fatal("ApplyBorderFrame did not create replica entity for NetID 42")
	}

	// Verify position translated from world-space to receiver-local.
	posMap := ecs.NewMap1[component.Position](base.ECSWorld())
	pos := posMap.Get(ent)
	if pos.X != 76 || pos.Y != 500 {
		t.Fatalf("replica position = (%.1f, %.1f), want (76, 500)", pos.X, pos.Y)
	}

	// Verify velocity round-trip (quantized — allow small tolerance).
	velMap := ecs.NewMap1[component.Velocity](base.ECSWorld())
	vel := velMap.Get(ent)
	if absDiff(vel.X, 50) > 1 {
		t.Fatalf("replica velocity X = %.2f, want ~50", vel.X)
	}
	if absDiff(vel.Y, -25) > 1 {
		t.Fatalf("replica velocity Y = %.2f, want ~-25", vel.Y)
	}

	// Verify radius round-trip (exact — not quantized).
	colliderMap := ecs.NewMap1[component.Collider](base.ECSWorld())
	col := colliderMap.Get(ent)
	if col.Radius != 15 {
		t.Fatalf("replica radius = %.2f, want 15", col.Radius)
	}

	// Verify epoch recorded.
	if base.highestSeenEpoch[42] != 1 {
		t.Fatalf("highestSeenEpoch[42] = %d, want 1", base.highestSeenEpoch[42])
	}
}

func TestApplyBorderFrame_UpdatesExistingReplica(t *testing.T) {
	// Apply twice — second call should update, not create a second entity.
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	f1 := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 7, Epoch: 1},
				Kind:     2,
				DeltaBuf: buildWireEntry(1100, 200, 10, 0, 0),
			},
		},
	}
	base.ApplyBorderFrame(f1, "source")
	ent1, ok := base.replicaNetIDs[7]
	if !ok {
		t.Fatal("initial replica not created")
	}

	// Second update with a moved position and bumped epoch.
	f2 := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 7, Epoch: 2},
				Kind:     2,
				DeltaBuf: buildWireEntry(1200, 250, 10, 100, 0),
			},
		},
	}
	base.ApplyBorderFrame(f2, "source")

	ent2, ok := base.replicaNetIDs[7]
	if !ok {
		t.Fatal("replica gone after second apply")
	}
	if ent1 != ent2 {
		t.Fatal("second ApplyBorderFrame created a new entity instead of updating")
	}

	posMap := ecs.NewMap1[component.Position](base.ECSWorld())
	pos := posMap.Get(ent2)
	if pos.X != 176 || pos.Y != 250 { // 1200 - 1024 = 176
		t.Fatalf("updated position = (%.1f, %.1f), want (176, 250)", pos.X, pos.Y)
	}

	if base.highestSeenEpoch[7] != 2 {
		t.Fatalf("highestSeenEpoch[7] = %d, want 2 after epoch bump", base.highestSeenEpoch[7])
	}
}

func TestApplyBorderFrame_DropsStaleEpoch(t *testing.T) {
	// Send a frame at epoch 5, then a stale frame at epoch 3 — the stale
	// one must not overwrite state.
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	current := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 9, Epoch: 5},
				Kind:     1,
				DeltaBuf: buildWireEntry(1300, 100, 5, 0, 0),
			},
		},
	}
	base.ApplyBorderFrame(current, "source")

	stale := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 9, Epoch: 3},
				Kind:     1,
				DeltaBuf: buildWireEntry(9999, 9999, 999, 0, 0),
			},
		},
	}
	base.ApplyBorderFrame(stale, "source")

	// Position must still reflect the epoch-5 values, not the stale ones.
	ent := base.replicaNetIDs[9]
	posMap := ecs.NewMap1[component.Position](base.ECSWorld())
	pos := posMap.Get(ent)
	if pos.X != 276 { // 1300 - 1024 = 276
		t.Fatalf("stale frame overwrote position: got X=%.1f, want 276", pos.X)
	}
	// Epoch tracking must not rewind.
	if base.highestSeenEpoch[9] != 5 {
		t.Fatalf("stale frame rewound highestSeenEpoch: got %d, want 5", base.highestSeenEpoch[9])
	}
}

func TestApplyBorderFrame_SkipsShortDeltaBuf(t *testing.T) {
	// Truncated DeltaBuf (< 18 bytes) must be silently skipped, not panic.
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 11, Epoch: 1},
				Kind:     1,
				DeltaBuf: []byte{0xAA, 0xBB, 0xCC}, // only 3 bytes
			},
		},
	}
	base.ApplyBorderFrame(frame, "source")

	if _, ok := base.replicaNetIDs[11]; ok {
		t.Fatal("truncated DeltaBuf should not create a replica")
	}
}

func TestApplyBorderFrame_MultipleEntitiesIndependent(t *testing.T) {
	// Two entities in one frame should both land as separate replicas.
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 1, Epoch: 0},
				Kind:     1,
				DeltaBuf: buildWireEntry(100, 200, 5, 0, 0),
			},
			{
				NetID:    replication.NetID{ID: 2, Epoch: 0},
				Kind:     2,
				DeltaBuf: buildWireEntry(300, 400, 10, 0, 0),
			},
		},
	}
	base.ApplyBorderFrame(frame, "source")

	if _, ok := base.replicaNetIDs[1]; !ok {
		t.Fatal("entity 1 not created")
	}
	if _, ok := base.replicaNetIDs[2]; !ok {
		t.Fatal("entity 2 not created")
	}
	if base.replicaNetIDs[1] == base.replicaNetIDs[2] {
		t.Fatal("entities 1 and 2 collapsed into the same replica")
	}
}

func absDiff(a, b float32) float32 {
	if a > b {
		return a - b
	}
	return b - a
}
