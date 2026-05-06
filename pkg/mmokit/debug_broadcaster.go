package mmokit

import (
	"hash/fnv"
	"sort"
	"strconv"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// debugBroadcasterWorld is the minimal interface debugBroadcaster needs
// from a game world. *Stage satisfies it directly; *GameWorld-shaped
// types satisfy it because they embed *Stage. SendEvent below frames
// the typed event and writes directly to ConnMgr — no Stage handle
// needed at the call site.
type debugBroadcasterWorld interface {
	Topology() []ClusterCellInfo
	GridDimensions() (uint32, uint32, float32)
	GetAoIRadius() float32
	Engine() *engine.Engine
	HasInflightTransfers() bool
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
	// Skip during cell-transfer commits: the cellToHostMap holds
	// transient state (e.g. merge's rename step where the survivor
	// briefly appears under both donor and parent names) that would
	// leak to clients as a 1-tick visual flicker. Each commit window
	// is ~1-3 ticks (50-150ms); the next tick after the orchestrator
	// finishes will fire the post-commit topology hash.
	if s.World().HasInflightTransfers() {
		return
	}

	cells := s.World().Topology()
	radius := s.World().GetAoIRadius()

	activeNow := make(map[uint32]struct{})
	pm := s.World().Engine().Players
	pm.ForEach(StateActive, func(sess *PlayerSession) {
		activeNow[sess.ConnID] = struct{}{}
		if sess.DebugFlags == 0 {
			// Revoke-to-zero transition: send one empty DebugInfo so the
			// client clears its overlay state. Detected by the presence of
			// a prior hash entry — first-tick zero-flag players get nothing.
			// Forget what we sent so a future re-grant fires a fresh send;
			// without this, revoke→grant of the same flag set produces an
			// identical hash and the broadcaster would silently skip.
			if _, hadFlags := s.sentHash[sess.ConnID]; hadFlags {
				var empty DebugInfo
				s.World().Engine().ConnMgr.SendReliable(sess.ConnID, BuildTypedEventFrame(&empty))
				delete(s.sentHash, sess.ConnID)
			}
			return
		}
		hash := hashDebugPayload(cells, radius, sess.DebugFlags)
		if s.sentHash[sess.ConnID] == hash {
			return
		}
		msg := buildDebugInfo(cells, radius, sess.DebugFlags)
		// Frame inline + write through ConnMgr — interface doesn't expose
		// a *Stage handle, but the engine's ConnMgr is enough for the
		// reliable typed-event channel.
		s.World().Engine().ConnMgr.SendReliable(sess.ConnID, BuildTypedEventFrame(&msg))
		s.sentHash[sess.ConnID] = hash
	})

	// GC entries for disconnected players.
	for connID := range s.sentHash {
		if _, alive := activeNow[connID]; !alive {
			delete(s.sentHash, connID)
		}
	}
}

// buildDebugInfo constructs a per-player DebugInfo with only the fields
// whose flag the player has. AoIRadius == 0 + empty cells is the
// "everything off" sentinel; clients render no overlay in that state.
func buildDebugInfo(cells []ClusterCellInfo, aoiRadius float32, flags engine.DebugFlag) DebugInfo {
	var msg DebugInfo
	if flags&engine.DebugTopology != 0 {
		msg.Topology = buildTopology(cells)
		msg.AoIRadius = aoiRadius
	}
	// Future flags slot here as additional `if flags & <bit> != 0` branches.
	return msg
}

// buildTopology is the cell-list → CellTopology helper.
func buildTopology(cells []ClusterCellInfo) CellTopology {
	var out CellTopology
	if len(cells) == 0 {
		return out
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
