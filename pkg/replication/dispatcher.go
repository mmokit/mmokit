package replication

import "iter"

// EntityRef is a lightweight handle the dispatcher uses to evaluate one
// candidate entity without depending on the caller's ECS layout. Callers
// construct an EntityRef per candidate and yield them through an iter.Seq
// to Dispatcher.Walk.
//
// The Build closure is invoked only if the dispatcher decides to send
// this entity to this viewer, so the caller pays zero serialization cost
// for entities that are filtered out by tier radius or update divisor.
type EntityRef struct {
	// NetID identifies the entity on the wire. Epoch is propagated into
	// the emitted FrameEntry so receivers can drop stale frames across
	// authority transfers.
	NetID NetID

	// Kind is the entity kind used to look up tier configuration on
	// the viewer.
	Kind uint16

	// X, Y is the entity's world-space position for tier distance checks.
	X, Y float32

	// Build returns the delta-encoded bytes for the entity's current
	// state. Returning nil or an empty slice tells the dispatcher to
	// drop this entity silently (useful when the caller discovers mid-
	// build that there are no meaningful changes).
	//
	// TODO(phase3): consider changing to `func(dst []byte) []byte` so
	// callers can reuse a scratch buffer from a sync.Pool and avoid a
	// per-entity allocation on every tick. Worth evaluating before
	// Phase 3 sprawls call sites across pkg/system and pkg/universe.
	Build func() []byte
}

// Dispatcher builds per-viewer frames from a candidate entity iterator.
// It is stateless across calls; all per-viewer state (baselines, priorities)
// lives in the Viewer's BaselineStore.
type Dispatcher struct{}

// NewDispatcher returns a fresh dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// Walk consumes the candidate iterator once, filters each candidate by
// tier radius and update divisor, invokes Build for the survivors, and
// returns a fully-populated Frame. The returned Frame is a fresh value —
// callers own it and may mutate or forward freely.
//
// Walk populates Frame.ViewerID and Frame.Tick from its arguments but
// leaves Frame.SenderNode as the zero value — the caller is responsible
// for stamping SenderNode post-Walk (the sender's node identity is
// knowable only at the caller site: Phase 3 client dispatch and Phase 7
// border replication set it at different points in the pipeline).
//
// Walk never retains the iterator or the EntityRef values past its
// return. Callers may reuse the underlying storage for the next tick.
func (d *Dispatcher) Walk(
	viewer Viewer,
	tick uint64,
	candidates iter.Seq[EntityRef],
) Frame {
	vx, vy := viewer.Position()
	frame := Frame{
		ViewerID: viewer.ID(),
		Tick:     tick,
	}
	for ref := range candidates {
		tier := viewer.Tier(ref.Kind)
		if !InsideRadius(tier, vx, vy, ref.X, ref.Y) {
			continue
		}
		if SkipThisTick(tier, tick) {
			continue
		}
		if ref.Build == nil {
			continue
		}
		delta := ref.Build()
		if len(delta) == 0 {
			continue
		}
		frame.Entries = append(frame.Entries, FrameEntry{
			NetID:    ref.NetID,
			Kind:     ref.Kind,
			DeltaBuf: delta,
		})
	}
	return frame
}
