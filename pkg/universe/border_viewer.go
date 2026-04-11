package universe

import (
	"hash/fnv"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/replication"
)

// NodeViewer adapts a neighbor Node as a replication.Viewer so the
// shared dispatcher in pkg/replication can treat neighbor nodes
// identically to player connections. One NodeViewer per neighbor
// relationship per owning node; the owning node creates a set of
// NodeViewers at build time and feeds them into BorderDispatcher.Walk
// every tick.
type NodeViewer struct {
	nodeID     string
	id         uint64
	x, y       float32
	tiers      map[uint16]replication.ReplicationTier
	baselines  *replication.BaselineStore
	sourceNode *Node  // node that owns this viewer (the source of frames)
	destNode   *Node  // destination neighbor node that receives frames
	dirDX      int32  // neighbor's direction from source cell (-1, 0, or +1)
	dirDY      int32  // neighbor's direction from source cell (-1, 0, or +1)
}

// NewNodeViewer constructs a viewer for a neighbor node.
//
// boundaryX/Y should be the midpoint of the shared cell boundary
// between this node and the neighbor; the dispatcher uses it as the
// viewer's "position" for tier-radius distance checks.
//
// tiers is an optional per-entity-kind tier override. Pass nil for the
// default tier (non-zero radius, no divisor), which is correct for
// most games. Games that replicate different entity kinds at
// different tiers can populate this map.
//
// sourceNode is the node that owns this viewer (may be nil in tests).
// destNode is the destination neighbor that will receive frames (may be nil in tests).
func NewNodeViewer(
	nodeID string,
	id uint64,
	boundaryX, boundaryY float32,
	tiers map[uint16]replication.ReplicationTier,
	sourceNode *Node,
	destNode *Node,
) *NodeViewer {
	return &NodeViewer{
		nodeID:     nodeID,
		id:         id,
		x:          boundaryX,
		y:          boundaryY,
		tiers:      tiers,
		baselines:  replication.NewBaselineStore(replication.AckReliable),
		sourceNode: sourceNode,
		destNode:   destNode,
	}
}

// SetDirection stores the neighbor direction (dx, dy) for edge-proximity checks.
// Called by BorderDispatcher construction after the neighbor map is resolved.
func (v *NodeViewer) SetDirection(dx, dy int32) {
	v.dirDX = dx
	v.dirDY = dy
}

// ID returns this viewer's unique identifier. Node IDs have the high
// bit set so they cannot collide with player connection IDs.
func (v *NodeViewer) ID() uint64 { return v.id }

// Position returns the boundary midpoint used for tier distance checks.
func (v *NodeViewer) Position() (float32, float32) { return v.x, v.y }

// Tier returns the replication tier for the given entity kind. If a
// custom tier is configured for this kind, that is returned; otherwise
// a default tier whose Radius covers the source cell's full diagonal
// is used — effectively disabling the dispatcher's InsideRadius check
// for this viewer, since geometric proximity is already enforced by
// BorderDispatcher.entityNearNeighborEdge.
//
// Why this radius is huge: NodeViewer represents a whole neighboring
// cell, not a point observer. The shared Dispatcher's InsideRadius
// check is a disc around viewer.Position() (the edge midpoint). An
// entity sitting near a corner of the source cell may be near the
// shared edge (correctly passing entityNearNeighborEdge) but 4000+
// units from the midpoint of a cardinal edge — far outside a 1000-unit
// disc. That caused corner-of-cell entities to only reach diagonal
// neighbors, not cardinal ones. Using the cell diagonal as the radius
// ceiling means any entity that passed the edge-strip filter will also
// pass the disc filter, for any cell size.
func (v *NodeViewer) Tier(kind uint16) replication.ReplicationTier {
	if v.tiers != nil {
		if t, ok := v.tiers[kind]; ok {
			return t
		}
	}
	// sqrt(2) * cellSize is a tight upper bound on the distance between
	// any two points within a single source cell. Multiplied by 2 for a
	// safety margin that covers the neighbor's half of the edge too,
	// plus any small floating-point drift.
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
func (v *NodeViewer) Baselines() *replication.BaselineStore {
	return v.baselines
}

// Send delivers a built frame to the neighbor. It encodes the Frame
// and writes a MsgBorderFrame envelope into the destination node's
// inbox. Non-blocking: if the inbox is full the frame is dropped.
// Records the encoded byte count on the source node's metrics.
func (v *NodeViewer) Send(frame replication.Frame) {
	if v.destNode == nil {
		return
	}
	if len(frame.Entries) == 0 {
		return
	}
	encoded := frame.Encode()
	msg := NodeMessage{
		Type:        MsgBorderFrame,
		BorderFrame: encoded,
	}
	if v.sourceNode != nil {
		msg.FromNodeID = v.sourceNode.ID
		if v.sourceNode.Metrics != nil {
			v.sourceNode.Metrics.RecordBorderFrameSent(len(encoded))
		}
	}
	// Non-blocking: drop frame if destination inbox is full.
	select {
	case v.destNode.Inbox <- msg:
	default:
	}
}

// NodeViewerID derives a stable uint64 viewer ID from a node ID string.
// The high bit is set so neighbor viewer IDs cannot collide with
// player connection IDs (which are zero-extended uint32 values).
// Callers should use this helper when constructing NodeViewer instances
// so that ID collisions with player connections are impossible.
func NodeViewerID(nodeID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(nodeID))
	return h.Sum64() | (1 << 63)
}
