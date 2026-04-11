package universe

import (
	"encoding/binary"
	"iter"
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/replication"
)

// BorderDispatcher walks local entities near shared cell boundaries
// and builds a replication.Frame per neighbor via the shared
// pkg/replication dispatcher.
//
// Phase 7.1-7.3: Tick() now runs a real spatial query, builds EntityRef
// candidates for each neighbor, and calls replication.Dispatcher.Walk
// to produce frames. NodeViewer.Send encodes each non-empty Frame into
// a MsgBorderFrame envelope and writes it to the destination node's Inbox.
//
// The old ScanBorderProxies / ScanBorderEntities path still runs in
// PostSystems in parallel — nothing is deleted yet. Phase 7.6 removes it.
type BorderDispatcher struct {
	base      *WorldBase
	neighbors map[string]*NodeViewer
	disp      *replication.Dispatcher
}

// NewBorderDispatcher creates a dispatcher bound to a WorldBase and
// a set of neighbor viewers. Both arguments may be nil for unit
// tests and during Phase 4 scaffolding.
func NewBorderDispatcher(base *WorldBase, neighbors map[string]*NodeViewer) *BorderDispatcher {
	return &BorderDispatcher{
		base:      base,
		neighbors: neighbors,
		disp:      replication.NewDispatcher(),
	}
}

// Tick runs one pass of the border dispatcher. For each neighbor it
// builds a Frame via replication.Dispatcher.Walk and hands the frame
// to NodeViewer.Send.
func (bd *BorderDispatcher) Tick(currentTick uint64) {
	if len(bd.neighbors) == 0 {
		return
	}
	for _, nv := range bd.neighbors {
		if nv == nil {
			continue
		}
		cands := bd.candidatesFor(nv)
		frame := bd.disp.Walk(nv, currentTick, cands)
		nv.Send(frame)
	}
}

// candidatesFor returns an iterator over entities eligible for
// replication to the given neighbor. Uses the same ECS filter pattern
// as ScanBorderProxies: Position, NetworkID, EntityKind, Collider
// excluding Ghost, Replica, Proxy, Dormant. Only entities within
// the AoI margin of the shared edge with nv are yielded.
func (bd *BorderDispatcher) candidatesFor(nv *NodeViewer) iter.Seq[replication.EntityRef] {
	return func(yield func(replication.EntityRef) bool) {
		if bd.base == nil {
			return
		}
		world := bd.base.ECSWorld()
		cellSize := coords.CellSize
		lMinX, lMinY, lMaxX, lMaxY := bd.base.Cell().LocalBounds(cellSize)
		margin := bd.base.GetAoIRadius()
		if margin <= 0 {
			margin = 100
		}

		filter := ecs.NewFilter4[component.Position, component.NetworkID, component.EntityKind, component.Collider](world).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy](), ecs.C[component.Dormant]())
		velMap := ecs.NewMap1[component.Velocity](world)

		query := filter.Query()
		for query.Next() {
			pos, netID, kind, collider := query.Get()
			entity := query.Entity()

			if !bd.entityNearNeighborEdge(pos, nv, lMinX, lMinY, lMaxX, lMaxY, margin) {
				continue
			}

			// Snapshot values by value so the closure captures stable copies.
			px, py := pos.X, pos.Y
			radius := collider.Radius
			nid := *netID
			_ = entity  // velMap lookup available if needed in future
			_ = velMap

			ref := replication.EntityRef{
				NetID: replication.NetID{ID: nid.ID, Epoch: nid.Epoch},
				Kind:  uint16(kind.Type),
				X:     px,
				Y:     py,
				Build: func(dst []byte) []byte {
					dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(px))
					dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(py))
					dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(radius))
					return dst
				},
			}
			if !yield(ref) {
				query.Close()
				return
			}
		}
	}
}

// entityNearNeighborEdge reports whether an entity at (pos.X, pos.Y) sits
// within margin of the shared edge with the neighbor at direction (dirDX, dirDY).
func (bd *BorderDispatcher) entityNearNeighborEdge(
	pos *component.Position,
	nv *NodeViewer,
	lMinX, lMinY, lMaxX, lMaxY, margin float32,
) bool {
	nearLeft := pos.X < lMinX+margin
	nearRight := pos.X > lMaxX-margin
	nearBottom := pos.Y < lMinY+margin
	nearTop := pos.Y > lMaxY-margin

	switch {
	case nv.dirDX == -1 && nv.dirDY == 0:
		return nearLeft
	case nv.dirDX == 1 && nv.dirDY == 0:
		return nearRight
	case nv.dirDX == 0 && nv.dirDY == -1:
		return nearBottom
	case nv.dirDX == 0 && nv.dirDY == 1:
		return nearTop
	case nv.dirDX == -1 && nv.dirDY == -1:
		return nearLeft && nearBottom
	case nv.dirDX == -1 && nv.dirDY == 1:
		return nearLeft && nearTop
	case nv.dirDX == 1 && nv.dirDY == -1:
		return nearRight && nearBottom
	case nv.dirDX == 1 && nv.dirDY == 1:
		return nearRight && nearTop
	}
	return false
}
