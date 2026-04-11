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

	// Build appends the delta-encoded bytes for this entity's current
	// state to dst and returns the extended slice. The dispatcher passes
	// a zero-length slice whose capacity is a reusable scratch buffer;
	// Build should use append to avoid a fresh allocation on every tick.
	//
	// Returning dst unchanged (len(result) == 0) tells the dispatcher to
	// drop this entity silently, which is useful when the caller
	// discovers mid-build that there are no meaningful changes.
	//
	// Build must not retain the returned slice past the call — the
	// dispatcher copies the bytes into the frame entry before yielding
	// control back for the next candidate, and the scratch buffer is
	// reused across candidates within a single Walk.
	Build func(dst []byte) []byte
}

// Dispatcher builds per-viewer frames from a candidate entity iterator.
// It is stateless across calls; all per-viewer state (baselines, priorities)
// lives in the Viewer's BaselineStore.
//
// Phase 2 scope: tier radius filtering, update-divisor skipping, and
// per-viewer frame assembly. Phase 3 will extend the Walk loop with
// baseline reads, priority accumulation on skipped ticks, priority reset
// on successful sends, and dormancy hash checks. Readers should not
// assume this is the final shape.
type Dispatcher struct {
	// scratch is reused across Build calls within a single Walk to
	// amortize delta-encoding allocations. Grows as needed and persists
	// across Walk calls so repeated Walks on the same dispatcher instance
	// reuse the same capacity.
	scratch []byte
}

// NewDispatcher returns a fresh dispatcher with a small initial scratch
// buffer. Callers that expect large frames can pre-grow the capacity by
// wrapping with sync.Pool or sharing one dispatcher across ticks.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{scratch: make([]byte, 0, 512)}
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
		// Offer the reusable scratch as a zero-length slice with existing
		// capacity. Build appends into it and returns the grown slice;
		// we copy out into a fresh allocation owned by the frame entry.
		d.scratch = d.scratch[:0]
		delta := ref.Build(d.scratch)
		if len(delta) == 0 {
			continue
		}
		// Copy out so the next iteration's Build can safely reuse the
		// scratch buffer. The frame entry must own a stable slice.
		entryBuf := make([]byte, len(delta))
		copy(entryBuf, delta)
		// Preserve any capacity growth from Build back into scratch.
		if cap(delta) > cap(d.scratch) {
			d.scratch = delta[:0]
		}
		frame.Entries = append(frame.Entries, FrameEntry{
			NetID:    ref.NetID,
			Kind:     ref.Kind,
			DeltaBuf: entryBuf,
		})
	}
	return frame
}
