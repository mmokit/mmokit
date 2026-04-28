package mmokit

import (
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/coords"
)

// topologyView is the minimal interface BuildCellTopologyMsg needs from a
// game world. *Stage satisfies it via Topology() + GridDimensions().
type topologyView interface {
	Topology() []ClusterCellInfo
	GridDimensions() (uint32, uint32, float32)
}

// BuildCellTopologyMsg constructs the CellTopologyMsg payload for the
// world's current cluster view. Returns the message ready to embed in
// a DebugInfoMsg and send via gw.SendEvent(connID, SE_DEBUG_INFO, msg).
func BuildCellTopologyMsg(gw topologyView) *enginepb.CellTopologyMsg {
	cells := gw.Topology()
	gridX, gridY, baseCS := gw.GridDimensions()
	msg := &enginepb.CellTopologyMsg{
		GridW:        int32(gridX),
		GridH:        int32(gridY),
		BaseCellSize: baseCS,
	}
	for _, c := range cells {
		size := c.Cell.Size(coords.CellSize)
		ox, oy := c.Cell.WorldOrigin(coords.CellSize)
		msg.Cells = append(msg.Cells, &enginepb.CellInfo{
			CellX:   c.Cell.X,
			CellY:   c.Cell.Y,
			Depth:   uint32(c.Cell.Depth),
			Size:    size,
			OriginX: ox,
			OriginY: oy,
			NodeId:  c.HostID,
		})
	}
	return msg
}

// topologyDispatcher is the minimal contract SendCellTopology needs: build
// the message AND ship a reliable frame. *Stage satisfies it.
type topologyDispatcher interface {
	topologyView
	SendEvent(connID, code uint32, msg interface{ Reset() })
}

// SendCellTopology builds and sends the SE_DEBUG_INFO message (with topology
// populated) to a single connection. Convenience for the common case.
func SendCellTopology(gw topologyDispatcher, connID uint32) {
	msg := BuildCellTopologyMsg(gw)
	gw.SendEvent(connID, uint32(enginepb.ServerEventCode_SE_DEBUG_INFO), msg)
}
