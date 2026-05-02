package universe

import (
	"hash/fnv"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/replication"
)

// CellViewer adapts a neighbor Cell as a replication.Viewer so the
// shared dispatcher in pkg/replication can treat neighbor cells
// identically to player connections. One CellViewer per neighbor
// relationship per owning cell; the owning cell creates a set of
// CellViewers at build time and feeds them into BorderDispatcher.Walk
// every tick.
type CellViewer struct {
	cellID       string // immutable snapshot of destCell.ID at construction time
	sourceCellID string // immutable snapshot of sourceCell.ID at construction time
	id           uint64
	x, y         float32
	tiers        map[uint16]replication.ReplicationTier
	baselines    *replication.BaselineStore
	sourceCell   *Cell // cell that owns this viewer (the source of frames)
	destCell     *Cell // destination neighbor cell that receives frames
	dirDX        int32 // neighbor's direction from source cell (-1, 0, or +1)
	dirDY        int32 // neighbor's direction from source cell (-1, 0, or +1)

	// prevInSet is the set of netIDs that were in this viewer's push set
	// on the previous tick. currInSet is the set being accumulated on the
	// current tick. candidatesFor consults prevInSet for hysteresis
	// (entities already in the set use exitMargin instead of enterMargin)
	// and writes additions into currInSet. After the walk, Tick swaps
	// the two, discarding any netIDs that did not pass the predicate
	// this tick.
	prevInSet map[uint32]struct{}
	currInSet map[uint32]struct{}
}

// NewCellViewer constructs a viewer for a neighbor cell.
//
// boundaryX/Y should be the midpoint of the shared cell boundary
// between this cell and the neighbor; the dispatcher uses it as the
// viewer's "position" for tier-radius distance checks.
//
// tiers is an optional per-entity-kind tier override. Pass nil for the
// default tier (non-zero radius, no divisor), which is correct for
// most games. Games that replicate different entity kinds at
// different tiers can populate this map.
//
// sourceCell is the cell that owns this viewer (may be nil in tests).
// destCell is the destination neighbor that will receive frames (may be nil in tests).
func NewCellViewer(
	cellID string,
	id uint64,
	boundaryX, boundaryY float32,
	tiers map[uint16]replication.ReplicationTier,
	sourceCell *Cell,
	destCell *Cell,
) *CellViewer {
	// Capture sourceCell.ID at construction time. The underlying cell's
	// ID field is rewritten on merge/rename from its own game loop;
	// reading it from another goroutine (this viewer lives on a NEIGHBOR
	// cell's loop, not the source) races. Cells are always rebuilt via a
	// rewire directive after a rename, so a stale cached ID just means
	// one tick of unroutable frames while the receiver's Cells map is
	// already carrying the new key — acceptable.
	var sourceCellID string
	if sourceCell != nil {
		sourceCellID = string(sourceCell.MeshID)
	}
	return &CellViewer{
		cellID:       cellID,
		sourceCellID: sourceCellID,
		id:           id,
		x:            boundaryX,
		y:            boundaryY,
		tiers:        tiers,
		baselines:    replication.NewBaselineStore(replication.AckReliable),
		sourceCell:   sourceCell,
		destCell:     destCell,
		prevInSet:    make(map[uint32]struct{}),
		currInSet:    make(map[uint32]struct{}),
	}
}

// WasInSet reports whether netID was in this viewer's push set on the
// previous tick. BorderDispatcher consults this to apply exit-margin
// hysteresis: entities already in the set use a looser margin to leave
// than to enter, preventing flicker churn around the threshold.
func (v *CellViewer) WasInSet(netID uint32) bool {
	_, ok := v.prevInSet[netID]
	return ok
}

// MarkInSet records that netID is part of this tick's push set. Called
// by BorderDispatcher after the proximity predicate passes.
func (v *CellViewer) MarkInSet(netID uint32) {
	v.currInSet[netID] = struct{}{}
}

// SwapInSet rotates currInSet into prevInSet and empties the now-
// previous map for reuse on the next tick. Called by BorderDispatcher
// at the end of each tick's walk so that any netID which failed the
// predicate this tick is dropped from the hysteresis window going
// forward.
func (v *CellViewer) SwapInSet() {
	v.prevInSet, v.currInSet = v.currInSet, v.prevInSet
	for k := range v.currInSet {
		delete(v.currInSet, k)
	}
}

// SetDirection stores the neighbor direction (dx, dy) for edge-proximity checks.
// Called by BorderDispatcher construction after the neighbor map is resolved.
func (v *CellViewer) SetDirection(dx, dy int32) {
	v.dirDX = dx
	v.dirDY = dy
}

// ID returns this viewer's unique identifier. Cell IDs have the high
// bit set so they cannot collide with player connection IDs.
func (v *CellViewer) ID() uint64 { return v.id }

// Position returns the boundary midpoint used for tier distance checks.
func (v *CellViewer) Position() (float32, float32) { return v.x, v.y }

// Tier returns the replication tier for the given entity kind. If a
// custom tier is configured for this kind, that is returned; otherwise
// a default tier whose Radius covers the source cell's full diagonal
// is used — effectively disabling the dispatcher's InsideRadius check
// for this viewer, since geometric proximity is already enforced by
// BorderDispatcher.entityNearNeighborEdge.
//
// Why this radius is huge: CellViewer represents a whole neighboring
// cell, not a point observer. The shared Dispatcher's InsideRadius
// check is a disc around viewer.Position() (the edge midpoint). An
// entity sitting near a corner of the source cell may be near the
// shared edge (correctly passing entityNearNeighborEdge) but 4000+
// units from the midpoint of a cardinal edge — far outside a 1000-unit
// disc. That caused corner-of-cell entities to only reach diagonal
// neighbors, not cardinal ones. Using the cell diagonal as the radius
// ceiling means any entity that passed the edge-strip filter will also
// pass the disc filter, for any cell size.
func (v *CellViewer) Tier(kind uint16) replication.ReplicationTier {
	if v.tiers != nil {
		if t, ok := v.tiers[kind]; ok {
			return t
		}
	}
	// sqrt(2) * cellSize is a tight upper bound on the distance between
	// any two points within a single source cell. Multiplied by 2 for a
	// safety margin that covers the neighbor's half of the edge too,
	// plus any small floating-point drift.
	return replication.ReplicationTier{
		Radius:        coords.CellSize * 2,
		UpdateDivisor: 1,
		BaseWeight:    1,
	}
}

// Baselines returns the per-neighbor acknowledged snapshot store.
// Phase 7 uses this to maintain delta encoding state for entities
// replicated to the neighbor, exactly parallel to how the client
// dispatcher maintains baselines for player connections.
func (v *CellViewer) Baselines() *replication.BaselineStore {
	return v.baselines
}

// Send delivers a built frame to the neighbor by dispatching through
// the source cell's Bridge. The Bridge decides whether to push the
// frame directly into the destination cell's inbox (single-host
// colocated, or multi-host local-shortcut) or encode it through the
// meshpb codec and send via gRPC (multi-host cross-host). Lossy: the
// 30-tick forced resync recovers from dropped frames automatically.
// Records the encoded byte count on the source cell's metrics.
//
// Empty frames carrying authoritative "my interest set for you is now
// empty" state are sent through — the receiver's diff logic relies on
// them to remove replicas for entities that dropped out. We only elide
// the wire hop in the fully-degenerate case: the sender had zero
// entries last tick AND zero entries this tick, so there is nothing
// for the receiver to diff against. This keeps the wire quiet during
// long stretches where a neighbor is uninteresting.
func (v *CellViewer) Send(frame replication.Frame) {
	if v.destCell == nil || v.sourceCell == nil || v.sourceCell.Bridge == nil {
		return
	}
	if len(frame.Entries) == 0 && len(v.prevInSet) == 0 {
		return
	}
	encoded := frame.Encode()
	if v.sourceCell.Metrics != nil {
		v.sourceCell.Metrics.RecordBorderFrameSent(len(encoded))
	}
	// Use cached IDs rather than v.destCell.ID / v.sourceCell.ID — those
	// fields are rewritten by the merge commit's rename path and reading
	// them here races against the survivor's own game loop. Cached
	// snapshots are captured at CellViewer construction and invalidated
	// by the rewire directive that follows a rename.
	v.sourceCell.Bridge.SendBorderFrame(v.cellID, v.sourceCellID, encoded)
}

// CellViewerID derives a stable uint64 viewer ID from a cell ID string.
// The high bit is set so neighbor viewer IDs cannot collide with
// player connection IDs (which are zero-extended uint32 values).
// Callers should use this helper when constructing CellViewer instances
// so that ID collisions with player connections are impossible.
func CellViewerID(cellID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(cellID))
	return h.Sum64() | (1 << 63)
}
