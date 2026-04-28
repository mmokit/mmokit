package mmokit

import (
	"hash/fnv"
	"sort"
	"strconv"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// debugBroadcasterWorld is the minimal interface debugBroadcaster needs
// from a game world. *Stage satisfies it via Topology() + GetAoIRadius()
// + Engine() + SendEvent().
type debugBroadcasterWorld interface {
	Topology() []ClusterCellInfo
	GridDimensions() (uint32, uint32, float32)
	GetAoIRadius() float32
	SendEvent(connID, code uint32, msg interface{ Reset() })
	Engine() *engine.Engine
}

type debugBroadcaster struct {
	SystemBase[debugBroadcasterWorld]
	sentHash map[uint32]uint64
}

// NewDebugBroadcaster returns a SystemDef that pushes per-player debug
// overlay data (topology, AoI radius, ...) to every active player whose
// DebugFlags includes the matching bit. Reactive on a per-player payload
// hash — zero per-tick overhead when nothing has changed AND no debug
// players are active.
func NewDebugBroadcaster() SystemDef {
	return SystemDef{
		Name:    "DebugBroadcaster",
		Factory: func() System { return &debugBroadcaster{} },
	}
}

func (s *debugBroadcaster) Init() {
	s.sentHash = make(map[uint32]uint64)
}

func (s *debugBroadcaster) Update(dt float32) {
	cells := s.World().Topology()
	radius := s.World().GetAoIRadius()

	activeNow := make(map[uint32]struct{})
	pm := s.World().Engine().Players
	pm.ForEach(StateActive, func(sess *PlayerSession) {
		activeNow[sess.ConnID] = struct{}{}
		if sess.DebugFlags == 0 {
			return // no debug enabled for this player
		}
		hash := hashDebugPayload(cells, radius, sess.DebugFlags)
		if s.sentHash[sess.ConnID] == hash {
			return
		}
		msg := buildDebugInfoPayload(cells, radius, sess.DebugFlags)
		s.World().SendEvent(sess.ConnID, uint32(enginepb.ServerEventCode_SE_DEBUG_INFO), msg)
		s.sentHash[sess.ConnID] = hash
	})

	// GC entries for disconnected players.
	for connID := range s.sentHash {
		if _, alive := activeNow[connID]; !alive {
			delete(s.sentHash, connID)
		}
	}
}

// buildDebugInfoPayload constructs a per-player DebugInfoMsg with only
// the fields whose flag the player has.
func buildDebugInfoPayload(cells []ClusterCellInfo, aoiRadius float32, flags engine.DebugFlag) *enginepb.DebugInfoMsg {
	msg := &enginepb.DebugInfoMsg{}
	if flags&engine.DebugTopology != 0 {
		msg.Topology = buildTopologyMsg(cells)
		r := aoiRadius
		msg.AoiRadius = &r
	}
	// Future flags slot here as additional `if flags & <bit> != 0` branches.
	return msg
}

// buildTopologyMsg is the cell-list → CellTopologyMsg helper.
func buildTopologyMsg(cells []ClusterCellInfo) *enginepb.CellTopologyMsg {
	msg := &enginepb.CellTopologyMsg{}
	if len(cells) == 0 {
		return msg
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

// hashDebugPayload fingerprints the per-player payload contents. Clones
// the cells slice before sorting so the caller's view is not mutated.
func hashDebugPayload(cells []ClusterCellInfo, aoiRadius float32, flags engine.DebugFlag) uint64 {
	h := fnv.New64a()
	var buf [16]byte

	h.Write(strconv.AppendUint(buf[:0], uint64(flags), 10))
	h.Write([]byte{'|'})

	if flags&engine.DebugTopology != 0 {
		sorted := make([]ClusterCellInfo, len(cells))
		copy(sorted, cells)
		sort.Slice(sorted, func(i, j int) bool {
			ci, cj := sorted[i].Cell, sorted[j].Cell
			if ci.Depth != cj.Depth {
				return ci.Depth < cj.Depth
			}
			if ci.X != cj.X {
				return ci.X < cj.X
			}
			if ci.Y != cj.Y {
				return ci.Y < cj.Y
			}
			return sorted[i].HostID < sorted[j].HostID
		})
		for _, c := range sorted {
			h.Write(strconv.AppendInt(buf[:0], int64(c.Cell.X), 10))
			h.Write([]byte{','})
			h.Write(strconv.AppendInt(buf[:0], int64(c.Cell.Y), 10))
			h.Write([]byte{',', 'd'})
			h.Write(strconv.AppendUint(buf[:0], uint64(c.Cell.Depth), 10))
			h.Write([]byte{'@'})
			h.Write([]byte(c.HostID))
			h.Write([]byte{'|'})
		}
		h.Write([]byte("aoi:"))
		h.Write(strconv.AppendFloat(buf[:0], float64(aoiRadius), 'f', 6, 32))
		h.Write([]byte{'|'})
	}

	return h.Sum64()
}
