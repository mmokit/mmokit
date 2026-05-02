package universe

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
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
}

// StartupCategories are always enabled so server lifecycle is visible.
var StartupCategories = []string{CatMeshCell, CatEngineLoop, CatNetConn}

// CrossingEvent records that an entity has crossed a cell boundary
// and needs to be handed off. The HandoffDriver reads and drains
// this queue in PostSystems.
type CrossingEvent struct {
	Entity         ecs.Entity
	NetID          uint32
	ConnID         uint32 // non-zero for player entities
	Username       string // non-empty for player entities
	DestCellID     string // cell ID string the entity crossed into
	BypassCooldown bool   // true for explicit teleports; false for natural boundary crossings
}

// SpawnOption configures optional components when spawning an entity via Stage.SpawnEntity.
type SpawnOption func(*spawnOpts)

type spawnOpts struct {
	velX, velY  float32
	rotation    float32
	collider    component.Collider
	entityType  uint8
	hasVel      bool
	hasRot      bool
	hasCollider bool
	hasKind     bool
	noSpatial   bool
	postInit    []func(*ecs.World, ecs.Entity) // user-provided post-spawn callbacks
}

// WithVelocity sets the entity's velocity.
func WithVelocity(vx, vy float32) SpawnOption {
	return func(o *spawnOpts) {
		o.velX, o.velY = vx, vy
		o.hasVel = true
	}
}

// WithCollider sets the entity's collision radius.
func WithCollider(radius float32) SpawnOption {
	return func(o *spawnOpts) {
		o.collider = component.Collider{Radius: radius}
		o.hasCollider = true
	}
}

// WithEntityKind sets the entity's type identifier.
func WithEntityKind(t uint8) SpawnOption {
	return func(o *spawnOpts) {
		o.entityType = t
		o.hasKind = true
	}
}

// WithRotation sets the entity's rotation angle in radians.
func WithRotation(angle float32) SpawnOption {
	return func(o *spawnOpts) {
		o.rotation = angle
		o.hasRot = true
	}
}

// WithFacing sets the entity's facing angle from a Location.Facing value.
// Equivalent to WithRotation — provided as a dedicated option so the
// intent ("apply the destination's facing") is obvious at the call site
// and so a future teleport API can reuse it without semantic drift.
func WithFacing(radians float32) SpawnOption {
	return func(o *spawnOpts) {
		o.rotation = radians
		o.hasRot = true
	}
}

// WithoutSpatial prevents SpawnEntity from auto-registering the entity with the
// spatial hash grid. By default, entities with a collider are registered automatically.
func WithoutSpatial() SpawnOption {
	return func(o *spawnOpts) { o.noSpatial = true }
}

// WithPostInit registers a callback to run after all kind components are
// attached but before SpawnEntity returns. mmokit.Init(fn) uses this to
// populate typed component bundles via reflection.
//
// Internal API; game code uses mmokit.Init(fn).
func WithPostInit(fn func(*ecs.World, ecs.Entity)) SpawnOption {
	return func(o *spawnOpts) {
		o.postInit = append(o.postInit, fn)
	}
}

// WithComponents is a no-op as of the auto-attach refactor. WithEntityKind(K)
// now auto-attaches the kind's components automatically. Kept temporarily so
// existing callers compile; will be deleted after that migration.
func WithComponents() SpawnOption {
	return func(*spawnOpts) {}
}

// Stage provides default implementations for all GameWorld interface methods.
// Embed it in your game world struct to get working multi-node support out of the box.
//
// Usage:
//
//	type myWorld struct {
//	    universe.Stage
//	}
//
// All methods can be overridden by defining them on the outer struct.
type Stage struct {
	eng         *engine.Engine
	cell        CellID
	cellID      string
	aoiRadius   float32
	bridge      Bridge
	spatialGrid *spatial.HashGrid

	coord     *Process // set by Process.createNode after world factory
	fromSplit bool         // true if created during a cell split (skip initial entity spawning)

	// clusterClock stamps outbound border-frame entries with the
	// authoritative producer's cluster-coherent wall time. Threaded from
	// Process.ClusterClock at cell construction; tests that build a
	// Stage without a Process get a fresh pre-observed clock so
	// Now() falls back to the local wall clock rather than panicking.
	clusterClock *ClusterClock

	replicaNetIDs    map[uint32]ecs.Entity
	highestSeenEpoch map[uint32]uint32 // per-netID: highest epoch seen from border frames

	// borderLastSeen is the per-source-cell snapshot of the netIDs we
	// last received from that source in a MsgBorderFrame. ApplyBorderFrame
	// diffs the incoming frame's netID set against this snapshot and
	// removes replicas for any netID that dropped out — the explicit
	// despawn signal that replaces the old passive TTL-decay path.
	// Keyed on fromCellID (the source cell's string ID).
	borderLastSeen map[string]map[uint32]struct{}
	replRegistry     *ReplicationRegistry
	velScale         float32 // max velocity for qvel quantization

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
}

// NewStage creates a Stage for use within a world factory.
// Typically called by the Process; games that need manual setup can call this directly.
func NewStage(eng *engine.Engine, cell CellID, aoiRadius float32, replRegistry *ReplicationRegistry) *Stage {
	w := eng.ECS
	if replRegistry == nil {
		replRegistry = NewReplicationRegistry()
	}

	cellID := string(cell.MeshID())

	// Default to a fresh, pre-observed clock so WorldBases built outside
	// a Process (tests, stand-alone benchmarks) have a working Now().
	// Production paths overwrite this via base.clusterClock = c.ClusterClock
	// in Process.createNode immediately after NewStage.
	defaultClock := NewClusterClock()
	defaultClock.Observe(uint64(time.Now().UnixMilli()), 0)

	base := Stage{
		eng:              eng,
		cell:             cell,
		cellID:           cellID,
		aoiRadius:        aoiRadius,
		bridge:           NoopBridge{},
		clusterClock:     defaultClock,
		replicaNetIDs:    make(map[uint32]ecs.Entity),
		highestSeenEpoch: make(map[uint32]uint32),
		borderLastSeen:   make(map[string]map[uint32]struct{}),
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
	}

	base.netIDIdx = newNetIDIndex()

	// Register all framework log categories.
	eng.Log.RegisterCategories(MeshCategories...)

	// Wire GetNetID so the engine can track removed network IDs
	eng.GetNetID = func(e ecs.Entity) (uint32, bool) {
		if base.netIDMap.HasAll(e) {
			return base.netIDMap.Get(e).ID, true
		}
		return 0, false
	}

	return &base
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

// Bridge returns the bridge for inter-cell communication.
func (b *Stage) Bridge() Bridge { return b.bridge }

// Cell returns this node's cell coordinates.
func (b *Stage) Cell() CellID { return b.cell }

// Process returns the coordinator that owns this node, or nil in single-node mode.
func (b *Stage) Process() *Process { return b.coord }

// Protocol returns the user-supplied Config.Protocol via the owning Process.
// Game code retrieves the typed *mmokit.Protocol via mmokit.ProtocolOf.
func (b *Stage) Protocol() any {
	if b.coord == nil {
		return nil
	}
	return b.coord.Protocol()
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

// CellSize returns the base cell size. Entities always use base-cell coordinates
// regardless of quadtree depth, so this always returns coords.CellSize.
func (b *Stage) CellSize() float32 { return coords.CellSize }

// CellID returns this cell.s unique identifier (e.g., "cell_0_0").
func (b *Stage) CellID() string { return b.cellID }

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

// SendSpawnedMsg sends the framework-level SpawnedMsg to a client, informing it
// of its entity NetID and world position. Uses the node's root cell coordinates.
func (b *Stage) SendSpawnedMsg(connID uint32, entity ecs.Entity) {
	netID := uint32(0)
	if b.netIDMap.HasAll(entity) {
		netID = b.netIDMap.Get(entity).ID
	}
	cell := b.rootCell()
	cs := coords.CellSize
	var worldX, worldY float32
	if b.posMap.HasAll(entity) {
		pos := b.posMap.Get(entity)
		worldX = pos.X + float32(cell.X)*cs
		worldY = pos.Y + float32(cell.Y)*cs
	}
	msg := &enginepb.SpawnedMsg{
		EntityNetId: netID,
		WorldX:      worldX,
		WorldY:      worldY,
	}
	frame := makeEventFrame(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), msg)
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
		return 0, 0, coords.CellSize
	}
	return b.coord.cfg.CellsX, b.coord.cfg.CellsY, coords.CellSize
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
// GameWorld interface — default implementations
// ---------------------------------------------------------------------------

func (b *Stage) ECSWorld() *ecs.World                                 { return b.eng.ECS }
func (b *Stage) GetAoIRadius() float32                                { return b.aoiRadius }
func (b *Stage) SetBridge(bridge Bridge)                              { b.bridge = bridge }
func (b *Stage) MarkForRemoval(e ecs.Entity)                          { b.eng.MarkForRemoval(e) }
func (b *Stage) Hooks() engine.Hooks                                  { return engine.Hooks{} }
func (b *Stage) Shutdown()                                            {}

// SendEvent encodes a server→client event using the Process's typed
// ServerEvents registry and dispatches it on the engine ConnMgr. Replaces
// the gw.ServerEvents().Send(gw.Engine().ConnMgr, connID, code, msg) chain.
// Panics if the code is not registered or msg type does not match — the
// registry's existing safety checks bubble up unchanged.
func (b *Stage) SendEvent(connID uint32, code uint32, msg interface{ Reset() }) {
	if worldBaseSendEvent == nil {
		return
	}
	worldBaseSendEvent(b, connID, code, msg)
}

// worldBaseSendEvent is set by pkg/mmokit's init() so Stage can dispatch
// without importing mmokit. The same function-variable indirection pattern
// the codebase uses to expose Process.cfg.Protocol as an `any` field.
var worldBaseSendEvent func(b *Stage, connID uint32, code uint32, msg interface{ Reset() })

// SetWorldBaseSendEvent wires the mmokit-side dispatcher. Called from
// pkg/mmokit's init().
func SetWorldBaseSendEvent(fn func(*Stage, uint32, uint32, interface{ Reset() })) {
	worldBaseSendEvent = fn
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
	b.cellID = string(cell.MeshID())

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
func (b *Stage) DispatchChat(string, string)                          {}
func (b *Stage) HandleCrossCellAction(*CrossCellAction) *ActionResult { return nil }
func (b *Stage) HandleActionResult(*ActionResult)                     {}

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
				f.DebugFlags = uint32(s.DebugFlags)
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
	if b.replRegistry != nil {
		for _, cs := range frame.Components {
			if rep := b.replRegistry.Get(cs.ID); rep != nil {
				if rep.IsTransferCore {
					continue
				}
				if rep.Add != nil {
					rep.Add(entity, cs.Data)
				} else if rep.Apply != nil {
					rep.Apply(entity, cs.Data)
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

	// Player-specific hook
	if frame.ConnID != 0 && b.onPlayerTransferReceived != nil {
		b.onPlayerTransferReceived(entity, frame)
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
				b.eng.ECS.RemoveEntity(entity)
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
				b.eng.ECS.RemoveEntity(entity)
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
func (b *Stage) ApplyBorderFrame(frame replication.Frame, sourceCellID string) {
	cellSize := coords.CellSize
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
func (b *Stage) netIDStillPushedByOtherSource(netID uint32, excludeSource string) bool {
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

// upsertBorderReplica is the single entry point for creating or updating a
// replica entity from a border frame. Tracks the highest-seen epoch per netID
// so stale frames are dropped trivially. componentTail is the length-prefixed
// component-slice section of the wire entry (may be empty) and is applied via
// the ReplicationRegistry after fixed-field updates.
func (b *Stage) upsertBorderReplica(
	netID uint32, epoch uint32, kind uint8,
	localX, localY, radius, vx, vy, angle float32,
	sourceCellID string,
	producedAtMs uint64,
	componentTail []byte,
) {
	if prev, ok := b.highestSeenEpoch[netID]; ok && epoch < prev {
		return // stale
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
		if b.replicaMap.HasAll(ent) {
			rep := b.replicaMap.Get(ent)
			rep.TTL = 30
			rep.SourceCellID = sourceCellID
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
		SourceCellID: sourceCellID,
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
				b.eng.ECS.RemoveEntity(ent)
			}
			return
		}
	}
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
			if b.replicaMap.HasAll(e) {
				netID := b.replicaMap.Get(e).SourceNetID
				delete(b.replicaNetIDs, netID)
				// Also drop the netID from every per-source interest-set
				// snapshot. Keeps borderLastSeen bounded when an orphaned
				// source is cleaned up via TTL, so a later "source came
				// back" case doesn't see phantom removals from a stale
				// snapshot.
				for _, seen := range b.borderLastSeen {
					delete(seen, netID)
				}
			}
			b.eng.MarkForRemoval(e)
		}
	}
}

func (b *Stage) RemoveReplicaByNetID(netID uint32) {
	if e, ok := b.replicaNetIDs[netID]; ok {
		b.eng.Log.Log(CatMeshReplica, "[%s] replica removed: netID=%d", b.cellID, netID)
		if b.eng.ECS.Alive(e) {
			b.eng.ECS.RemoveEntity(e)
		}
		delete(b.replicaNetIDs, netID)
		// ECS.RemoveEntity here is synchronous and bypasses
		// engine.FlushRemovals — so the engine's OnEntityRemoved hook
		// (which normally Exits the netIDIdx) does NOT fire. Exit
		// explicitly so a subsequent SpawnFromTransferCore(Live) for
		// this netID finds an empty slot (ActionInstalled) instead of
		// colliding with the stale Replica slot (ActionRejected).
		if b.netIDIdx != nil {
			b.netIDIdx.Exit(netID)
		}
	}
	// Always drop the netID from every per-source snapshot, even if the
	// replica entity was already gone. Called both from ApplyBorderFrame
	// during interest-set diffs and from handoff teardown — both need
	// to forget this netID so the next diff tick doesn't see it as
	// "missing" and re-issue a RemoveReplicaByNetID for a ghost.
	for _, seen := range b.borderLastSeen {
		delete(seen, netID)
	}
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
			b.eng.MarkForRemoval(e)
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

// SpawnEntity creates a new entity with Position, Velocity, NetworkID,
// EntityKind, Collider, and CellCoord. Use SpawnOptions to configure
// velocity, collider, entity kind, and rotation.
func (b *Stage) SpawnEntity(pos component.Position, opts ...SpawnOption) ecs.Entity {
	o := spawnOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	vel := component.Velocity{}
	if o.hasVel {
		vel.X, vel.Y = o.velX, o.velY
	}

	collider := component.Collider{Radius: 10}
	if o.hasCollider {
		collider = o.collider
	}

	kind := component.EntityKind{Type: 1}
	if o.hasKind {
		kind.Type = o.entityType
	}

	nid := b.eng.NextNetID()
	entity := b.spawner.NewEntity(
		&pos,
		&vel,
		&component.NetworkID{ID: nid},
		&kind,
		&collider,
		&component.CellCoord{CellX: b.rootCell().X, CellY: b.rootCell().Y},
	)

	if o.hasRot {
		b.rotMap.Add(entity, &component.Rotation{Angle: o.rotation})
	}

	// Auto-register with spatial grid if entity has a collider.
	if !o.noSpatial && o.hasCollider && b.spatialGrid != nil {
		b.spatialGrid.Register(spatial.Entry{
			Entity: entity,
			X:      pos.X,
			Y:      pos.Y,
			Radius: collider.Radius,
		})
	}

	// Auto-add registered components for this entity kind.
	if o.hasKind {
		b.EnsureEntityKindComponents(entity)
	}

	// Run post-init callbacks after all kind components are attached.
	for _, fn := range o.postInit {
		fn(b.ECSWorld(), entity)
	}

	if b.netIDIdx != nil && nid != 0 {
		res := b.netIDIdx.Enter(nid, entity, PresenceLive)
		switch res.Action {
		case ActionInstalled, ActionUpdated:
			// Normal install; no rollback.
		case ActionDuplicate:
			b.eng.Log.Log(CatMeshCell,
				"[%s] duplicate live spawn blocked: netID=%d", b.cellID, nid)
			if b.strictNetIDIndex {
				b.eng.ECS.RemoveEntity(entity)
				return ecs.Entity{}
			}
		case ActionRejected:
			// Local live spawns shouldn't conflict with existing Replica
			// under normal operation; if they do, strict mode rolls back.
			// The sanctioned Replica→Live path is PromoteReplicaToLive.
			if b.strictNetIDIndex && b.eng.ECS.Alive(entity) {
				b.eng.ECS.RemoveEntity(entity)
				return ecs.Entity{}
			}
		}
	}

	b.eng.Log.Log(CatMeshCell, "[%s] spawned entity netID=%d at (%.0f,%.0f)", b.cellID, nid, pos.X, pos.Y)
	return entity
}

// SpawnAtLocation spawns an entity at the given world-space Location.
//
// The Location must fall within this cell's world bounds; callers at the
// gateway already enforce that via CellAtPosition, so this is a correctness
// invariant, not user-facing validation. Out-of-bounds calls log under
// CatInvariant, append a commit-log violation, panic under InvariantPanic,
// or (under InvariantOff/InvariantLog) clamp and continue.
//
// Facing is NOT auto-applied — pass WithFacing(loc.Facing) if the game
// uses rotation.
func (b *Stage) SpawnAtLocation(loc coords.Location, opts ...SpawnOption) ecs.Entity {
	rootCell := b.rootCell()
	cellSize := coords.CellSize
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
	return b.SpawnEntity(pos, opts...)
}

// SpawnPlayer is the canonical player-spawn helper. It performs four universal
// steps that every game's OnEnter handler needs:
//
//  1. Call SpawnAtLocation(session.SpawnLocation, opts...) to create the entity.
//  2. Attach component.PlayerConn{ConnID: session.ConnID} to the entity.
//  3. Assign session.Entity = e.
//  4. Call SendSpawnedMsg(session.ConnID, e) to notify the client.
//
// When session.ConnID is 0 (no active client connection, e.g. a transferred
// session before reconnect), SendSpawnedMsg is a safe no-op — ConnManager.Send
// ignores unknown connection IDs.
//
// Per-game setup (name, game-specific components) belongs in a mmokit.Init(fn)
// SpawnOption passed via opts; SpawnPlayer has no game-specific knowledge.
func (b *Stage) SpawnPlayer(session *engine.PlayerSession, opts ...SpawnOption) ecs.Entity {
	e := b.SpawnAtLocation(session.SpawnLocation, opts...)

	pcMap := ecs.NewMap1[component.PlayerConn](b.ECSWorld())
	pcMap.Add(e, &component.PlayerConn{ConnID: session.ConnID})

	session.Entity = e
	b.SendSpawnedMsg(session.ConnID, e)
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
