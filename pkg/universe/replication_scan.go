package universe

import (
	"encoding/binary"
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
)

// ScanBorderWithRegistry scans for entities near cell boundaries and builds
// ReplicaFrames using the given replication registry. This replaces the
// game-specific hardcoded component checks with a generic, registry-driven approach.
//
// localMinX/minY/maxX/maxY are the cell's bounds in base-cell-local coordinates
// (from CellID.LocalBounds). For depth-0 cells these are [0, baseCellSize).
func ScanBorderWithRegistry(
	world *ecs.World,
	registry *ReplicationRegistry,
	cell CellID,
	cellSize float32,
	localMinX, localMinY, localMaxX, localMaxY float32,
	margin float32,
	neighbors map[string]NeighborInfo,
) map[string][][]byte {
	// Build direction -> nodeIDs lookup. After a cell split, multiple subcell
	// neighbors can share the same direction relative to a larger cell, so
	// each direction maps to a slice of node IDs.
	dirToNeighbors := make(map[[2]int32][]string, len(neighbors))
	for nID, info := range neighbors {
		key := [2]int32{info.DX, info.DY}
		dirToNeighbors[key] = append(dirToNeighbors[key], nID)
	}

	result := make(map[string][][]byte)

	filter := ecs.NewFilter4[component.Position, component.NetworkID, component.EntityKind, component.Collider](world).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy](), ecs.C[component.Dormant]())
	query := filter.Query()
	for query.Next() {
		pos, netID, kind, collider := query.Get()
		entity := query.Entity()

		nearLeft := pos.X < localMinX+margin
		nearRight := pos.X > localMaxX-margin
		nearBottom := pos.Y < localMinY+margin
		nearTop := pos.Y > localMaxY-margin

		if !nearLeft && !nearRight && !nearBottom && !nearTop {
			continue
		}

		// Build the frame with core fields + all registered component data
		frame := &ReplicaFrame{
			NetworkID:  netID.ID,
			EntityType: kind.Type,
			PosX:       pos.X,
			PosY:       pos.Y,
			CellX:      cell.X,
			CellY:      cell.Y,
		}

		// Always include collider in the frame as a registered component would,
		// but collider is part of the base entity creation — we encode it as
		// component ID 0 (reserved for collider).
		frame.Components = append(frame.Components, ComponentSlice{
			ID:   0,
			Data: marshalCollider(collider),
		})

		// Scan all registered optional components
		for _, rep := range registry.All() {
			if data := rep.Scan(entity); data != nil {
				frame.Components = append(frame.Components, ComponentSlice{
					ID:   rep.ID,
					Data: data,
				})
			}
		}

		frameBytes := MarshalReplicaFrame(frame)

		// Send to cardinal neighbors
		if nearLeft {
			for _, nID := range dirToNeighbors[[2]int32{-1, 0}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearRight {
			for _, nID := range dirToNeighbors[[2]int32{1, 0}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearBottom {
			for _, nID := range dirToNeighbors[[2]int32{0, -1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearTop {
			for _, nID := range dirToNeighbors[[2]int32{0, 1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}

		// Diagonal neighbors for corner entities
		if nearLeft && nearBottom {
			for _, nID := range dirToNeighbors[[2]int32{-1, -1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearLeft && nearTop {
			for _, nID := range dirToNeighbors[[2]int32{-1, 1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearRight && nearBottom {
			for _, nID := range dirToNeighbors[[2]int32{1, -1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearRight && nearTop {
			for _, nID := range dirToNeighbors[[2]int32{1, 1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
	}

	return result
}

// ScanBorderProxies scans for entities near cell boundaries and builds lightweight
// ProxySummary messages (~29 bytes each). Unlike ScanBorderWithRegistry, this reads
// only standard pkg/component types — no ReplicationRegistry needed.
// Game devs get proxies for free with zero configuration.
//
// localMinX/minY/maxX/maxY are the cell's bounds in base-cell-local coordinates.
func ScanBorderProxies(
	world *ecs.World,
	cell CellID,
	cellSize float32,
	localMinX, localMinY, localMaxX, localMaxY float32,
	margin float32,
	neighbors map[string]NeighborInfo,
	velScale float32,
) map[string][][]byte {
	dirToNeighbors := make(map[[2]int32][]string, len(neighbors))
	for nID, info := range neighbors {
		key := [2]int32{info.DX, info.DY}
		dirToNeighbors[key] = append(dirToNeighbors[key], nID)
	}

	result := make(map[string][][]byte)

	filter := ecs.NewFilter4[component.Position, component.NetworkID, component.EntityKind, component.Collider](world).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy](), ecs.C[component.Dormant]())
	velMap := ecs.NewMap1[component.Velocity](world)

	query := filter.Query()
	for query.Next() {
		pos, netID, kind, collider := query.Get()
		entity := query.Entity()

		nearLeft := pos.X < localMinX+margin
		nearRight := pos.X > localMaxX-margin
		nearBottom := pos.Y < localMinY+margin
		nearTop := pos.Y > localMaxY-margin

		if !nearLeft && !nearRight && !nearBottom && !nearTop {
			continue
		}

		// Read velocity for dead-reckoning (optional — zero if absent)
		var qvx, qvy int16
		if velMap.HasAll(entity) {
			vel := velMap.Get(entity)
			qvx = quantizeVelI16(vel.X, velScale)
			qvy = quantizeVelI16(vel.Y, velScale)
		}

		summary := &ProxySummary{
			NetworkID:  netID.ID,
			EntityType: kind.Type,
			PosX:       pos.X,
			PosY:       pos.Y,
			CellX:      cell.X,
			CellY:      cell.Y,
			Radius:     collider.Radius,
			QVelX:      qvx,
			QVelY:      qvy,
		}

		frameBytes := MarshalProxySummary(summary)

		if nearLeft {
			for _, nID := range dirToNeighbors[[2]int32{-1, 0}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearRight {
			for _, nID := range dirToNeighbors[[2]int32{1, 0}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearBottom {
			for _, nID := range dirToNeighbors[[2]int32{0, -1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearTop {
			for _, nID := range dirToNeighbors[[2]int32{0, 1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}

		if nearLeft && nearBottom {
			for _, nID := range dirToNeighbors[[2]int32{-1, -1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearLeft && nearTop {
			for _, nID := range dirToNeighbors[[2]int32{-1, 1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearRight && nearBottom {
			for _, nID := range dirToNeighbors[[2]int32{1, -1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
		if nearRight && nearTop {
			for _, nID := range dirToNeighbors[[2]int32{1, 1}] {
				result[nID] = append(result[nID], frameBytes)
			}
		}
	}

	return result
}


// ReplicaApplyContext provides the callbacks needed by ApplyReplicasWithRegistry
// to create and manage replica entities. The game adapter implements this.
type ReplicaApplyContext interface {
	// FindReplica returns the existing replica entity for a network ID, or false.
	FindReplica(netID uint32) (ecs.Entity, bool)
	// CreateReplica creates a new replica entity with the base components
	// (Position, Velocity, Rotation, Collider, NetworkID, EntityKind, CellCoord, Replica).
	// Returns the created entity.
	CreateReplica(frame *ReplicaFrame, localX, localY float32, sourceNodeID string) ecs.Entity
	// UpdateReplicaBase updates the core fields (position, TTL, source node) on an existing replica.
	UpdateReplicaBase(entity ecs.Entity, localX, localY float32, sourceNodeID string)
}

// ApplyReplicasWithRegistry processes replica snapshots using the registry.
// For each frame, it creates or updates the replica entity and applies all
// registered component data.
func ApplyReplicasWithRegistry(
	snapshots [][]byte,
	sourceNodeID string,
	receiverCell CellID,
	cellSize float32,
	registry *ReplicationRegistry,
	ctx ReplicaApplyContext,
) {
	for _, snapBytes := range snapshots {
		frame, err := UnmarshalReplicaFrame(snapBytes)
		if err != nil {
			continue
		}

		// Translate coordinates to receiver's local space
		offsetX := float32(frame.CellX-receiverCell.X) * cellSize
		offsetY := float32(frame.CellY-receiverCell.Y) * cellSize
		localX := frame.PosX + offsetX
		localY := frame.PosY + offsetY

		var entity ecs.Entity
		var isNew bool

		if existing, ok := ctx.FindReplica(frame.NetworkID); ok {
			entity = existing
			ctx.UpdateReplicaBase(entity, localX, localY, sourceNodeID)
		} else {
			entity = ctx.CreateReplica(frame, localX, localY, sourceNodeID)
			isNew = true
		}

		// Apply all component data from the frame via registry
		for _, cs := range frame.Components {
			if cs.ID == 0 {
				continue // collider is handled in CreateReplica/base update
			}
			rep := registry.Get(cs.ID)
			if rep == nil {
				continue // unknown component — receiver doesn't know about it
			}
			if isNew && rep.Add != nil {
				rep.Add(entity, cs.Data)
			} else {
				rep.Apply(entity, cs.Data)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Collider codec (component ID 0, always included in frames)
// ---------------------------------------------------------------------------

func marshalCollider(c *component.Collider) []byte {
	buf := make([]byte, 14)
	putFloat32(buf[0:], c.Radius)
	putFloat32(buf[4:], c.Width)
	putFloat32(buf[8:], c.Height)
	buf[12] = c.Layer
	buf[13] = c.Shape
	return buf
}

// UnmarshalCollider decodes a Collider from binary data.
func UnmarshalCollider(data []byte) component.Collider {
	if len(data) < 14 {
		return component.Collider{}
	}
	return component.Collider{
		Radius: getFloat32(data[0:]),
		Width:  getFloat32(data[4:]),
		Height: getFloat32(data[8:]),
		Layer:  data[12],
		Shape:  data[13],
	}
}

func putFloat32(buf []byte, v float32) {
	binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
}

func getFloat32(buf []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(buf))
}
