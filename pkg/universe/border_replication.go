package universe

import (
	"iter"

	"github.com/zenion/mmoserver/pkg/replication"
)

// BorderDispatcher walks local entities near shared cell boundaries
// and builds a replication.Frame per neighbor via the shared
// pkg/replication dispatcher.
//
// Phase 4 (this file): standalone and inert. Tick() walks the neighbor
// map but yields no candidates, so no frames are produced and
// NodeViewer.Send() is never called with real content.
//
// Phase 7 replaces candidatesFor with a real spatial query that
// iterates entities within the maximum tier radius of each shared
// boundary, builds EntityRef values via the ReplicationRegistry,
// and hooks the dispatcher into node_bridge_impl.PostSystems.
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
//
// Phase 4: candidatesFor always yields an empty sequence, so every
// frame is empty and NodeViewer.Send receives a frame with no entries.
// That is fine — the stub exists to reserve the shape, not to ship
// traffic. Phase 7 lights this up with a real spatial query.
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
// replication to the given neighbor. Phase 4 returns an empty
// iterator; Phase 7 will implement the real spatial query + filter.
func (bd *BorderDispatcher) candidatesFor(nv *NodeViewer) iter.Seq[replication.EntityRef] {
	return func(yield func(replication.EntityRef) bool) {
		// Phase 4 stub: no entities yielded.
		_ = nv
	}
}
