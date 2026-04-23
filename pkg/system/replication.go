package system

import (
	"math"
	"sort"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// ClusterClock is the minimum surface ReplicationSystem needs from a
// cluster-coherent wall clock. pkg/universe.ClusterClock satisfies this
// structurally via its Now() method. Declared here (not imported from
// pkg/universe) because pkg/system cannot import pkg/universe without
// creating a cycle through pkg/mmokit.
type ClusterClock interface {
	// Now returns the current cluster-coherent wall-clock in milliseconds.
	Now() uint64
}

// ViewerInfo describes a connection that receives replicated entity state.
type ViewerInfo struct {
	ConnID uint32
	Entity ecs.Entity
	X, Y   float32
}

// FullPayload is a full entity snapshot for new or keyframe entities.
type FullPayload struct {
	NetID        uint32
	Epoch        uint32 // authority handoff epoch from NetworkID (0 until Phase 5)
	Type         uint8
	ProducedAtMs uint64 // authoritative producer's ClusterClock.Now() at emit time
	Snapshot     []byte // full snapshot bytes
	InitialData  []byte // nil unless first time visible
}

// DeltaPayload is a delta-encoded entity update.
type DeltaPayload struct {
	NetID        uint32
	Epoch        uint32 // authority handoff epoch from NetworkID (0 until Phase 5)
	Type         uint8
	ProducedAtMs uint64 // authoritative producer's ClusterClock.Now() at emit time
	Data         []byte // bitmask + changed fields from DeltaEncoder
}

// ReplicationFrame is the per-viewer per-tick replication data passed to the FrameWriter.
type ReplicationFrame struct {
	Tick    uint32
	Seq     uint32 // frame sequence number for client ack tracking
	Flags   uint32 // wire-format header flags (quantize.FrameFlag*)
	Viewer  *ViewerInfo
	Full    []FullPayload
	Deltas  []DeltaPayload
	Entered []uint32 // netIDs newly visible to this viewer
	Exited  []uint32 // netIDs that left AoI (entity still alive)
	Removed []uint32 // netIDs destroyed/despawned this tick
}

// ---------------------------------------------------------------------------
// Interfaces — game implementations plug in here
// ---------------------------------------------------------------------------

// ViewerSource provides the set of active viewers each tick.
type ViewerSource interface {
	ActiveViewers() []ViewerInfo
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

// FrameWriter assembles a ReplicationFrame into wire-format bytes and sends
// it to the viewer.
type FrameWriter interface {
	WriteFrame(frame *ReplicationFrame)
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
	// cluster-coherent wall-clock at emit time. Replicas re-use their
	// cached Replica.ProducedAtMs (populated by the border-frame codec)
	// so a client's view of a replicated entity carries the SOURCE cell's
	// producer stamp, not the receiver's emit time. If nil, the system
	// falls back to the local wall clock — acceptable for single-cell
	// tests, never correct across hosts.
	ClusterClock ClusterClock

	AoIRadius           float32
	GetAoIRadius        func() float32 // dynamic AoI radius (overrides AoIRadius if set)
	FullRefreshInterval uint32         // ticks between forced keyframe (0 = disabled)
	DormancyThreshold   uint32         // ticks unchanged before entity goes dormant (0 = disabled)

	// AckMode controls baseline advancement.
	AckMode replication.AckMode

	// SentHistoryDepth is the ring buffer depth for AckExplicit mode.
	// Default 32 (~1.6s at 20Hz). Ignored for AckReliable.
	SentHistoryDepth int

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
	store    *replication.BaselineStore
	ackedSeq uint32
	nextSeq  uint32
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
}

func newConnState(mode replication.AckMode) *connState {
	return &connState{
		store:          replication.NewBaselineStore(mode),
		recentRemovals: make(map[uint32]uint64),
	}
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
	snapWriter *quantize.SnapshotWriter
	snapBuf    []byte
	deltaTmp   []byte // reusable buffer for DeltaEncoder output
}

// NewReplicationSystem creates a replication system with the given configuration.
func NewReplicationSystem(cfg ReplicationConfig) *ReplicationSystem {
	if cfg.SentHistoryDepth == 0 {
		cfg.SentHistoryDepth = 32
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
	return &ReplicationSystem{
		cfg:           cfg,
		netIDMap:      ecs.NewMap1[component.NetworkID](cfg.World),
		kindMap:       ecs.NewMap1[component.EntityKind](cfg.World),
		ghostMap:      ecs.NewMap1[component.Ghost](cfg.World),
		replicaMap:    ecs.NewMap1[component.Replica](cfg.World),
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
}

func (s *ReplicationSystem) Name() string { return "Replication" }

// producedAtMs returns the cluster-clock stamp for an entity about to be
// emitted. Local-authoritative entities are stamped with clock.Now() at
// emit time; replicas pass through the cached Replica.ProducedAtMs so the
// client's view carries the SOURCE cell's producer stamp (not this cell's
// emit time). When ClusterClock is nil, falls back to the local wall
// clock — OK for single-cell tests, never correct across hosts.
func (s *ReplicationSystem) producedAtMs(entity ecs.Entity) uint64 {
	if s.replicaMap.HasAll(entity) {
		return s.replicaMap.Get(entity).ProducedAtMs
	}
	if s.cfg.ClusterClock != nil {
		return s.cfg.ClusterClock.Now()
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

// AckSequence advances the acked baseline for a connection to the given sequence.
// For AckExplicit mode (UDP): called when the server receives a client ack.
// For AckReliable mode (TCP): this is a no-op (baselines auto-advance on send).
func (s *ReplicationSystem) AckSequence(connID, seq uint32) {
	if s.cfg.AckMode != replication.AckExplicit {
		return
	}
	conn, ok := s.connections[connID]
	if !ok {
		return
	}
	conn.ackedSeq = seq
	conn.store.ForEachBaseline(func(_ uint32, bl *replication.EntityBaseline) {
		bl.AdvanceTo(seq)
	})
}

// ringDepth returns the appropriate ring buffer depth based on ack mode.
func (s *ReplicationSystem) ringDepth() int {
	if s.cfg.AckMode == replication.AckReliable {
		return 0 // no ring needed; baseline promoted immediately
	}
	return s.cfg.SentHistoryDepth
}

// getConn returns (or creates) connection state for a viewer.
func (s *ReplicationSystem) getConn(connID uint32) *connState {
	conn, ok := s.connections[connID]
	if !ok {
		conn = newConnState(s.cfg.AckMode)
		s.connections[connID] = conn
	}
	return conn
}

// Update runs the replication loop for one tick.
func (s *ReplicationSystem) Update(dt float32) {
	tick := s.cfg.GetTick()

	if s.cfg.OnBeforeTick != nil {
		s.cfg.OnBeforeTick(tick)
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

	// Clean up state for disconnected viewers.
	//
	// A viewer disappearing from ActiveViewers can mean either "WebSocket
	// closed" (real disconnect) or "session handed off to another cell"
	// (cross-cell handoff). In both cases the server must tell the
	// client to forget every entity it had visible from THIS cell's
	// perspective — otherwise the client UI retains ghost copies that
	// never despawn. This is the per-client analog of the border-frame
	// interest-set diff that runs for cross-cell replicas: when a
	// session stops being served by this cell, its visibility set is
	// conceptually "dropped from this sender" and the client needs the
	// explicit Removed notification. Without this, stale entities pile
	// up on the client whenever a player crosses a cell boundary.
	//
	// The farewell is sent through the normal FrameWriter so it flows
	// over whichever transport the client is attached to (WebSocket via
	// ConnMgr, or VCM in node mode). If the connection is genuinely
	// gone the send is a silent no-op — harmless.
	// When a conn leaves this cell's ActiveViewers (handoff or disconnect),
	// CLEAN UP local tracking state but do NOT send a farewell frame. Earlier
	// designs sent Removed:[all visible] to the departing conn, which was
	// meant to make the client drop stale entities. In practice the
	// farewell raced with the destination cell's FreshSnapshot frame: if
	// the farewell arrived on the client AFTER the fresh frame, it would
	// delete the client's per-entity baselines for entities that are still
	// visible from the destination's perspective, causing the server's
	// subsequent deltas to be undecodable and the entities to stay
	// invisible for up to ~1.5s until a TTL-driven resync. The
	// FreshSnapshot flag on the destination's first frame is the
	// authoritative state-reset signal; this cell's visibility is
	// implicitly dropped when the conn moves. Topology-transparent: the
	// client never sees per-cell farewell frames.
	viewers := s.cfg.Viewers.ActiveViewers()
	activeConns := make(map[uint32]bool, len(viewers))
	for i := range viewers {
		activeConns[viewers[i].ConnID] = true
	}
	for connID := range s.lastVisible {
		if activeConns[connID] {
			continue
		}
		delete(s.lastVisible, connID)
		delete(s.connections, connID)
	}

	ringDepth := s.ringDepth()

	// Per-viewer replication loop.
	for i := range viewers {
		viewer := &viewers[i]
		conn := s.getConn(viewer.ConnID)

		// "Fresh" = this ReplicationSystem has never sent a frame to this
		// conn before, which is the canonical signal the client needs to
		// reset its per-entity decoder baselines. Happens on initial login
		// and on every cross-cell handoff — the destination cell's
		// ReplicationSystem starts with empty lastVisible for the
		// newly-arrived conn. Matches the Valve Source cl_fullupdate /
		// Gaffer "encoded relative to initial state" pattern: no topology
		// concept leaks to the client, just a normal delta frame that
		// happens to carry a "reset your decoder" bit.
		_, hadPriorState := s.lastVisible[viewer.ConnID]

		// Cache the viewer's own player netID for the farewell path. If
		// the entity lacks a NetworkID (e.g. spectator with no body)
		// leave selfNetID at zero — nothing to exclude.
		if s.netIDMap.HasAll(viewer.Entity) {
			conn.selfNetID = s.netIDMap.Get(viewer.Entity).ID
		}

		// Assign frame sequence.
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
			// Border replicas flow through the normal dispatcher path. They
			// carry the full component set of their entity kind (auto-filled
			// to zero values by WorldBase.upsertBorderReplica via
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

			// Is this entity new to this viewer?
			isNew := true
			if prev, ok := s.lastVisible[viewer.ConnID]; ok && prev[netID] {
				isNew = false
			}
			if isNew {
				entered = append(entered, netID)
				if s.cfg.BlinkDetectorTicks > 0 && s.cfg.OnBlinkDetected != nil {
					if removedTick, ok := conn.recentRemovals[netID]; ok {
						delta := uint64(tick) - removedTick
						if delta <= s.cfg.BlinkDetectorTicks {
							s.cfg.OnBlinkDetected(viewer.ConnID, netID, delta)
						}
						delete(conn.recentRemovals, netID)
					}
				}
			}

			// Dormancy: skip all replication work for entities unchanged for N ticks.
			ps := conn.store.Priority(netID)
			if !isNew && s.cfg.DormancyThreshold > 0 && ps.UnchangedTicks >= s.cfg.DormancyThreshold {
				currentVisible[netID] = true
				continue
			}

			// Fast hash pre-check.
			s.hasher.Reset()
			rep.Hash(&s.hasher, viewer, entry)
			hash := s.hasher.Sum()

			if !isNew && !isKeyframe && conn.store.HasLastHash(netID) {
				if conn.store.LastHash(netID) == hash {
					ps.UnchangedTicks++
					currentVisible[netID] = true
					continue // unchanged — skip snapshot
				}
			}
			ps.UnchangedTicks = 0
			conn.store.SetLastHash(netID, hash)

			// Update divisor gate: skip snapshot on non-divisor ticks.
			if !isNew && tier.UpdateDivisor > 1 && tick%tier.UpdateDivisor != 0 {
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
			ps.LastSentTick = tick
			ps.Accumulator = 0

			// Build snapshot.
			s.snapWriter.Reset()
			rep.Snapshot(s.snapWriter, viewer, entry)
			curr := s.snapWriter.Bytes()

			bl := conn.store.GetOrCreateBaseline(netID, ringDepth)
			enc := s.deltaEncoders[entityType]

			if isNew || bl.Acked == nil {
				// Full snapshot with initial data.
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

				// Store baseline.
				if s.cfg.AckMode == replication.AckReliable {
					bl.Acked = snap
				} else {
					bl.Acked = snap
					bl.PushSent(frameSeq, snap)
				}
			} else if isKeyframe {
				// Keyframe: full snapshot, no initial data.
				snap := make([]byte, len(curr))
				copy(snap, curr)

				s.fullBuf = append(s.fullBuf, FullPayload{
					NetID:        netID,
					Epoch:        epoch,
					Type:         entityType,
					ProducedAtMs: s.producedAtMs(entry.Entity),
					Snapshot:     snap,
				})

				if s.cfg.AckMode == replication.AckReliable {
					bl.Acked = snap
				} else {
					bl.PushSent(frameSeq, snap)
				}
			} else if enc != nil {
				// Delta encode against acked baseline.
				s.deltaTmp = s.deltaTmp[:0]
				delta := enc.Encode(bl.Acked, curr, s.deltaTmp)
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
				snap := make([]byte, len(curr))
				copy(snap, curr)
				if s.cfg.AckMode == replication.AckReliable {
					bl.Acked = snap
				} else {
					bl.PushSent(frameSeq, snap)
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
				if removedSet[netID] {
					removed = append(removed, netID)
				} else if netID != conn.selfNetID {
					exited = append(exited, netID)
				}
			}
		}

		// Clean up baselines for entities leaving AoI.
		for _, netID := range exited {
			conn.store.DropBaseline(netID)
		}
		for _, netID := range removed {
			conn.store.DropBaseline(netID)
		}

		// Record removals for blink detection.
		if s.cfg.BlinkDetectorTicks > 0 {
			for _, netID := range removed {
				conn.recentRemovals[netID] = uint64(tick)
			}
		}

		// Save current visible set.
		s.lastVisible[viewer.ConnID] = currentVisible

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
		s.cfg.Frame.WriteFrame(&ReplicationFrame{
			Tick:    tick,
			Seq:     frameSeq,
			Flags:   frameFlags,
			Viewer:  viewer,
			Full:    s.fullBuf,
			Deltas:  s.deltaBuf,
			Entered: entered,
			Exited:  exited,
			Removed: removed,
		})

		// GC stale blink-detector entries.
		if s.cfg.BlinkDetectorTicks > 0 && len(conn.recentRemovals) > 0 {
			t64 := uint64(tick)
			if t64 >= s.cfg.BlinkDetectorTicks {
				windowStart := t64 - s.cfg.BlinkDetectorTicks
				for id, t := range conn.recentRemovals {
					if t < windowStart {
						delete(conn.recentRemovals, id)
					}
				}
			}
		}

		// Post-send callback.
		if s.cfg.OnAfterSend != nil {
			s.cfg.OnAfterSend(viewer, currentVisible)
		}
	}

	if s.cfg.OnAfterTick != nil {
		s.cfg.OnAfterTick(tick)
	}
}
