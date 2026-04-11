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

// testReplicaComponent is a tagged component used to verify that
// EnsureEntityKindComponents auto-fills kind-registered components on
// border replicas, and that Option A's registry-driven component tail
// correctly round-trips component data across the wire.
type testReplicaComponent struct {
	Health float32
	Shield float32
}

// appendEntryWithComponents encodes a border-frame entry with a
// componentCount + length-prefixed tail matching the format that
// BorderDispatcher.scanEntityComponents produces.
func appendEntryWithComponents(worldX, worldY, radius, vx, vy float32, comps []struct {
	ID   uint16
	Data []byte
}) []byte {
	buf := buildWireEntry(worldX, worldY, radius, vx, vy)
	// Replace the 2-byte padding with the real component count + slices.
	buf = buf[:16] // drop the zero padding
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(comps)))
	for _, c := range comps {
		buf = binary.LittleEndian.AppendUint16(buf, c.ID)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(c.Data)))
		buf = append(buf, c.Data...)
	}
	return buf
}

// TestApplyBorderFrame_AutoFillsKindComponents verifies that a newly
// created border replica has all components from its EntityKindDef
// auto-filled with zero values. This is the mechanism that lets
// AutoReplicator's reflectBinding safely hash/snapshot replicas:
// reflectBinding panics on missing required components, and the border
// frame wire format only carries position/velocity/radius. Without
// auto-fill, replicas of rich entity kinds (ships with Health, Shield,
// etc.) panic the client dispatcher on the first hash call.
func TestApplyBorderFrame_AutoFillsKindComponents(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	// Register an entity kind that declares a test component.
	healthMap := ecs.NewMap1[testReplicaComponent](base.ECSWorld())
	def := EntityKindDef{Kind: 5, Name: "TestShip"}
	KindComponent(&def, healthMap)
	base.RegisterEntityKind(def)

	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 100, Epoch: 1},
				Kind:     5,
				DeltaBuf: buildWireEntry(1100, 500, 20, 0, 0),
			},
		},
	}
	base.ApplyBorderFrame(frame, "source_node")

	ent, ok := base.replicaNetIDs[100]
	if !ok {
		t.Fatal("replica entity not created")
	}

	// The registered component must be present on the replica entity,
	// auto-filled with zero values by EnsureEntityKindComponents.
	if !healthMap.HasAll(ent) {
		t.Fatal("replica is missing kind-registered component testReplicaComponent — EnsureEntityKindComponents not called in upsertBorderReplica")
	}
	c := healthMap.Get(ent)
	if c.Health != 0 || c.Shield != 0 {
		t.Fatalf("auto-filled component should be zero-valued, got %+v", *c)
	}
}

// TestApplyBorderFrame_AppliesComponentTail verifies that per-component
// data carried in the border frame's length-prefixed tail is applied
// onto the replica entity (not just zero-filled). This is the Option A
// round-trip: sender scans Health+Shield values, receiver decodes them
// and calls ComponentReplicator.Apply to overwrite the defaults.
func TestApplyBorderFrame_AppliesComponentTail(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	// Register a component with default reflection-based marshal and a
	// kind that uses it. ReplicationRegistry auto-assigns ID 1 to the
	// first registered component.
	compMap := ecs.NewMap1[testReplicaComponent](base.ECSWorld())
	def := EntityKindDef{Kind: 7, Name: "TestShip"}
	KindComponent(&def, compMap)
	base.RegisterEntityKind(def)

	// Scan the wire format that the sender would produce for a
	// component with Health=99, Shield=50 — use the registry's Scan
	// closure so we go through the exact same codec on both ends.
	compID := uint16(1) // first registered in this world's registry
	wireData := base.ReplicationRegistry().Get(ComponentID(compID)).Scan
	// Stash a temporary entity just to harvest the serialized bytes.
	stash := base.ECSWorld().NewEntity()
	compMap.Add(stash, &testReplicaComponent{Health: 99, Shield: 50})
	serialized := wireData(stash)
	base.ECSWorld().RemoveEntity(stash)
	if len(serialized) == 0 {
		t.Fatal("Scan returned empty bytes for a present component")
	}

	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID: replication.NetID{ID: 200, Epoch: 1},
				Kind:  7,
				DeltaBuf: appendEntryWithComponents(1100, 500, 20, 0, 0, []struct {
					ID   uint16
					Data []byte
				}{
					{ID: compID, Data: serialized},
				}),
			},
		},
	}
	base.ApplyBorderFrame(frame, "source_node")

	ent, ok := base.replicaNetIDs[200]
	if !ok {
		t.Fatal("replica entity not created")
	}
	if !compMap.HasAll(ent) {
		t.Fatal("replica missing testReplicaComponent")
	}
	got := compMap.Get(ent)
	if got.Health != 99 || got.Shield != 50 {
		t.Fatalf("component tail not applied: got %+v, want {Health:99 Shield:50}", *got)
	}
}

// TestApplyBorderFrame_LegacyZeroPaddingBackwardCompat verifies that old
// 18-byte entries ending in zero padding (the pre-Option-A wire format)
// still decode cleanly: the trailing 0x00 0x00 reads as componentCount
// = 0 and the per-component loop is a no-op. No component data is
// applied — the replica keeps its EnsureEntityKindComponents zero
// defaults.
func TestApplyBorderFrame_LegacyZeroPaddingBackwardCompat(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	compMap := ecs.NewMap1[testReplicaComponent](base.ECSWorld())
	def := EntityKindDef{Kind: 8, Name: "LegacyShip"}
	KindComponent(&def, compMap)
	base.RegisterEntityKind(def)

	// Build an 18-byte entry via the legacy helper — trailing zero padding.
	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 300, Epoch: 1},
				Kind:     8,
				DeltaBuf: buildWireEntry(1100, 500, 20, 0, 0),
			},
		},
	}
	base.ApplyBorderFrame(frame, "source_node")

	ent, ok := base.replicaNetIDs[300]
	if !ok {
		t.Fatal("replica entity not created from legacy 18-byte entry")
	}
	if !compMap.HasAll(ent) {
		t.Fatal("replica missing auto-filled component")
	}
	got := compMap.Get(ent)
	if got.Health != 0 || got.Shield != 0 {
		t.Fatalf("legacy entry should leave component at zero, got %+v", *got)
	}
}

// TestApplyBorderFrame_UnknownComponentIDSkipped verifies that a
// component ID unknown to the receiver (e.g. a newer game version on
// the sender) is skipped but does not corrupt the decode of subsequent
// components. The length prefix lets the decoder advance past unknown
// data safely.
func TestApplyBorderFrame_UnknownComponentIDSkipped(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	compMap := ecs.NewMap1[testReplicaComponent](base.ECSWorld())
	def := EntityKindDef{Kind: 9, Name: "Ship"}
	KindComponent(&def, compMap)
	base.RegisterEntityKind(def)

	// Build a serialized payload for the known component (ID 1 after
	// registration) and prepend an unknown-ID component.
	compID := uint16(1)
	stash := base.ECSWorld().NewEntity()
	compMap.Add(stash, &testReplicaComponent{Health: 42, Shield: 7})
	serialized := base.ReplicationRegistry().Get(ComponentID(compID)).Scan(stash)
	base.ECSWorld().RemoveEntity(stash)

	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID: replication.NetID{ID: 400, Epoch: 1},
				Kind:  9,
				DeltaBuf: appendEntryWithComponents(1100, 500, 20, 0, 0, []struct {
					ID   uint16
					Data []byte
				}{
					{ID: 9999, Data: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
					{ID: compID, Data: serialized},
				}),
			},
		},
	}
	base.ApplyBorderFrame(frame, "source_node")

	ent, ok := base.replicaNetIDs[400]
	if !ok {
		t.Fatal("replica entity not created")
	}
	got := compMap.Get(ent)
	if got.Health != 42 || got.Shield != 7 {
		t.Fatalf("known component lost when unknown preceded it: got %+v, want {Health:42 Shield:7}", *got)
	}
}

// TestApplyBorderFrame_UpdatesComponentsOnSecondFrame verifies that
// subsequent border frames update the replica's component data (not
// just position/velocity). This is how Health/Shield stay fresh on a
// replica as the sender damages it over time.
func TestApplyBorderFrame_UpdatesComponentsOnSecondFrame(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	compMap := ecs.NewMap1[testReplicaComponent](base.ECSWorld())
	def := EntityKindDef{Kind: 10, Name: "Ship"}
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

	// Frame 1: Health=100, Shield=50.
	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{{
		NetID: replication.NetID{ID: 500, Epoch: 1},
		Kind:  10,
		DeltaBuf: appendEntryWithComponents(1100, 500, 20, 0, 0, []struct {
			ID   uint16
			Data []byte
		}{
			{ID: compID, Data: scan(100, 50)},
		}),
	}}}, "source_node")

	// Frame 2: Health=25, Shield=0 (took damage).
	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{{
		NetID: replication.NetID{ID: 500, Epoch: 2},
		Kind:  10,
		DeltaBuf: appendEntryWithComponents(1150, 500, 20, 0, 0, []struct {
			ID   uint16
			Data []byte
		}{
			{ID: compID, Data: scan(25, 0)},
		}),
	}}}, "source_node")

	ent := base.replicaNetIDs[500]
	got := compMap.Get(ent)
	if got.Health != 25 || got.Shield != 0 {
		t.Fatalf("second frame did not update component data: got %+v, want {Health:25 Shield:0}", *got)
	}
}
