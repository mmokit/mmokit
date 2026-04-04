package main

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DebugInfoSystem updates the DebugInfo component each tick based on
// Ghost/Replica/CellCoord state. This demonstrates a game-specific system
// that feeds custom data into the AutoReplicator.
type DebugInfoSystem struct {
	mmokit.SystemBase
	filter   *ecs.Filter1[DebugInfo]
	ghostMap *ecs.Map1[mmokit.Ghost]
	repMap   *ecs.Map1[mmokit.Replica]
	cellMap  *ecs.Map1[mmokit.CellCoord]
}

func (s *DebugInfoSystem) Init() {
	w := s.ECSWorld()
	s.filter = ecs.NewFilter1[DebugInfo](w)
	s.ghostMap = ecs.NewMap1[mmokit.Ghost](w)
	s.repMap = ecs.NewMap1[mmokit.Replica](w)
	s.cellMap = ecs.NewMap1[mmokit.CellCoord](w)
}

func (s *DebugInfoSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		di := query.Get()
		entity := query.Entity()

		if s.repMap.HasAll(entity) {
			di.State = EntityReplica
		} else if s.ghostMap.HasAll(entity) {
			di.State = EntityGhost
		} else {
			di.State = EntityLocal
		}

		if s.repMap.HasAll(entity) {
			di.OwnerNode = parseNodeIndex(s.repMap.Get(entity).SourceNodeID)
		} else if s.ghostMap.HasAll(entity) {
			di.OwnerNode = parseNodeIndex(s.ghostMap.Get(entity).DestNodeID)
		} else if s.cellMap.HasAll(entity) {
			cc := s.cellMap.Get(entity)
			di.OwnerNode = uint8(int(cc.CellY)*int(MeshCellsX) + int(cc.CellX))
		}
	}
}

// parseNodeIndex extracts cell coords from a nodeID like "node_1_0" or "node_d1_2_3" and returns the index.
func parseNodeIndex(nodeID string) uint8 {
	var sx, sy int32
	// Try depth-prefixed format first ("node_dN_X_Y")
	if _, err := fmt.Sscanf(nodeID, "node_d%*d_%d_%d", &sx, &sy); err != nil {
		// Fall back to depth-0 format ("node_X_Y")
		fmt.Sscanf(nodeID, "node_%d_%d", &sx, &sy)
	}
	return uint8(int(sy)*int(MeshCellsX) + int(sx))
}
