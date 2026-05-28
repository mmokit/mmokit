package universe

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
)

// buildWireEntry encodes a border-frame entry's DeltaBuf in the exact
// format BorderDispatcher.Build produces (28 bytes):
//
//	[4] worldX        float32 LE
//	[4] worldY        float32 LE
//	[4] radius        float32 LE
//	[2] qvx           int16 LE
//	[2] qvy           int16 LE
//	[2] qangle        uint16 LE (zero — see buildWireEntryAtAngle for non-zero)
//	[8] producedAtMs  uint64 LE (zero — see buildWireEntryAt for non-zero)
//	[2] componentCount zero
func buildWireEntry(worldX, worldY, radius, vx, vy float32) []byte {
	return buildWireEntryFull(worldX, worldY, radius, vx, vy, 0, 0)
}

// buildWireEntryAt is buildWireEntry with an explicit producedAtMs stamp —
// used by round-trip tests that assert the stamp propagates through to
// Replica.ProducedAtMs.
func buildWireEntryAt(worldX, worldY, radius, vx, vy float32, producedAtMs uint64) []byte {
	return buildWireEntryFull(worldX, worldY, radius, vx, vy, 0, producedAtMs)
}

// buildWireEntryFull is the underlying encoder accepting both rotation
// angle and producedAtMs. Used by the rotation-replication regression
// tests; all other test entries default angle=0 / producedAtMs=0 via
// the wrappers above.
func buildWireEntryFull(worldX, worldY, radius, vx, vy, angle float32, producedAtMs uint64) []byte {
	buf := make([]byte, 0, 28)
	buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(worldX))
	buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(worldY))
	buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(radius))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(quantizeVelI16(vx, 2000)))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(quantizeVelI16(vy, 2000)))
	buf = binary.LittleEndian.AppendUint16(buf, quantize.Angle(angle))
	buf = binary.LittleEndian.AppendUint64(buf, producedAtMs)
	buf = append(buf, 0, 0)
	return buf
}

// newTestWorldBase creates a Stage at the given cell with a fresh
// engine and no bridge. Used by ApplyBorderFrame tests to assert on
// created replica entities.
func newTestWorldBase(t *testing.T, cell CellID) *Stage {
	t.Helper()
	coords.SetCellSize(1024)
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), log)
	return NewStage(eng, cell, 300, nil)
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
	// Truncated DeltaBuf (< 28 bytes) must be silently skipped, not panic.
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
	// Replace the 2-byte zero componentCount with the real count + slices.
	// buildWireEntry produces 28 bytes: [26 fixed header][2 zero count].
	buf = buf[:26] // drop the zero count
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
	w := base.ECSWorld()
	healthMap := ecs.NewMap1[testReplicaComponent](w)
	def := EntityKindDef{Kind: 5, Name: "TestShip"}
	KindComponentByID(&def, w, ecs.ComponentID[testReplicaComponent](w), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
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
	w := base.ECSWorld()
	compMap := ecs.NewMap1[testReplicaComponent](w)
	def := EntityKindDef{Kind: 7, Name: "TestShip"}
	KindComponentByID(&def, w, ecs.ComponentID[testReplicaComponent](w), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
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

// TestApplyBorderFrame_LegacyZeroPaddingBackwardCompat verifies that
// header-only entries (componentCount = 0) still decode cleanly — the
// per-component loop is a no-op and the replica keeps its
// EnsureEntityKindComponents zero defaults.
func TestApplyBorderFrame_LegacyZeroPaddingBackwardCompat(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	w := base.ECSWorld()
	compMap := ecs.NewMap1[testReplicaComponent](w)
	def := EntityKindDef{Kind: 8, Name: "LegacyShip"}
	KindComponentByID(&def, w, ecs.ComponentID[testReplicaComponent](w), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
	base.RegisterEntityKind(def)

	// Build a header-only entry (no component tail beyond the count=0 field).
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
		t.Fatal("replica entity not created from header-only entry")
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

	w := base.ECSWorld()
	compMap := ecs.NewMap1[testReplicaComponent](w)
	def := EntityKindDef{Kind: 9, Name: "Ship"}
	KindComponentByID(&def, w, ecs.ComponentID[testReplicaComponent](w), reflect.TypeFor[testReplicaComponent](), KindComponentRequired)
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

	w := base.ECSWorld()
	compMap := ecs.NewMap1[testReplicaComponent](w)
	def := EntityKindDef{Kind: 10, Name: "Ship"}
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

// TestApplyBorderFrame_InterestSetDiffRemovesDropped verifies the
// primary fix for the stranded-replica bug: when a source cell's next
// frame omits a netID that was in its previous frame, the receiver
// removes the replica immediately without waiting for the TTL fallback.
func TestApplyBorderFrame_InterestSetDiffRemovesDropped(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	// Tick 1: source pushes three entities A, B, C.
	tick1 := replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 1, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0)},
		{NetID: replication.NetID{ID: 2, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1150, 500, 10, 0, 0)},
		{NetID: replication.NetID{ID: 3, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1200, 500, 10, 0, 0)},
	}}
	base.ApplyBorderFrame(tick1, "src_a")
	for _, id := range []uint32{1, 2, 3} {
		if _, ok := base.replicaNetIDs[id]; !ok {
			t.Fatalf("tick1: replica netID=%d missing", id)
		}
	}

	// Tick 2: source's push set shrinks to just A and B. C dropped out.
	tick2 := replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 1, Epoch: 2}, Kind: 1, DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0)},
		{NetID: replication.NetID{ID: 2, Epoch: 2}, Kind: 1, DeltaBuf: buildWireEntry(1150, 500, 10, 0, 0)},
	}}
	base.ApplyBorderFrame(tick2, "src_a")

	if _, ok := base.replicaNetIDs[3]; ok {
		t.Error("tick2: netID=3 should have been removed by interest-set diff")
	}
	for _, id := range []uint32{1, 2} {
		if _, ok := base.replicaNetIDs[id]; !ok {
			t.Errorf("tick2: surviving netID=%d was incorrectly removed", id)
		}
	}
}

// TestApplyBorderFrame_InterestSetDiffEmptyFrameClears verifies that an
// empty frame (sender has zero push-set entries for this neighbor) is
// treated as an authoritative "clear my interest set" signal and
// removes every replica the receiver had from that source. Before the
// diff protocol, empty frames were suppressed on the sender and the
// receiver relied on TTL decay.
func TestApplyBorderFrame_InterestSetDiffEmptyFrameClears(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 10, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0)},
		{NetID: replication.NetID{ID: 11, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1150, 500, 10, 0, 0)},
	}}, "src_b")
	if len(base.replicaNetIDs) != 2 {
		t.Fatalf("pre-condition: expected 2 replicas, got %d", len(base.replicaNetIDs))
	}

	base.ApplyBorderFrame(replication.Frame{Entries: nil}, "src_b")

	if len(base.replicaNetIDs) != 0 {
		t.Errorf("empty frame did not clear replicas: remaining=%v", base.replicaNetIDs)
	}
}

// TestApplyBorderFrame_InterestSetDiffSelfHealingAfterDrop verifies the
// self-healing property the SpatialOS-style snapshot-diff protocol
// gives us: even if one frame is dropped entirely, the diff on the
// next frame still correctly removes entities that left the push set
// during the gap. This is why we don't need an ack/retransmit layer.
func TestApplyBorderFrame_InterestSetDiffSelfHealingAfterDrop(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	// Tick 1 delivered: A,B,C pushed.
	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 1, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0)},
		{NetID: replication.NetID{ID: 2, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1150, 500, 10, 0, 0)},
		{NetID: replication.NetID{ID: 3, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1200, 500, 10, 0, 0)},
	}}, "src_c")

	// Tick 2 is dropped (simulated — never delivered).

	// Tick 3 delivered: set is now just A. B and C have dropped out.
	// The diff is against tick 1's snapshot, not the lost tick 2, so
	// B and C are correctly removed on this single frame.
	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 1, Epoch: 3}, Kind: 1, DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0)},
	}}, "src_c")

	if _, ok := base.replicaNetIDs[1]; !ok {
		t.Error("self-heal: netID=1 was incorrectly removed")
	}
	if _, ok := base.replicaNetIDs[2]; ok {
		t.Error("self-heal: netID=2 should have been removed via diff")
	}
	if _, ok := base.replicaNetIDs[3]; ok {
		t.Error("self-heal: netID=3 should have been removed via diff")
	}
}

// TestApplyBorderFrame_InterestSetDiffIsolatesSources verifies that the
// diff is per-source: a frame from src_x should not remove replicas
// the receiver holds from src_y. Prevents cross-talk when two cells are
// pushing independent sets of entities to the same neighbor.
func TestApplyBorderFrame_InterestSetDiffIsolatesSources(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 100, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0)},
	}}, "src_x")
	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 200, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1150, 500, 10, 0, 0)},
	}}, "src_y")

	// src_x pushes a different entity. Its diff removes netID 100.
	base.ApplyBorderFrame(replication.Frame{Entries: []replication.FrameEntry{
		{NetID: replication.NetID{ID: 101, Epoch: 1}, Kind: 1, DeltaBuf: buildWireEntry(1100, 500, 10, 0, 0)},
	}}, "src_x")

	// 100 is gone (diff'd out by src_x). 200 must still be present —
	// it was pushed by src_y, which never sent a second frame.
	if _, ok := base.replicaNetIDs[100]; ok {
		t.Error("src_x second frame should have diff-removed netID=100")
	}
	if _, ok := base.replicaNetIDs[200]; !ok {
		t.Error("src_y's netID=200 was incorrectly removed by unrelated src_x frame")
	}
}

// TestApplyBorderFrame_ProducedAtMsRoundTrip verifies the F1 contract:
// the source cell's ClusterClock.Now() is stamped into the per-entity
// DeltaBuf at frame-build time, the destination cell reads it back out
// of the frame, and upsertBorderReplica caches the stamp on
// Replica.ProducedAtMs. Because the stamp is passed opaquely through
// the wire format, outbound replication from the destination cell can
// relay it unchanged — downstream clients see one coherent timeline
// regardless of how many cells the entity's state passed through.
func TestApplyBorderFrame_ProducedAtMsRoundTrip(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})

	// Build a frame that carries an explicit producedAtMs stamp through
	// the fixed 28-byte header. worldX=1100 places the entity just inside
	// the receiver's cell (cellSize=1024, localX=76).
	const wantStamp uint64 = 1_742_000_000_123
	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 777, Epoch: 1},
				Kind:     1,
				DeltaBuf: buildWireEntryAt(1100, 500, 10, 0, 0, wantStamp),
			},
		},
	}
	base.ApplyBorderFrame(frame, "source_cell")

	ent, ok := base.replicaNetIDs[777]
	if !ok {
		t.Fatal("replica not created")
	}
	replicaMap := ecs.NewMap1[component.Replica](base.ECSWorld())
	rep := replicaMap.Get(ent)
	if rep.ProducedAtMs != wantStamp {
		t.Fatalf("Replica.ProducedAtMs = %d, want %d (stamp did not round-trip through border frame)",
			rep.ProducedAtMs, wantStamp)
	}

	// Re-apply with a newer epoch and a later stamp — the replica-update
	// branch must also refresh ProducedAtMs (not just leave the original).
	const nextStamp uint64 = 1_742_000_000_999
	frame2 := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 777, Epoch: 2},
				Kind:     1,
				DeltaBuf: buildWireEntryAt(1150, 500, 10, 0, 0, nextStamp),
			},
		},
	}
	base.ApplyBorderFrame(frame2, "source_cell")
	rep = replicaMap.Get(ent)
	if rep.ProducedAtMs != nextStamp {
		t.Fatalf("Replica.ProducedAtMs after refresh = %d, want %d (update branch failed to refresh stamp)",
			rep.ProducedAtMs, nextStamp)
	}
}

// TestBorderDispatcher_StampsClusterClockTickTime verifies the encoder
// half of F1: BorderDispatcher's Build closure writes the cluster
// clock's tick-aligned time (ClusterClock.TickTime) into bytes [18:26]
// of the per-entity DeltaBuf (qangle occupies bytes [16:18]). Pairs
// with the round-trip test above to pin both sides of the codec.
func TestBorderDispatcher_StampsClusterClockTickTime(t *testing.T) {
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024)
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	// Pin a deterministic clock value so the assertion is exact.
	const stamp uint64 = 5_000_000_042
	base.clusterClock = NewClusterClock()
	base.clusterClock.Observe(stamp, 1)
	// Preempt local-wall drift: observeAt sets offset=coord-local, and
	// Now() adds that offset back to a fresh local read. The stamp is
	// then quantized down to the nearest tickInterval by TickTime (up
	// to -49 ms at 20 Hz), so the tolerance accommodates both that
	// quantization and sub-ms wall-clock drift.

	world := base.ECSWorld()
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

	bd := NewBorderDispatcher(base, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, 1, 1)
	nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, nil, nil, nil)
	nv.SetDirection(1, 1)

	got := bd.disp.Walk(nv, 5, bd.candidatesFor(nv, 5))
	if len(got.Entries) != 1 {
		t.Fatalf("expected 1 border entry, got %d", len(got.Entries))
	}
	buf := got.Entries[0].DeltaBuf
	if len(buf) < 26 {
		t.Fatalf("DeltaBuf too short: %d bytes", len(buf))
	}
	producedAtMs := binary.LittleEndian.Uint64(buf[18:26])
	diff := int64(producedAtMs) - int64(stamp)
	if diff < -100 || diff > 100 {
		t.Fatalf("producedAtMs=%d not within 100ms of observed stamp=%d (diff=%d ms)",
			producedAtMs, stamp, diff)
	}
}

// TestApplyBorderFrame_RotationRoundTrip verifies that rotation flows
// from the border-frame qangle field into the replica's Rotation
// component on both create and update paths. Regression guard for the
// "ship snaps to face east on every cell crossing" bug — before the
// fix, replicas were created with Rotation=0 and never updated, so
// PromoteReplicaToLive at handoff inherited the zeroed angle.
func TestApplyBorderFrame_RotationRoundTrip(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})
	rotMap := ecs.NewMap1[component.Rotation](base.ECSWorld())

	const wantAngle1 float32 = 1.5708 // ~pi/2 (north)
	frame1 := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 555, Epoch: 1},
				Kind:     1,
				DeltaBuf: buildWireEntryFull(1100, 500, 10, 0, 0, wantAngle1, 0),
			},
		},
	}
	base.ApplyBorderFrame(frame1, "source")
	ent, ok := base.replicaNetIDs[555]
	if !ok {
		t.Fatal("replica not created")
	}
	if !rotMap.HasAll(ent) {
		t.Fatal("replica missing Rotation component")
	}
	if absDiff(rotMap.Get(ent).Angle, wantAngle1) > 0.01 {
		t.Fatalf("create path: replica angle = %.4f, want ~%.4f (qangle round-trip lost rotation)",
			rotMap.Get(ent).Angle, wantAngle1)
	}

	const wantAngle2 float32 = -2.3562 // ~-3pi/4 (sw)
	frame2 := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 555, Epoch: 2},
				Kind:     1,
				DeltaBuf: buildWireEntryFull(1150, 500, 10, 0, 0, wantAngle2, 0),
			},
		},
	}
	base.ApplyBorderFrame(frame2, "source")
	if absDiff(rotMap.Get(ent).Angle, wantAngle2) > 0.01 {
		t.Fatalf("update path: replica angle = %.4f, want ~%.4f (refresh failed to update rotation)",
			rotMap.Get(ent).Angle, wantAngle2)
	}
}

// TestPromoteReplicaToLive_PreservesBorderRotation is the end-to-end
// regression guard for the visible "ship snaps east at cell boundary"
// symptom. A border replica with rotation θ, then promoted via the
// handoff path, must keep angle θ on the now-Live entity. Previously
// rotation never reached the replica → promote path inherited 0 →
// client renders sprite facing east (qangle=0).
func TestPromoteReplicaToLive_PreservesBorderRotation(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 0})
	rotMap := ecs.NewMap1[component.Rotation](base.ECSWorld())

	const wantAngle float32 = 0.7854 // ~pi/4 (ne)
	frame := replication.Frame{
		Entries: []replication.FrameEntry{
			{
				NetID:    replication.NetID{ID: 999, Epoch: 1},
				Kind:     1,
				DeltaBuf: buildWireEntryFull(1100, 500, 10, 100, 50, wantAngle, 0),
			},
		},
	}
	base.ApplyBorderFrame(frame, "source")

	// Promote replica → live (the handoff commit path).
	if err := base.PromoteReplicaToLive(999, 2); err != nil {
		t.Fatalf("PromoteReplicaToLive: %v", err)
	}
	ent, presence, ok := base.LookupNetID(999)
	if !ok || presence != PresenceLive {
		t.Fatalf("post-promote: presence=%v ok=%v, want Live", presence, ok)
	}
	if !rotMap.HasAll(ent) {
		t.Fatal("post-promote entity missing Rotation component")
	}
	if absDiff(rotMap.Get(ent).Angle, wantAngle) > 0.01 {
		t.Fatalf("post-promote angle = %.4f, want ~%.4f (rotation lost across handoff — would visibly snap)",
			rotMap.Get(ent).Angle, wantAngle)
	}
}

