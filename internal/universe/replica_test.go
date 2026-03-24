package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func marshalSnap(t *testing.T, snap *game.ReplicaSnapshot) []byte {
	t.Helper()
	data, err := game.MarshalReplicaSnapshot(snap)
	if err != nil {
		t.Fatalf("failed to marshal snapshot: %v", err)
	}
	return data
}

func TestApplyReplicas_CreatesNewEntity(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	fromNodeID := pkguniverse.SectorID(coords.SectorCoord{SX: 1, SY: 0})

	snapshots := [][]byte{
		marshalSnap(t, &game.ReplicaSnapshot{
			NetworkID:  42,
			EntityType: gamecomp.TypeShip,
			Position:   comp.Position{X: 100, Y: 200},
			Sector:     comp.SectorCoord{SX: 1, SY: 0},
			Velocity:   comp.Velocity{X: 1, Y: 2},
			Rotation:   comp.Rotation{Angle: 0.5},
			Collider:   comp.Collider{Radius: 2},
		}),
	}

	node.World.ApplyReplicas(snapshots, fromNodeID)

	// Verify entity was created — get adapter's replicaNetIDs
	adapter := node.World.(*gameWorldAdapter)
	entity, ok := adapter.replicaNetIDs[42]
	if !ok {
		t.Fatal("expected replica entity to be tracked in replicaNetIDs")
	}
	if !gw.ECS.Alive(entity) {
		t.Fatal("expected replica entity to be alive")
	}

	// Verify translated position: offset = (1-0)*SectorSize = 8192
	pos := gw.C.Position.Get(entity)
	expectedX := float32(100) + coords.SectorSize
	expectedY := float32(200)
	if pos.X != expectedX || pos.Y != expectedY {
		t.Fatalf("expected position (%.0f, %.0f), got (%.0f, %.0f)", expectedX, expectedY, pos.X, pos.Y)
	}

	// Verify Replica component
	if !gw.C.Replica.HasAll(entity) {
		t.Fatal("expected Replica component on entity")
	}
	rep := gw.C.Replica.Get(entity)
	if rep.TTL != 30 {
		t.Fatalf("expected TTL=30, got %d", rep.TTL)
	}
	if rep.SourceNodeID != fromNodeID {
		t.Fatalf("expected SourceNodeID=%s, got %s", fromNodeID, rep.SourceNodeID)
	}
}

func TestApplyReplicas_UpdatesExisting(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)
	fromNodeID := pkguniverse.SectorID(coords.SectorCoord{SX: 1, SY: 0})

	snap1 := [][]byte{
		marshalSnap(t, &game.ReplicaSnapshot{
			NetworkID:  99,
			EntityType: gamecomp.TypeShip,
			Position:   comp.Position{X: 100, Y: 100},
			Sector:     comp.SectorCoord{SX: 1, SY: 0},
			Velocity:   comp.Velocity{},
			Rotation:   comp.Rotation{},
			Collider:   comp.Collider{Radius: 1},
		}),
	}

	node.World.ApplyReplicas(snap1, fromNodeID)

	adapter := node.World.(*gameWorldAdapter)
	entity := adapter.replicaNetIDs[99]

	// Manually decrement TTL to verify reset
	rep := gw.C.Replica.Get(entity)
	rep.TTL = 5

	// Apply second snapshot with updated position
	snap2 := [][]byte{
		marshalSnap(t, &game.ReplicaSnapshot{
			NetworkID:  99,
			EntityType: gamecomp.TypeShip,
			Position:   comp.Position{X: 200, Y: 300},
			Sector:     comp.SectorCoord{SX: 1, SY: 0},
			Velocity:   comp.Velocity{X: 10, Y: 20},
			Rotation:   comp.Rotation{Angle: 1.0},
			Collider:   comp.Collider{Radius: 1},
		}),
	}

	node.World.ApplyReplicas(snap2, fromNodeID)

	// Verify position updated (translated)
	pos := gw.C.Position.Get(entity)
	expectedX := float32(200) + coords.SectorSize
	expectedY := float32(300)
	if pos.X != expectedX || pos.Y != expectedY {
		t.Fatalf("expected position (%.0f, %.0f), got (%.0f, %.0f)", expectedX, expectedY, pos.X, pos.Y)
	}

	// Verify TTL reset to 30
	rep = gw.C.Replica.Get(entity)
	if rep.TTL != 30 {
		t.Fatalf("expected TTL reset to 30, got %d", rep.TTL)
	}

	// Verify velocity updated
	vel := gw.C.Velocity.Get(entity)
	if vel.X != 10 || vel.Y != 20 {
		t.Fatalf("expected velocity (10, 20), got (%.0f, %.0f)", vel.X, vel.Y)
	}
}

func TestExpireReplicas_RemovesExpired(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)
	adapter := node.World.(*gameWorldAdapter)

	// Manually create a replica entity
	entity := gw.C.ReplicaMapper.NewEntity(
		&comp.Position{X: 100, Y: 100},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: 55},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)
	gw.C.Replica.Add(entity, &comp.Replica{
		SourceNodeID: "node_1_0",
		SourceNetID:  55,
		TTL:          1,
	})
	adapter.replicaNetIDs[55] = entity

	// First call: TTL decrements from 1 to 0, entity marked for removal
	node.World.ExpireReplicas()

	// Verify it was cleaned up from replicaNetIDs
	if _, ok := adapter.replicaNetIDs[55]; ok {
		t.Fatal("expected replica to be removed from replicaNetIDs")
	}

	// Entity is marked for removal but not yet flushed (needs FlushRemovals)
	gw.FlushRemovals(func(e ecs.Entity) (uint32, bool) {
		return 0, false
	})
}

func TestScanBorderEntities_NearEdge(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	// Set up a neighbor to the east (1, 0)
	eastNode := newTestNode(coords.SectorCoord{SX: 1, SY: 0})
	node.Neighbors[eastNode.ID] = eastNode

	aoiRadius := gw.Config.AoIRadius

	// Create entity near the east edge
	nearEdgeX := coords.SectorSize - aoiRadius/2
	gw.C.ReplicaMapper.NewEntity(
		&comp.Position{X: nearEdgeX, Y: 500},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: gw.NextNetID()},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)

	// Build neighbor info
	neighbors := map[string]pkguniverse.NeighborInfo{
		eastNode.ID: {NodeID: eastNode.ID, DX: 1, DY: 0},
	}

	result := node.World.ScanBorderEntities(neighbors)

	snaps, ok := result[eastNode.ID]
	if !ok || len(snaps) == 0 {
		t.Fatalf("expected snapshot sent to east neighbor %s, got none", eastNode.ID)
	}

	// Unmarshal and check position
	snap, err := game.UnmarshalReplicaSnapshot(snaps[0])
	if err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}
	if snap.Position.X != nearEdgeX {
		t.Fatalf("expected position X=%.0f, got %.0f", nearEdgeX, snap.Position.X)
	}
}

func TestScanBorderEntities_Center(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	gw := testGW(node)

	// Set up neighbors in all directions
	neighbors := make(map[string]pkguniverse.NeighborInfo)
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			if dx == 0 && dy == 0 {
				continue
			}
			neighbor := newTestNode(coords.SectorCoord{SX: dx, SY: dy})
			node.Neighbors[neighbor.ID] = neighbor
			neighbors[neighbor.ID] = pkguniverse.NeighborInfo{NodeID: neighbor.ID, DX: dx, DY: dy}
		}
	}

	// Create entity in the center of the sector
	centerX := coords.SectorSize / 2
	centerY := coords.SectorSize / 2
	gw.C.ReplicaMapper.NewEntity(
		&comp.Position{X: centerX, Y: centerY},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: 1},
		&comp.NetworkID{ID: gw.NextNetID()},
		&comp.EntityKind{Type: gamecomp.TypeShip},
	)

	result := node.World.ScanBorderEntities(neighbors)

	total := 0
	for _, snaps := range result {
		total += len(snaps)
	}
	if total != 0 {
		t.Fatalf("expected no snapshots for center entity, got %d", total)
	}
}
