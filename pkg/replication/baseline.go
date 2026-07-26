package replication

// AckMode controls how baselines advance after sending delta frames.
type AckMode uint8

const (
	// AckReliable auto-advances the baseline after each send accepted by a
	// reliable ordered transport.
	// Use for TCP/WebSocket where delivery is guaranteed and ordered.
	AckReliable AckMode = iota

	// AckExplicit advances the baseline only when AckSequence() is called.
	// Use for UDP where packets can be lost or arrive out of order.
	AckExplicit
)

// SentSnapshot records a snapshot sent at a particular sequence number.
type SentSnapshot struct {
	Seq  uint32
	Data []byte
}

// EntityBaseline tracks per-entity acknowledged state for one viewer.
//
// Fields are exported (unlike the old lowercase types in
// pkg/system/baseline.go) because the dispatcher needs to mutate Acked
// directly while delta-encoding snapshots. The facade methods on
// BaselineStore cover allocation and lookup; everything else is direct
// field access on the hot path. See Phase 3 for the concrete consumer.
type EntityBaseline struct {
	// AuthorityEpoch identifies the entity lifecycle that Acked belongs to.
	// Border replication sets this field and invalidates the baseline when an
	// authority handoff advances the epoch, preventing byte-identical state
	// from being encoded as "unchanged" against the prior lifecycle.
	AuthorityEpoch    uint32
	HasAuthorityEpoch bool

	// FullTailFramesRemaining is border replication's bounded redundancy
	// window for a newly-created or epoch-reset baseline. Each full-tail
	// emission decrements it; unchanged sentinels are suppressed until it
	// reaches zero so one lossy send cannot strand a fresh replica.
	FullTailFramesRemaining uint8

	// Acked is the snapshot the client has confirmed receiving.
	// Deltas are always encoded against this.
	Acked []byte

	// Ring is a circular buffer of recently sent snapshots.
	// On AckReliable this is unused (Acked is promoted immediately).
	// On AckExplicit, entries are retained until the client acks.
	Ring     []SentSnapshot
	RingHead int // next write position
	RingLen  int // number of valid entries

	// InitialHash is the hash of the initial-only fields last sent to this
	// viewer for this entity. HasInitialHash is false until the first send.
	// Used to detect when initial data changed and must be re-sent.
	InitialHash    uint64
	HasInitialHash bool
}

// PushSent stores a sent snapshot in the ring buffer.
func (b *EntityBaseline) PushSent(seq uint32, data []byte) {
	if len(b.Ring) == 0 {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	b.Ring[b.RingHead] = SentSnapshot{Seq: seq, Data: cp}
	b.RingHead = (b.RingHead + 1) % len(b.Ring)
	if b.RingLen < len(b.Ring) {
		b.RingLen++
	}
}

// AdvanceTo promotes the acked baseline to the snapshot at the given sequence.
// If no exact match is found, it uses the most recent snapshot <= seq.
func (b *EntityBaseline) AdvanceTo(seq uint32) {
	if len(b.Ring) == 0 || b.RingLen == 0 {
		return
	}
	// Walk ring from oldest to newest, find best match <= seq.
	var best *SentSnapshot
	start := (b.RingHead - b.RingLen + len(b.Ring)) % len(b.Ring)
	for i := 0; i < b.RingLen; i++ {
		idx := (start + i) % len(b.Ring)
		entry := &b.Ring[idx]
		if int32(entry.Seq-seq) <= 0 {
			best = entry
		}
	}
	if best != nil {
		b.Acked = best.Data
		// Prune entries at or before the acked sequence.
		pruned := 0
		for i := 0; i < b.RingLen; i++ {
			idx := (start + i) % len(b.Ring)
			if int32(b.Ring[idx].Seq-seq) <= 0 {
				b.Ring[idx] = SentSnapshot{} // release memory
				pruned++
			} else {
				break
			}
		}
		b.RingLen -= pruned
		b.RingHead = (start + pruned + b.RingLen) % len(b.Ring)
	}
}

// PruneSentThrough releases attempted snapshots at or before seq without
// changing Acked. Explicit-ACK replication uses this after promoting the exact
// frame transaction rather than inferring acknowledged state from ring order.
func (b *EntityBaseline) PruneSentThrough(seq uint32) {
	if len(b.Ring) == 0 || b.RingLen == 0 {
		return
	}
	start := (b.RingHead - b.RingLen + len(b.Ring)) % len(b.Ring)
	pruned := 0
	for i := 0; i < b.RingLen; i++ {
		idx := (start + i) % len(b.Ring)
		entry := b.Ring[idx]
		if int32(entry.Seq-seq) > 0 {
			break
		}
		b.Ring[idx] = SentSnapshot{}
		pruned++
	}
	b.RingLen -= pruned
	// RingHead remains the next write position. The retained suffix stays in
	// its original circular slots, avoiding overlap corruption when start is
	// near the end of the backing array.
}

// ClearSent releases every unacknowledged attempted snapshot.
func (b *EntityBaseline) ClearSent() {
	clear(b.Ring)
	b.RingHead = 0
	b.RingLen = 0
}

// EntityPriorityState tracks per-entity replication priority for one viewer.
type EntityPriorityState struct {
	Accumulator    float32 // accumulated priority since last send
	LastSentTick   uint32  // tick when last update was sent
	UnchangedTicks uint32  // consecutive ticks with same hash (dormancy tracking)
}

// BaselineStore holds per-entity acknowledged snapshots for one viewer.
// Wraps EntityBaseline and EntityPriorityState collections with convenience
// methods for the dispatcher. The old per-connection 'ackedSeq' / 'nextSeq'
// fields from pkg/system/baseline.go stay with the system package in
// Phase 3 — they are NOT part of BaselineStore.
type BaselineStore struct {
	mode       AckMode
	baselines  map[uint32]*EntityBaseline
	lastHash   map[uint32]uint64
	priorities map[uint32]*EntityPriorityState
}

// NewBaselineStore creates an empty store with the given ack mode.
func NewBaselineStore(mode AckMode) *BaselineStore {
	return &BaselineStore{
		mode:       mode,
		baselines:  make(map[uint32]*EntityBaseline),
		lastHash:   make(map[uint32]uint64),
		priorities: make(map[uint32]*EntityPriorityState),
	}
}

// Mode returns the ack mode this store was created with.
func (s *BaselineStore) Mode() AckMode {
	return s.mode
}

// Baseline returns the baseline for an entity, or nil if absent.
// Does NOT create on miss — use GetOrCreateBaseline for that.
func (s *BaselineStore) Baseline(netID uint32) *EntityBaseline {
	return s.baselines[netID]
}

// GetOrCreateBaseline returns the baseline for an entity, allocating
// on first access. ringDepth is passed through to the new EntityBaseline
// if allocation occurs; ignored if the entity already has a baseline.
func (s *BaselineStore) GetOrCreateBaseline(netID uint32, ringDepth int) *EntityBaseline {
	b, ok := s.baselines[netID]
	if !ok {
		b = &EntityBaseline{}
		if ringDepth > 0 {
			b.Ring = make([]SentSnapshot, ringDepth)
		}
		s.baselines[netID] = b
	}
	return b
}

// SetBaseline explicitly installs a baseline (used by baseline handover
// in Phase 5 when receiving a new authority's state).
func (s *BaselineStore) SetBaseline(netID uint32, b *EntityBaseline) {
	s.baselines[netID] = b
}

// DropBaseline removes all state for an entity (AoI exit or removal).
// Removes the baseline, hash, and priority entries atomically.
func (s *BaselineStore) DropBaseline(netID uint32) {
	delete(s.baselines, netID)
	delete(s.lastHash, netID)
	delete(s.priorities, netID)
}

// LastHash returns the most recent content hash for an entity, or 0.
// Callers that need to distinguish "hash not recorded" from "recorded zero"
// should use HasLastHash first.
func (s *BaselineStore) LastHash(netID uint32) uint64 {
	return s.lastHash[netID]
}

// HasLastHash reports whether the store has a recorded hash for the given
// entity. Use this to distinguish first-sighting-this-tick from
// seen-before-but-coincidentally-zero-hash when making dormancy decisions.
func (s *BaselineStore) HasLastHash(netID uint32) bool {
	_, ok := s.lastHash[netID]
	return ok
}

// SetLastHash records the content hash for an entity.
func (s *BaselineStore) SetLastHash(netID uint32, hash uint64) {
	s.lastHash[netID] = hash
}

// Priority returns the accumulator state for an entity, allocating on
// first access. Always returns a non-nil pointer.
func (s *BaselineStore) Priority(netID uint32) *EntityPriorityState {
	ps, ok := s.priorities[netID]
	if !ok {
		ps = &EntityPriorityState{}
		s.priorities[netID] = ps
	}
	return ps
}

// ExistingPriority returns the priority state for an entity without
// allocating it. Callers must treat the returned pointer as read-only unless
// they intend to mutate the store immediately; use Priority to allocate on a
// miss.
func (s *BaselineStore) ExistingPriority(netID uint32) *EntityPriorityState {
	return s.priorities[netID]
}

// ForEachBaseline invokes fn for every baseline currently in the store.
// Iteration order is unspecified. fn must not mutate the store's baseline
// map (call DropBaseline after iteration if you need to remove entries).
func (s *BaselineStore) ForEachBaseline(fn func(netID uint32, b *EntityBaseline)) {
	for id, b := range s.baselines {
		fn(id, b)
	}
}
