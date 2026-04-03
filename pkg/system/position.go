package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// CellRelativePos computes an entity's position relative to the viewer's cell origin.
// For same-cell entities, this returns the entity's cell-local position (entry.X, entry.Y).
// For cross-cell entities (replicas), it includes the cell offset:
//
//	relX = (entityCellX - viewerCellX) * cellSize + entry.X
//
// This is the standard position encoding for cell-meshed games. The result should
// be written as Float32 (not quantized) to avoid boundary shift artifacts.
func CellRelativePos(
	cellCoordMap *ecs.Map1[component.CellCoord],
	viewerEntity ecs.Entity,
	entry spatial.Entry,
) (float32, float32) {
	var entitySX, entitySY int32
	if cellCoordMap.HasAll(entry.Entity) {
		c := cellCoordMap.Get(entry.Entity)
		entitySX = c.CellX
		entitySY = c.CellY
	}
	var viewerSX, viewerSY int32
	if cellCoordMap.HasAll(viewerEntity) {
		c := cellCoordMap.Get(viewerEntity)
		viewerSX = c.CellX
		viewerSY = c.CellY
	}
	relX := float32(entitySX-viewerSX)*coords.CellSize + entry.X
	relY := float32(entitySY-viewerSY)*coords.CellSize + entry.Y
	return relX, relY
}
