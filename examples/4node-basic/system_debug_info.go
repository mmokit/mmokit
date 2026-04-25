package main

import (
	"hash/fnv"
	"sort"
	"strconv"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DebugInfoSystem updates game-specific debug fields each tick AND pushes
// the current cluster topology to every active player on change. Topology
// is global (not per-entity), so it can't ride the ECS replication system
// like AoIRadius does; instead the system holds a per-connID "last sent
// topology hash" map and re-sends when the cluster view changes (cell
// split/merge, host join/leave, or a brand-new player connecting).
//
// Moved off the OnEnter player-spawn hook so new players still get the
// current topology AND existing players see updates on dynamic cell
// changes without a reconnect. Replaces the engine-side DebugTopology
// broadcast that was deleted in the topology refactor.
type DebugInfoSystem struct {
	mmokit.SystemBase
	gw       *World
	entities mmokit.Query[struct {
		DI *DebugInfo
	}]

	// Per-player tracking: what topology hash did we last push to them?
	// Empty string = never sent. Mismatch = push again. Stale entries for
	// disconnected players are harmless (cheap strings) and pruned on the
	// next tick whose active-player set doesn't include them.
	sentHash map[uint32]string
}

func (s *DebugInfoSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll())
	s.gw = mmokit.WorldOf[*World](s)
	s.sentHash = make(map[uint32]string)
}

func (s *DebugInfoSystem) Update(dt float32) {
	// 1. Per-entity AoI radius (existing behavior).
	for _, b := range s.entities.All() {
		b.DI.AoIRadius = AoIRadius
	}

	// 2. Topology push to every active player, reactive to changes.
	cells := s.gw.ClusterCells()
	if len(cells) == 0 {
		return
	}
	currentHash := hashClusterCells(cells)

	activeNow := make(map[uint32]bool)
	pm := s.gw.Engine().Players
	pm.ForEach(mmokit.StateActive, func(sess *mmokit.PlayerSession) {
		activeNow[sess.ConnID] = true
		if s.sentHash[sess.ConnID] == currentHash {
			return
		}
		s.gw.sendCellTopology(sess.ConnID)
		s.sentHash[sess.ConnID] = currentHash
	})

	// Prune tracking for connIDs that are no longer active — keeps the map
	// from growing unbounded across reconnect cycles.
	for connID := range s.sentHash {
		if !activeNow[connID] {
			delete(s.sentHash, connID)
		}
	}
}

// hashClusterCells produces a deterministic fingerprint of the current
// cluster topology (cell identity + owning host). Cheap enough to compute
// every tick for small clusters; the system's early-out on hash equality
// keeps the actual wire send rare.
//
// The input slice comes from coord.ClusterCells() which builds it from a
// map range — i.e. non-deterministic ordering. Sort first so the hash
// stays stable across ticks when the topology hasn't actually changed;
// otherwise every tick produces a fresh hash and the system re-sends
// SE_CELL_TOPOLOGY to every connected player 20x/sec.
func hashClusterCells(cells []mmokit.ClusterCellInfo) string {
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Cell.Depth != cells[j].Cell.Depth {
			return cells[i].Cell.Depth < cells[j].Cell.Depth
		}
		if cells[i].Cell.X != cells[j].Cell.X {
			return cells[i].Cell.X < cells[j].Cell.X
		}
		if cells[i].Cell.Y != cells[j].Cell.Y {
			return cells[i].Cell.Y < cells[j].Cell.Y
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
	return strconv.FormatUint(h.Sum64(), 16)
}

