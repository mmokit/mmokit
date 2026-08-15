package mmokit

import (
	"github.com/mmokit/mmokit/pkg/coords"
)

// topologyView is the minimal interface BuildCellTopology needs from a
// game world. *Stage satisfies it via Topology() + GridDimensions().
type topologyView interface {
	Topology() []ClusterCellInfo
	GridDimensions() (uint32, uint32, float32)
}

// BuildCellTopology constructs the typed CellTopology payload for the
// world's current cluster view. Returns the value ready to embed in a
// DebugInfo and send via mmokit.SendEvent.
func BuildCellTopology(gw topologyView) CellTopology {
	cells := gw.Topology()
	gridX, gridY, baseCS := gw.GridDimensions()
	out := CellTopology{
		GridW:        int32(gridX),
		GridH:        int32(gridY),
		BaseCellSize: baseCS,
	}
	for _, c := range cells {
		size := c.Cell.Size(coords.CellSize)
		ox, oy := c.Cell.WorldOrigin(coords.CellSize)
		out.Cells = append(out.Cells, CellInfo{
			CellX:   c.Cell.X,
			CellY:   c.Cell.Y,
			Depth:   uint32(c.Cell.Depth),
			Size:    size,
			OriginX: ox,
			OriginY: oy,
			NodeID:  c.HostID,
		})
	}
	return out
}

// SendCellTopology builds and sends a typed DebugInfo to one connection
// with the cluster topology populated. AoIRadius defaults to zero (overlay
// off); use the lower-level SendEvent on a custom DebugInfo if you need to
// set it. Convenience for player-spawn hooks that always want the topology
// overlay primed.
func SendCellTopology(gw topologyView, stage *Stage, connID uint32) {
	SendEvent(stage, connID, &DebugInfo{Topology: BuildCellTopology(gw)})
}
