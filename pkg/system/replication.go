package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// ViewerInfo describes a connection that receives replicated entity state.
type ViewerInfo struct {
	ConnID uint32
	Entity ecs.Entity
	X, Y   float32
}

// FullPayload is a full entity snapshot for new or keyframe entities.
type FullPayload struct {
	NetID       uint32
	Type        uint8
	Snapshot    []byte // full snapshot bytes
	InitialData []byte // nil unless first time visible
}

// DeltaPayload is a delta-encoded entity update.
type DeltaPayload struct {
	NetID uint32
	Type  uint8
	Data  []byte // bitmask + changed fields from DeltaEncoder
}

// ReplicationFrame is the per-viewer per-tick replication data passed to the FrameWriter.
type ReplicationFrame struct {
	Tick    uint32
	Seq     uint32 // frame sequence number for client ack tracking
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
// SchemaProvider. AutoReplicator implements it automatically; hand-coded
// replicators can opt in by implementing the SchemaProvider interface.
func (r *ReplicatorRegistry) Schema() []EntitySchema {
	var schemas []EntitySchema
	for _, rep := range r.replicators {
		if sp, ok := rep.(SchemaProvider); ok {
			schemas = append(schemas, sp.Schema())
		}
	}
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

	AoIRadius           float32
	GetAoIRadius        func() float32 // dynamic AoI radius (overrides AoIRadius if set)
	FullRefreshInterval uint32 // ticks between forced keyframe (0 = disabled)
	DormancyThreshold   uint32 // ticks unchanged before entity goes dormant (0 = disabled)

	// AckMode controls baseline advancement.
	AckMode AckMode

	// SentHistoryDepth is the ring buffer depth for AckExplicit mode.
	// Default 32 (~1.6s at 20Hz). Ignored for AckReliable.
	SentHistoryDepth int

	// GetTick returns the current game tick number.
	GetTick func() uint32

	// KilledIDs returns netIDs of entities destroyed this tick.
	KilledIDs func() []uint32

	// SnapshotBufSize is the pre-allocated buffer size for SnapshotWriter.
	// Default 256 bytes. Increase if entity snapshots are larger.
	SnapshotBufSize int

	// Optional per-tick lifecycle callbacks.
	OnBeforeTick func(tick uint32)
	OnAfterTick  func(tick uint32)
	OnBeforeSend func(viewer *ViewerInfo, visible map[uint32]bool)
	OnAfterSend  func(viewer *ViewerInfo, visible map[uint32]bool)

	// OnProxiesInView is called once per tick with the netIDs of proxy entities
	// found in any viewer's AoI. The game should call RequestPromotion on these
	// to upgrade them from lightweight proxies to full replicas.
	OnProxiesInView func(netIDs []uint32)
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
	proxyMap   *ecs.Map1[component.Proxy]

	// Per-viewer state
	lastVisible map[uint32]map[uint32]bool // connID -> set of visible netIDs
	connections map[uint32]*connectionState // connID -> baseline + hash state

	// Cached delta encoders per entity type
	deltaEncoders map[uint8]*quantize.DeltaEncoder

	// Cached tier configs per entity type
	tierConfigs    map[uint8]ReplicationTier
	maxTierRadius  float32 // largest explicit tier radius (0 if none)
	initAoIRadius  float32 // AoI radius at init time (fallback)

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
		proxyMap:      ecs.NewMap1[component.Proxy](cfg.World),
		ghostMap:      ecs.NewMap1[component.Ghost](cfg.World),
		replicaMap:    ecs.NewMap1[component.Replica](cfg.World),
		lastVisible:   make(map[uint32]map[uint32]bool),
		connections:   make(map[uint32]*connectionState),
		deltaEncoders: encoders,
		tierConfigs:    tierConfigs,
		maxTierRadius:  maxTierRadius,
		initAoIRadius:  cfg.AoIRadius,
		results:       make([]spatial.Entry, 0, 256),
		fullBuf:       make([]FullPayload, 0, 64),
		deltaBuf:      make([]DeltaPayload, 0, 128),
		snapBuf:       snapBuf,
		snapWriter:    quantize.NewSnapshotWriter(snapBuf),
		deltaTmp:      make([]byte, 0, 256),
	}
}

func (s *ReplicationSystem) Name() string { return "Replication" }

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
	if s.cfg.AckMode != AckExplicit {
		return
	}
	conn, ok := s.connections[connID]
	if !ok {
		return
	}
	conn.ackedSeq = seq
	for _, bl := range conn.baselines {
		bl.advanceTo(seq)
	}
}

// ringDepth returns the appropriate ring buffer depth based on ack mode.
func (s *ReplicationSystem) ringDepth() int {
	if s.cfg.AckMode == AckReliable {
		return 0 // no ring needed; baseline promoted immediately
	}
	return s.cfg.SentHistoryDepth
}

// getConn returns (or creates) connection state for a viewer.
func (s *ReplicationSystem) getConn(connID uint32) *connectionState {
	conn, ok := s.connections[connID]
	if !ok {
		conn = newConnectionState()
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

	// Build killed set once per tick.
	var killedSet map[uint32]bool
	if s.cfg.KilledIDs != nil {
		killed := s.cfg.KilledIDs()
		if len(killed) > 0 {
			killedSet = make(map[uint32]bool, len(killed))
			for _, id := range killed {
				killedSet[id] = true
			}
		}
	}

	// Clean up state for disconnected viewers.
	viewers := s.cfg.Viewers.ActiveViewers()
	activeConns := make(map[uint32]bool, len(viewers))
	for i := range viewers {
		activeConns[viewers[i].ConnID] = true
	}
	for connID := range s.lastVisible {
		if !activeConns[connID] {
			delete(s.lastVisible, connID)
			delete(s.connections, connID)
		}
	}

	ringDepth := s.ringDepth()

	// Collect proxy netIDs across all viewers for batch promotion.
	var proxyNetIDs []uint32
	proxyCollected := make(map[uint32]bool)

	// Per-viewer replication loop.
	for i := range viewers {
		viewer := &viewers[i]
		conn := s.getConn(viewer.ConnID)

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
			netID := s.netIDMap.Get(entry.Entity).ID
			if currentVisible[netID] {
				continue
			}

			// Proxy entities in AoI: collect for promotion, skip replication.
			if s.cfg.OnProxiesInView != nil && s.proxyMap.HasAll(entry.Entity) {
				if !proxyCollected[netID] {
					proxyCollected[netID] = true
					proxyNetIDs = append(proxyNetIDs, netID)
				}
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
			}

			// Dormancy: skip all replication work for entities unchanged for N ticks.
			ps := conn.getPriorityState(netID)
			if !isNew && s.cfg.DormancyThreshold > 0 && ps.unchangedTicks >= s.cfg.DormancyThreshold {
				currentVisible[netID] = true
				continue
			}

			// Fast hash pre-check.
			s.hasher.Reset()
			rep.Hash(&s.hasher, viewer, entry)
			hash := s.hasher.Sum()

			if !isNew && !isKeyframe {
				if lastHash, ok := conn.lastHash[netID]; ok && lastHash == hash {
					ps.unchangedTicks++
					currentVisible[netID] = true
					continue // unchanged — skip snapshot
				}
			}
			ps.unchangedTicks = 0
			conn.lastHash[netID] = hash

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
				ps.accumulator += basePriority
				continue
			}
			ps.lastSentTick = tick
			ps.accumulator = 0

			// Build snapshot.
			s.snapWriter.Reset()
			rep.Snapshot(s.snapWriter, viewer, entry)
			curr := s.snapWriter.Bytes()

			bl := conn.getBaseline(netID, ringDepth)
			enc := s.deltaEncoders[entityType]

			if isNew || bl.acked == nil {
				// Full snapshot with initial data.
				snap := make([]byte, len(curr))
				copy(snap, curr)

				var initData []byte
				initData = rep.InitialData(viewer, entry)

				s.fullBuf = append(s.fullBuf, FullPayload{
					NetID:       netID,
					Type:        entityType,
					Snapshot:    snap,
					InitialData: initData,
				})

				// Store baseline.
				if s.cfg.AckMode == AckReliable {
					bl.acked = snap
				} else {
					bl.acked = snap
					bl.pushSent(frameSeq, snap)
				}
			} else if isKeyframe {
				// Keyframe: full snapshot, no initial data.
				snap := make([]byte, len(curr))
				copy(snap, curr)

				s.fullBuf = append(s.fullBuf, FullPayload{
					NetID:    netID,
					Type:     entityType,
					Snapshot: snap,
				})

				if s.cfg.AckMode == AckReliable {
					bl.acked = snap
				} else {
					bl.pushSent(frameSeq, snap)
				}
			} else if enc != nil {
				// Delta encode against acked baseline.
				s.deltaTmp = s.deltaTmp[:0]
				delta := enc.Encode(bl.acked, curr, s.deltaTmp)
				if delta == nil {
					// Identical after quantization despite hash change.
					continue
				}

				deltaData := make([]byte, len(delta))
				copy(deltaData, delta)

				s.deltaBuf = append(s.deltaBuf, DeltaPayload{
					NetID: netID,
					Type:  entityType,
					Data:  deltaData,
				})

				// Store for baseline advancement.
				snap := make([]byte, len(curr))
				copy(snap, curr)
				if s.cfg.AckMode == AckReliable {
					bl.acked = snap
				} else {
					bl.pushSent(frameSeq, snap)
				}
			}
		}

		// Compute exited and removed sets.
		var exited, removed []uint32
		if prev, ok := s.lastVisible[viewer.ConnID]; ok {
			for netID := range prev {
				if currentVisible[netID] {
					continue
				}
				if killedSet[netID] {
					removed = append(removed, netID)
				} else {
					exited = append(exited, netID)
				}
			}
		}

		// Clean up baselines for entities leaving AoI.
		for _, netID := range exited {
			conn.removeBaseline(netID)
		}
		for _, netID := range removed {
			conn.removeBaseline(netID)
		}

		// Save current visible set.
		s.lastVisible[viewer.ConnID] = currentVisible

		// Pre-send callback.
		if s.cfg.OnBeforeSend != nil {
			s.cfg.OnBeforeSend(viewer, currentVisible)
		}

		// Build and send frame.
		s.cfg.Frame.WriteFrame(&ReplicationFrame{
			Tick:    tick,
			Seq:     frameSeq,
			Viewer:  viewer,
			Full:    s.fullBuf,
			Deltas:  s.deltaBuf,
			Entered: entered,
			Exited:  exited,
			Removed: removed,
		})

		// Post-send callback.
		if s.cfg.OnAfterSend != nil {
			s.cfg.OnAfterSend(viewer, currentVisible)
		}
	}

	// Notify game about proxy entities in view for batch promotion.
	if s.cfg.OnProxiesInView != nil && len(proxyNetIDs) > 0 {
		s.cfg.OnProxiesInView(proxyNetIDs)
	}

	if s.cfg.OnAfterTick != nil {
		s.cfg.OnAfterTick(tick)
	}
}
