package system

import (
	"math"
	"sort"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// ClusterClock is the minimum surface ReplicationSystem needs from a
// cluster-coherent wall clock. pkg/universe.ClusterClock satisfies this
// structurally. Declared here (not imported from pkg/universe) because
// pkg/system cannot import pkg/universe without creating a cycle
// through pkg/mmokit.
type ClusterClock interface {
	// Now returns the current cluster-coherent wall-clock in milliseconds.
	// Used for diagnostics and non-replication timing (e.g. cooldowns).
	Now() uint64
	// TickTime returns the cluster-coherent wall clock quantized to the
	// nearest tick boundary. This is the canonical stamp for client-visible
	// replication samples: stagger between asynchronously-ticking cells
	// collapses to zero on this axis, so the client's interpolator sees
	// speed-continuous motion across authority handoff.
	TickTime(tickIntervalMs uint64) uint64
}

// ViewerInfo describes a connection that receives replicated entity state.
type ViewerInfo struct {
	ConnID      uint32
	Entity      ecs.Entity
	X, Y        float32
	StreamEpoch uint32 // generation that scopes this viewer's whole frame stream
}

// FullPayload is a full entity snapshot for new or keyframe entities.
type FullPayload struct {
	NetID        uint32
	Epoch        uint32 // authority handoff epoch from NetworkID (0 until Phase 5)
	Type         uint8
	ProducedAtMs uint64 // authoritative producer's ClusterClock.TickTime at emit (tick-aligned)
	Snapshot     []byte // full snapshot bytes
	InitialData  []byte // nil unless first time visible
}

// DeltaPayload is a delta-encoded entity update.
type DeltaPayload struct {
	NetID        uint32
	Epoch        uint32 // authority handoff epoch from NetworkID (0 until Phase 5)
	Type         uint8
	ProducedAtMs uint64 // authoritative producer's ClusterClock.TickTime at emit (tick-aligned)
	Data         []byte // bitmask + changed fields from DeltaEncoder
}

// ReplicationFrame is the per-viewer per-tick replication data passed to the FrameWriter.
type ReplicationFrame struct {
	Tick              uint32
	Seq               uint32 // frame sequence number for client ack tracking
	StreamEpoch       uint32 // session replication generation; scopes Seq across restarts
	Flags             uint32 // wire-format header flags (quantize.FrameFlag*)
	HasInputAck       bool
	ProcessedInputSeq uint32 // game-defined input stream reflected by this frame
	Viewer            *ViewerInfo
	Full              []FullPayload
	Deltas            []DeltaPayload
	Entered           []uint32 // netIDs newly visible to this viewer
	Exited            []uint32 // netIDs that left AoI (entity still alive)
	Removed           []uint32 // netIDs destroyed/despawned this tick
}

// ---------------------------------------------------------------------------
// Interfaces — game implementations plug in here
// ---------------------------------------------------------------------------

// ViewerSource provides the set of active viewers each tick.
type ViewerSource interface {
	ActiveViewers() []ViewerInfo
}

// ViewerLiveness is an optional capability on ViewerSource. It distinguishes
// a viewer that is temporarily omitted from ActiveViewers from one whose
// session is truly gone. Temporarily omitted viewers retain their frame
// sequence and committed replication state, but resume with a fresh snapshot.
type ViewerLiveness interface {
	ViewerExists(connID uint32) bool
}

// EntityReplicator defines how one entity type participates in replication.
// The system calls Hash for fast diff detection, Snapshot for compact binary
// state capture, and uses SnapshotLayout for field-level delta encoding.
type EntityReplicator interface {
	EntityType() uint8

	// Hash writes all diff-relevant fields into the hasher.
	// Fast pre-check: if hash unchanged and not keyframe, Snapshot() is not called.
	Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry)

	// Snapshot writes the entity's quantized transport state into a compact binary buffer.
	// Fields must match SnapshotLayout order.
	Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry)

	// SnapshotLayout returns field sizes for the DeltaEncoder.
	// Called once at registration time and cached by the system.
	// Positive values are fixed-size fields. A single -1 as the last element
	// indicates a variable-length tail.
	SnapshotLayout() []int

	// InitialData returns one-time data for newly-visible entities (name, skin, etc).
	// Returns nil if no initial data. Equivalent to Unreal's COND_InitialOnly.
	InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte

	// HasInitial reports whether this replicator has any initial-only fields.
	HasInitial() bool

	// InitialHash writes only the initial-only fields into the hasher, so the
	// system can detect when initial data changed and re-send it to already
	// visible viewers. Only called when HasInitial() is true.
	InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry)
}

// RelevancyProvider is an optional interface on EntityReplicator.
// Implement it for custom relevancy rules beyond simple AoI radius.
type RelevancyProvider interface {
	IsRelevantTo(viewer *ViewerInfo, entry spatial.Entry) bool
}

// PriorityProvider is an optional interface on EntityReplicator.
// Implement it for priority-based bandwidth allocation.
type PriorityProvider interface {
	NetPriority(viewer *ViewerInfo, entry spatial.Entry) float32
}

// ReplicationTier configures per-entity-type replication behavior.
type ReplicationTier struct {
	Radius        float32 // AoI radius for this type (0 = use global AoIRadius)
	UpdateDivisor uint32  // send every N ticks (1 = every tick, 3 = every 3rd)
	BaseWeight    float32 // priority accumulator weight (higher = more important)
}

// TierProvider is an optional interface on EntityReplicator.
type TierProvider interface {
	ReplicationTier() ReplicationTier
}

// FrameWriter assembles a ReplicationFrame into wire-format bytes and submits
// it to the viewer's transport. Replication state advances only when the
// result reports reliable ordered acceptance.
type FrameWriter interface {
	WriteFrame(frame *ReplicationFrame) net.SendResult
}

// ---------------------------------------------------------------------------
// ReplicatorRegistry
// ---------------------------------------------------------------------------

// ReplicatorRegistry maps entity type constants to their EntityReplicator.
type ReplicatorRegistry struct {
	replicators map[uint8]EntityReplicator
}

// NewReplicatorRegistry creates an empty replicator registry.
func NewReplicatorRegistry() *ReplicatorRegistry {
	return &ReplicatorRegistry{
		replicators: make(map[uint8]EntityReplicator),
	}
}

// Register adds a replicator for its entity type (from EntityType()).
func (r *ReplicatorRegistry) Register(rep EntityReplicator) {
	r.replicators[rep.EntityType()] = rep
}

// RegisterForType adds a replicator for a specific entity type, overriding
// the replicator's own EntityType().
func (r *ReplicatorRegistry) RegisterForType(entityType uint8, rep EntityReplicator) {
	r.replicators[entityType] = rep
}

// Get returns the replicator for the given entity type, or nil.
func (r *ReplicatorRegistry) Get(entityType uint8) EntityReplicator {
	return r.replicators[entityType]
}

// Schema returns entity schemas for all registered replicators that implement
// SchemaProvider, sorted by Kind for deterministic codegen output. AutoReplicator
// implements it automatically; hand-coded replicators can opt in by implementing
// the SchemaProvider interface.
func (r *ReplicatorRegistry) Schema() []EntitySchema {
	var schemas []EntitySchema
	for _, rep := range r.replicators {
		if sp, ok := rep.(SchemaProvider); ok {
			schemas = append(schemas, sp.Schema())
		}
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].Kind < schemas[j].Kind })
	return schemas
}

// ---------------------------------------------------------------------------
// ReplicationConfig
// ---------------------------------------------------------------------------

// ReplicationConfig holds all dependencies for the ReplicationSystem.
type ReplicationConfig struct {
	World       *ecs.World
	SpatialGrid *spatial.HashGrid
	Viewers     ViewerSource
	Frame       FrameWriter
	Replicators *ReplicatorRegistry

	// ClusterClock stamps locally-authoritative entities with a
	// cluster-coherent tick-aligned time at emit via ClusterClock.TickTime.
	// Replicas re-use their cached Replica.ProducedAtMs (populated by the
	// border-frame codec) so a client's view of a replicated entity
	// carries the SOURCE cell's producer stamp, not the receiver's emit
	// time. If nil, the system falls back to the local wall clock —
	// acceptable for single-cell tests, never correct across hosts.
	ClusterClock ClusterClock

	// TickIntervalMs is the game-loop tick interval used to quantize
	// ClusterClock stamps to tick boundaries. Required alongside
	// ClusterClock; DefaultReplicationConfig wires it from eng.TickIntervalMs.
	TickIntervalMs uint64

	AoIRadius           float32
	GetAoIRadius        func() float32 // dynamic AoI radius (overrides AoIRadius if set)
	FullRefreshInterval uint32         // ticks between forced keyframe (0 = disabled)
	DormancyThreshold   uint32         // ticks unchanged before entity goes dormant (0 = disabled)

	// AckMode controls baseline advancement. Used only when AckModeFor is
	// nil, and as the fallback for connections AckModeFor cannot classify.
	AckMode replication.AckMode

	// AckModeFor resolves the ACK mode for one connection from its
	// transport's STATIC delivery class. Resolved exactly once, when the
	// connection's state is created, and latched for its lifetime — a single
	// ConnManager holds a mixed map of WebSocket and UDP transports, so two
	// viewers of the same cell can legitimately have different delivery
	// classes. Must never be driven from mutable runtime state (queue depth,
	// backpressure, a SendResult): the generated wire schema is built
	// assuming the answer is a property of the transport type.
	//
	// nil falls back to AckMode.
	AckModeFor func(connID uint32) replication.AckMode

	// RegisterAck is invoked once at construction with this system's AckFrame
	// method so the inbound typed-client-input path can reach it. nil
	// disables explicit ACK routing (unit tests call AckFrame directly).
	RegisterAck func(ack func(connID, streamEpoch, seq uint32))

	// SentHistoryDepth is the ring buffer depth for AckExplicit mode.
	// Default 4. Ignored for AckReliable.
	//
	// The depth is small on purpose. At most one causal attempt is in flight
	// per connection (see pendingReplicationAttempt), so the ring never holds
	// more than one live entry: retainSent pushes one per frame,
	// commitExplicit prunes through the acked seq, discardPendingAttempt
	// clears. Meanwhile GetOrCreateBaseline eagerly allocates
	// ringDepth * sizeof(SentSnapshot) PER ENTITY PER VIEWER, so the previous
	// default of 32 was pure waste.
	//
	// This is only safe under the one-in-flight invariant. Pipelined delta
	// replication (CE-003b) must raise it in the same change that relaxes
	// that invariant.
	SentHistoryDepth int

	// PendingReceiptTimeoutTicks bounds how long an AckReliable connection
	// waits for an asynchronous final-enqueue receipt before abandoning the
	// attempt and forcing a fresh full snapshot. Default 3 ticks.
	PendingReceiptTimeoutTicks uint32

	// PendingAckTimeoutTicks bounds how long AckExplicit waits for the
	// matching application ACK before abandoning the attempt and forcing a
	// fresh full snapshot. Default 10 ticks (~500ms at 20Hz).
	//
	// Observable behaviour, now that this has a production consumer. While an
	// attempt is in flight the per-viewer loop skips emission entirely, so a
	// datagram viewer's effective AoI frame rate is the tick rate when
	// RTT <= tickInterval (the ACK lands in the typed-client-input phase,
	// which runs before the system loop, so it commits in the same tick), and
	// ceil(RTT/tickInterval) ticks otherwise. A LOST ack costs one full
	// PendingAckTimeoutTicks stall followed by a self-contained
	// FreshSnapshot.
	//
	// Do not lower the default without measuring: at ~4 ticks every link with
	// RTT > 200 ms would time out on every frame and degrade to permanent
	// full snapshots — strictly worse than no explicit ACK at all. The real
	// fix is a per-connection adaptive timeout derived from observed ACK RTT.
	//
	// OnBeforeSend/OnAfterSend still fire on skipped ticks, so per-tick
	// own-state events and client prediction are unaffected; only other
	// entities' AoI updates slow down.
	PendingAckTimeoutTicks uint32

	// GetTick returns the current game tick number.
	GetTick func() uint32

	// RemovedIDs returns netIDs of entities removed this tick.
	RemovedIDs func() []uint32

	// SnapshotBufSize is the pre-allocated buffer size for SnapshotWriter.
	// Default 256 bytes. Increase if entity snapshots are larger.
	SnapshotBufSize int

	// Optional per-tick lifecycle callbacks.
	OnBeforeTick func(tick uint32)
	OnAfterTick  func(tick uint32)
	OnBeforeSend func(viewer *ViewerInfo, visible map[uint32]bool)
	OnAfterSend  func(viewer *ViewerInfo, visible map[uint32]bool)

	// ProcessedInputSeq optionally returns the newest sequence from one
	// game-defined, idempotent prediction stream whose effects are included in
	// this viewer's frame. It is encoded atomically with the world update so a
	// client never reconciles an acknowledgement against the wrong snapshot.
	ProcessedInputSeq func(viewer *ViewerInfo) (uint32, bool)

	// StreamGeneration optionally returns the session-scoped generation that
	// fences the viewer's entire replication frame stream. A wrap-safe forward
	// change starts a new seq=1 fresh-snapshot stream. Returning false (or
	// leaving this nil) preserves the legacy NetworkID epoch fallback.
	StreamGeneration func(viewer *ViewerInfo) (uint32, bool)

	// BlinkDetectorTicks is the recent-removals window size. 0 disables
	// the detector entirely. Typical value: 30 (1.5s at 20Hz).
	BlinkDetectorTicks uint64

	// OnBlinkDetected is called when a SPAWN is about to be emitted for
	// a (connID, netID) that was the subject of a SE_ENTITY_REMOVED
	// within BlinkDetectorTicks ticks. Implementations record to the
	// commit log and (in InvariantPanic mode) panic. nil disables.
	OnBlinkDetected func(connID, netID uint32, ticksSinceRemove uint64)
}

// ---------------------------------------------------------------------------
// connState — per-connection system-level state
// ---------------------------------------------------------------------------

// connState holds system-specific per-connection fields alongside a
// BaselineStore that manages baselines, hashes, and priorities.
type connState struct {
	// mode is this connection's ACK mode, latched from its transport's
	// static delivery class when the state was created. It must stay in sync
	// with the BaselineStore, which is constructed from the same value — so
	// changing a connection's mode means replacing the whole connState, never
	// mutating this field in place.
	mode  replication.AckMode
	store *replication.BaselineStore
	txn   replicationStateTxn
	// pendingScratch owns the transaction and slice storage for the sole
	// in-flight attempt. On submission its transaction is swapped with txn,
	// giving the attempt exclusive ownership without copying every staged
	// entity. The two transactions alternate as receipts are resolved.
	pendingScratch pendingReplicationAttempt
	pending        *pendingReplicationAttempt
	ackedSeq       uint32
	nextSeq        uint32
	// streamEpoch scopes frame ordering. Prefer the explicit, session-scoped
	// StreamGeneration callback; NetworkID epoch capture remains as a legacy
	// fallback when that callback is unavailable.
	streamEpoch    uint32
	hasStreamEpoch bool
	// hasExplicitGeneration distinguishes the session generation domain from
	// the legacy NetworkID epoch fallback. The first explicit value adopts that
	// domain; only wrap-safe forward values may replace it thereafter.
	hasExplicitGeneration bool
	// forceFresh makes the next submitted frame a complete authoritative
	// reset. It is set when an attempt with uncertain delivery is abandoned,
	// so a late copy cannot make the next delta depend on ambiguous state.
	forceFresh bool
	// selfNetID is the NetworkID of the viewer's own player entity as
	// seen on the most recent active tick. Cached here so the farewell
	// loop (which runs after the session is gone from ActiveViewers)
	// can exclude it from the Removed list: when a player hands off to
	// another cell, the destination cell takes ownership of that netID
	// and will send its own Entered frame to the client. If this cell's
	// farewell said "remove netID=P" the client would drop an entity
	// the destination just installed, causing a black screen that
	// persists until the next handoff or reconnect.
	selfNetID uint32

	// recentRemovals maps netID → the tick at which SE_ENTITY_REMOVED
	// was most recently emitted for this connection. Consulted on every
	// subsequent SE_ENTITY_SPAWN emission by the blink-detector path:
	// if the SPAWN arrives within BlinkDetectorTicks of the removal,
	// it's a client-visible blink. GC'd in-band each tick (entries
	// older than the window are dropped).
	recentRemovals map[uint32]uint64

	// pendingRemoved retains destroyed netIDs whose removal frame was not
	// accepted by a reliable ordered transport. RemovedIDs is a per-tick feed,
	// so without this outbox a rejected removal would degrade into an AoI exit
	// on the next tick and the client would never receive the tombstone.
	pendingRemoved map[uint32]bool
}

func newConnState(mode replication.AckMode) *connState {
	store := replication.NewBaselineStore(mode)
	return &connState{
		mode:  mode,
		store: store,
		txn:   newReplicationStateTxn(store),
		pendingScratch: pendingReplicationAttempt{
			txn: newReplicationStateTxn(store),
		},
		recentRemovals: make(map[uint32]uint64),
		pendingRemoved: make(map[uint32]bool),
	}
}

// stagedEntityReplicationState is a copy-on-write overlay for one entity's
// per-viewer replication state. Frame construction mutates only this overlay;
// the committed BaselineStore is updated after the frame has been accepted by
// a reliable ordered transport.
type stagedEntityReplicationState struct {
	netID uint32

	baseline      replication.EntityBaseline
	baselineReady bool
	baselineDirty bool

	priority      replication.EntityPriorityState
	priorityReady bool
	priorityDirty bool

	lastHash      uint64
	lastHashDirty bool

	// promoteSnapshot is the exact snapshot carried by this frame. Explicit
	// ACK mode retains it as attempted state, but does not install it as Acked
	// until the matching frame sequence is acknowledged.
	promoteSnapshot []byte
	promoteReady    bool

	drop bool
}

// replicationStateTxn stages hot-path baseline, hash, and priority mutations
// without cloning the entire connection store. Scratch is owned by connState
// and reused across ticks. Entity state lives inline in a slice, with the map
// storing slice indices, so steady-state staging does not allocate one heap
// object per visible entity. Sent-snapshot rings are cloned lazily only when
// PushSent is actually required.
type replicationStateTxn struct {
	store     *replication.BaselineStore
	ringDepth int
	indices   map[uint32]int
	entities  []stagedEntityReplicationState
}

func newReplicationStateTxn(store *replication.BaselineStore) replicationStateTxn {
	return replicationStateTxn{
		store:   store,
		indices: make(map[uint32]int),
	}
}

// begin resets the transaction while retaining its map buckets and entity
// storage. Clearing the prior states releases any rejected-frame snapshots
// that were not also retained by the committed BaselineStore.
func (tx *replicationStateTxn) begin(ringDepth int) *replicationStateTxn {
	clear(tx.indices)
	clear(tx.entities)
	tx.entities = tx.entities[:0]
	tx.ringDepth = ringDepth
	return tx
}

func (tx *replicationStateTxn) entity(netID uint32) *stagedEntityReplicationState {
	if idx, ok := tx.indices[netID]; ok {
		return &tx.entities[idx]
	}
	tx.entities = append(tx.entities, stagedEntityReplicationState{netID: netID})
	idx := len(tx.entities) - 1
	tx.indices[netID] = idx
	return &tx.entities[idx]
}

func (tx *replicationStateTxn) existingEntity(netID uint32) *stagedEntityReplicationState {
	if idx, ok := tx.indices[netID]; ok {
		return &tx.entities[idx]
	}
	return nil
}

func (tx *replicationStateTxn) baseline(netID uint32) *replication.EntityBaseline {
	state := tx.entity(netID)
	if !state.baselineReady {
		if current := tx.store.Baseline(netID); current != nil {
			state.baseline = *current
		}
		state.baselineReady = true
	}
	state.baselineDirty = true
	return &state.baseline
}

func (tx *replicationStateTxn) pushSent(netID uint32, snapshot []byte) {
	// The acknowledged metadata belongs to the frozen transaction, but the
	// attempted-snapshot ring is retained directly in the committed store only
	// after transport admission. Copying that ring into every transaction would
	// allocate O(visible entities) and is unnecessary: commitExplicit preserves
	// the live ring while promoting this exact snapshot.
	tx.baseline(netID)
	state := tx.entity(netID)
	state.promoteSnapshot = snapshot
	state.promoteReady = true
}

// retainSent records only the snapshots that were actually queued. It leaves
// Acked, hashes, priorities, initial metadata, and visibility untouched.
func (tx *replicationStateTxn) retainSent(seq uint32) {
	for i := range tx.entities {
		state := &tx.entities[i]
		if !state.promoteReady || state.drop {
			continue
		}
		tx.store.GetOrCreateBaseline(state.netID, tx.ringDepth).PushSent(seq, state.promoteSnapshot)
	}
}

func (tx *replicationStateTxn) priority(netID uint32) *replication.EntityPriorityState {
	state := tx.entity(netID)
	if !state.priorityReady {
		if current := tx.store.ExistingPriority(netID); current != nil {
			state.priority = *current
		}
		state.priorityReady = true
	}
	state.priorityDirty = true
	return &state.priority
}

func (tx *replicationStateTxn) hasLastHash(netID uint32) bool {
	if state := tx.existingEntity(netID); state != nil && state.lastHashDirty {
		return true
	}
	return tx.store.HasLastHash(netID)
}

func (tx *replicationStateTxn) lastHash(netID uint32) uint64 {
	if state := tx.existingEntity(netID); state != nil && state.lastHashDirty {
		return state.lastHash
	}
	return tx.store.LastHash(netID)
}

func (tx *replicationStateTxn) setLastHash(netID uint32, hash uint64) {
	state := tx.entity(netID)
	state.lastHash = hash
	state.lastHashDirty = true
}

func (tx *replicationStateTxn) drop(netID uint32) {
	tx.entity(netID).drop = true
}

func (tx *replicationStateTxn) commit() {
	for i := range tx.entities {
		state := &tx.entities[i]
		netID := state.netID
		if state.drop {
			tx.store.DropBaseline(netID)
			continue
		}
		if state.baselineDirty {
			// Preserve the identity of an established baseline. Other engine
			// paths (notably explicit ACK advancement) may hold this pointer,
			// and replacing it every tick creates O(viewer*entity) garbage.
			committed := tx.store.Baseline(netID)
			if committed == nil {
				committed = tx.store.GetOrCreateBaseline(netID, 0)
			}
			*committed = state.baseline
		}
		if state.lastHashDirty {
			tx.store.SetLastHash(netID, state.lastHash)
		}
		if state.priorityDirty {
			*tx.store.Priority(netID) = state.priority
		}
	}
}

// commitExplicit promotes exactly the state carried by one acknowledged
// frame. Sent rings may contain the same attempt, but are bookkeeping only;
// they never choose the promoted baseline implicitly.
func (tx *replicationStateTxn) commitExplicit(seq uint32) {
	for i := range tx.entities {
		state := &tx.entities[i]
		netID := state.netID
		if state.drop {
			tx.store.DropBaseline(netID)
			continue
		}
		if state.baselineDirty {
			committed := tx.store.Baseline(netID)
			if committed == nil {
				committed = tx.store.GetOrCreateBaseline(netID, tx.ringDepth)
			}

			// Attempt rings are updated when frames queue and can therefore be
			// newer than this frozen transaction. Preserve the live ring while
			// installing only the acknowledged metadata and snapshot.
			ring := committed.Ring
			ringHead := committed.RingHead
			ringLen := committed.RingLen
			acked := committed.Acked
			*committed = state.baseline
			committed.Ring = ring
			committed.RingHead = ringHead
			committed.RingLen = ringLen
			committed.Acked = acked
			if state.promoteReady {
				committed.Acked = state.promoteSnapshot
			}
		}
		if state.lastHashDirty {
			tx.store.SetLastHash(netID, state.lastHash)
		}
		if state.priorityDirty {
			*tx.store.Priority(netID) = state.priority
		}
	}

	tx.store.ForEachBaseline(func(_ uint32, baseline *replication.EntityBaseline) {
		baseline.PruneSentThrough(seq)
	})
}

// pendingReplicationAttempt is the sole causal frame awaiting either a
// transport receipt (AckReliable) or an application ACK (AckExplicit).
// Keeping at most one prevents later deltas from being encoded against state
// that may or may not have reached the client.
type pendingReplicationAttempt struct {
	streamEpoch uint32
	seq         uint32
	sentTick    uint32
	txn         replicationStateTxn

	visible        map[uint32]bool
	selfNetID      uint32
	recentRemovals map[uint32]uint64
	blinks         []stagedBlinkDetection
	removed        []uint32
	exited         []uint32
	// clearPendingRemoved is captured at submission time. Removals observed
	// while this attempt is in flight must survive its later commit.
	clearPendingRemoved []uint32

	awaitReceipt bool
}

type stagedBlinkDetection struct {
	netID            uint32
	ticksSinceRemove uint64
}

// stagePendingAttempt moves the just-built transaction into the reusable
// pending slot. Swapping transaction scratch buffers avoids an O(entities)
// freeze copy while preserving exclusive ownership: no new frame is built for
// a connection until this attempt commits, is rejected, or times out.
func (conn *connState) stagePendingAttempt(
	streamEpoch, seq, sentTick uint32,
	visible map[uint32]bool,
	selfNetID uint32,
	recentRemovals map[uint32]uint64,
	blinks []stagedBlinkDetection,
	exited []uint32,
	removed []uint32,
	awaitReceipt bool,
) {
	pending := &conn.pendingScratch
	pending.streamEpoch = streamEpoch
	pending.seq = seq
	pending.sentTick = sentTick
	pending.visible = visible
	pending.selfNetID = selfNetID
	pending.recentRemovals = recentRemovals
	pending.blinks = append(pending.blinks[:0], blinks...)
	pending.exited = append(pending.exited[:0], exited...)
	pending.removed = append(pending.removed[:0], removed...)
	pending.clearPendingRemoved = pending.clearPendingRemoved[:0]
	for netID := range visible {
		if conn.pendingRemoved[netID] {
			pending.clearPendingRemoved = append(pending.clearPendingRemoved, netID)
		}
	}
	pending.clearPendingRemoved = append(pending.clearPendingRemoved, removed...)
	pending.awaitReceipt = awaitReceipt

	// conn.txn is the transaction used to build this attempt. Give it to the
	// pending slot and retain the slot's previous scratch for the next frame.
	pending.txn, conn.txn = conn.txn, pending.txn
	conn.pending = pending
}

func cloneRecentRemovals(src map[uint32]uint64) map[uint32]uint64 {
	dst := make(map[uint32]uint64, len(src))
	for netID, tick := range src {
		dst[netID] = tick
	}
	return dst
}

// ---------------------------------------------------------------------------
// ReplicationSystem
// ---------------------------------------------------------------------------

// ReplicationSystem manages per-viewer AoI visibility, hash-based diff
// detection, snapshot-based delta encoding, and frame dispatch.
type ReplicationSystem struct {
	cfg ReplicationConfig

	// ECS component mappers
	netIDMap   *ecs.Map1[component.NetworkID]
	kindMap    *ecs.Map1[component.EntityKind]
	ghostMap   *ecs.Map1[component.Ghost]
	replicaMap *ecs.Map1[component.Replica]
	dormantMap *ecs.Map1[component.Dormant]

	// Per-viewer state
	lastVisible map[uint32]map[uint32]bool // connID -> set of visible netIDs
	connections map[uint32]*connState      // connID -> baseline + hash state

	// Cached delta encoders per entity type
	deltaEncoders map[uint8]*quantize.DeltaEncoder

	// Cached tier configs per entity type
	tierConfigs   map[uint8]ReplicationTier
	maxTierRadius float32 // largest explicit tier radius (0 if none)
	initAoIRadius float32 // AoI radius at init time (fallback)

	// Reusable buffers
	results    []spatial.Entry
	fullBuf    []FullPayload
	deltaBuf   []DeltaPayload
	hasher     Hasher
	initHasher Hasher // initial-fields-only hash, kept separate from `hasher`
	snapWriter *quantize.SnapshotWriter
	snapBuf    []byte
	deltaTmp   []byte // reusable buffer for DeltaEncoder output
}

// NewReplicationSystem creates a replication system with the given configuration.
func NewReplicationSystem(cfg ReplicationConfig) *ReplicationSystem {
	if cfg.SentHistoryDepth == 0 {
		cfg.SentHistoryDepth = 4
	}
	if cfg.PendingReceiptTimeoutTicks == 0 {
		cfg.PendingReceiptTimeoutTicks = 3
	}
	if cfg.PendingAckTimeoutTicks == 0 {
		cfg.PendingAckTimeoutTicks = 10
	}
	snapBufSize := cfg.SnapshotBufSize
	if snapBufSize == 0 {
		snapBufSize = 256
	}

	// Build delta encoders from registered replicators.
	encoders := make(map[uint8]*quantize.DeltaEncoder)
	for entityType, rep := range cfg.Replicators.replicators {
		layout := rep.SnapshotLayout()
		encoders[entityType] = quantize.NewDeltaEncoder(layout...)
	}

	// Cache tier configs from replicators that implement TierProvider.
	defaultTier := ReplicationTier{Radius: 0, UpdateDivisor: 1, BaseWeight: 1.0}
	tierConfigs := make(map[uint8]ReplicationTier)
	var maxTierRadius float32
	for entityType, rep := range cfg.Replicators.replicators {
		if tp, ok := rep.(TierProvider); ok {
			tier := tp.ReplicationTier()
			if tier.UpdateDivisor == 0 {
				tier.UpdateDivisor = 1
			}
			if tier.BaseWeight == 0 {
				tier.BaseWeight = 1.0
			}
			tierConfigs[entityType] = tier
			if tier.Radius > maxTierRadius {
				maxTierRadius = tier.Radius
			}
		} else {
			tierConfigs[entityType] = defaultTier
		}
	}

	snapBuf := make([]byte, snapBufSize)
	sys := &ReplicationSystem{
		cfg:           cfg,
		netIDMap:      ecs.NewMap1[component.NetworkID](cfg.World),
		kindMap:       ecs.NewMap1[component.EntityKind](cfg.World),
		ghostMap:      ecs.NewMap1[component.Ghost](cfg.World),
		replicaMap:    ecs.NewMap1[component.Replica](cfg.World),
		dormantMap:    ecs.NewMap1[component.Dormant](cfg.World),
		lastVisible:   make(map[uint32]map[uint32]bool),
		connections:   make(map[uint32]*connState),
		deltaEncoders: encoders,
		tierConfigs:   tierConfigs,
		maxTierRadius: maxTierRadius,
		initAoIRadius: cfg.AoIRadius,
		results:       make([]spatial.Entry, 0, 256),
		fullBuf:       make([]FullPayload, 0, 64),
		deltaBuf:      make([]DeltaPayload, 0, 128),
		snapBuf:       snapBuf,
		snapWriter:    quantize.NewSnapshotWriter(snapBuf),
		deltaTmp:      make([]byte, 0, 256),
	}
	// Hand the explicit frame-ACK entry point to whatever owns the inbound
	// typed-client-input path (DefaultReplicationConfig wires this to
	// engine.SetReplicationAck). Doing it here means every construction site
	// picks the sink up with no game-code change.
	if cfg.RegisterAck != nil {
		cfg.RegisterAck(sys.AckFrame)
	}
	return sys
}

func (s *ReplicationSystem) Name() string { return "Replication" }

// producedAtMs returns the cluster-clock stamp for an entity about to be
// emitted. Local-authoritative entities are stamped with
// ClusterClock.TickTime (tick-aligned logical simulation time, not
// wall-clock-at-emit) so cell-tick stagger collapses to zero on the
// client's timeline — the seam between source's last sample and
// destination's first sample after a handoff lands exactly one
// tickInterval apart, matching the one physics tick of entity advance.
// Replicas pass through the cached Replica.ProducedAtMs so the client's
// view carries the SOURCE cell's producer stamp (not this cell's emit
// time). When ClusterClock is nil, falls back to the local wall clock —
// OK for single-cell tests, never correct across hosts.
func (s *ReplicationSystem) producedAtMs(entity ecs.Entity) uint64 {
	if s.replicaMap.HasAll(entity) {
		return s.replicaMap.Get(entity).ProducedAtMs
	}
	if s.cfg.ClusterClock != nil {
		return s.cfg.ClusterClock.TickTime(s.cfg.TickIntervalMs)
	}
	return uint64(time.Now().UnixMilli())
}

// aoiRadius returns the current AoI radius, preferring the dynamic getter.
func (s *ReplicationSystem) aoiRadius() float32 {
	if s.cfg.GetAoIRadius != nil {
		return s.cfg.GetAoIRadius()
	}
	return s.initAoIRadius
}

// queryRadius returns the spatial query radius (max of AoI and tier radii).
func (s *ReplicationSystem) queryRadius() float32 {
	r := s.aoiRadius()
	if s.maxTierRadius > r {
		return s.maxTierRadius
	}
	return r
}

// LastVisible returns and removes the visibility set for a connection.
func (s *ReplicationSystem) LastVisible(connID uint32) map[uint32]bool {
	vis := s.lastVisible[connID]
	delete(s.lastVisible, connID)
	delete(s.connections, connID)
	return vis
}

// IsVisible returns whether a specific entity is currently visible to a viewer.
func (s *ReplicationSystem) IsVisible(connID uint32, netID uint32) bool {
	if vis, ok := s.lastVisible[connID]; ok {
		return vis[netID]
	}
	return false
}

// AckSequence promotes the exact pending frame for a legacy connection that
// does not use an explicit session StreamGeneration. New generation-aware
// UDP integrations must call AckFrame so a delayed same-sequence ACK from a
// previous stream cannot commit the current transaction.
func (s *ReplicationSystem) AckSequence(connID, seq uint32) {
	conn, ok := s.connections[connID]
	if !ok || conn.mode != replication.AckExplicit {
		return
	}
	if conn.hasExplicitGeneration || conn.pending == nil || conn.pending.awaitReceipt || conn.pending.seq != seq {
		return
	}
	s.commitPendingAttempt(connID, conn, true)
}

// AckFrame promotes one explicitly scoped UDP replication attempt. Stream
// generation zero is valid and is compared exactly rather than treated as an
// absent value.
func (s *ReplicationSystem) AckFrame(connID, streamEpoch, seq uint32) {
	conn, ok := s.connections[connID]
	if !ok || conn.mode != replication.AckExplicit {
		return
	}
	if conn.pending == nil || conn.pending.awaitReceipt ||
		conn.pending.streamEpoch != streamEpoch || conn.pending.seq != seq {
		return
	}
	s.commitPendingAttempt(connID, conn, true)
}

func (s *ReplicationSystem) commitPendingAttempt(connID uint32, conn *connState, explicit bool) {
	pending := conn.pending
	if pending == nil {
		return
	}
	if explicit {
		pending.txn.commitExplicit(pending.seq)
		conn.ackedSeq = pending.seq
	} else {
		pending.txn.commit()
	}

	s.lastVisible[connID] = pending.visible
	conn.selfNetID = pending.selfNetID
	if s.cfg.BlinkDetectorTicks > 0 {
		conn.recentRemovals = pending.recentRemovals
	}
	for _, netID := range pending.clearPendingRemoved {
		delete(conn.pendingRemoved, netID)
	}
	// A destruction can race an in-flight AoI exit. Once that exact exit
	// commits, the client has already discarded the entity, so a tombstone
	// observed later in the wait window is neither necessary nor reachable
	// from the newly-committed visibility set. Retire it here instead of
	// leaking pendingRemoved forever.
	for _, netID := range pending.exited {
		delete(conn.pendingRemoved, netID)
	}
	for _, blink := range pending.blinks {
		s.cfg.OnBlinkDetected(connID, blink.netID, blink.ticksSinceRemove)
	}
	conn.pending = nil
	conn.forceFresh = false
}

func (s *ReplicationSystem) discardPendingAttempt(conn *connState) {
	if conn.pending == nil {
		return
	}
	for _, netID := range conn.pending.removed {
		conn.pendingRemoved[netID] = true
	}
	conn.store.ForEachBaseline(func(_ uint32, baseline *replication.EntityBaseline) {
		baseline.ClearSent()
	})
	conn.pending = nil
	conn.forceFresh = true
}

func (s *ReplicationSystem) drainFrameReceipts(connID uint32, conn *connState) {
	source, ok := s.cfg.Frame.(FrameReceiptSource)
	if !ok {
		return
	}
	for _, receipt := range source.DrainFrameReceipts(connID) {
		pending := conn.pending
		if pending == nil || receipt.StreamEpoch != pending.streamEpoch || receipt.Seq != pending.seq {
			continue
		}
		if !pending.awaitReceipt {
			// Explicit mode still needs the client's application ACK after a
			// successful enqueue, but a definite downstream rejection can end
			// the wait early and force a self-contained retry.
			if !receipt.Result.Queued() {
				s.discardPendingAttempt(conn)
			}
			continue
		}
		if receipt.Result.Queued() && receipt.Result.Delivery < net.DeliveryReliableOrdered &&
			conn.mode == replication.AckReliable {
			// Distributed mode: this cell's ConnMgr is a VirtualConnManager,
			// so AckModeFor could not see the client's transport and started
			// the connection reliable. The gateway's receipt reports the class
			// actually achieved, and it is weaker than reliable-ordered — the
			// client is on a datagram transport and owes us explicit ACKs.
			//
			// The BaselineStore is constructed from the mode, so replace the
			// whole connection state rather than mutating conn.mode in place;
			// letting the two diverge would silently mis-manage the sent ring.
			// Cost is exactly one wasted frame per connection, self-healing.
			fresh := newConnState(replication.AckExplicit)
			fresh.streamEpoch = conn.streamEpoch
			fresh.hasStreamEpoch = conn.hasStreamEpoch
			fresh.hasExplicitGeneration = conn.hasExplicitGeneration
			fresh.nextSeq = conn.nextSeq
			fresh.selfNetID = conn.selfNetID
			fresh.forceFresh = true
			s.connections[connID] = fresh
			delete(s.lastVisible, connID)
			// Update ranges s.connections; replacing the value for a key it
			// has already visited is safe, but the caller's local conn is now
			// stale, so return rather than continue.
			return
		}
		if receipt.Result.Supports(net.DeliveryReliableOrdered) {
			s.commitPendingAttempt(connID, conn, false)
		} else {
			s.discardPendingAttempt(conn)
		}
	}
}

func (s *ReplicationSystem) frameWriterTracksReceipts() bool {
	_, ok := s.cfg.Frame.(FrameReceiptSource)
	return ok
}

func (s *ReplicationSystem) pendingTimedOut(conn *connState, tick uint32) bool {
	if conn.pending == nil {
		return false
	}
	timeout := s.cfg.PendingAckTimeoutTicks
	if conn.pending.awaitReceipt {
		timeout = s.cfg.PendingReceiptTimeoutTicks
	}
	if timeout == 0 {
		timeout = 1
	}
	return tick-conn.pending.sentTick >= timeout
}

// ringDepth returns the appropriate ring buffer depth for one connection's
// latched ack mode.
func (s *ReplicationSystem) ringDepth(mode replication.AckMode) int {
	if mode == replication.AckReliable {
		return 0 // no ring needed; baseline promoted immediately
	}
	return s.cfg.SentHistoryDepth
}

// ackModeFor resolves the ack mode for a connection about to be created.
// Resolved exactly once per connection and latched on its connState — see
// ReplicationConfig.AckModeFor for why it must not come from runtime state.
func (s *ReplicationSystem) ackModeFor(connID uint32) replication.AckMode {
	if s.cfg.AckModeFor != nil {
		return s.cfg.AckModeFor(connID)
	}
	return s.cfg.AckMode
}

// getConn returns (or creates) connection state for a viewer.
func (s *ReplicationSystem) getConn(connID uint32) *connState {
	conn, ok := s.connections[connID]
	if !ok {
		conn = newConnState(s.ackModeFor(connID))
		s.connections[connID] = conn
	}
	return conn
}

// streamGenerationAfter applies RFC 1982-style serial arithmetic to uint32
// generations. This accepts wrap from MaxUint32 to zero while rejecting stale
// values from the previous half of the sequence space.
func streamGenerationAfter(candidate, current uint32) bool {
	return candidate != current && candidate-current < 1<<31
}

// resolveStreamGeneration adopts an explicit session generation when the
// configured callback provides one. A forward generation replaces the whole
// per-connection replication stream: sequences restart at one, all baselines
// are rebuilt, and the first frame carries FreshSnapshot. The returned bool is
// true whenever an explicit value was available this tick, including when that
// value was stale and therefore ignored.
func (s *ReplicationSystem) resolveStreamGeneration(viewer *ViewerInfo, conn *connState) (*connState, bool) {
	if s.cfg.StreamGeneration == nil {
		return conn, false
	}
	generation, ok := s.cfg.StreamGeneration(viewer)
	if !ok {
		return conn, false
	}

	if !conn.hasExplicitGeneration {
		// A callback may become available after legacy epoch-zero or NetworkID-
		// scoped frames have already been sent. A different value changes the
		// client's ordering domain, so make that adoption a complete reset.
		if conn.nextSeq != 0 && conn.streamEpoch != generation {
			conn = newConnState(conn.mode)
			s.connections[viewer.ConnID] = conn
			delete(s.lastVisible, viewer.ConnID)
		}
		conn.streamEpoch = generation
		conn.hasStreamEpoch = true
		conn.hasExplicitGeneration = true
		return conn, true
	}

	if generation == conn.streamEpoch {
		return conn, true
	}
	if !streamGenerationAfter(generation, conn.streamEpoch) {
		return conn, true
	}

	conn = newConnState(conn.mode)
	conn.streamEpoch = generation
	conn.hasStreamEpoch = true
	conn.hasExplicitGeneration = true
	s.connections[viewer.ConnID] = conn
	delete(s.lastVisible, viewer.ConnID)
	return conn, true
}

// Update runs the replication loop for one tick.
func (s *ReplicationSystem) Update(dt float32) {
	tick := s.cfg.GetTick()

	if s.cfg.OnBeforeTick != nil {
		s.cfg.OnBeforeTick(tick)
	}

	// Final gateway enqueue is asynchronous for tracked distributed writers.
	// Drain it before building this tick so an accepted attempt becomes the
	// baseline for the next frame immediately.
	for connID, conn := range s.connections {
		s.drainFrameReceipts(connID, conn)
	}

	isKeyframe := s.cfg.FullRefreshInterval > 0 &&
		tick%s.cfg.FullRefreshInterval == 0

	// Build removed set once per tick.
	var removedSet map[uint32]bool
	if s.cfg.RemovedIDs != nil {
		removed := s.cfg.RemovedIDs()
		if len(removed) > 0 {
			removedSet = make(map[uint32]bool, len(removed))
			for _, id := range removed {
				removedSet[id] = true
			}
		}
	}

	// Reconcile viewers omitted this tick. A liveness-aware source can identify
	// sessions in a temporary non-viewing state: retain their committed state
	// and sequence, abandon any ambiguous in-flight attempt, and force a fresh
	// snapshot on reactivation. Sources without that capability retain the
	// legacy behavior of treating omission as permanent removal. No farewell
	// frame is sent; FreshSnapshot is the topology-transparent reset signal.
	viewers := s.cfg.Viewers.ActiveViewers()
	activeConns := make(map[uint32]bool, len(viewers))
	for i := range viewers {
		activeConns[viewers[i].ConnID] = true
	}
	for connID := range s.connections {
		if activeConns[connID] {
			continue
		}
		if liveness, ok := s.cfg.Viewers.(ViewerLiveness); ok && liveness.ViewerExists(connID) {
			conn := s.connections[connID]
			if conn.pending != nil {
				s.discardPendingAttempt(conn)
			}
			conn.forceFresh = true
			continue
		}
		delete(s.lastVisible, connID)
		delete(s.connections, connID)
	}

	// Per-viewer replication loop.
	for i := range viewers {
		viewer := &viewers[i]
		conn := s.getConn(viewer.ConnID)
		var explicitGeneration bool
		conn, explicitGeneration = s.resolveStreamGeneration(viewer, conn)
		viewer.StreamEpoch = conn.streamEpoch

		// A connection may have only one causal attempt in flight. Preserve
		// tick-scoped removals while waiting, keep lifecycle callbacks alive
		// with the last committed visibility, and resume only after receipt,
		// explicit ACK, or a bounded fresh-reset timeout.
		if conn.pending != nil {
			for netID := range removedSet {
				if s.lastVisible[viewer.ConnID][netID] || conn.pending.visible[netID] {
					conn.pendingRemoved[netID] = true
				}
			}
			if s.pendingTimedOut(conn, tick) {
				s.discardPendingAttempt(conn)
			} else {
				committedVisible := s.lastVisible[viewer.ConnID]
				if committedVisible == nil {
					committedVisible = map[uint32]bool{}
				}
				if s.cfg.OnBeforeSend != nil {
					s.cfg.OnBeforeSend(viewer, committedVisible)
				}
				if s.cfg.OnAfterSend != nil {
					s.cfg.OnAfterSend(viewer, committedVisible)
				}
				continue
			}
		}
		// Per-connection: two viewers of this cell can be on transports with
		// different delivery classes and therefore different latched modes.
		ringDepth := s.ringDepth(conn.mode)
		tx := conn.txn.begin(ringDepth)

		stagedRecentRemovals := conn.recentRemovals
		if s.cfg.BlinkDetectorTicks > 0 {
			stagedRecentRemovals = cloneRecentRemovals(conn.recentRemovals)
		}
		var stagedBlinks []stagedBlinkDetection

		// "Fresh" means this frame starts (or repairs) a self-contained
		// replication stream. Initial login, session-generation advances, and
		// reactivation after temporary omission all reach this path. The client
		// can reset decoder baselines without learning why the stream changed.
		_, hadPriorState := s.lastVisible[viewer.ConnID]
		forceFresh := conn.forceFresh
		if forceFresh {
			hadPriorState = false
		}

		// Cache the viewer's own player netID for the farewell path. If
		// the entity lacks a NetworkID (e.g. spectator with no body)
		// leave selfNetID at zero — nothing to exclude.
		stagedSelfNetID := conn.selfNetID
		streamEpoch := conn.streamEpoch
		if s.netIDMap.HasAll(viewer.Entity) {
			viewerNetID := s.netIDMap.Get(viewer.Entity)
			stagedSelfNetID = viewerNetID.ID
			if !explicitGeneration && !conn.hasStreamEpoch {
				// A spectator-like viewer may have emitted epoch-zero frames before
				// acquiring an entity. Changing the stream scope invalidates those
				// baselines, so make this exact frame a complete reset as well.
				if hadPriorState {
					forceFresh = true
					hadPriorState = false
				}
				conn.streamEpoch = viewerNetID.Epoch
				conn.hasStreamEpoch = true
				streamEpoch = viewerNetID.Epoch
			}
		}
		viewer.StreamEpoch = streamEpoch

		// Assign a unique sequence to every write attempt. A queued frame with
		// an insufficient delivery class is not safe for baseline commit, but
		// it may still reach the client; reusing its sequence for the retry
		// would make two different frames indistinguishable to ack tracking.
		conn.nextSeq++
		frameSeq := conn.nextSeq

		// Query spatial grid within max AoI (covers all tier radii).
		s.results = s.cfg.SpatialGrid.QueryRadius(viewer.X, viewer.Y, s.queryRadius(), s.results[:0])

		// Build current visible set and encode changed entities.
		s.fullBuf = s.fullBuf[:0]
		s.deltaBuf = s.deltaBuf[:0]
		currentVisible := make(map[uint32]bool, len(s.results))
		var entered []uint32

		for _, entry := range s.results {
			if !s.cfg.World.Alive(entry.Entity) {
				continue
			}
			if !s.netIDMap.HasAll(entry.Entity) {
				continue
			}
			// Dormant entities are present in the world (and the spatial grid,
			// for proximity wake-up) but invisible to OTHER viewers. Used by
			// games to model "in-station" players, sleeping NPCs, etc.: the
			// entity stays a viewer itself (still receives WorldUpdateMsg with
			// the tick + AoI deltas), but other players' AoI queries skip it.
			//
			// Self-visibility exception: a Dormant viewer still sees its own
			// entity in its frame so the HUD can render position/cell/etc.
			// (a docked player should still see their own ship sitting in the
			// hangar). Without this, the client has no entity to bind myEntityId
			// to and the position/cell readout in the top bar disappears.
			if s.dormantMap.HasAll(entry.Entity) && entry.Entity != viewer.Entity {
				continue
			}
			// Border replicas flow through the normal dispatcher path. They
			// carry the full component set of their entity kind (auto-filled
			// to zero values by Stage.upsertBorderReplica via
			// EnsureEntityKindComponents), so reflectBinding.HasAll checks
			// succeed and every binding hashes/snapshots cleanly. This is the
			// only way the local client can see neighbor-owned entities
			// across cell boundaries.
			nid := s.netIDMap.Get(entry.Entity)
			netID := nid.ID
			epoch := nid.Epoch
			if currentVisible[netID] {
				continue
			}

			var entityType uint8
			if s.kindMap.HasAll(entry.Entity) {
				entityType = s.kindMap.Get(entry.Entity).Type
			}

			rep := s.cfg.Replicators.Get(entityType)
			if rep == nil {
				continue
			}

			// Optional relevancy check.
			if rp, ok := rep.(RelevancyProvider); ok {
				if !rp.IsRelevantTo(viewer, entry) {
					continue
				}
			}

			// Per-tier radius cutoff.
			tier := s.tierConfigs[entityType]
			tierRadius := tier.Radius
			if tierRadius == 0 {
				tierRadius = s.aoiRadius()
			}
			dx := entry.X - viewer.X
			dy := entry.Y - viewer.Y
			dist2 := dx*dx + dy*dy
			if dist2 > tierRadius*tierRadius {
				continue
			}

			currentVisible[netID] = true

			// Semantic entry is based only on committed visibility. A forced
			// fresh reset still needs a full payload for every visible entity,
			// but must not masquerade as an AoI re-entry to blink detection.
			wasVisible := false
			if prev, ok := s.lastVisible[viewer.ConnID]; ok {
				wasVisible = prev[netID]
			}
			isNew := !wasVisible
			// A retained tombstone means this netID was destroyed while another
			// causal frame was in flight. If it is visible again before that
			// tombstone commits, treat the representation as a new lifecycle and
			// never delta against its pre-destruction baseline. This is a wire
			// full-refresh only; semantic entry/blink detection remains based on
			// committed visibility above.
			needsFull := forceFresh || isNew || conn.pendingRemoved[netID]
			committedBaseline := conn.store.Baseline(netID)
			// A baseline is valid only for the authority lifecycle that produced
			// it. Handoffs can keep an entity continuously visible while advancing
			// NetworkID.Epoch; force a full entry (including initial-only fields)
			// so epoch-fencing clients can establish the new decoder baseline.
			epochChanged := committedBaseline != nil &&
				(!committedBaseline.HasAuthorityEpoch || committedBaseline.AuthorityEpoch != epoch)
			needsFull = needsFull || epochChanged
			if isNew {
				entered = append(entered, netID)
				if s.cfg.BlinkDetectorTicks > 0 {
					if removedTick, ok := stagedRecentRemovals[netID]; ok {
						delta := uint64(tick) - removedTick
						if delta <= s.cfg.BlinkDetectorTicks && s.cfg.OnBlinkDetected != nil {
							stagedBlinks = append(stagedBlinks, stagedBlinkDetection{
								netID:            netID,
								ticksSinceRemove: delta,
							})
						}
						delete(stagedRecentRemovals, netID)
					}
				}
			}

			// Detect a change in initial-only fields (name, etc.) so it can be
			// re-sent to already-visible viewers. Cheap: only the initial
			// fields are hashed, and only when the kind has any.
			hasInit := rep.HasInitial()
			var curInitHash uint64
			if hasInit {
				s.initHasher.Reset()
				rep.InitialHash(&s.initHasher, viewer, entry)
				curInitHash = s.initHasher.Sum()
			}
			initialChanged := hasInit && (committedBaseline == nil ||
				!committedBaseline.HasInitialHash || committedBaseline.InitialHash != curInitHash)

			// Dormancy: skip all replication work for entities unchanged for N
			// ticks — unless an initial field changed (static entities like
			// stations are dormant exactly when they get renamed). Consult the
			// committed priority read-only so a dormant entity does not enter the
			// transaction at all.
			if !needsFull && !initialChanged && s.cfg.DormancyThreshold > 0 {
				if committedPriority := conn.store.ExistingPriority(netID); committedPriority != nil &&
					committedPriority.UnchangedTicks >= s.cfg.DormancyThreshold {
					currentVisible[netID] = true
					continue
				}
			}

			// Fast hash pre-check.
			s.hasher.Reset()
			rep.Hash(&s.hasher, viewer, entry)
			hash := s.hasher.Sum()

			if !needsFull && !isKeyframe && !initialChanged && conn.store.HasLastHash(netID) {
				if conn.store.LastHash(netID) == hash {
					ps := tx.priority(netID)
					ps.UnchangedTicks++
					currentVisible[netID] = true
					continue // unchanged — skip snapshot
				}
			}
			ps := tx.priority(netID)
			ps.UnchangedTicks = 0

			// Update divisor gate: skip snapshot on non-divisor ticks.
			if !needsFull && !initialChanged && tier.UpdateDivisor > 1 && tick%tier.UpdateDivisor != 0 {
				currentVisible[netID] = true
				dist := float32(math.Sqrt(float64(dist2)))
				distFactor := float32(1.0) - (dist / tierRadius)
				if distFactor < 0 {
					distFactor = 0
				}
				basePriority := tier.BaseWeight * distFactor
				if pp, ok := rep.(PriorityProvider); ok {
					basePriority *= pp.NetPriority(viewer, entry)
				}
				ps.Accumulator += basePriority
				continue
			}
			// LastHash is the last state admitted to snapshot/delta work, not
			// merely the last state observed. Recording it before the divisor
			// gate would permanently lose a one-time change that happened on a
			// skipped tick.
			tx.setLastHash(netID, hash)
			ps.LastSentTick = tick
			ps.Accumulator = 0

			// Build snapshot.
			s.snapWriter.Reset()
			rep.Snapshot(s.snapWriter, viewer, entry)
			curr := s.snapWriter.Bytes()

			enc := s.deltaEncoders[entityType]

			if needsFull || committedBaseline == nil || committedBaseline.Acked == nil || initialChanged {
				// Full snapshot with initial data. Reached on first visibility,
				// missing baseline, OR when an initial-only field changed.
				bl := tx.baseline(netID)
				bl.AuthorityEpoch = epoch
				bl.HasAuthorityEpoch = true
				snap := make([]byte, len(curr))
				copy(snap, curr)

				var initData []byte
				initData = rep.InitialData(viewer, entry)

				s.fullBuf = append(s.fullBuf, FullPayload{
					NetID:        netID,
					Epoch:        epoch,
					Type:         entityType,
					ProducedAtMs: s.producedAtMs(entry.Entity),
					Snapshot:     snap,
					InitialData:  initData,
				})

				if hasInit {
					bl.InitialHash = curInitHash
					bl.HasInitialHash = true
				}

				// Store baseline.
				if conn.mode == replication.AckReliable {
					bl.Acked = snap
				} else {
					tx.pushSent(netID, snap)
				}
			} else if isKeyframe {
				// Keyframe: full snapshot, no initial data.
				bl := tx.baseline(netID)
				bl.AuthorityEpoch = epoch
				bl.HasAuthorityEpoch = true
				snap := make([]byte, len(curr))
				copy(snap, curr)

				s.fullBuf = append(s.fullBuf, FullPayload{
					NetID:        netID,
					Epoch:        epoch,
					Type:         entityType,
					ProducedAtMs: s.producedAtMs(entry.Entity),
					Snapshot:     snap,
				})

				if conn.mode == replication.AckReliable {
					bl.Acked = snap
				} else {
					tx.pushSent(netID, snap)
				}
			} else if enc != nil {
				// Delta encode against acked baseline.
				s.deltaTmp = s.deltaTmp[:0]
				delta := enc.Encode(committedBaseline.Acked, curr, s.deltaTmp)
				if delta == nil {
					// Identical after quantization despite hash change.
					continue
				}

				deltaData := make([]byte, len(delta))
				copy(deltaData, delta)

				s.deltaBuf = append(s.deltaBuf, DeltaPayload{
					NetID:        netID,
					Epoch:        epoch,
					Type:         entityType,
					ProducedAtMs: s.producedAtMs(entry.Entity),
					Data:         deltaData,
				})

				// Store for baseline advancement.
				bl := tx.baseline(netID)
				bl.AuthorityEpoch = epoch
				bl.HasAuthorityEpoch = true
				snap := make([]byte, len(curr))
				copy(snap, curr)
				if conn.mode == replication.AckReliable {
					bl.Acked = snap
				} else {
					tx.pushSent(netID, snap)
				}
			}
		}

		// Compute exited and removed sets.
		//
		// selfNetID is excluded from `exited`: the viewer can't logically
		// leave their own AoI, so its only path into `exited` is a
		// transfer-out (entity moved from this cell's ECS to the
		// destination's). Signaling that to the client as "exited" deletes
		// the local ClientEntity along with its sample ring, then the
		// destination's fresh frame has to rebuild from one sample and
		// interp falls into applyStatic for ~100ms — a visible hop.
		// Destination's fresh frame will authoritatively repopulate
		// everything; leaving the viewer's own entity alone keeps its
		// interpolation anchor intact through handoff.
		//
		// `removed` is NOT filtered: that path is for genuine despawn
		// (player died). The client needs to see the death signal.
		var exited, removed []uint32
		if prev, ok := s.lastVisible[viewer.ConnID]; ok {
			for netID := range prev {
				if currentVisible[netID] {
					continue
				}
				if removedSet[netID] || conn.pendingRemoved[netID] {
					removed = append(removed, netID)
				} else if netID != stagedSelfNetID {
					exited = append(exited, netID)
				}
			}
		}

		// Stage baseline cleanup for entities leaving AoI. A rejected frame
		// must retain these baselines so the exact exit/removal can be retried.
		for _, netID := range exited {
			tx.drop(netID)
		}
		for _, netID := range removed {
			tx.drop(netID)
		}

		// Stage removals for blink detection. The removal only becomes
		// client-visible after a reliable ordered acceptance.
		if s.cfg.BlinkDetectorTicks > 0 {
			for _, netID := range removed {
				stagedRecentRemovals[netID] = uint64(tick)
			}
		}

		// GC stale blink-detector entries in the staged map. Rejection leaves
		// the committed map untouched.
		if s.cfg.BlinkDetectorTicks > 0 && len(stagedRecentRemovals) > 0 {
			t64 := uint64(tick)
			if t64 >= s.cfg.BlinkDetectorTicks {
				windowStart := t64 - s.cfg.BlinkDetectorTicks
				for id, removedTick := range stagedRecentRemovals {
					if removedTick < windowStart {
						delete(stagedRecentRemovals, id)
					}
				}
			}
		}

		// Pre-send callback.
		if s.cfg.OnBeforeSend != nil {
			s.cfg.OnBeforeSend(viewer, currentVisible)
		}

		// Build and send frame. Mark fresh if this is the first frame for
		// the conn — the client's decoder will drop its per-entity
		// baselines before applying this frame's Full entries.
		var frameFlags uint32
		if !hadPriorState {
			frameFlags |= quantize.FrameFlagFreshSnapshot
		}
		processedInputSeq, hasInputAck := uint32(0), false
		if s.cfg.ProcessedInputSeq != nil {
			processedInputSeq, hasInputAck = s.cfg.ProcessedInputSeq(viewer)
		}
		result := s.cfg.Frame.WriteFrame(&ReplicationFrame{
			Tick:              tick,
			Seq:               frameSeq,
			StreamEpoch:       streamEpoch,
			Flags:             frameFlags,
			HasInputAck:       hasInputAck,
			ProcessedInputSeq: processedInputSeq,
			Viewer:            viewer,
			Full:              s.fullBuf,
			Deltas:            s.deltaBuf,
			Entered:           entered,
			Exited:            exited,
			Removed:           removed,
		})

		if conn.mode == replication.AckReliable && result.Supports(net.DeliveryReliableOrdered) {
			tx.commit()
			s.lastVisible[viewer.ConnID] = currentVisible
			conn.selfNetID = stagedSelfNetID
			conn.forceFresh = false
			if s.cfg.BlinkDetectorTicks > 0 {
				conn.recentRemovals = stagedRecentRemovals
			}

			// Accepted current state supersedes an undelivered tombstone for a
			// netID that became visible again. Accepted removals leave the
			// outbox only after their tombstone is on the reliable stream.
			for netID := range currentVisible {
				delete(conn.pendingRemoved, netID)
			}
			for _, netID := range removed {
				delete(conn.pendingRemoved, netID)
			}

			for _, blink := range stagedBlinks {
				s.cfg.OnBlinkDetected(viewer.ConnID, blink.netID, blink.ticksSinceRemove)
			}

		} else if result.Queued() && (conn.mode == replication.AckExplicit ||
			(result.Supports(net.DeliveryOrdered) && s.frameWriterTracksReceipts())) {
			// AckExplicit retains only attempted snapshot history before the
			// application ACK. AckReliable's ordered distributed path retains
			// the whole transaction until its final gateway receipt arrives.
			if conn.mode == replication.AckExplicit {
				tx.retainSent(frameSeq)
			}
			for _, netID := range removed {
				conn.pendingRemoved[netID] = true
			}
			conn.stagePendingAttempt(
				streamEpoch,
				frameSeq,
				tick,
				currentVisible,
				stagedSelfNetID,
				stagedRecentRemovals,
				stagedBlinks,
				exited,
				removed,
				conn.mode == replication.AckReliable,
			)
		} else {
			// RemovedIDs is tick-scoped. Preserve rejected removals in a
			// per-connection outbox so the next attempt remains a tombstone
			// instead of silently degrading into an AoI exit.
			for _, netID := range removed {
				conn.pendingRemoved[netID] = true
			}
			// A queued path with insufficient guarantees, or an indeterminate
			// handoff to an intermediate queue, may still reach the client.
			// Make the retry self-contained instead of building a delta on that
			// ambiguous outcome.
			if result.Queued() || result.Disposition == net.SendIndeterminate {
				conn.forceFresh = true
			}
		}

		// Preserve callback semantics for queued best-effort/ordered paths
		// (notably distributed auto-broadcast delivery). Transactional state
		// advancement has the stricter reliable-ordered requirement above,
		// but OnAfterSend remains an after-attempt lifecycle hook.
		if s.cfg.OnAfterSend != nil {
			s.cfg.OnAfterSend(viewer, currentVisible)
		}
	}

	if s.cfg.OnAfterTick != nil {
		s.cfg.OnAfterTick(tick)
	}
}
