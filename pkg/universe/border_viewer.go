package universe

import (
	"hash/fnv"

	"github.com/zenion/mmoserver/pkg/replication"
)

// NodeViewer adapts a neighbor Node as a replication.Viewer so the
// shared dispatcher in pkg/replication can treat neighbor nodes
// identically to player connections. One NodeViewer per neighbor
// relationship per owning node; the owning node creates a set of
// NodeViewers at build time and feeds them into BorderDispatcher.Walk
// every tick.
type NodeViewer struct {
	nodeID    string
	id        uint64
	x, y      float32
	tiers     map[uint16]replication.ReplicationTier
	baselines *replication.BaselineStore
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
func NewNodeViewer(
	nodeID string,
	id uint64,
	boundaryX, boundaryY float32,
	tiers map[uint16]replication.ReplicationTier,
) *NodeViewer {
	return &NodeViewer{
		nodeID:    nodeID,
		id:        id,
		x:         boundaryX,
		y:         boundaryY,
		tiers:     tiers,
		baselines: replication.NewBaselineStore(replication.AckReliable),
	}
}

// ID returns this viewer's unique identifier. Node IDs have the high
// bit set so they cannot collide with player connection IDs.
func (v *NodeViewer) ID() uint64 { return v.id }

// Position returns the boundary midpoint used for tier distance checks.
func (v *NodeViewer) Position() (float32, float32) { return v.x, v.y }

// Tier returns the replication tier for the given entity kind. If a
// custom tier is configured for this kind, that is returned; otherwise
// a default tier with radius 1000, no divisor, and weight 1 is used.
func (v *NodeViewer) Tier(kind uint16) replication.ReplicationTier {
	if v.tiers != nil {
		if t, ok := v.tiers[kind]; ok {
			return t
		}
	}
	return replication.ReplicationTier{
		Radius:        1000,
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

// Send delivers a built frame to the neighbor. This is a stub for
// Phase 4; Phase 7 wires it to encode the Frame and write a
// MsgBorderFrame envelope into the destination node's inbox. Kept as
// a stub so the unit tests in this package are independent of the
// bridge wiring.
func (v *NodeViewer) Send(frame replication.Frame) {
	_ = frame
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
