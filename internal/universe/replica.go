package universe

import (
	"log"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
)

// ScanBorderEntities finds entities near sector edges and groups snapshots
// by which neighbor node should receive them.
// margin is the AoI radius — entities within this distance of an edge are replicated.
func ScanBorderEntities(node *Node) map[string][]game.ReplicaSnapshot {
	gw := node.World
	margin := gw.Config.AoIRadius
	size := coords.SectorSize

	// Build a lookup from relative direction (dx,dy) → neighborNodeID
	dirToNeighbor := make(map[[2]int32]string, len(node.Neighbors))
	for nID, neighbor := range node.Neighbors {
		dx := neighbor.Sector.SX - node.Sector.SX
		dy := neighbor.Sector.SY - node.Sector.SY
		dirToNeighbor[[2]int32{dx, dy}] = nID
	}

	result := make(map[string][]game.ReplicaSnapshot)

	filter := ecs.NewFilter4[component.Position, component.NetworkID, component.EntityKind, component.Collider](gw.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	query := filter.Query()
	for query.Next() {
		pos, netID, kind, collider := query.Get()

		// Determine which edges this entity is near
		nearLeft := pos.X < margin
		nearRight := pos.X > (size - margin)
		nearBottom := pos.Y < margin
		nearTop := pos.Y > (size - margin)

		if !nearLeft && !nearRight && !nearBottom && !nearTop {
			continue
		}

		snap := game.ReplicaSnapshot{
			NetworkID:  netID.ID,
			EntityType: kind.Type,
			Position:   *pos,
			Sector:     component.SectorCoord{SX: node.Sector.SX, SY: node.Sector.SY},
			Velocity:   component.Velocity{},
			Rotation:   component.Rotation{},
			Collider:   *collider,
		}

		entity := query.Entity()

		if gw.VelocityMap.HasAll(entity) {
			v := gw.VelocityMap.Get(entity)
			snap.Velocity = *v
		}
		if gw.RotationMap.HasAll(entity) {
			r := gw.RotationMap.Get(entity)
			snap.Rotation = *r
		}
		if gw.HealthMap.HasAll(entity) {
			h := gw.HealthMap.Get(entity)
			hCopy := *h
			snap.Health = &hCopy
		}
		if gw.ShieldMap.HasAll(entity) {
			s := gw.ShieldMap.Get(entity)
			sCopy := *s
			snap.Shield = &sCopy
		}
		if gw.MinableMap.HasAll(entity) {
			m := gw.MinableMap.Get(entity)
			mCopy := *m
			snap.Minable = &mCopy
		}

		// Send to cardinal neighbors
		if nearLeft {
			if nID, ok := dirToNeighbor[[2]int32{-1, 0}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}
		if nearRight {
			if nID, ok := dirToNeighbor[[2]int32{1, 0}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}
		if nearBottom {
			if nID, ok := dirToNeighbor[[2]int32{0, -1}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}
		if nearTop {
			if nID, ok := dirToNeighbor[[2]int32{0, 1}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}

		// Diagonal neighbors for corner entities
		if nearLeft && nearBottom {
			if nID, ok := dirToNeighbor[[2]int32{-1, -1}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}
		if nearLeft && nearTop {
			if nID, ok := dirToNeighbor[[2]int32{-1, 1}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}
		if nearRight && nearBottom {
			if nID, ok := dirToNeighbor[[2]int32{1, -1}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}
		if nearRight && nearTop {
			if nID, ok := dirToNeighbor[[2]int32{1, 1}]; ok {
				result[nID] = append(result[nID], snap)
			}
		}
	}

	return result
}

// SendReplicas scans border entities and sends replica snapshots to neighboring nodes.
func SendReplicas(node *Node) {
	snapsByNeighbor := ScanBorderEntities(node)
	for neighborID, snaps := range snapsByNeighbor {
		if neighbor, ok := node.Neighbors[neighborID]; ok {
			neighbor.Inbox <- NodeMessage{
				Type:       MsgReplica,
				FromNodeID: node.ID,
				Replicas:   snaps,
			}
		}
	}
	// Debug: log every tick if there are border entities
	total := 0
	for _, snaps := range snapsByNeighbor {
		total += len(snaps)
	}
	if total > 0 {
		log.Printf("[%s] replicate: sent %d snapshots to %d neighbors", node.ID, total, len(snapsByNeighbor))
	}
}

// ApplyReplicas creates or updates replica entities from incoming snapshots.
func ApplyReplicas(node *Node, snapshots []game.ReplicaSnapshot, fromNodeID string) {
	gw := node.World

	for i := range snapshots {
		snap := &snapshots[i]

		// Translate coordinates to receiver's local space
		offsetX := float32(snap.Sector.SX-gw.Sector.SX) * coords.SectorSize
		offsetY := float32(snap.Sector.SY-gw.Sector.SY) * coords.SectorSize
		localX := snap.Position.X + offsetX
		localY := snap.Position.Y + offsetY

		if existing, ok := node.ReplicaNetIDs[snap.NetworkID]; ok && gw.ECS.Alive(existing) {
			// Update existing replica
			if gw.PositionMap.HasAll(existing) {
				pos := gw.PositionMap.Get(existing)
				pos.X = localX
				pos.Y = localY
			}
			if gw.VelocityMap.HasAll(existing) {
				vel := gw.VelocityMap.Get(existing)
				*vel = snap.Velocity
			}
			if gw.RotationMap.HasAll(existing) {
				rot := gw.RotationMap.Get(existing)
				*rot = snap.Rotation
			}
			// Reset TTL
			if gw.ReplicaMap.HasAll(existing) {
				rep := gw.ReplicaMap.Get(existing)
				rep.TTL = 30
			}
			// Update health/shield if present
			if snap.Health != nil && gw.HealthMap.HasAll(existing) {
				h := gw.HealthMap.Get(existing)
				*h = *snap.Health
			}
			if snap.Shield != nil && gw.ShieldMap.HasAll(existing) {
				s := gw.ShieldMap.Get(existing)
				*s = *snap.Shield
			}
		} else {
			// Create new replica entity
			entity := gw.ReplicaMapper.NewEntity(
				&component.Position{X: localX, Y: localY},
				&snap.Velocity,
				&snap.Rotation,
				&snap.Collider,
				&component.NetworkID{ID: snap.NetworkID},
				&component.EntityKind{Type: snap.EntityType},
			)

			// Set SectorCoord to the receiving node's sector (position is already translated)
			gw.SectorCoordMap.Add(entity, &component.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})

			gw.ReplicaMap.Add(entity, &component.Replica{
				SourceNodeID: fromNodeID,
				SourceNetID:  snap.NetworkID,
				TTL:          30,
			})

			// Add optional components
			if snap.Health != nil {
				gw.HealthMap.Add(entity, snap.Health)
			}
			if snap.Shield != nil {
				gw.ShieldMap.Add(entity, snap.Shield)
			}
			if snap.Minable != nil {
				gw.MinableMap.Add(entity, snap.Minable)
			}

			node.ReplicaNetIDs[snap.NetworkID] = entity
			gw.Log.Log(game.CatReplica, "created replica netID=%d type=%d from=%s at (%.0f,%.0f)",
				snap.NetworkID, snap.EntityType, fromNodeID, localX, localY)
		}
	}
}

// ExpireReplicas decrements TTL on all replica entities and removes expired ones.
func ExpireReplicas(node *Node) {
	gw := node.World
	filter := ecs.NewFilter1[component.Replica](gw.ECS)
	var expired []ecs.Entity

	query := filter.Query()
	for query.Next() {
		rep := query.Get()
		rep.TTL--
		if rep.TTL <= 0 {
			expired = append(expired, query.Entity())
		}
	}

	for _, e := range expired {
		if gw.ECS.Alive(e) {
			netID := uint32(0)
			if gw.ReplicaMap.HasAll(e) {
				rep := gw.ReplicaMap.Get(e)
				netID = rep.SourceNetID
				delete(node.ReplicaNetIDs, netID)
			}
			gw.MarkForRemoval(e)
			gw.Log.Log(game.CatReplica, "replica expired: netID=%d (TTL reached 0)", netID)
		}
	}
}
