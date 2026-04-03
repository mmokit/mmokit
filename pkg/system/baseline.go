package system

// AckMode controls how baselines advance after sending delta frames.
type AckMode uint8

const (
	// AckReliable auto-advances the baseline after each send.
	// Use for TCP/WebSocket where delivery is guaranteed and ordered.
	AckReliable AckMode = iota

	// AckExplicit advances the baseline only when AckSequence() is called.
	// Use for UDP where packets can be lost or arrive out of order.
	AckExplicit
)

// sentSnapshot records a snapshot sent at a particular sequence number.
type sentSnapshot struct {
	seq  uint32
	data []byte
}

// entityBaseline tracks per-entity acknowledged state for one connection.
type entityBaseline struct {
	// acked is the snapshot the client has confirmed receiving.
	// Deltas are always encoded against this.
	acked []byte

	// ring is a circular buffer of recently sent snapshots.
	// On AckReliable this is unused (acked is promoted immediately).
	// On AckExplicit, entries are retained until the client acks.
	ring     []sentSnapshot
	ringHead int // next write position
	ringLen  int // number of valid entries
}

// pushSent stores a sent snapshot in the ring buffer.
func (b *entityBaseline) pushSent(seq uint32, data []byte) {
	if len(b.ring) == 0 {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	b.ring[b.ringHead] = sentSnapshot{seq: seq, data: cp}
	b.ringHead = (b.ringHead + 1) % len(b.ring)
	if b.ringLen < len(b.ring) {
		b.ringLen++
	}
}

// advanceTo promotes the acked baseline to the snapshot at the given sequence.
// If no exact match is found, it uses the most recent snapshot <= seq.
func (b *entityBaseline) advanceTo(seq uint32) {
	if len(b.ring) == 0 || b.ringLen == 0 {
		return
	}
	// Walk ring from oldest to newest, find best match <= seq.
	var best *sentSnapshot
	start := (b.ringHead - b.ringLen + len(b.ring)) % len(b.ring)
	for i := 0; i < b.ringLen; i++ {
		idx := (start + i) % len(b.ring)
		entry := &b.ring[idx]
		if entry.seq <= seq {
			best = entry
		}
	}
	if best != nil {
		b.acked = best.data
		// Prune entries at or before the acked sequence.
		pruned := 0
		for i := 0; i < b.ringLen; i++ {
			idx := (start + i) % len(b.ring)
			if b.ring[idx].seq <= seq {
				b.ring[idx] = sentSnapshot{} // release memory
				pruned++
			} else {
				break
			}
		}
		b.ringLen -= pruned
		b.ringHead = (start + pruned + b.ringLen) % len(b.ring)
	}
}

// entityPriorityState tracks per-entity replication priority for one connection.
type entityPriorityState struct {
	accumulator    float32 // accumulated priority since last send
	lastSentTick   uint32  // tick when last update was sent
	unchangedTicks uint32  // consecutive ticks with same hash (dormancy tracking)
}

// connectionState tracks all per-connection replication state.
type connectionState struct {
	ackedSeq   uint32
	nextSeq    uint32
	baselines  map[uint32]*entityBaseline    // netID -> baseline
	lastHash   map[uint32]uint64             // netID -> last computed hash
	priorities map[uint32]*entityPriorityState // netID -> priority state
}

func newConnectionState() *connectionState {
	return &connectionState{
		baselines:  make(map[uint32]*entityBaseline),
		lastHash:   make(map[uint32]uint64),
		priorities: make(map[uint32]*entityPriorityState),
	}
}

// getBaseline returns the baseline for an entity, creating one if needed.
func (c *connectionState) getBaseline(netID uint32, ringDepth int) *entityBaseline {
	b, ok := c.baselines[netID]
	if !ok {
		b = &entityBaseline{}
		if ringDepth > 0 {
			b.ring = make([]sentSnapshot, ringDepth)
		}
		c.baselines[netID] = b
	}
	return b
}

// getPriorityState returns the priority state for an entity, creating one if needed.
func (c *connectionState) getPriorityState(netID uint32) *entityPriorityState {
	ps, ok := c.priorities[netID]
	if !ok {
		ps = &entityPriorityState{}
		c.priorities[netID] = ps
	}
	return ps
}

// removePriorityState removes the priority state for an entity.
func (c *connectionState) removePriorityState(netID uint32) {
	delete(c.priorities, netID)
}

// removeBaseline removes the baseline for an entity leaving AoI.
func (c *connectionState) removeBaseline(netID uint32) {
	delete(c.baselines, netID)
	delete(c.lastHash, netID)
	delete(c.priorities, netID)
}
