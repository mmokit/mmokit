package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
)

func TestApplyReplicas_CreatesNewEntity(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	// Source node is at (1, 0) — the snapshot position is in the source's local space.
	fromNodeID := SectorID(coords.SectorCoord{SX: 1, SY: 0})

	snapshots := []game.ReplicaSnapshot{
		{
			NetworkID:  42,
			EntityType: component.TypeShip,
			Position:   component.Position{X: 100, Y: 200},
			Sector:     component.SectorCoord{SX: 1, SY: 0},
			Velocity:   component.Velocity{X: 1, Y: 2},
			Rotation:   component.Rotation{Angle: 0.5},
			Collider:   component.Collider{Radius: 2},
		},
	}

	ApplyReplicas(node, snapshots, fromNodeID)

	// Verify entity was created in ReplicaNetIDs
	entity, ok := node.ReplicaNetIDs[42]
	if !ok {
		t.Fatal("expected replica entity to be tracked in ReplicaNetIDs")
	}
	if !node.World.ECS.Alive(entity) {
		t.Fatal("expected replica entity to be alive")
	}

	// Verify translated position: offset = (1-0)*SectorSize = 8192
	pos := node.World.C.Position.Get(entity)
	expectedX := float32(100) + coords.SectorSize
	expectedY := float32(200)
	if pos.X != expectedX || pos.Y != expectedY {
		t.Fatalf("expected position (%.0f, %.0f), got (%.0f, %.0f)", expectedX, expectedY, pos.X, pos.Y)
	}

	// Verify Replica component
	if !node.World.C.Replica.HasAll(entity) {
		t.Fatal("expected Replica component on entity")
	}
	rep := node.World.C.Replica.Get(entity)
	if rep.TTL != 30 {
		t.Fatalf("expected TTL=30, got %d", rep.TTL)
	}
	if rep.SourceNodeID != fromNodeID {
		t.Fatalf("expected SourceNodeID=%s, got %s", fromNodeID, rep.SourceNodeID)
	}
}

func TestApplyReplicas_UpdatesExisting(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})
	fromNodeID := SectorID(coords.SectorCoord{SX: 1, SY: 0})

	snap1 := []game.ReplicaSnapshot{
		{
			NetworkID:  99,
			EntityType: component.TypeShip,
			Position:   component.Position{X: 100, Y: 100},
			Sector:     component.SectorCoord{SX: 1, SY: 0},
			Velocity:   component.Velocity{},
			Rotation:   component.Rotation{},
			Collider:   component.Collider{Radius: 1},
		},
	}

	ApplyReplicas(node, snap1, fromNodeID)

	entity := node.ReplicaNetIDs[99]

	// Manually decrement TTL to verify reset
	rep := node.World.C.Replica.Get(entity)
	rep.TTL = 5

	// Apply second snapshot with updated position
	snap2 := []game.ReplicaSnapshot{
		{
			NetworkID:  99,
			EntityType: component.TypeShip,
			Position:   component.Position{X: 200, Y: 300},
			Sector:     component.SectorCoord{SX: 1, SY: 0},
			Velocity:   component.Velocity{X: 10, Y: 20},
			Rotation:   component.Rotation{Angle: 1.0},
			Collider:   component.Collider{Radius: 1},
		},
	}

	ApplyReplicas(node, snap2, fromNodeID)

	// Verify position updated (translated)
	pos := node.World.C.Position.Get(entity)
	expectedX := float32(200) + coords.SectorSize
	expectedY := float32(300)
	if pos.X != expectedX || pos.Y != expectedY {
		t.Fatalf("expected position (%.0f, %.0f), got (%.0f, %.0f)", expectedX, expectedY, pos.X, pos.Y)
	}

	// Verify TTL reset to 30
	rep = node.World.C.Replica.Get(entity)
	if rep.TTL != 30 {
		t.Fatalf("expected TTL reset to 30, got %d", rep.TTL)
	}

	// Verify velocity updated
	vel := node.World.C.Velocity.Get(entity)
	if vel.X != 10 || vel.Y != 20 {
		t.Fatalf("expected velocity (10, 20), got (%.0f, %.0f)", vel.X, vel.Y)
	}
}

func TestExpireReplicas_RemovesExpired(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	// Manually create a replica entity
	entity := node.World.C.ReplicaMapper.NewEntity(
		&component.Position{X: 100, Y: 100},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: 1},
		&component.NetworkID{ID: 55},
		&component.EntityKind{Type: component.TypeShip},
	)
	node.World.C.Replica.Add(entity, &component.Replica{
		SourceNodeID: "node_1_0",
		SourceNetID:  55,
		TTL:          1,
	})
	node.ReplicaNetIDs[55] = entity

	// First call: TTL decrements from 1 to 0, entity marked for removal
	ExpireReplicas(node)

	// Verify it was cleaned up from ReplicaNetIDs
	if _, ok := node.ReplicaNetIDs[55]; ok {
		t.Fatal("expected replica to be removed from ReplicaNetIDs")
	}

	// Entity is marked for removal but not yet flushed (needs FlushRemovals)
	// Flush so we can check Alive
	node.World.FlushRemovals(func(e ecs.Entity) (uint32, bool) {
		return 0, false
	})
}

func TestScanBorderEntities_NearEdge(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	// Set up a neighbor to the east (1, 0)
	eastNode := newTestNode(coords.SectorCoord{SX: 1, SY: 0})
	node.Neighbors[eastNode.ID] = eastNode

	aoiRadius := node.World.Config.AoIRadius // 100

	// Create entity near the east edge: X close to SectorSize, within AoIRadius
	nearEdgeX := coords.SectorSize - aoiRadius/2 // within margin of east edge
	entity := node.World.C.ReplicaMapper.NewEntity(
		&component.Position{X: nearEdgeX, Y: 500},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: 1},
		&component.NetworkID{ID: node.World.NextNetID()},
		&component.EntityKind{Type: component.TypeShip},
	)
	_ = entity

	result := ScanBorderEntities(node)

	snaps, ok := result[eastNode.ID]
	if !ok || len(snaps) == 0 {
		t.Fatalf("expected snapshot sent to east neighbor %s, got none", eastNode.ID)
	}
	if snaps[0].Position.X != nearEdgeX {
		t.Fatalf("expected position X=%.0f, got %.0f", nearEdgeX, snaps[0].Position.X)
	}
}

func TestScanBorderEntities_Center(t *testing.T) {
	node := newTestNode(coords.SectorCoord{SX: 0, SY: 0})

	// Set up neighbors in all directions
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			if dx == 0 && dy == 0 {
				continue
			}
			neighbor := newTestNode(coords.SectorCoord{SX: dx, SY: dy})
			node.Neighbors[neighbor.ID] = neighbor
		}
	}

	// Create entity in the center of the sector — far from any edge
	centerX := coords.SectorSize / 2
	centerY := coords.SectorSize / 2
	entity := node.World.C.ReplicaMapper.NewEntity(
		&component.Position{X: centerX, Y: centerY},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: 1},
		&component.NetworkID{ID: node.World.NextNetID()},
		&component.EntityKind{Type: component.TypeShip},
	)
	_ = entity

	result := ScanBorderEntities(node)

	total := 0
	for _, snaps := range result {
		total += len(snaps)
	}
	if total != 0 {
		t.Fatalf("expected no snapshots for center entity, got %d", total)
	}
}
