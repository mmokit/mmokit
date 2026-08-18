package universe

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/replication"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// Framework-level log categories for the server meshing subsystem.
// These are registered automatically on Stage initialization.
const (
	CatMeshTransfer = "mesh:transfer" // entity transfer send/receive/confirm
	CatMeshReplica  = "mesh:replica"  // replica CRUD: apply, create, update, expire, remove
	CatMeshProxy    = "mesh:proxy"    // proxy lifecycle: create, expire, promote, summaries
	CatMeshDormancy = "mesh:dormancy" // dormant entity wake events
	CatMeshCell     = "mesh:cell"     // cell start/stop/shutdown, coordinator lifecycle
	CatMeshAction   = "mesh:action"   // cross-cell action dispatch and results
	CatMeshMsg      = "mesh:msg"      // inter-node message routing
	CatMeshGrpc     = "mesh:grpc"     // grpcBridge routing decisions + HostNetwork dispatch
	CatNetConn      = "net:conn"      // connection lifecycle (WebSocket/UDP)
	CatNetTransport = "net:transport" // transport-level: UDP errors, buffer full, timeouts
	CatEngineLoop   = "engine:loop"   // game loop start/stop
)

// MeshCategories lists all framework log categories.
var MeshCategories = []string{
	CatMeshTransfer, CatMeshReplica, CatMeshProxy, CatMeshDormancy,
	CatMeshCell, CatMeshAction, CatMeshMsg, CatMeshGrpc,
	CatNetConn, CatNetTransport,
	CatEngineLoop,
	CatClientInput,
}

// StartupCategories are always enabled so server lifecycle is visible.
var StartupCategories = []string{CatMeshCell, CatEngineLoop, CatNetConn}

// CrossingEvent records that an entity has crossed a cell boundary
// and needs to be handed off. The HandoffDriver reads and drains
// this queue in PostSystems.
type CrossingEvent struct {
	Entity         ecs.Entity
	NetID          uint32
	ConnID         uint32     // non-zero for player entities
	Username       string     // non-empty for player entities
	DestCellID     MeshCellID // cell ID the entity crossed into
	BypassCooldown bool       // true for explicit teleports; false for natural boundary crossings
}

// Stage is one cell's runtime surface. It owns the cell's engine/ECS adapters,
// spatial index, entity-kind registry, messaging hooks, and transfer state.
// Games attach their own per-cell state through Process state factories rather
// than embedding Stage.
type Stage struct {
	eng         *engine.Engine
	cell        CellID
	cellID      MeshCellID
	aoiRadius   float32
	bridge      Bridge
	spatialGrid *spatial.HashGrid

	coord     *Process // set by Process.createNode after world factory
	fromSplit bool     // true if created during a cell split (skip initial entity spawning)

	// baseCellSize is this process's cell geometry, injected at construction
	// rather than read from a package global (CE-010). It is immutable for the
	// life of the stage: cell size is a property of the world layout, fixed
	// before any cell exists.
	//
	// Stored rather than reached through s.coord on demand for two reasons.
	// It is read on per-entity, per-tick paths, so a pointer hop and a nil
	// check per read is not free. And the point of CE-010 is that geometry is
	// INJECTED — a back-pointer to a Process that may be nil is the same
	// ambient-lookup shape as the global, just spelled differently.
	//
	// NewStage defaults it; Process.createNode overwrites it from
	// CellSize(), the same way it overwrites clusterClock.
	baseCellSize float32

	// clusterClock stamps outbound border-frame entries with the
	// authoritative producer's cluster-coherent wall time. Threaded from
	// Process.ClusterClock at cell construction; tests that build a
	// Stage without a Process get a fresh pre-observed clock so
	// Now() falls back to the local wall clock rather than panicking.
	clusterClock *ClusterClock

	replicaNetIDs    map[uint32]ecs.Entity
	highestSeenEpoch map[uint32]uint32 // per-netID: latest epoch seen under RFC 1982 ordering

	// borderLastSeen is the per-source-cell snapshot of the netIDs we
	// last received from that source in a MsgBorderFrame. ApplyBorderFrame
	// diffs the incoming frame's netID set against this snapshot and
	// removes replicas for any netID that dropped out — the explicit
	// despawn signal that replaces the old passive TTL-decay path.
	// Keyed on fromCellID (the source cell's mesh ID).
	borderLastSeen map[MeshCellID]map[uint32]struct{}
	replRegistry   *ReplicationRegistry
	velScale       float32 // max velocity for qvel quantization

	entityKinds map[uint8]*EntityKindDef // registered via RegisterEntityKind

	crossingQueue []CrossingEvent // entities that crossed a cell boundary this tick

	onTransferReceived       func(entity ecs.Entity, frame *TransferFrame)
	onPlayerTransferReceived func(entity ecs.Entity, frame *TransferFrame)

	// Called after UpdateCellBounds remaps entity positions.
	// connID is each connected player on this node.
	onCellBoundsChanged func(connID uint32)

	// Component mappers for core components
	posMap      *ecs.Map1[component.Position]
	velMap      *ecs.Map1[component.Velocity]
	rotMap      *ecs.Map1[component.Rotation]
	netIDMap    *ecs.Map1[component.NetworkID]
	kindMap     *ecs.Map1[component.EntityKind]
	colliderMap *ecs.Map1[component.Collider]
	cellMap     *ecs.Map1[component.CellCoord]
	ghostMap    *ecs.Map1[component.Ghost]
	replicaMap  *ecs.Map1[component.Replica]
	dormantMap  *ecs.Map1[component.Dormant]
	cooldownMap *ecs.Map1[component.TransferCooldown]
	playerMap   *ecs.Map1[component.PlayerConn]

	spawner *ecs.Map6[component.Position, component.Velocity, component.NetworkID, component.EntityKind, component.Collider, component.CellCoord]

	// Replica creation mapper (includes Rotation for full-fidelity replicas)
	replicaCreator *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]

	// netIDIdx tracks per-netID presence in this cell (Live / Replica).
	// Populated by SpawnFromTransferCore, SpawnLiveFromTransfer,
	// PromoteReplicaToLive, DemoteLiveToReplica, and upsertBorderReplica;
	// consulted by the invNoDuplicatePresencePerCell invariant.
	netIDIdx *netIDIndex

	// handoffAccepted records the highest Handoff epoch accepted per netID on
	// this cell, so a retried or duplicated Handoff cannot queue a second
	// promote or re-run the eager session pre-register. netIDIdx cannot serve
	// this: it is presence-only and its ActionDuplicate result merely logs in
	// non-strict mode (how the reference game runs). highestSeenEpoch cannot
	// either: the source's pre-commit border pushes have already advanced it
	// to the bumped epoch, so it would reject the FIRST handoff.
	//
	// Loop-owned — written from Cell.processMessage and read/pruned on the
	// same cell goroutine. No lock.
	handoffAccepted map[uint32]uint32

	// strictNetIDIndex enables enforcement of the netIDIndex transition
	// policy (reject duplicates, etc). When false (default during
	// rollout) the index tracks state observationally but transitions
	// are advisory.
	strictNetIDIndex bool

	// state holds typed per-stage state values keyed by reflect type name.
	// Populated by Process.createNode after kind-spec realization.
	// Game code accesses values via mmokit.State[T].
	state map[string]any

	// drainingForMerge, when true, suspends the handoff_driver on this
	// cell's game loop. Set by the MERGE executor when it starts
	// serializing the cell's entities for a drain-to-survivor transfer;
	// prevents the donor from emitting Prepare+Commit messages that would
	// race with the merge populate (and produce duplicate netIDs on the
	// survivor cell). Cleared implicitly when the cell is torn down by
	// stepMergeReleaseDonors; explicitly cleared on executor error
	// paths so a failed serialize or ship doesn't strand a live donor
	// with handoffs disabled.
	drainingForMerge atomic.Bool

	// transferDeactivatedViewers holds every connected, non-Pending player
	// session whose replication was suspended for a split/merge/migrate.
	// Active sessions additionally transition to StateTransferring; custom
	// lifecycle states retain their state while ReplicationSuspended keeps
	// them out of the ViewerSource. Recorded so abort can clear suspension and
	// advance the resumed source beyond the abandoned destination generation;
	// the normal success path tears the stage down. Loop-goroutine only.
	transferDeactivatedViewers []*engine.PlayerSession

	// dispatcher routes typed messages to registered handlers (mmokit.Handle /
	// Entity.Send). Lazily initialized via Stage.Dispatcher().
	dispatcher *MessageDispatcher

	// commands is the per-stage deferred-mutation buffer. Initialized in
	// NewStage; flushed by the engine loop driver between systems.
	commands *Commands

	// broadcastQueue accumulates auto-broadcast events for end-of-tick
	// AoI-filtered fanout. Populated by Stage.maybeBroadcast on the same-
	// cell post-handler path, the cross-cell source pre-handler path, and
	// the cross-cell dest post-handler path. Drained by the network /
	// replication phase. Initialized in NewStage.
	broadcastQueue *BroadcastQueue

	// tickCallbacks are invoked once per simulation tick by the loop driver
	// (and by tests that call TickCallbacks() directly). Registered via
	// RegisterTickCallback; backs mmokit.OnWorldTick / OnTick / OnTickEach.
	// Guarded by tickCallbacksMu so off-loop registrations during init are
	// race-safe with the loop driver iterating the slice.
	tickCallbacksMu sync.Mutex
	tickCallbacks   []func(dt float32)
}

// RegisterTickCallback adds fn to the per-tick callback list. Called by the
// engine's game-loop adapter once per tick (before per-entity callbacks).
// Safe to call from any goroutine — typically used at process startup.
func (s *Stage) RegisterTickCallback(fn func(dt float32)) {
	s.tickCallbacksMu.Lock()
	defer s.tickCallbacksMu.Unlock()
	s.tickCallbacks = append(s.tickCallbacks, fn)
}

// TickCallbacks returns a snapshot of the registered callbacks. Used by the
// loop driver and tests that want to drive ticks manually.
func (s *Stage) TickCallbacks() []func(dt float32) {
	s.tickCallbacksMu.Lock()
	defer s.tickCallbacksMu.Unlock()
	out := make([]func(dt float32), len(s.tickCallbacks))
	copy(out, s.tickCallbacks)
	return out
}

// NewStage creates a Stage for use within a world factory.
// Typically called by the Process; games that need manual setup can call this directly.
func NewStage(eng *engine.Engine, cell CellID, aoiRadius float32, replRegistry *ReplicationRegistry) *Stage {
	w := eng.ECS
	if replRegistry == nil {
		replRegistry = NewReplicationRegistry()
	}

	cellID := cell.MeshID()

	// Default to a fresh, pre-observed clock so WorldBases built outside
	// a Process (tests, stand-alone benchmarks) have a working Now().
	// Production paths overwrite this via base.clusterClock = c.ClusterClock
	// in Process.createNode immediately after NewStage.
	defaultClock := NewClusterClock()
	defaultClock.Observe(uint64(time.Now().UnixMilli()), 0)

	base := Stage{
		eng:          eng,
		cell:         cell,
		cellID:       cellID,
		aoiRadius:    aoiRadius,
		bridge:       NoopBridge{},
		clusterClock: defaultClock,
		// A Stage built outside a Process (tests, benchmarks) gets the
		// default; Process.createNode overwrites it with the configured
		// geometry immediately after construction.
		baseCellSize:     coords.DefaultCellSize,
		replicaNetIDs:    make(map[uint32]ecs.Entity),
		highestSeenEpoch: make(map[uint32]uint32),
		borderLastSeen:   make(map[MeshCellID]map[uint32]struct{}),
		replRegistry:     replRegistry,
		velScale:         1000, // default max velocity for qvel quantization

		posMap:      ecs.NewMap1[component.Position](w),
		velMap:      ecs.NewMap1[component.Velocity](w),
		rotMap:      ecs.NewMap1[component.Rotation](w),
		netIDMap:    ecs.NewMap1[component.NetworkID](w),
		kindMap:     ecs.NewMap1[component.EntityKind](w),
		colliderMap: ecs.NewMap1[component.Collider](w),
		cellMap:     ecs.NewMap1[component.CellCoord](w),
		ghostMap:    ecs.NewMap1[component.Ghost](w),
		replicaMap:  ecs.NewMap1[component.Replica](w),
		dormantMap:  ecs.NewMap1[component.Dormant](w),
		cooldownMap: ecs.NewMap1[component.TransferCooldown](w),
		playerMap:   ecs.NewMap1[component.PlayerConn](w),

		spawner:        ecs.NewMap6[component.Position, component.Velocity, component.NetworkID, component.EntityKind, component.Collider, component.CellCoord](w),
		replicaCreator: ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](w),

		broadcastQueue: &BroadcastQueue{},
	}

	base.netIDIdx = newNetIDIndex()
	base.handoffAccepted = make(map[uint32]uint32)

	// Register all framework log categories.
	eng.Log.RegisterCategories(MeshCategories...)

	// Wire GetNetID so the engine can track removed network IDs
	eng.GetNetID = func(e ecs.Entity) (uint32, bool) {
		if base.netIDMap.HasAll(e) {
			return base.netIDMap.Get(e).ID, true
		}
		return 0, false
	}

	result := &base
	result.commands = &Commands{world: w, stage: result}
	eng.SetEntityRemovalCleanup(result.cleanupEntityRemoval)
	return result
}

// cleanupEntityRemoval is the mandatory Stage-side half of every
// engine-mediated teardown. It runs while ECS components are still readable
// and deliberately uses entity-identity checks when clearing netID tracking so
// rollback of a rejected duplicate cannot erase the entity that won the slot.
func (b *Stage) cleanupEntityRemoval(entity ecs.Entity) {
	if b.spatialGrid != nil {
		b.spatialGrid.Deregister(entity)
	}

	if b.replicaMap.HasAll(entity) {
		netID := b.replicaMap.Get(entity).SourceNetID
		if tracked, ok := b.replicaNetIDs[netID]; ok && tracked == entity {
			delete(b.replicaNetIDs, netID)
			b.forgetBorderNetID(netID)
		}
	}

	if b.netIDIdx != nil && b.netIDMap.HasAll(entity) {
		b.netIDIdx.ExitEntity(b.netIDMap.Get(entity).ID, entity)
	}
}

func (b *Stage) forgetBorderNetID(netID uint32) {
	for _, seen := range b.borderLastSeen {
		delete(seen, netID)
	}
}

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

// Engine returns the underlying engine.
func (b *Stage) Engine() *engine.Engine { return b.eng }

// LookupNetID returns the currently-tracked ECS entity for netID and its
// presence on this cell, or (zero, 0, false) if not present. Useful for
// cross-cell transfer plumbing that needs to re-wire state against an
// entity the netIDIndex already owns (e.g. rewiring a player session
// against an entity that crossed via handoff before a merge populate).
func (b *Stage) LookupNetID(netID uint32) (ecs.Entity, EntityPresence, bool) {
	if b.netIDIdx == nil {
		return ecs.Entity{}, PresenceNone, false
	}
	return b.netIDIdx.Lookup(netID)
}

// handoffAlreadyAccepted reports whether this cell has already accepted a
// Handoff for netID at an epoch at least as new as the candidate. Uses the
// same RFC 1982 serial comparison as upsertBorderReplica so an epoch wrap
// does not permanently wedge the entity.
func (b *Stage) handoffAlreadyAccepted(netID, epoch uint32) bool {
	if b.handoffAccepted == nil {
		return false
	}
	stored, ok := b.handoffAccepted[netID]
	if !ok {
		return false
	}
	return !serialUint32After(epoch, stored)
}

// markHandoffAccepted records that this cell has accepted a Handoff for
// netID at epoch. Only advances the stored value.
func (b *Stage) markHandoffAccepted(netID, epoch uint32) {
	if b.handoffAccepted == nil {
		b.handoffAccepted = make(map[uint32]uint32)
	}
	if stored, ok := b.handoffAccepted[netID]; ok && !serialUint32After(epoch, stored) {
		return
	}
	b.handoffAccepted[netID] = epoch
}

// forgetHandoffAccepted drops the acceptance mark for netID. Called ONLY from
// DemoteLiveToReplica: authority has left this cell, so a future handoff back
// in (necessarily at a higher epoch, but the mark must not linger across an
// epoch wrap either) has to be accepted afresh.
//
// Deliberately NOT called from RemoveReplicaByNetID — drainPendingPromotes
// invokes that immediately before SpawnLiveFromTransfer and would erase the
// mark the promote it is executing depends on.
func (b *Stage) forgetHandoffAccepted(netID uint32) {
	if b.handoffAccepted == nil {
		return
	}
	delete(b.handoffAccepted, netID)
}

// RegisterLiveNetID adds (netID, handle) to the stage's local NetID index
// as a Live presence. Used by tests; production code reaches the index via
// the entity-spawn paths.
func (b *Stage) RegisterLiveNetID(netID uint32, h ecs.Entity) {
	if b.netIDIdx == nil {
		return
	}
	b.netIDIdx.Enter(netID, h, PresenceLive)
}

// RegisterReplicaNetID adds (netID, handle) to the stage's local NetID index
// as a Replica presence. Used by tests; production code reaches the index
// via upsertBorderReplica during border-frame application.
func (b *Stage) RegisterReplicaNetID(netID uint32, h ecs.Entity) {
	if b.netIDIdx == nil {
		return
	}
	b.netIDIdx.Enter(netID, h, PresenceReplica)
}

// SetDrainingForMerge toggles the drain-for-merge flag, which suspends
// this cell's handoff_driver. Called by the MERGE executor at serialize
// time (set=true) and on executor failure or abort (set=false).
// Implicitly cleared when the cell's game loop exits during
// stepMergeReleaseDonors; the explicit clear only matters on error paths
// that keep the donor alive.
func (b *Stage) SetDrainingForMerge(v bool) {
	b.drainingForMerge.Store(v)
}

// IsDrainingForMerge returns true while the handoff_driver should skip
// this cell's crossings — see SetDrainingForMerge.
func (b *Stage) IsDrainingForMerge() bool {
	return b.drainingForMerge.Load()
}

// DeactivateTransferViewers suspends replication for every connected,
// non-Pending player on this cell. Active sessions also retain the existing
// StateActive→StateTransferring lifecycle transition; custom states such as
// dead, docking, or docked remain unchanged while their viewer is suppressed.
// Called by the executor at serialize time for split/merge/migrate — all of
// which fully evacuate the source cell. Recorded for
// ReactivateTransferViewers (abort recovery). MUST run on this cell's
// game-loop goroutine (it mutates engine.Players). Idempotent across the four
// per-quadrant SPLIT Execute calls because already-suspended sessions are
// skipped.
func (b *Stage) DeactivateTransferViewers() {
	var moved []*engine.PlayerSession
	for _, s := range b.eng.Players.AllSessions() {
		if s.ConnID == 0 || s.State == engine.StatePending || s.ReplicationSuspended {
			continue
		}
		s.ReplicationSuspended = true
		moved = append(moved, s)
	}
	for _, s := range moved {
		if s.State == engine.StateActive {
			_ = b.eng.Players.Transition(s, engine.StateTransferring)
		}
	}
	b.transferDeactivatedViewers = append(b.transferDeactivatedViewers, moved...)
}

// ReactivateTransferViewers returns the sessions deactivated by
// DeactivateTransferViewers to StateActive. Called on the source cell when a
// transfer is aborted or its serialize/ship fails and the source cell survives.
// The attempted destination may already have emitted generation N+1 before an
// abort arrives, so the resumed source advances from N to N+2 before becoming
// visible again. This forces a fresh replication stream that cannot be rejected
// behind the abandoned destination stream. NetworkID epochs are intentionally
// untouched: no entity-authority commit occurred.
// MUST run on this cell's game-loop goroutine.
func (b *Stage) ReactivateTransferViewers() {
	for _, s := range b.transferDeactivatedViewers {
		s.StreamGeneration += 2
		s.ReplicationSuspended = false
		if s.State == engine.StateTransferring {
			_ = b.eng.Players.Transition(s, engine.StateActive)
		}
	}
	b.transferDeactivatedViewers = nil
}

// Bridge returns the bridge for inter-cell communication.
func (b *Stage) Bridge() Bridge { return b.bridge }

// Commands returns the per-stage deferred-mutation buffer. Use from
// inside system Update or any other locked-world context to queue
// structural changes that will apply after the current system finishes.
func (b *Stage) Commands() *Commands { return b.commands }

// TickOne is a test-only helper that runs a single system's Update
// then flushes the Commands buffer. Mirrors the engine game-loop's
// per-system contract (sys.Update(dt); commands.Flush()) so unit
// tests that bypass the full loop see the same mutation visibility.
//
// Not for production use — the game loop drives the real flush.
func (b *Stage) TickOne(sys engine.System, dt float32) {
	sys.Update(dt)
	if b.commands != nil {
		b.commands.Flush()
	}
}

// Cell returns this node's cell coordinates.
func (b *Stage) Cell() CellID { return b.cell }

// Process returns the coordinator that owns this node, or nil in single-node mode.
func (b *Stage) Process() *Process { return b.coord }

// ClusterClock returns this cell's cluster clock — the cross-cell/cross-host
// coherent time source used to stamp outbound replication. Never nil for a
// stage built via NewStage (falls back to a local-wall-clock instance until a
// CoordTimeSync is observed). Tests can drive it via (*ClusterClock).SetNowFn.
func (b *Stage) ClusterClock() *ClusterClock { return b.clusterClock }

// ClusterTimeMs returns the cluster-coherent wall clock in milliseconds,
// quantized to this cell's tick boundary (the same stamp used for replication
// producedAtMs). Identical across every cell and host on a given cluster tick,
// so it is the right basis for time-based animation that must stay continuous
// across a cell-boundary handoff.
func (b *Stage) ClusterTimeMs() uint64 {
	if b.clusterClock == nil {
		return 0
	}
	return b.clusterClock.TickTime(b.eng.TickIntervalMs())
}

// FromSplit returns true if this world was created during a cell split.
// Split-created worlds should skip initial entity spawning since entities
// arrive via transfer from the parent cell.
func (b *Stage) FromSplit() bool { return b.fromSplit }

// rootCell returns the depth-0 ancestor of this node's cell.
func (b *Stage) rootCell() CellID {
	c := b.cell
	for c.Depth > 0 {
		c = c.Parent()
	}
	return c
}

// CellSize returns the base cell size this stage's process was configured with.
// Entities always use base-cell coordinates regardless of quadtree depth, so a
// split cell's entities are still expressed against this value, not against the
// smaller extent of the subcell they happen to occupy.
//
// Reads an injected field rather than a package global (CE-010): two Processes
// in one binary have their own geometry, and the global could only ever hold
// one of them.
func (b *Stage) CellSize() float32 { return b.baseCellSize }

// CellID returns this cell's mesh-form identifier (e.g., "cell_0_0").
func (b *Stage) CellID() MeshCellID { return b.cellID }

// SpatialGrid returns the spatial hash grid for AoI/collision queries.
func (b *Stage) SpatialGrid() *spatial.HashGrid { return b.spatialGrid }

// SetSpatialGrid replaces the spatial hash grid (useful for tests or manual node setup).
func (b *Stage) SetSpatialGrid(g *spatial.HashGrid) { b.spatialGrid = g }

// ReplicaNetIDs returns the map tracking replica entities by network ID.
func (b *Stage) ReplicaNetIDs() map[uint32]ecs.Entity { return b.replicaNetIDs }

// ReplicationRegistry returns the registry used for replica scanning.
func (b *Stage) ReplicationRegistry() *ReplicationRegistry { return b.replRegistry }

// SetOnTransferReceived sets a hook called after any entity is spawned from a transfer.
func (b *Stage) SetOnTransferReceived(fn func(ecs.Entity, *TransferFrame)) {
	b.onTransferReceived = fn
}

// SetOnPlayerTransferReceived sets a hook called after a player entity is spawned from a transfer.
func (b *Stage) SetOnPlayerTransferReceived(fn func(ecs.Entity, *TransferFrame)) {
	b.onPlayerTransferReceived = fn
}

// SetOnCellBoundsChanged sets a callback invoked for each connected player
// after UpdateCellBounds remaps entity positions. Use this to send updated
// cell metadata to clients (e.g. new cell coordinates and size).
func (b *Stage) SetOnCellBoundsChanged(fn func(connID uint32)) {
	b.onCellBoundsChanged = fn
}

// RegisterEntityKind registers an entity kind definition. This:
//   - Registers all components with the transfer ReplicationRegistry
//   - Stores ensureExists callbacks for auto-filling on transfer receive
//   - Stores the def for NewNetworkSystem to build replicators automatically
func (b *Stage) RegisterEntityKind(def EntityKindDef) {
	if b.entityKinds == nil {
		b.entityKinds = make(map[uint8]*EntityKindDef)
	}
	for _, c := range def.components {
		if c.registerTransfer != nil {
			c.registerTransfer(b.replRegistry)
		}
	}
	b.entityKinds[def.Kind] = &def
}

// EntityKindDefs returns the registered entity kind definitions.
func (b *Stage) EntityKindDefs() map[uint8]*EntityKindDef {
	return b.entityKinds
}

// EnsureEntityKindComponents adds zero-value components for all components
// registered on the entity's kind. If the entity already has a component,
// it is left unchanged.
func (b *Stage) EnsureEntityKindComponents(entity ecs.Entity) {
	if !b.kindMap.HasAll(entity) {
		return
	}
	kind := b.kindMap.Get(entity).Type
	def, ok := b.entityKinds[kind]
	if !ok {
		return
	}
	for _, c := range def.components {
		c.ensureExists(entity)
	}
}

// SendPlayerEntityAssigned sends the framework-level PlayerEntityAssigned
// typed event to a client, informing it of its entity NetID and world
// position. Uses the node's root cell coordinates.
//
// Builds the encoded frame via the registry's PlayerEntityAssigned encoder.
// When that encoder is unset (test paths that never import mmokit, so the
// framework wire types are undeclared) this is a silent no-op.
func (b *Stage) SendPlayerEntityAssigned(connID uint32, entity ecs.Entity) {
	build := b.Wire().FrameworkEncoders().PlayerEntityAssigned
	if build == nil {
		return
	}
	netID := uint32(0)
	if b.netIDMap.HasAll(entity) {
		netID = b.netIDMap.Get(entity).ID
	}
	cell := b.rootCell()
	cs := b.baseCellSize
	var worldX, worldY float32
	if b.posMap.HasAll(entity) {
		pos := b.posMap.Get(entity)
		worldX = pos.X + float32(cell.X)*cs
		worldY = pos.Y + float32(cell.Y)*cs
	}
	frame := build(netID, worldX, worldY)
	if frame == nil {
		// Encoder guard rejected the payload — already counted and logged.
		return
	}
	b.eng.ConnMgr.Send(connID, frame)
}

// ClusterCells returns the current cluster topology view from this
// Stage's coordinator reference. Wraps Process.ClusterCells;
// returns nil when this Stage has no coordinator wiring.
//
// Games use this to build their own SE_DEBUG_INFO messages and push
// them to clients via gw.Engine().ConnMgr.SendReliable — see
// examples/4node-basic for the pattern. Topology distribution is a
// game concern: different games want different debug data, so the
// engine no longer ships a built-in broadcaster.
func (b *Stage) ClusterCells() []ClusterCellInfo {
	if b.coord == nil {
		return nil
	}
	return b.coord.ClusterCells()
}

// GhostMap returns the Ghost component mapper. Used by games that still
// reference Ghost entities (e.g., visual continuity). No longer used by
// BoundarySystem after the handoff-protocol refactor.
func (b *Stage) GhostMap() *ecs.Map1[component.Ghost] { return b.ghostMap }

// IsGhost reports whether the entity has the Ghost component. Convenience
// wrapper around GhostMap().HasAll(e).
func (b *Stage) IsGhost(e ecs.Entity) bool {
	return b.ghostMap.HasAll(e)
}

// IsReplica reports whether the entity is a local continuity representation
// whose authority lives on another cell. Lifecycle cleanup must not treat a
// Replica as an authoritative despawn: handoff demotes Live -> Replica before
// transitioning the source player session to StateTransferring.
func (b *Stage) IsReplica(e ecs.Entity) bool {
	return b.replicaMap.HasAll(e)
}

// Topology is an alias for ClusterCells. Reads more naturally at call
// sites that build cell-topology messages.
func (b *Stage) Topology() []ClusterCellInfo {
	return b.ClusterCells()
}

// HasInflightTransfers reports whether any cell-transfer request is in
// flight cluster-wide. Stage delegates to Process so per-cell systems
// (e.g. debugBroadcaster) can skip work during commit windows that
// would otherwise expose transient cellToHostMap state to clients.
func (b *Stage) HasInflightTransfers() bool {
	if b.coord == nil {
		return false
	}
	return b.coord.HasInflightTransfers()
}

// GridDimensions returns the configured grid size (cells X, cells Y, base cell size)
// for cluster topology messages. Reads from Process.cfg.
func (b *Stage) GridDimensions() (uint32, uint32, float32) {
	if b.coord == nil {
		return 0, 0, b.baseCellSize
	}
	return b.coord.cfg.CellsX, b.coord.cfg.CellsY, b.baseCellSize
}

// QueueCrossing appends an entity crossing event to the per-tick queue.
// The HandoffDriver drains this queue in PostSystems.
func (b *Stage) QueueCrossing(evt CrossingEvent) {
	b.crossingQueue = append(b.crossingQueue, evt)
}

// DrainCrossingQueue returns the current crossing queue and resets it for
// the next tick. The returned slice is reused — callers must not retain it
// across ticks.
func (b *Stage) DrainCrossingQueue() []CrossingEvent {
	q := b.crossingQueue
	b.crossingQueue = b.crossingQueue[:0]
	return q
}

// PositionMap returns the Position component mapper.
func (b *Stage) PositionMap() *ecs.Map1[component.Position] { return b.posMap }

// VelocityMap returns the Velocity component mapper.
func (b *Stage) VelocityMap() *ecs.Map1[component.Velocity] { return b.velMap }

// NetworkIDMap returns the NetworkID component mapper.
func (b *Stage) NetworkIDMap() *ecs.Map1[component.NetworkID] { return b.netIDMap }

// EntityKindMap returns the EntityKind component mapper.
func (b *Stage) EntityKindMap() *ecs.Map1[component.EntityKind] { return b.kindMap }

// ColliderMap returns the Collider component mapper.
func (b *Stage) ColliderMap() *ecs.Map1[component.Collider] { return b.colliderMap }

// CellCoordMap returns the CellCoord component mapper.
func (b *Stage) CellCoordMap() *ecs.Map1[component.CellCoord] { return b.cellMap }

// ---------------------------------------------------------------------------
// Cell runtime accessors and default lifecycle behavior
// ---------------------------------------------------------------------------

func (b *Stage) ECSWorld() *ecs.World        { return b.eng.ECS }
func (b *Stage) GetAoIRadius() float32       { return b.aoiRadius }
func (b *Stage) SetBridge(bridge Bridge)     { b.bridge = bridge }
func (b *Stage) MarkForRemoval(e ecs.Entity) { b.eng.MarkForRemoval(e) }
func (b *Stage) Shutdown()                   {}

// Hooks returns the engine lifecycle hooks for this Stage. If a state
// registered via mmokit.AddState[T] implements `Hooks() engine.Hooks`,
// its hooks are returned — restoring the pre-refactor behavior where
// game state (e.g. GameWorld) supplied PreFlush / PostFlush / etc via
// implicit-embedding pointer promotion. Without this lookup, game hooks
// (processDockCompletions, postFlush, clearTickState, postTick) never
// fire, and game state silently desyncs (the first symptom is usually
// docking timers reaching 0 but no Docked transition / event).
//
// At most one registered state may provide Hooks; if multiple do, the
// last-registered wins (factories iterate the underlying map in
// indeterminate order — games should keep this single-owner). If no
// registered state has a Hooks method, returns an empty hook set.
func (b *Stage) Hooks() engine.Hooks {
	type hooksProvider interface {
		Hooks() engine.Hooks
	}
	for _, v := range b.state {
		if hp, ok := v.(hooksProvider); ok {
			return hp.Hooks()
		}
	}
	return engine.Hooks{}
}

// SendEventTyped writes a single-event 0x00 frame for msg to connID.
// msg must be a pointer to a Go struct registered via mmokit.RegisterEvent[T].
// The reflection codec serializes the body; the typeID is derived from T.
//
// Free function rather than a method on *Stage because Go does not allow
// generic methods on non-generic types. This is the canonical send path
// after Plan 1 Phase 7 retired the proto-envelope Stage.SendEvent.
func SendEventTyped[T any](stage *Stage, connID uint32, msg *T) {
	frame := BuildTypedEventFrameRaw(msg)
	if frame == nil {
		// Encoder guard rejected the payload; BuildTypedEventFrameRaw already
		// counted and logged it. Sending a nil frame would close the read side
		// on a zero-length write.
		return
	}
	stage.eng.ConnMgr.SendReliable(connID, frame)
}

// BuildTypedEventFrameRaw returns the encoded channel-0x00 typed-event
// frame for msg without sending it. Used by direct-Conn.Send paths that
// need to deliver a frame without a *Stage in scope (e.g. off-loop
// marketplace/bank notifications). T must be registered via
// mmokit.RegisterEvent[T] at startup; panics otherwise.
//
// Same wire layout as SendEventTyped; the only difference is that the
// caller is responsible for delivering the frame.
//
// Returns nil — never panics — when the encoder length guards reject the
// payload, e.g. an event carrying a player-supplied string over 65535 bytes.
// Misregistration is a startup-time programmer error and still panics; an
// oversized field is runtime data on the tick goroutine and must not be.
func BuildTypedEventFrameRaw[T any](msg *T) []byte {
	t := reflect.TypeFor[T]()
	if !globalWire.ServerEventRegistered(t) {
		panic(fmt.Sprintf("BuildTypedEventFrameRaw: type %s not registered via mmokit.RegisterEvent[T]", t.String()))
	}
	id := TypeIDOf(t)
	body, err := ReflectMarshal(msg)
	if err != nil {
		NoteMarshalDrop(CatMeshAction, "typed event %s dropped — %v", t.String(), err)
		return nil
	}
	return EncodeTypedEventFrame(id, body)
}

// UpdateCellBounds updates the cell identity and coordinate bounds for this world.
// Called from the game loop during dynamic cell split/merge operations.
//
// Entities always use base-cell (depth-0) coordinates, so position remapping
// is only needed when the root cell changes (cross-root transfers). For subcell
// depth changes within the same root cell (split/merge), only the cell identity
// and node ID are updated.
func (b *Stage) UpdateCellBounds(cell CellID, cellSize float32) {
	oldCell := b.cell
	b.cell = cell
	b.cellID = cell.MeshID()

	// Check if root cell changed — only then do positions need remapping.
	oldRoot := oldCell
	for oldRoot.Depth > 0 {
		oldRoot = oldRoot.Parent()
	}
	newRoot := cell
	for newRoot.Depth > 0 {
		newRoot = newRoot.Parent()
	}

	if oldRoot != newRoot {
		dx := float32(oldRoot.X-newRoot.X) * cellSize
		dy := float32(oldRoot.Y-newRoot.Y) * cellSize

		if dx != 0 || dy != 0 {
			filter := ecs.NewFilter1[component.Position](b.eng.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			query := filter.Query()
			for query.Next() {
				entity := query.Entity()
				pos := b.posMap.Get(entity)
				pos.X += dx
				pos.Y += dy
				if b.cellMap.HasAll(entity) {
					cc := b.cellMap.Get(entity)
					cc.CellX = newRoot.X
					cc.CellY = newRoot.Y
				}
			}
		}
	}

	// Notify connected players about the cell change
	if b.onCellBoundsChanged != nil {
		playerFilter := ecs.NewFilter1[component.PlayerConn](b.eng.ECS).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
		pq := playerFilter.Query()
		for pq.Next() {
			pc := b.playerMap.Get(pq.Entity())
			if pc.ConnID != 0 {
				b.onCellBoundsChanged(pc.ConnID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Transfer serialization
// ---------------------------------------------------------------------------

// SerializeEntityCore reads core components from an entity and returns a
// TransferFrame. Games can call this from their own SerializeEntity override
// to get the core fields, then append game-specific component slices.
func (b *Stage) SerializeEntityCore(entity ecs.Entity) *TransferFrame {
	f := &TransferFrame{}

	if b.netIDMap.HasAll(entity) {
		nid := b.netIDMap.Get(entity)
		f.NetworkID = nid.ID
		f.Epoch = nid.Epoch
	}
	if b.kindMap.HasAll(entity) {
		f.EntityType = b.kindMap.Get(entity).Type
	}
	if b.posMap.HasAll(entity) {
		pos := b.posMap.Get(entity)
		f.PosX, f.PosY = pos.X, pos.Y
	}
	if b.velMap.HasAll(entity) {
		vel := b.velMap.Get(entity)
		f.VelX, f.VelY = vel.X, vel.Y
	}
	if b.rotMap.HasAll(entity) {
		f.Rotation = b.rotMap.Get(entity).Angle
	}
	if b.colliderMap.HasAll(entity) {
		f.Collider = *b.colliderMap.Get(entity)
	}
	if b.cellMap.HasAll(entity) {
		sec := b.cellMap.Get(entity)
		f.CellX, f.CellY = sec.CellX, sec.CellY
	}
	if b.playerMap.HasAll(entity) {
		f.ConnID = b.playerMap.Get(entity).ConnID
		if f.ConnID != 0 {
			if s := b.eng.Players.ByConnID(f.ConnID); s != nil {
				f.Username = s.Username
				f.StreamGeneration = s.StreamGeneration
				f.DebugFlags = uint32(s.DebugFlags)
				f.UserID = s.UserID
			}
			// Populate the SessionKey from the VCM if this is node mode.
			// Without this, the destination cannot remap the player to a
			// local VCM entry after transfer, and client I/O stops flowing
			// to the transferred player. In single-host colocated mode there
			// is no VCM and SessionKey fields stay empty — the destination
			// falls back to the source ConnID which is valid there.
			if b.coord != nil && b.coord.vcm != nil {
				if key, ok := b.coord.vcm.LookupByLocal(f.ConnID); ok {
					f.GatewayID = key.GatewayID
					f.GatewayConnID = key.ConnID
				}
			}
		}
	}

	return f
}

// SerializeEntity encodes an entity's core components plus all registered
// game-specific components for cross-cell transfer.
func (b *Stage) SerializeEntity(entity ecs.Entity) ([]byte, error) {
	frame := b.SerializeEntityCore(entity)
	// Append all registered game-specific components.
	// Skip IsTransferCore replicators — those values are already carried by
	// the dedicated frame fields (PosX/PosY etc.) and must not be duplicated.
	if b.replRegistry != nil {
		for _, rep := range b.replRegistry.All() {
			if rep.IsTransferCore {
				continue
			}
			if data := rep.Scan(entity); data != nil {
				frame.Components = append(frame.Components, ComponentSlice{ID: rep.ID, Data: data})
			}
		}
	}
	return MarshalTransferFrame(frame)
}

// SpawnFromTransferCore decodes transfer data, creates an entity with core
// components, and returns the entity plus the decoded frame (for applying
// game-specific components). Adds TransferCooldown automatically.
//
// The `presence` argument controls how the netID is registered in the
// netIDIndex — all current callers pass PresenceLive. The parameter is
// kept explicit so the intent is visible at each call site.
func (b *Stage) SpawnFromTransferCore(data []byte, presence EntityPresence) (ecs.Entity, *TransferFrame, error) {
	frame, err := UnmarshalTransferFrame(data)
	if err != nil {
		return ecs.Entity{}, nil, err
	}

	// Node-mode VCM remap: if the frame carries a SessionKey (GatewayID +
	// GatewayConnID from the source's VCM) and this node has its own VCM,
	// register the session here and replace frame.ConnID with the
	// destination-local ID. RegisterSession is idempotent; when the cell
	// handler pre-registered the session before calling this method, the
	// same localID is returned and the engine player session stays wired.
	if frame.ConnID != 0 && frame.GatewayConnID != 0 && b.coord != nil && b.coord.vcm != nil {
		key := SessionKey{GatewayID: frame.GatewayID, ConnID: frame.GatewayConnID}
		localID := b.coord.vcm.RegisterSession(key, frame.Username, 1, b.cellID)
		frame.ConnID = localID
	}

	entity := b.spawner.NewEntity(
		&component.Position{X: frame.PosX, Y: frame.PosY},
		&component.Velocity{X: frame.VelX, Y: frame.VelY},
		&component.NetworkID{ID: frame.NetworkID, Epoch: frame.Epoch},
		&component.EntityKind{Type: frame.EntityType},
		&frame.Collider,
		&component.CellCoord{CellX: frame.CellX, CellY: frame.CellY},
	)

	b.rotMap.Add(entity, &component.Rotation{Angle: frame.Rotation})
	if frame.ConnID != 0 {
		b.playerMap.Add(entity, &component.PlayerConn{ConnID: frame.ConnID})
	}

	b.cooldownMap.Add(entity, &component.TransferCooldown{
		Remaining:     20,
		ArrivalWallMs: uint64(time.Now().UnixMilli()),
	})

	// Apply registered game-specific components.
	// Skip IsTransferCore replicators — their authoritative values were already
	// written from the dedicated frame fields (PosX/PosY etc.) above.
	//
	// A component that fails to decode is skipped and the spawn continues. The
	// source cell has already demoted this entity by the time its blob arrives,
	// so returning an error here would delete it from the cluster outright —
	// see noteComponentDecodeDrop.
	if b.replRegistry != nil {
		for _, cs := range frame.Components {
			if rep := b.replRegistry.Get(cs.ID); rep != nil {
				if rep.IsTransferCore {
					continue
				}
				var applyErr error
				if rep.Add != nil {
					applyErr = rep.Add(entity, cs.Data)
				} else if rep.Apply != nil {
					applyErr = rep.Apply(entity, cs.Data)
				}
				if applyErr != nil {
					noteComponentDecodeDrop("transfer spawn", cs.ID, applyErr)
				}
			}
		}
	}

	// Auto-fill any registered components that weren't in the transfer data.
	b.EnsureEntityKindComponents(entity)

	// Game-specific post-processing hook
	if b.onTransferReceived != nil {
		b.onTransferReceived(entity, frame)
	}

	// Framework-required wiring on player transfers: re-attach the entity,
	// restore the per-session DebugFlags bitmask, and stamp the auth UserID
	// onto the pre-registered session BEFORE any game-specific hook runs.
	// Two callers reach this with different session-state preconditions:
	//
	//   - cell_transfer_executor.populateCell (split/merge/migrate): session
	//     is NOT yet registered; ByConnID returns nil; this block no-ops.
	//     populateCell registers the session and sets these fields itself
	//     after RegisterSessionTransfer returns.
	//
	//   - cell.MsgHandoff → drainPendingPromotes (boundary handoff): session
	//     IS pre-registered by RegisterTransferSession; this block is the
	//     only path that restores DebugFlags. Without it, every cross-cell
	//     walk zeroes a player's debug grants and the debugBroadcaster
	//     short-circuits on `sess.DebugFlags == 0` — topology overlay
	//     freezes after the first boundary crossing even though split/merge
	//     keep firing.
	//
	// Doing this in the framework instead of the optional onPlayerTransferReceived
	// hook means games that override the hook for game-specific behavior (e.g.
	// the space game's MapData send + PlayerSessions tracking) can't accidentally
	// drop these field assignments.
	if frame.ConnID != 0 {
		if s := b.eng.Players.ByConnID(frame.ConnID); s != nil {
			s.Entity = entity
			s.StreamGeneration = frame.StreamGeneration
			s.DebugFlags = engine.DebugFlag(frame.DebugFlags)
			s.UserID = frame.UserID
		}
		if b.onPlayerTransferReceived != nil {
			b.onPlayerTransferReceived(entity, frame)
		}
	}

	b.eng.Log.Log(CatMeshTransfer, "[%s] transfer received: netID=%d at (%.0f,%.0f)", b.cellID, frame.NetworkID, frame.PosX, frame.PosY)

	if b.netIDIdx != nil && frame.NetworkID != 0 {
		res := b.netIDIdx.Enter(frame.NetworkID, entity, presence)
		switch res.Action {
		case ActionInstalled, ActionUpdated:
			// Normal install; Replica→Replica updates would land here
			// but transfers always use PresenceLive in current callers.
		case ActionDuplicate:
			b.eng.Log.Log(CatMeshTransfer,
				"[%s] duplicate live spawn blocked: netID=%d", b.cellID, frame.NetworkID)
			if b.strictNetIDIndex {
				b.eng.RemoveEntityNowWithPolicy(entity, engine.RemovalLocalOnly)
				return ecs.Entity{}, nil, fmt.Errorf("duplicate live netID %d", frame.NetworkID)
			}
		case ActionRejected:
			// Happens when presence=Live lands on a slot already Replica
			// (or similar). The sanctioned path for Replica→Live is
			// PromoteReplicaToLive, not a second Enter. In strict mode
			// tear down the new ECS row so a stray transfer cannot
			// silently create a second Live for the same netID.
			b.eng.Log.Log(CatMeshTransfer,
				"[%s] transfer rejected by netIDIndex: netID=%d presence=%d",
				b.cellID, frame.NetworkID, presence)
			if b.strictNetIDIndex {
				b.eng.RemoveEntityNowWithPolicy(entity, engine.RemovalLocalOnly)
				return ecs.Entity{}, nil, fmt.Errorf("transfer rejected: netID %d conflicts with existing presence", frame.NetworkID)
			}
		}
	}

	return entity, frame, nil
}

// SpawnFromTransfer creates an entity from transfer data.
func (b *Stage) SpawnFromTransfer(data []byte) (uint32, uint32, error) {
	_, frame, err := b.SpawnFromTransferCore(data, PresenceLive)
	if err != nil {
		return 0, 0, err
	}
	return frame.NetworkID, frame.ConnID, nil
}

// DemoteLiveToReplica is the source-side mirror of PromoteReplicaToLive. At
// handoff commit, the source cell converts its Live entity for netID
// into a Replica of the destination cell — the SAME ECS entity, same
// Position/Velocity/Rotation/components — so downstream replication
// continues to scan the entity and emit SE_ENTITY_UPDATE frames to
// nearby clients. No SE_ENTITY_REMOVED is ever emitted, which is what
// makes the handoff client-invisible.
//
// After this call:
//   - The source's BorderDispatcher push walk skips the entity
//     (replicas aren't in the push set).
//   - The source's client-facing ReplicationSystem continues to scan
//     the entity for viewers in AoI.
//   - The destination's first post-Commit border frame flows into
//     upsertBorderReplica's existing replica-update branch and refreshes
//     Position/Velocity/component tail from the new authoritative sim.
//
// Returns an error only if no Live entity exists for netID; on a
// successful demote the error is nil.
func (b *Stage) DemoteLiveToReplica(netID uint32, newSourceCellID string) error {
	ent, presence, ok := b.netIDIdx.Lookup(netID)
	if !ok || presence != PresenceLive {
		return fmt.Errorf("DemoteLiveToReplica: netID=%d not live on cell %s", netID, b.cellID)
	}
	if !b.eng.ECS.Alive(ent) {
		return fmt.Errorf("DemoteLiveToReplica: entity for netID=%d not alive", netID)
	}

	// Add or refresh Replica component. A fresh TTL (30 = 1.5s at 20Hz)
	// gives the destination's subsequent border frames time to arrive
	// and refresh the replica's fields. Stamp ProducedAtMs with this
	// host's cluster-clock Now() — the source's final pre-handoff sample
	// IS the authoritative producer stamp until the destination's first
	// post-commit border frame overwrites it.
	var nowMs uint64
	if b.clusterClock != nil {
		nowMs = b.clusterClock.TickTime(b.eng.TickIntervalMs())
	}
	if !b.replicaMap.HasAll(ent) {
		b.replicaMap.Add(ent, &component.Replica{
			SourceCellID: newSourceCellID,
			SourceNetID:  netID,
			TTL:          30,
			ProducedAtMs: nowMs,
		})
	} else {
		rep := b.replicaMap.Get(ent)
		rep.SourceCellID = newSourceCellID
		rep.SourceNetID = netID
		rep.TTL = 30
		rep.ProducedAtMs = nowMs
	}

	// Flip netIDIdx slot Live → Replica via the sanctioned Demote path.
	if res := b.netIDIdx.Demote(netID, ent); res.Action != ActionUpdated {
		return fmt.Errorf("DemoteLiveToReplica: netIDIdx.Demote returned action=%d for netID=%d",
			res.Action, netID)
	}

	// Register so subsequent border frames update this entity in place
	// instead of creating a second ECS replica.
	b.replicaNetIDs[netID] = ent

	// Authority has left this cell: clear the handoff-acceptance mark so a
	// later handoff back into this cell is accepted rather than deduped away.
	b.forgetHandoffAccepted(netID)

	b.eng.Log.Log(CatMeshTransfer,
		"[%s] demoted live→replica: netID=%d newSource=%s",
		b.cellID, netID, newSourceCellID)
	return nil
}

// PromoteReplicaToLive is the destination-side counterpart of
// DemoteLiveToReplica. At handoff commit, the destination cell flips
// a border-replica entity into an authoritative Live entity — SAME
// ECS entity, same Position/Velocity/Rotation/components — so the
// border-dispatcher push walk and outbound client replication can
// begin treating it as an authority. Replica component is removed
// and the netIDIdx slot transitions Replica → Live via the sanctioned
// Promote path. Epoch is bumped to the value carried in the Handoff
// payload.
//
// Returns an error if no Replica exists for netID or the entity is
// not alive.
func (b *Stage) PromoteReplicaToLive(netID uint32, newEpoch uint32) error {
	ent, presence, ok := b.netIDIdx.Lookup(netID)
	if !ok || presence != PresenceReplica {
		return fmt.Errorf("PromoteReplicaToLive: netID=%d not a replica on cell %s", netID, b.cellID)
	}
	if !b.eng.ECS.Alive(ent) {
		return fmt.Errorf("PromoteReplicaToLive: entity for netID=%d not alive", netID)
	}

	// Bump epoch on the NetworkID to match the authoritative value
	// the source stamped into the Handoff payload.
	if b.netIDMap.HasAll(ent) {
		b.netIDMap.Get(ent).Epoch = newEpoch
	}

	// Remove Replica marker — entity becomes a normal local entity.
	if b.replicaMap.HasAll(ent) {
		b.replicaMap.Remove(ent)
	}
	delete(b.replicaNetIDs, netID)

	// Drop netID from every per-source interest-set snapshot so a
	// future frame from any source cannot re-interpret this Live
	// entity as a replica that should be removed.
	for _, seen := range b.borderLastSeen {
		delete(seen, netID)
	}

	// Flip netIDIdx slot Replica → Live via the sanctioned Promote path.
	if res := b.netIDIdx.Promote(netID, ent); res.Action != ActionUpdated {
		return fmt.Errorf("PromoteReplicaToLive: netIDIdx.Promote returned action=%d for netID=%d",
			res.Action, netID)
	}

	b.eng.Log.Log(CatMeshTransfer,
		"[%s] promoted replica→live: netID=%d newEpoch=%d",
		b.cellID, netID, newEpoch)
	return nil
}

// SpawnLiveFromTransfer materializes a new Live entity from a
// TransferBlob. Used by the destination-side commit path when no
// pre-existing border replica exists for netID — the fast-mover case
// where the entity crossed faster than the border-frame cadence, or
// the cross-boundary-spawn case where the entity never existed on
// the destination at all. Epoch on the spawned entity is set from
// the Handoff payload.
func (b *Stage) SpawnLiveFromTransfer(netID uint32, epoch uint32, blob []byte) (ecs.Entity, error) {
	ent, _, err := b.SpawnFromTransferCore(blob, PresenceLive)
	if err != nil {
		return ecs.Entity{}, err
	}
	if b.netIDMap.HasAll(ent) {
		b.netIDMap.Get(ent).Epoch = epoch
	}
	b.eng.Log.Log(CatMeshTransfer,
		"[%s] spawned live from transfer: netID=%d epoch=%d",
		b.cellID, netID, epoch)
	return ent, nil
}

// ---------------------------------------------------------------------------
// Replication
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Border frame apply (new path, replaces ApplyProxySummaries)
// ---------------------------------------------------------------------------

// ApplyBorderFrame applies each entry in a decoded border frame, creating
// or updating a replica entity for the entity described. Stale epochs
// (frame entry epoch < highest seen epoch for that netID) are dropped
// silently.
//
// The frame's Entries list is also treated as the authoritative interest
// set for `sourceCellID` — any netID in b.borderLastSeen[sourceCellID]
// that is NOT in this frame is removed immediately. This replaces the
// old passive TTL-decay despawn path: a single subsequent frame (or
// even an empty frame) is enough to clean up replicas whose source
// entity left the push set on the previous tick. Self-healing after a
// dropped frame because the next frame's diff still sees the same
// "missing" netIDs.
//
// Wire format per DeltaBuf (see also pkg/universe/border_components.go):
//
//	[0:4]   worldX        float32 LE
//	[4:8]   worldY        float32 LE
//	[8:12]  radius        float32 LE
//	[12:14] qvx           int16 LE
//	[14:16] qvy           int16 LE
//	[16:18] qangle        uint16 LE — quantize.Angle(Rotation.Angle)
//	[18:26] producedAtMs  uint64 LE — authoritative producer's ClusterClock.TickTime (tick-aligned)
//	[26:]   component tail: [u16 count][repeated: u16 id, u16 len, N bytes]
func (b *Stage) ApplyBorderFrame(frame replication.Frame, sourceCellID MeshCellID) {
	cellSize := b.baseCellSize
	rootCell := b.cell
	for rootCell.Depth > 0 {
		rootCell = rootCell.Parent()
	}
	recvCellX := float32(rootCell.X) * cellSize
	recvCellY := float32(rootCell.Y) * cellSize

	currentSet := make(map[uint32]struct{}, len(frame.Entries))
	for _, entry := range frame.Entries {
		currentSet[entry.NetID.ID] = struct{}{}
		if len(entry.DeltaBuf) < 28 {
			continue
		}
		worldX := math.Float32frombits(binary.LittleEndian.Uint32(entry.DeltaBuf[0:4]))
		worldY := math.Float32frombits(binary.LittleEndian.Uint32(entry.DeltaBuf[4:8]))
		radius := math.Float32frombits(binary.LittleEndian.Uint32(entry.DeltaBuf[8:12]))
		qvx := int16(binary.LittleEndian.Uint16(entry.DeltaBuf[12:14]))
		qvy := int16(binary.LittleEndian.Uint16(entry.DeltaBuf[14:16]))
		qangle := binary.LittleEndian.Uint16(entry.DeltaBuf[16:18])
		producedAtMs := binary.LittleEndian.Uint64(entry.DeltaBuf[18:26])
		componentTail := entry.DeltaBuf[26:]
		vx := dequantizeVelI16(qvx, 2000)
		vy := dequantizeVelI16(qvy, 2000)
		angle := quantize.UnAngle(qangle)

		localX := worldX - recvCellX
		localY := worldY - recvCellY

		b.upsertBorderReplica(entry.NetID.ID, entry.NetID.Epoch, uint8(entry.Kind), localX, localY, radius, vx, vy, angle, sourceCellID, producedAtMs, componentTail)
	}

	// Diff against the previous snapshot from this source. Any netID we
	// saw last time but didn't see this time has dropped out of the
	// sender's push set and its replica must be removed — UNLESS some
	// other source is still pushing that netID to us.
	//
	// The multi-source guard catches the cell-crossing scenario: at a
	// hard-cut handoff, source A emits one final frame with the entity
	// (still Live in its PostSystems step 2, before the step 3 demote)
	// AND dest B emits its first frame with the entity (Live post-
	// promote). The next tick, A's frame no longer contains the netID
	// (it's now a Replica on A, filtered from the push set). Without
	// this guard, our receiver would evict the replica on the same tick
	// A's "gone" frame arrives — before B's "still here" frame is
	// integrated by our ReplicationSystem. The ReplicationSystem would
	// emit EXITED to subscribed clients; a tick later when B's frame
	// lands we re-upsert the netID and ReplicationSystem emits SPAWN.
	// Client renders that as an entity blinking out and back in just
	// past the cell boundary.
	//
	// By consulting the other sources' last-known push sets, we keep
	// the replica alive through the source-A-stops / source-B-continues
	// handoff instant. O(neighbors) per missing netID per frame.
	prev := b.borderLastSeen[sourceCellID]
	var removed int
	for netID := range prev {
		if _, stillThere := currentSet[netID]; stillThere {
			continue
		}
		if b.netIDStillPushedByOtherSource(netID, sourceCellID) {
			continue
		}
		b.RemoveReplicaByNetID(netID)
		removed++
	}
	b.borderLastSeen[sourceCellID] = currentSet
	if removed > 0 {
		b.eng.Log.Log(CatMeshReplica, "[%s] interest-set diff: removed %d netIDs from=%s kept=%d",
			b.cellID, removed, sourceCellID, len(currentSet))
	}
}

// netIDStillPushedByOtherSource reports whether any source cell other
// than excludeSource currently lists netID in its last-known push set.
// Used by ApplyBorderFrame's interest-set diff to keep a replica alive
// through a hard-cut handoff — source A's border frame at commitTick+1
// no longer contains the migrated entity, but dest B is pushing it
// instead, so the replica should remain.
func (b *Stage) netIDStillPushedByOtherSource(netID uint32, excludeSource MeshCellID) bool {
	for src, seen := range b.borderLastSeen {
		if src == excludeSource {
			continue
		}
		if _, ok := seen[netID]; ok {
			return true
		}
	}
	return false
}

// serialUint32After reports whether candidate follows current under RFC 1982
// serial-number arithmetic. The comparison is intentionally false for equal
// values and for the undefined half-range distance (1<<31).
func serialUint32After(candidate, current uint32) bool {
	delta := candidate - current
	return delta != 0 && delta < 1<<31
}

// upsertBorderReplica is the single entry point for creating or updating a
// replica entity from a border frame. Tracks the serial-latest epoch per netID
// so stale frames are dropped trivially. componentTail is the length-prefixed
// component-slice section of the wire entry (may be empty) and is applied via
// the ReplicationRegistry after fixed-field updates.
func (b *Stage) upsertBorderReplica(
	netID uint32, epoch uint32, kind uint8,
	localX, localY, radius, vx, vy, angle float32,
	sourceCellID MeshCellID,
	producedAtMs uint64,
	componentTail []byte,
) {
	// highestSeenEpoch is normally sufficient to reject stale frames, but
	// compare against the materialized component too so an existing replica
	// can never be rewound if bookkeeping was reconstructed independently.
	if ent, ok := b.replicaNetIDs[netID]; ok && b.eng.ECS.Alive(ent) && b.netIDMap.HasAll(ent) {
		materializedEpoch := b.netIDMap.Get(ent).Epoch
		if epoch != materializedEpoch && !serialUint32After(epoch, materializedEpoch) {
			return // stale
		}
	}
	if prev, ok := b.highestSeenEpoch[netID]; ok {
		if epoch != prev && !serialUint32After(epoch, prev) {
			return // stale
		}
	}
	b.highestSeenEpoch[netID] = epoch

	// A stale border frame from the previous authority can arrive on the
	// new authority at or just after a hard-cut handoff's commit tick —
	// the source's BorderDispatcher pushes at PostSystems step 2, the
	// demote fires at PostSystems step 3, and the in-flight push lands
	// on dest a tick or two later, AFTER dest has already promoted the
	// entity to Live. Materializing a Replica here would briefly put a
	// zombie entity into the ECS + spatial grid for this netID, and the
	// client's own AoI query would see BOTH its Live self-entity and
	// the Replica shadow, dedup by-netID picks whichever comes first,
	// and if the Replica wins the client renders itself as REPLICA for
	// a frame. Drop the stale frame instead.
	if b.netIDIdx != nil {
		if _, presence, ok := b.netIDIdx.Lookup(netID); ok && presence == PresenceLive {
			return
		}
	}

	if ent, ok := b.replicaNetIDs[netID]; ok && b.eng.ECS.Alive(ent) {
		// Update existing replica position, velocity, and rotation.
		if b.netIDMap.HasAll(ent) {
			nid := b.netIDMap.Get(ent)
			if serialUint32After(epoch, nid.Epoch) {
				nid.Epoch = epoch
			}
		}
		if b.posMap.HasAll(ent) {
			pos := b.posMap.Get(ent)
			pos.X = localX
			pos.Y = localY
		}
		if b.velMap.HasAll(ent) {
			vel := b.velMap.Get(ent)
			vel.X = vx
			vel.Y = vy
		}
		if b.rotMap.HasAll(ent) {
			b.rotMap.Get(ent).Angle = angle
		}
		// Refresh the collider radius from the frame. Radius is a per-tick
		// animatable field (the original WASM pulse demo breathed it), so a
		// replica whose radius froze at its creation value would render a stale
		// size — visible as a size jump when a viewer crosses the boundary and
		// the entity flips between live (breathing) and replica (frozen).
		// Mirrors the create branch, which seeds Collider from the same radius
		// arg.
		if b.colliderMap.HasAll(ent) {
			b.colliderMap.Get(ent).Radius = radius
		}
		if b.replicaMap.HasAll(ent) {
			rep := b.replicaMap.Get(ent)
			rep.TTL = 30
			rep.SourceCellID = string(sourceCellID)
			rep.ProducedAtMs = producedAtMs
		}
		// Apply updated per-component data so Health/Shield/etc. stay
		// in sync with the sender across the border.
		b.applyEntityComponents(ent, componentTail)
		return
	}

	// Create new replica entity.
	rootCell := b.cell
	for rootCell.Depth > 0 {
		rootCell = rootCell.Parent()
	}
	ent := b.replicaCreator.NewEntity(
		&component.Position{X: localX, Y: localY},
		&component.Velocity{X: vx, Y: vy},
		&component.Rotation{Angle: angle},
		&component.Collider{Radius: radius},
		&component.NetworkID{ID: netID, Epoch: epoch},
		&component.EntityKind{Type: kind},
	)
	b.cellMap.Add(ent, &component.CellCoord{CellX: rootCell.X, CellY: rootCell.Y})
	b.replicaMap.Add(ent, &component.Replica{
		SourceCellID: string(sourceCellID),
		SourceNetID:  netID,
		TTL:          30,
		ProducedAtMs: producedAtMs,
	})
	// Auto-fill all kind-registered components with zero values. The
	// border-frame component tail (below) fills in real data from the
	// sender for components the receiver recognizes; any that are not
	// sent or are unknown stay at their zero values. reflectBinding in
	// the local ReplicationSystem's AutoReplicator therefore always
	// finds its target component present, avoiding the
	// "required component missing on entity" panic.
	b.EnsureEntityKindComponents(ent)
	// Apply initial per-component data so Health/Shield/etc. match the
	// sender on the first frame, not just after the next update.
	b.applyEntityComponents(ent, componentTail)
	b.replicaNetIDs[netID] = ent
	b.eng.Log.Log(CatMeshReplica, "[%s] border replica created: netID=%d kind=%d from=%s pos=(%.0f,%.0f)",
		b.cellID, netID, kind, sourceCellID, localX, localY)
	if b.netIDIdx != nil {
		res := b.netIDIdx.Enter(netID, ent, PresenceReplica)
		if res.Action == ActionRejected {
			b.eng.Log.Log(CatMeshReplica,
				"[%s] replica ignored: netID=%d is already live here",
				b.cellID, netID)
			if b.strictNetIDIndex && b.eng.ECS.Alive(ent) {
				b.eng.RemoveEntityNowWithPolicy(ent, engine.RemovalLocalOnly)
			}
			return
		}
	}
}

// upsertBorderReplicaFromTransfer materializes a TransferFrame as a border
// replica on this Stage. Used at SPLIT/MERGE/MIGRATE populate time to seed
// cross-cell visibility before any tick runs, so the destination's first
// replication frame is complete (locals + context) and clients see no
// transition artifact. Routes through the unchanged upsertBorderReplica
// primitive; the only difference is the trigger (commit-time seed vs
// steady-state border tick).
func (b *Stage) upsertBorderReplicaFromTransfer(frame *TransferFrame, sourceCellID MeshCellID) {
	// TransferFrame positions are ALREADY base-cell-local (relative to the
	// entity's root cell origin) — SerializeEntityCore copies component.Position
	// verbatim and SpawnFromTransferCore writes frame.PosX/PosY straight into
	// Position with no conversion. upsertBorderReplica's localX/localY params
	// are in that same base-cell-local space, so pass them through unchanged.
	// (Do NOT subtract the root origin like ApplyBorderFrame does — that path
	// decodes WORLD-absolute wire bytes; this one already has local coords. The
	// bug it replaces was masked at root cell (0,0) where the offset is zero.)

	// TransferFrame carries no producedAtMs, so stamp the seed with the
	// destination cell's clock at populate time. In single-host all cells
	// share one ClusterClock so this stays monotonic with the client's
	// existing samples; the seed is overwritten by the first real border
	// frame from the owning neighbor within ~1 tick regardless.
	var producedAtMs uint64
	if b.clusterClock != nil {
		producedAtMs = b.clusterClock.TickTime(b.eng.TickIntervalMs())
	}

	// nil componentTail: the seed provides only core transform state
	// (position/velocity/rotation/collider). Game-specific component state
	// is filled by the first real border frame from the owning neighbor
	// ~1 tick later.
	b.upsertBorderReplica(
		frame.NetworkID, frame.Epoch, frame.EntityType,
		frame.PosX, frame.PosY, frame.Collider.Radius, frame.VelX, frame.VelY, frame.Rotation,
		sourceCellID, producedAtMs, nil,
	)
}

// collectBorderContextFor walks this stage's networked entities (locals +
// replicas, excluding Ghost/Dormant) and serializes the ones the caller's
// shouldInclude predicate accepts into BorderContextEntry blobs. Used at
// SPLIT/MERGE/MIGRATE serialize time to ship cross-cell visibility to the
// destination so its first replication frame is complete. maxCount > 0
// caps the result (overflow logged; remainder falls back to async border
// replication). The predicate receives per-entity replica info so callers
// don't need access to the private replicaMap.
//
// Must be called from the cell's game loop goroutine (it reads ECS).
func (b *Stage) collectBorderContextFor(
	maxCount int,
	shouldInclude func(e ecs.Entity, posX, posY float32, isReplica bool, replicaSource string) (include bool, sourceCellID string),
) ([]*meshpb.BorderContextEntry, error) {
	// Unlike the authoritative serializers, we do NOT exclude Replica here —
	// outer-neighbor replicas are exactly the cross-cell context we want to
	// ship. Ghost (proxy) and Dormant entities are never visibility context.
	filter := ecs.NewFilter1[component.Position](b.eng.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Dormant]())

	var result []*meshpb.BorderContextEntry
	query := filter.Query()
	// defer Close() so the world unlocks even if SerializeEntity panics on
	// game-specific Scan code (see serializeAllEntities for the full rationale).
	defer query.Close()
	for query.Next() {
		entity := query.Entity()
		pos := query.Get()

		isReplica := b.replicaMap.HasAll(entity)
		replicaSource := ""
		if isReplica {
			replicaSource = b.replicaMap.Get(entity).SourceCellID
		}

		include, sourceCellID := shouldInclude(entity, pos.X, pos.Y, isReplica, replicaSource)
		if !include {
			continue
		}

		if maxCount > 0 && len(result) >= maxCount {
			b.eng.Log.Log(CatMeshTransfer,
				"[%s] border context capped at maxCount=%d; remainder falls back to async border replication",
				b.cellID, maxCount)
			break
		}

		data, err := b.SerializeEntity(entity)
		if err != nil {
			return nil, fmt.Errorf("serialize border context entity: %w", err)
		}
		result = append(result, &meshpb.BorderContextEntry{
			Entity:       data,
			SourceCellId: sourceCellID,
		})
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Lifecycle management (ghost, replica, cooldown TTLs)
// ---------------------------------------------------------------------------

// ExpireReplicas is the fallback despawn path for replicas whose source
// cell has gone silent (shut down, crashed, or lost its network route).
// The primary despawn path is ApplyBorderFrame's interest-set diff,
// which removes replicas immediately when the sender reports they've
// left the push set. ExpireReplicas catches the case where no further
// frames ever arrive at all — the diff never runs, so TTL is the only
// signal that the source is gone. Decrements every tick, removes at
// TTL <= 0 (~1.5s at 20Hz).
func (b *Stage) ExpireReplicas() {
	filter := ecs.NewFilter1[component.Replica](b.eng.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		rep := query.Get()
		rep.TTL--
		if rep.TTL <= 0 {
			expired = append(expired, query.Entity())
		}
	}
	if len(expired) > 0 {
		b.eng.Log.Log(CatMeshReplica, "[%s] replicas expired via TTL fallback: count=%d", b.cellID, len(expired))
	}
	for _, e := range expired {
		if b.eng.ECS.Alive(e) {
			b.eng.MarkForRemovalWithPolicy(e, engine.RemovalLocalOnly)
		}
	}
}

func (b *Stage) RemoveReplicaByNetID(netID uint32) {
	if e, ok := b.replicaNetIDs[netID]; ok {
		b.eng.Log.Log(CatMeshReplica, "[%s] replica removed: netID=%d", b.cellID, netID)
		if b.eng.ECS.Alive(e) {
			b.eng.RemoveEntityNowWithPolicy(e, engine.RemovalLocalOnly)
		} else {
			// Heal stale bookkeeping even if another path removed the ECS row
			// without going through the engine primitive.
			if tracked, exists := b.replicaNetIDs[netID]; exists && tracked == e {
				delete(b.replicaNetIDs, netID)
			}
			if b.netIDIdx != nil {
				b.netIDIdx.ExitEntity(netID, e)
			}
		}
	} else if b.netIDIdx != nil {
		// replicaNetIDs is the fast path, but recover a replica slot if its
		// companion map entry was lost. Never touch a Live slot here.
		if e, presence, ok := b.netIDIdx.Lookup(netID); ok && presence == PresenceReplica {
			if b.eng.ECS.Alive(e) {
				b.eng.RemoveEntityNowWithPolicy(e, engine.RemovalLocalOnly)
			} else {
				b.netIDIdx.ExitEntity(netID, e)
			}
		}
	}
	// Always drop the netID from every per-source snapshot, even if the
	// replica entity was already gone. Called both from ApplyBorderFrame
	// during interest-set diffs and from handoff teardown — both need
	// to forget this netID so the next diff tick doesn't see it as
	// "missing" and re-issue a RemoveReplicaByNetID for a ghost.
	b.forgetBorderNetID(netID)
}

// VelScale returns the max velocity scale used for qvel quantization.
func (b *Stage) VelScale() float32 { return b.velScale }

// SetVelScale sets the max velocity scale for qvel quantization.
func (b *Stage) SetVelScale(scale float32) { b.velScale = scale }

// ---------------------------------------------------------------------------
// Dormancy system (sleep until player proximity)
// ---------------------------------------------------------------------------

// WakeDormantEntities checks all dormant entities against the spatial grid for
// nearby players (local or proxy). If any player entity or player proxy is within
// wakeRadius, the Dormant component is removed and the entity becomes active.
func (b *Stage) WakeDormantEntities(wakeRadius float32) {
	if b.spatialGrid == nil {
		return
	}

	filter := ecs.NewFilter2[component.Dormant, component.Position](b.eng.ECS)
	var toWake []ecs.Entity
	var results []spatial.Entry

	query := filter.Query()
	for query.Next() {
		_, pos := query.Get()
		entity := query.Entity()

		// Query spatial grid for nearby entities
		results = b.spatialGrid.QueryRadius(pos.X, pos.Y, wakeRadius, results[:0])
		for _, entry := range results {
			if !b.eng.ECS.Alive(entry.Entity) {
				continue
			}
			// Check if this is a player entity (has PlayerConn)
			if b.playerMap.HasAll(entry.Entity) {
				toWake = append(toWake, entity)
				break
			}
		}
	}

	for _, e := range toWake {
		if b.eng.ECS.Alive(e) && b.dormantMap.HasAll(e) {
			b.dormantMap.Remove(e)
			var netID uint32
			if b.netIDMap.HasAll(e) {
				netID = b.netIDMap.Get(e).ID
			}
			b.eng.Log.Log(CatMeshDormancy, "[%s] dormant entity woke: netID=%d", b.cellID, netID)
		}
	}
}

// TickGhosts removes any entity tagged with the Ghost marker. Ghost is a
// pure marker (no TTL, no confirmation state) — a caller tags an entity
// Ghost immediately before an authority flip, and the next TickGhosts pass
// cleans it up. One tick of visibility is sufficient because the destination
// cell's replica has already spawned by then.
func (b *Stage) TickGhosts() {
	filter := ecs.NewFilter1[component.Ghost](b.eng.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		expired = append(expired, query.Entity())
	}
	for _, e := range expired {
		if b.eng.ECS.Alive(e) {
			b.eng.MarkForRemovalWithPolicy(e, engine.RemovalLocalOnly)
		}
	}
}

func (b *Stage) TickTransferCooldowns() {
	filter := ecs.NewFilter1[component.TransferCooldown](b.eng.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		tc := query.Get()
		tc.Remaining--
		if tc.Remaining <= 0 {
			expired = append(expired, query.Entity())
		}
	}
	for _, e := range expired {
		if b.eng.ECS.Alive(e) {
			b.cooldownMap.Remove(e)
		}
	}
}

// ---------------------------------------------------------------------------
// Convenience spawn
// ---------------------------------------------------------------------------

// SpawnAtLocation spawns an entity at the given world-space Location.
//
// The Location must fall within this cell's world bounds; callers at the
// gateway already enforce that via CellAtPosition, so this is a correctness
// invariant, not user-facing validation. Out-of-bounds calls log under
// CatInvariant, append a commit-log violation, panic under InvariantPanic,
// or (under InvariantOff/InvariantLog) clamp and continue.
//
// Spawns a bare entity carrying only Position. Games that need additional
// components (Rotation, EntityKind, etc.) should call Stage.Spawn directly
// with the desired component set; SpawnAtLocation is a low-level convenience
// for tests and bounds-validated placement.
func (b *Stage) SpawnAtLocation(loc coords.Location) ecs.Entity {
	rootCell := b.rootCell()
	cellSize := b.baseCellSize
	minX := float32(rootCell.X) * cellSize
	minY := float32(rootCell.Y) * cellSize
	maxX := minX + cellSize
	maxY := minY + cellSize

	if loc.X < minX || loc.X >= maxX || loc.Y < minY || loc.Y >= maxY {
		msg := fmt.Sprintf(
			"SpawnAtLocation called with out-of-bounds Location: "+
				"loc=(%f,%f) cell=%s bounds=[%f,%f)×[%f,%f)",
			loc.X, loc.Y, b.cellID, minX, maxX, minY, maxY)
		b.eng.Log.Log(CatInvariant, "%s", msg)
		if b.coord != nil && b.coord.commitLog != nil {
			b.coord.commitLog.Append(CommitEvent{
				Kind:    EventInvariantViolation,
				Step:    "spawn-at-location-out-of-bounds",
				Success: false,
				Error:   msg,
			})
		}
		if b.coord != nil && b.coord.invariantMode == InvariantPanic {
			panic(msg)
		}
		if loc.X < minX {
			loc.X = minX
		} else if loc.X >= maxX {
			loc.X = maxX - 1
		}
		if loc.Y < minY {
			loc.Y = minY
		} else if loc.Y >= maxY {
			loc.Y = maxY - 1
		}
	}

	pos := component.Position{X: loc.X - minX, Y: loc.Y - minY}
	return b.Spawn(pos).Handle()
}

// SpawnPlayer is the canonical player-spawn helper. It performs the
// universal steps every game's OnEnter handler needs:
//
//  1. Translate session.SpawnLocation (world coords) to cell-local Position.
//  2. Call Spawn(components...) with the resolved Position + PlayerConn
//     + caller-supplied components.
//  3. Assign session.Entity = e.Handle().
//  4. Call SendPlayerEntityAssigned(session.ConnID, e.Handle()) to notify the client.
//
// When session.ConnID is 0 (no active client connection), SendPlayerEntityAssigned
// is a safe no-op — ConnManager.Send ignores unknown connection IDs.
//
// Caller-supplied components must NOT include Position (injected from
// session.SpawnLocation) or PlayerConn (injected from session.ConnID).
// Spawn panics on duplicate-type.
func (b *Stage) SpawnPlayer(session *engine.PlayerSession, components ...any) Entity {
	rootCell := b.rootCell()
	cellSize := b.baseCellSize
	minX := float32(rootCell.X) * cellSize
	minY := float32(rootCell.Y) * cellSize

	// NOTE: out-of-cell-bounds check is intentionally NOT replicated here.
	// Task 18 of the spawn-API plan rewrites SpawnAtLocation; that work
	// owns moving the bounds-validation logic into Spawn or keeping it
	// only on SpawnAtLocation. For now, callers must ensure SpawnLocation
	// falls within this cell — production already does (gateway routes
	// players to the cell that owns their spawn position).
	pos := component.Position{
		X: session.SpawnLocation.X - minX,
		Y: session.SpawnLocation.Y - minY,
	}

	args := make([]any, 0, len(components)+2)
	args = append(args, pos, component.PlayerConn{ConnID: session.ConnID})
	args = append(args, components...)

	e := b.Spawn(args...)
	session.Entity = e.Handle()
	b.SendPlayerEntityAssigned(session.ConnID, e.Handle())
	return e
}

// Init is a no-op default. Override in your game world for custom initialization.
func (b *Stage) Init() {}

// StateByName returns the per-stage state value associated with name.
// Internal API; game code uses mmokit.State[T].
func (b *Stage) StateByName(name string) (any, bool) {
	v, ok := b.state[name]
	return v, ok
}

// SetStateByName installs a per-stage state value under name. Internal API
// for tests and code paths that bypass the Process-level AddState factory
// (e.g. unit-test cell construction). Production code should register state
// via mmokit.AddState[T] on the Process before Build().
func (b *Stage) SetStateByName(name string, v any) {
	if b.state == nil {
		b.state = make(map[string]any, 1)
	}
	b.state[name] = v
}

// Dispatcher returns the per-Stage MessageDispatcher, lazily initialized.
// Used by mmokit.Handle and mmokit.Send to route typed messages.
func (s *Stage) Dispatcher() *MessageDispatcher {
	if s.dispatcher == nil {
		s.dispatcher = newMessageDispatcher(s)
	}
	return s.dispatcher
}

// BroadcastQueue returns this stage's per-tick auto-broadcast queue.
// Producers (Stage.maybeBroadcast) push BroadcastEvents; the framework's
// network/replication phase drains and AoI-filters per viewer at end-of-tick.
func (s *Stage) BroadcastQueue() *BroadcastQueue { return s.broadcastQueue }

// RouteTypedMessage delivers a typed message to the entity with targetNetID.
// If the entity is local Live on this stage, the registered handler runs
// synchronously. If the entity is a Replica, the message is wire-encoded and
// shipped to the replica's source cell via the bridge; the handler runs there
// when the frame arrives.
//
// Returns true if the message was dispatched (locally or remotely), false
// if the entity is not known to this stage.
func (s *Stage) RouteTypedMessage(targetNetID uint32, msgPtr any) bool {
	h, presence, ok := s.LookupNetID(targetNetID)
	if !ok {
		return false
	}
	if presence == PresenceLive {
		s.Dispatcher().Invoke(targetNetID, msgPtr)
		// Same-cell post-handler push: handler may have populated result
		// fields (e.g. Damage.Dealt), so we encode AFTER Invoke.
		s.maybeBroadcast(targetNetID, msgPtr)
		return true
	}
	// Replica — route to source cell.
	if !s.replicaMap.HasAll(h) {
		return false
	}
	rep := s.replicaMap.Get(h)
	typeName := reflect.TypeOf(msgPtr).Elem().String()
	payload, err := EncodeTypedMessage(typeName, msgPtr)
	if err != nil {
		s.eng.Log.Log(CatMeshAction,
			"[%s] RouteTypedMessage: encoding %q for netID=%d failed: %v",
			s.cellID, typeName, rep.SourceNetID, err)
		return false
	}
	if s.bridge == nil {
		return false
	}
	if _, isNoop := s.bridge.(NoopBridge); isNoop {
		s.eng.Log.Log(CatMeshAction,
			"[%s] RouteTypedMessage: dropping cross-cell %q to netID=%d (NoopBridge — no transport wired)",
			s.cellID, typeName, rep.SourceNetID)
		return false
	}
	s.bridge.SendAction(MeshCellID(rep.SourceCellID), &CrossCellAction{
		Type:         ActionTypedMessage,
		TargetNetID:  rep.SourceNetID,
		SourceCellID: string(s.cellID),
		Payload:      payload,
	})
	// Cross-cell source-cell pre-handler push: viewers near the caster on
	// this cell see the broadcast even though the authoritative handler
	// runs on the dest cell. Result fields (e.g. Dealt) carry their pre-
	// handler values here — that's correct for the caster-local AoI.
	s.maybeBroadcast(targetNetID, msgPtr)
	return true
}

// HandleEngineAction processes engine-level cross-cell actions (currently
// just ActionTypedMessage). Returns true if the action was consumed; the
// caller falls back to game-defined handling if false.
func (s *Stage) HandleEngineAction(action *CrossCellAction) bool {
	if action == nil || action.Type != ActionTypedMessage {
		return false
	}
	typeName, payload := SplitTypedMessage(action.Payload)
	msgType := s.Dispatcher().MessageType(typeName)
	if msgType == nil {
		// No registered handler — drop. Logged by caller for visibility.
		return true
	}
	msgPtr := reflect.New(msgType)
	// Stage-aware decode so Entity fields resolve their handles via this
	// stage's NetID index — without it Entity.Send / Entity.Get etc. on
	// decoded fields would no-op.
	if err := DecodeTypedMessageOnStage(s, payload, msgPtr.Interface()); err != nil {
		// Consumed (return true) but not dispatched: the action WAS an
		// ActionTypedMessage, so handing it to the game's fallback would only
		// make it decode the same refused payload again.
		s.eng.Log.Log(CatMeshAction,
			"[%s] HandleEngineAction: dropping cross-cell %q to netID=%d — %v",
			s.cellID, typeName, action.TargetNetID, err)
		return true
	}
	s.Dispatcher().Invoke(action.TargetNetID, msgPtr.Interface())
	// Dest-cell post-handler push: the handler ran here authoritatively,
	// result fields are populated, viewers near the target on this cell
	// see the broadcast.
	s.maybeBroadcast(action.TargetNetID, msgPtr.Interface())
	return true
}

// maybeBroadcast pushes msgPtr to this stage's broadcast queue if:
//
//  1. The msg type is broadcast-eligible (registered via mmokit.HandleAll
//     or RegisterBroadcastType; HandleAllInternal types are excluded).
//  2. msgPtr has at least one anchor whose NetID resolves on this stage
//     (avoids zero-recipient broadcasts on stages where no anchor entity
//     is locally known — viewers on this cell can't see anything).
//
// Called twice for cross-cell sends (source pre-handler + dest post-handler);
// once for same-cell sends.
func (s *Stage) maybeBroadcast(targetNetID uint32, msgPtr any) {
	t := reflect.TypeOf(msgPtr)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if !s.Wire().BroadcastEligible(t) {
		return
	}

	anchors := ExtractAnchors(msgPtr, EntityByNetID(s, targetNetID))
	// Filter anchors to those resolvable on this stage. Replicas count —
	// the AoI filter at drain time uses the local entity's position, which
	// is whatever this cell knows for that NetID.
	var localAnchors []uint32
	for _, nid := range anchors {
		if _, _, ok := s.LookupNetID(nid); ok {
			localAnchors = append(localAnchors, nid)
		}
	}
	if len(localAnchors) == 0 {
		// No locally-resolvable anchors → no recipients on this stage.
		// Skip the push and avoid a no-op broadcast.
		return
	}

	body, err := ReflectMarshal(msgPtr)
	if err != nil {
		s.eng.Log.Log(CatMeshAction,
			"[%s] broadcast %s dropped: %v", s.cellID, t.String(), err)
		return
	}
	s.broadcastQueue.Push(BroadcastEvent{
		TypeID:  TypeIDOf(t),
		Body:    body,
		Anchors: localAnchors,
	})
}
