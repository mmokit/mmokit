package mmokit

import (
	"hash/fnv"
	"sort"
	"strconv"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/coords"
)

// topologyView is the minimal interface BuildCellTopologyMsg needs from a
// game world. *WorldBase satisfies it via Topology() + GridDimensions().
type topologyView interface {
	Topology() []ClusterCellInfo
	GridDimensions() (uint32, uint32, float32)
}

// BuildCellTopologyMsg constructs the SE_CELL_TOPOLOGY payload for the
// world's current cluster view. Returns the message ready to send via
// gw.SendEvent(connID, SE_CELL_TOPOLOGY, msg).
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
// the message AND ship a reliable frame. *WorldBase satisfies it.
type topologyDispatcher interface {
	topologyView
	SendEvent(connID, code uint32, msg interface{ Reset() })
}

// SendCellTopology builds and sends the SE_CELL_TOPOLOGY message to a
// single connection. Convenience for the common case.
func SendCellTopology(gw topologyDispatcher, connID uint32) {
	msg := BuildCellTopologyMsg(gw)
	gw.SendEvent(connID, uint32(enginepb.ServerEventCode_SE_CELL_TOPOLOGY), msg)
}

// NewTopologyBroadcaster returns a SystemDef that pushes the current
// cluster topology to every active player whenever it changes (cell
// split/merge, host join/leave) or when a new player joins. Reactive — no
// per-tick overhead beyond a cheap fingerprint comparison.
//
// Replaces the bespoke topology-hashing system that every game with a
// debug overlay would otherwise hand-roll.
func NewTopologyBroadcaster() SystemDef {
	return SystemDef{
		Name:    "Topology",
		Factory: func() System { return &topologyBroadcaster{} },
	}
}

type topologyBroadcasterWorld interface {
	topologyDispatcher
	Engine() *Engine
}

type topologyBroadcaster struct {
	SystemBase[topologyBroadcasterWorld]
	gw       topologyBroadcasterWorld
	sentHash map[uint32]uint64
}

func (s *topologyBroadcaster) Init() {
	s.gw = WorldOf[topologyBroadcasterWorld](s)
	s.sentHash = make(map[uint32]uint64)
}

func (s *topologyBroadcaster) Update(dt float32) {
	cells := s.gw.Topology()
	if len(cells) == 0 {
		return
	}
	hash := hashTopology(cells)
	activeNow := make(map[uint32]struct{})
	pm := s.gw.Engine().Players
	pm.ForEach(StateActive, func(sess *PlayerSession) {
		activeNow[sess.ConnID] = struct{}{}
		if s.sentHash[sess.ConnID] == hash {
			return
		}
		SendCellTopology(s.gw, sess.ConnID)
		s.sentHash[sess.ConnID] = hash
	})
	for connID := range s.sentHash {
		if _, alive := activeNow[connID]; !alive {
			delete(s.sentHash, connID)
		}
	}
}

// hashTopology fingerprints the cluster's cell→host view. Sorts cells
// in place — callers must not reuse the slice after this call — so the
// hash is stable across the non-deterministic map-range orderings that
// produce ClusterCells.
func hashTopology(cells []ClusterCellInfo) uint64 {
	sort.Slice(cells, func(i, j int) bool {
		ci, cj := cells[i].Cell, cells[j].Cell
		if ci.Depth != cj.Depth {
			return ci.Depth < cj.Depth
		}
		if ci.X != cj.X {
			return ci.X < cj.X
		}
		if ci.Y != cj.Y {
			return ci.Y < cj.Y
		}
		return cells[i].HostID < cells[j].HostID
	})
	h := fnv.New64a()
	var buf [16]byte
	for _, c := range cells {
		h.Write(strconv.AppendInt(buf[:0], int64(c.Cell.X), 10))
		h.Write([]byte{','})
		h.Write(strconv.AppendInt(buf[:0], int64(c.Cell.Y), 10))
		h.Write([]byte{',', 'd'})
		h.Write(strconv.AppendUint(buf[:0], uint64(c.Cell.Depth), 10))
		h.Write([]byte{'@'})
		h.Write([]byte(c.HostID))
		h.Write([]byte{'|'})
	}
	return h.Sum64()
}
