package universe

import (
	"encoding/binary"
	"math"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// Framework-level log categories for the server meshing subsystem.
// These are registered automatically on WorldBase initialization.
const (
	CatMeshTransfer = "mesh:transfer" // entity transfer send/receive/confirm
	CatMeshReplica  = "mesh:replica"  // replica CRUD: apply, create, update, expire, remove
	CatMeshProxy    = "mesh:proxy"    // proxy lifecycle: create, expire, promote, summaries
	CatMeshDormancy = "mesh:dormancy" // dormant entity wake events
	CatMeshCell     = "mesh:cell"     // cell start/stop/shutdown, coordinator lifecycle
	CatMeshAction   = "mesh:action"   // cross-node action dispatch and results
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
	Entity     ecs.Entity
	NetID      uint32
	ConnID     uint32 // non-zero for player entities
	Username   string // non-empty for player entities
	DestCellID string // cell ID string the entity crossed into
}

// SpawnOption configures optional components when spawning an entity via WorldBase.SpawnEntity.
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
	withComps   bool
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

// WithoutSpatial prevents SpawnEntity from auto-registering the entity with the
// spatial hash grid. By default, entities with a collider are registered automatically.
func WithoutSpatial() SpawnOption {
	return func(o *spawnOpts) { o.noSpatial = true }
}

// WithComponents auto-adds zero-value components for all components registered
// on the entity's EntityKindDef (via RegisterEntityKind). The entity must also
// have WithEntityKind set. Use map.Get(entity) to set non-zero fields after spawn.
func WithComponents() SpawnOption {
	return func(o *spawnOpts) { o.withComps = true }
}

// WorldBase provides default implementations for all GameWorld interface methods.
// Embed it in your game world struct to get working multi-node support out of the box.
//
// Usage:
//
//	type myWorld struct {
//	    universe.WorldBase
//	}
//
// All methods can be overridden by defining them on the outer struct.
type WorldBase struct {
	eng         *engine.Engine
	cell        CellID
	nodeID      string
	aoiRadius   float32
	bridge      Bridge
	spatialGrid *spatial.HashGrid

	coord     *Coordinator // set by Coordinator.createNode after world factory
	fromSplit bool         // true if created during a cell split (skip initial entity spawning)

	replicaNetIDs    map[uint32]ecs.Entity
	highestSeenEpoch map[uint32]uint32 // per-netID: highest epoch seen from border frames
	replRegistry     *ReplicationRegistry
	velScale         float32 // max velocity for qvel quantization

	entityKinds map[uint8]*EntityKindDef // registered via RegisterEntityKind

	crossingQueue []CrossingEvent // entities that crossed a cell boundary this tick

	onTransferReceived       func(entity ecs.Entity, frame *TransferFrame)
	onPlayerTransferReceived func(entity ecs.Entity, frame *TransferFrame)

	// Called before/after SerializeEntity during cross-node transfers.
	// dx, dy is the coordinate delta applied to the entity's position.
	onPreSerialize  func(entity ecs.Entity, dx, dy float32)
	onPostSerialize func(entity ecs.Entity, dx, dy float32)

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
}

// NewWorldBase creates a WorldBase for use within a world factory.
// Typically called by the Coordinator; games that need manual setup can call this directly.
func NewWorldBase(eng *engine.Engine, cell CellID, aoiRadius float32, replRegistry *ReplicationRegistry) *WorldBase {
	w := eng.ECS
	if replRegistry == nil {
		replRegistry = NewReplicationRegistry()
	}

	nodeID := MeshCellID(cell)

	base := WorldBase{
		eng:              eng,
		cell:             cell,
		nodeID:           nodeID,
		aoiRadius:        aoiRadius,
		bridge:           NoopBridge{},
		replicaNetIDs:    make(map[uint32]ecs.Entity),
		highestSeenEpoch: make(map[uint32]uint32),
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
func (b *WorldBase) Engine() *engine.Engine { return b.eng }

// Bridge returns the bridge for inter-cell communication.
func (b *WorldBase) Bridge() Bridge { return b.bridge }

// Cell returns this node's cell coordinates.
func (b *WorldBase) Cell() CellID { return b.cell }

// Coordinator returns the coordinator that owns this node, or nil in single-node mode.
func (b *WorldBase) Coordinator() *Coordinator { return b.coord }

// FromSplit returns true if this world was created during a cell split.
// Split-created worlds should skip initial entity spawning since entities
// arrive via transfer from the parent cell.
func (b *WorldBase) FromSplit() bool { return b.fromSplit }

// rootCell returns the depth-0 ancestor of this node's cell.
func (b *WorldBase) rootCell() CellID {
	c := b.cell
	for c.Depth > 0 {
		c = c.Parent()
	}
	return c
}

// CellSize returns the base cell size. Entities always use base-cell coordinates
// regardless of quadtree depth, so this always returns coords.CellSize.
func (b *WorldBase) CellSize() float32 { return coords.CellSize }

// NodeID returns this node's unique identifier (e.g., "cell_0_0").
func (b *WorldBase) NodeID() string { return b.nodeID }

// SpatialGrid returns the spatial hash grid for AoI/collision queries.
func (b *WorldBase) SpatialGrid() *spatial.HashGrid { return b.spatialGrid }

// SetSpatialGrid replaces the spatial hash grid (useful for tests or manual node setup).
func (b *WorldBase) SetSpatialGrid(g *spatial.HashGrid) { b.spatialGrid = g }

// ReplicaNetIDs returns the map tracking replica entities by network ID.
func (b *WorldBase) ReplicaNetIDs() map[uint32]ecs.Entity { return b.replicaNetIDs }

// ReplicationRegistry returns the registry used for replica scanning.
func (b *WorldBase) ReplicationRegistry() *ReplicationRegistry { return b.replRegistry }

// SetReplicationRegistry replaces the replication registry (e.g., to inject
// a game-specific registry built with game component mappers).
func (b *WorldBase) SetReplicationRegistry(reg *ReplicationRegistry) { b.replRegistry = reg }

// SetOnTransferReceived sets a hook called after any entity is spawned from a transfer.
func (b *WorldBase) SetOnTransferReceived(fn func(ecs.Entity, *TransferFrame)) {
	b.onTransferReceived = fn
}

// SetOnPlayerTransferReceived sets a hook called after a player entity is spawned from a transfer.
func (b *WorldBase) SetOnPlayerTransferReceived(fn func(ecs.Entity, *TransferFrame)) {
	b.onPlayerTransferReceived = fn
}

// SetPreSerialize sets a hook called before entity serialization during transfers.
// dx, dy is the coordinate delta that will be applied to the position.
// Use this to adjust game-specific components (e.g. body segment ring buffers)
// that store absolute positions and need the same offset.
func (b *WorldBase) SetPreSerialize(fn func(ecs.Entity, float32, float32)) {
	b.onPreSerialize = fn
}

// SetPostSerialize sets a hook called after entity serialization during transfers.
// dx, dy is the inverse delta — use this to restore adjusted components.
func (b *WorldBase) SetPostSerialize(fn func(ecs.Entity, float32, float32)) {
	b.onPostSerialize = fn
}

// SetOnCellBoundsChanged sets a callback invoked for each connected player
// after UpdateCellBounds remaps entity positions. Use this to send updated
// cell metadata to clients (e.g. new cell coordinates and size).
func (b *WorldBase) SetOnCellBoundsChanged(fn func(connID uint32)) {
	b.onCellBoundsChanged = fn
}

// RegisterEntityKind registers an entity kind definition. This:
//   - Registers all components with the transfer ReplicationRegistry
//   - Stores ensureExists callbacks for auto-filling on transfer receive
//   - Stores the def for NewNetworkSystem to build replicators automatically
func (b *WorldBase) RegisterEntityKind(def EntityKindDef) {
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
func (b *WorldBase) EntityKindDefs() map[uint8]*EntityKindDef {
	return b.entityKinds
}

// EnsureEntityKindComponents adds zero-value components for all components
// registered on the entity's kind. If the entity already has a component,
// it is left unchanged.
func (b *WorldBase) EnsureEntityKindComponents(entity ecs.Entity) {
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
func (b *WorldBase) SendSpawnedMsg(connID uint32, entity ecs.Entity) {
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
// WorldBase's coordinator reference. Wraps Coordinator.ClusterCells;
// returns nil when this WorldBase has no coordinator wiring.
//
// Games use this to build their own SE_CELL_TOPOLOGY messages and push
// them to clients via gw.Engine().ConnMgr.SendReliable — see
// examples/4node-basic for the pattern. Topology distribution is a
// game concern: different games want different debug data, so the
// engine no longer ships a built-in broadcaster.
func (b *WorldBase) ClusterCells() []ClusterCellInfo {
	if b.coord == nil {
		return nil
	}
	return b.coord.ClusterCells()
}

// GhostMap returns the Ghost component mapper. Used by games that still
// reference Ghost entities (e.g., visual continuity). No longer used by
// BoundarySystem after the handoff-protocol refactor.
func (b *WorldBase) GhostMap() *ecs.Map1[component.Ghost] { return b.ghostMap }

// QueueCrossing appends an entity crossing event to the per-tick queue.
// The HandoffDriver drains this queue in PostSystems.
func (b *WorldBase) QueueCrossing(evt CrossingEvent) {
	b.crossingQueue = append(b.crossingQueue, evt)
}

// DrainCrossingQueue returns the current crossing queue and resets it for
// the next tick. The returned slice is reused — callers must not retain it
// across ticks.
func (b *WorldBase) DrainCrossingQueue() []CrossingEvent {
	q := b.crossingQueue
	b.crossingQueue = b.crossingQueue[:0]
	return q
}

// PositionMap returns the Position component mapper.
func (b *WorldBase) PositionMap() *ecs.Map1[component.Position] { return b.posMap }

// VelocityMap returns the Velocity component mapper.
func (b *WorldBase) VelocityMap() *ecs.Map1[component.Velocity] { return b.velMap }

// NetworkIDMap returns the NetworkID component mapper.
func (b *WorldBase) NetworkIDMap() *ecs.Map1[component.NetworkID] { return b.netIDMap }

// EntityKindMap returns the EntityKind component mapper.
func (b *WorldBase) EntityKindMap() *ecs.Map1[component.EntityKind] { return b.kindMap }

// ColliderMap returns the Collider component mapper.
func (b *WorldBase) ColliderMap() *ecs.Map1[component.Collider] { return b.colliderMap }

// CellCoordMap returns the CellCoord component mapper.
func (b *WorldBase) CellCoordMap() *ecs.Map1[component.CellCoord] { return b.cellMap }

// ---------------------------------------------------------------------------
// GameWorld interface — default implementations
// ---------------------------------------------------------------------------

func (b *WorldBase) ECSWorld() *ecs.World                                 { return b.eng.ECS }
func (b *WorldBase) GetAoIRadius() float32                                { return b.aoiRadius }
func (b *WorldBase) SetBridge(bridge Bridge)                              { b.bridge = bridge }
func (b *WorldBase) MarkForRemoval(e ecs.Entity)                          { b.eng.MarkForRemoval(e) }
func (b *WorldBase) Hooks() engine.Hooks                                  { return engine.Hooks{} }
func (b *WorldBase) Shutdown() {}

// UpdateCellBounds updates the cell identity and coordinate bounds for this world.
// Called from the game loop during dynamic cell split/merge operations.
//
// Entities always use base-cell (depth-0) coordinates, so position remapping
// is only needed when the root cell changes (cross-root transfers). For subcell
// depth changes within the same root cell (split/merge), only the cell identity
// and node ID are updated.
func (b *WorldBase) UpdateCellBounds(cell CellID, cellSize float32) {
	oldCell := b.cell
	b.cell = cell
	b.nodeID = MeshCellID(cell)

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
func (b *WorldBase) DispatchChat(string, string)                          {}
func (b *WorldBase) HandleCrossNodeAction(*CrossNodeAction) *ActionResult { return nil }
func (b *WorldBase) HandleActionResult(*ActionResult)                     {}

// ---------------------------------------------------------------------------
// Transfer serialization
// ---------------------------------------------------------------------------

// SerializeEntityCore reads core components from an entity and returns a
// TransferFrame. Games can call this from their own SerializeEntity override
// to get the core fields, then append game-specific component slices.
func (b *WorldBase) SerializeEntityCore(entity ecs.Entity) *TransferFrame {
	f := &TransferFrame{}

	if b.netIDMap.HasAll(entity) {
		f.NetworkID = b.netIDMap.Get(entity).ID
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
// game-specific components for cross-node transfer.
func (b *WorldBase) SerializeEntity(entity ecs.Entity) ([]byte, error) {
	frame := b.SerializeEntityCore(entity)
	// Append all registered game-specific components
	if b.replRegistry != nil {
		for _, rep := range b.replRegistry.All() {
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
func (b *WorldBase) SpawnFromTransferCore(data []byte) (ecs.Entity, *TransferFrame, error) {
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
		localID := b.coord.vcm.RegisterSession(key, frame.Username, 1, b.nodeID)
		frame.ConnID = localID
	}

	entity := b.spawner.NewEntity(
		&component.Position{X: frame.PosX, Y: frame.PosY},
		&component.Velocity{X: frame.VelX, Y: frame.VelY},
		&component.NetworkID{ID: frame.NetworkID},
		&component.EntityKind{Type: frame.EntityType},
		&frame.Collider,
		&component.CellCoord{CellX: frame.CellX, CellY: frame.CellY},
	)

	b.rotMap.Add(entity, &component.Rotation{Angle: frame.Rotation})
	if frame.ConnID != 0 {
		b.playerMap.Add(entity, &component.PlayerConn{ConnID: frame.ConnID})
	}

	b.cooldownMap.Add(entity, &component.TransferCooldown{Remaining: 20})

	// Apply registered game-specific components
	if b.replRegistry != nil {
		for _, cs := range frame.Components {
			if rep := b.replRegistry.Get(cs.ID); rep != nil {
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

	b.eng.Log.Log(CatMeshTransfer, "[%s] transfer received: netID=%d at (%.0f,%.0f)", b.nodeID, frame.NetworkID, frame.PosX, frame.PosY)
	return entity, frame, nil
}

// SpawnFromTransfer creates an entity from transfer data.
func (b *WorldBase) SpawnFromTransfer(data []byte) (uint32, uint32, error) {
	_, frame, err := b.SpawnFromTransferCore(data)
	if err != nil {
		return 0, 0, err
	}
	return frame.NetworkID, frame.ConnID, nil
}

// SpawnShadow creates a pre-authority shadow entity from a handoff prepare
// payload. Reuses SpawnFromTransferCore to deserialize the TransferBlob,
// then adds a Shadow component marking it as pre-authority.
//
// Game systems exclude shadows via mmokit.Query's default Without filter;
// the ReplicationSystem still iterates them so nearby players see the
// incoming entity before the handoff commits.
//
// The caller should fill in Shadow.SourceCellID after the method returns
// (it is left empty here because this helper does not have access to
// the CellMessage's FromCellID field).
func (b *WorldBase) SpawnShadow(payload *HandoffPreparePayload) (ecs.Entity, error) {
	entity, frame, err := b.SpawnFromTransferCore(payload.TransferBlob)
	if err != nil {
		return ecs.Entity{}, err
	}

	// The TransferFrame wire format does not carry the NetworkID.Epoch
	// field — it only serializes the 32-bit ID. Without this step, the
	// shadow entity would spawn with Epoch=0 and any border frames the
	// destination later sends back toward the source would be rejected
	// as stale (source's highestSeenEpoch[netID] was bumped to the new
	// value at handoff time). Set the epoch explicitly from the payload.
	netIDMap := ecs.NewMap1[component.NetworkID](b.eng.ECS)
	if netIDMap.HasAll(entity) {
		nid := netIDMap.Get(entity)
		nid.Epoch = payload.Epoch
	}

	shadowMap := ecs.NewMap1[component.Shadow](b.eng.ECS)
	shadowMap.Add(entity, &component.Shadow{
		NetID: payload.NetID,
		Epoch: payload.Epoch,
	})

	b.eng.Log.Log(CatMeshTransfer,
		"[%s] shadow created: netID=%d epoch=%d kind=%d (from prepare)",
		b.nodeID, frame.NetworkID, payload.Epoch, frame.EntityType)

	return entity, nil
}

// PromoteShadow removes the Shadow component from the entity matching the
// given NetID, turning it into a normal local entity that game systems will
// process. Also removes TransferCooldown so the promoted entity can
// immediately re-cross cell boundaries if game logic requires it.
//
// Returns true if the shadow was found and promoted, false if no matching
// shadow exists (e.g. a duplicate Commit or an out-of-order Commit that
// arrived before Prepare).
func (b *WorldBase) PromoteShadow(netID uint32) bool {
	shadowMap := ecs.NewMap1[component.Shadow](b.eng.ECS)
	filter := ecs.NewFilter2[component.Shadow, component.NetworkID](b.eng.ECS)
	query := filter.Query()
	for query.Next() {
		_, nid := query.Get()
		if nid.ID != netID {
			continue
		}
		entity := query.Entity()
		query.Close()

		// Remove Shadow marker — entity becomes a normal local entity.
		shadowMap.Remove(entity)

		// Remove TransferCooldown if present so the promoted entity can
		// cross boundaries again immediately.
		cooldownMap := ecs.NewMap1[component.TransferCooldown](b.eng.ECS)
		if cooldownMap.HasAll(entity) {
			cooldownMap.Remove(entity)
		}

		b.eng.Log.Log(CatMeshTransfer,
			"[%s] shadow promoted: netID=%d", b.nodeID, netID)
		return true
	}
	query.Close()
	return false
}

// RemoveShadowByNetID finds a shadow entity by NetworkID and marks it
// for removal. Used when a handoff is cancelled (source retreated,
// timed out, or committed to a different neighbor). Returns true if a
// matching shadow was found and marked for removal.
func (b *WorldBase) RemoveShadowByNetID(netID uint32) bool {
	filter := ecs.NewFilter2[component.Shadow, component.NetworkID](b.eng.ECS)
	query := filter.Query()
	for query.Next() {
		_, nid := query.Get()
		if nid.ID != netID {
			continue
		}
		entity := query.Entity()
		query.Close()
		b.eng.MarkForRemoval(entity)
		b.eng.Log.Log(CatMeshTransfer,
			"[%s] shadow removed (cancel): netID=%d", b.nodeID, netID)
		return true
	}
	return false
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
// Wire format per DeltaBuf (see also pkg/universe/border_components.go):
//
//	[0:4]   worldX  float32 LE
//	[4:8]   worldY  float32 LE
//	[8:12]  radius  float32 LE
//	[12:14] qvx     int16 LE
//	[14:16] qvy     int16 LE
//	[16:]   component tail: [u16 count][repeated: u16 id, u16 len, N bytes]
func (b *WorldBase) ApplyBorderFrame(frame replication.Frame, sourceNodeID string) {
	cellSize := coords.CellSize
	rootCell := b.cell
	for rootCell.Depth > 0 {
		rootCell = rootCell.Parent()
	}
	recvCellX := float32(rootCell.X) * cellSize
	recvCellY := float32(rootCell.Y) * cellSize

	for _, entry := range frame.Entries {
		if len(entry.DeltaBuf) < 18 {
			continue
		}
		worldX := math.Float32frombits(binary.LittleEndian.Uint32(entry.DeltaBuf[0:4]))
		worldY := math.Float32frombits(binary.LittleEndian.Uint32(entry.DeltaBuf[4:8]))
		radius := math.Float32frombits(binary.LittleEndian.Uint32(entry.DeltaBuf[8:12]))
		qvx := int16(binary.LittleEndian.Uint16(entry.DeltaBuf[12:14]))
		qvy := int16(binary.LittleEndian.Uint16(entry.DeltaBuf[14:16]))
		componentTail := entry.DeltaBuf[16:]
		vx := dequantizeVelI16(qvx, 2000)
		vy := dequantizeVelI16(qvy, 2000)

		localX := worldX - recvCellX
		localY := worldY - recvCellY

		b.upsertBorderReplica(entry.NetID.ID, entry.NetID.Epoch, uint8(entry.Kind), localX, localY, radius, vx, vy, sourceNodeID, componentTail)
	}
}

// upsertBorderReplica is the single entry point for creating or updating a
// replica entity from a border frame. Tracks the highest-seen epoch per netID
// so stale frames are dropped trivially. componentTail is the length-prefixed
// component-slice section of the wire entry (may be empty) and is applied via
// the ReplicationRegistry after fixed-field updates.
func (b *WorldBase) upsertBorderReplica(
	netID uint32, epoch uint32, kind uint8,
	localX, localY, radius, vx, vy float32,
	sourceNodeID string,
	componentTail []byte,
) {
	if prev, ok := b.highestSeenEpoch[netID]; ok && epoch < prev {
		return // stale
	}
	b.highestSeenEpoch[netID] = epoch

	if ent, ok := b.replicaNetIDs[netID]; ok && b.eng.ECS.Alive(ent) {
		// Update existing replica position and velocity.
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
		if b.replicaMap.HasAll(ent) {
			rep := b.replicaMap.Get(ent)
			rep.TTL = 30
			rep.UpdatedThisTick = true
			rep.SourceNodeID = sourceNodeID
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
		&component.Rotation{},
		&component.Collider{Radius: radius},
		&component.NetworkID{ID: netID, Epoch: epoch},
		&component.EntityKind{Type: kind},
	)
	b.cellMap.Add(ent, &component.CellCoord{CellX: rootCell.X, CellY: rootCell.Y})
	b.replicaMap.Add(ent, &component.Replica{
		SourceNodeID:    sourceNodeID,
		SourceNetID:     netID,
		TTL:             30,
		UpdatedThisTick: true,
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
		b.nodeID, netID, kind, sourceNodeID, localX, localY)
}


// ---------------------------------------------------------------------------
// Lifecycle management (ghost, replica, cooldown TTLs)
// ---------------------------------------------------------------------------

func (b *WorldBase) ClearReplicaUpdateFlags() {
	filter := ecs.NewFilter1[component.Replica](b.eng.ECS)
	query := filter.Query()
	for query.Next() {
		query.Get().UpdatedThisTick = false
	}
}

func (b *WorldBase) ExpireReplicas() {
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
		b.eng.Log.Log(CatMeshReplica, "[%s] replicas expired: count=%d", b.nodeID, len(expired))
	}
	for _, e := range expired {
		if b.eng.ECS.Alive(e) {
			if b.replicaMap.HasAll(e) {
				delete(b.replicaNetIDs, b.replicaMap.Get(e).SourceNetID)
			}
			b.eng.MarkForRemoval(e)
		}
	}
}

func (b *WorldBase) RemoveReplicaByNetID(netID uint32) {
	if e, ok := b.replicaNetIDs[netID]; ok {
		b.eng.Log.Log(CatMeshReplica, "[%s] replica removed: netID=%d (transfer arrived)", b.nodeID, netID)
		if b.eng.ECS.Alive(e) {
			b.eng.ECS.RemoveEntity(e)
		}
		delete(b.replicaNetIDs, netID)
	}
}

// VelScale returns the max velocity scale used for qvel quantization.
func (b *WorldBase) VelScale() float32 { return b.velScale }

// SetVelScale sets the max velocity scale for qvel quantization.
func (b *WorldBase) SetVelScale(scale float32) { b.velScale = scale }

// ---------------------------------------------------------------------------
// Dormancy system (sleep until player proximity)
// ---------------------------------------------------------------------------

// WakeDormantEntities checks all dormant entities against the spatial grid for
// nearby players (local or proxy). If any player entity or player proxy is within
// wakeRadius, the Dormant component is removed and the entity becomes active.
func (b *WorldBase) WakeDormantEntities(wakeRadius float32) {
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
			b.eng.Log.Log(CatMeshDormancy, "[%s] dormant entity woke: netID=%d", b.nodeID, netID)
		}
	}
}

// TickGhosts removes any entity tagged with the Ghost marker. Ghost is a
// pure marker (no TTL, no confirmation state) — a caller tags an entity
// Ghost immediately before an authority flip, and the next TickGhosts pass
// cleans it up. One tick of visibility is sufficient because the destination
// cell's replica or handoff shadow has already spawned by then.
func (b *WorldBase) TickGhosts() {
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

func (b *WorldBase) TickTransferCooldowns() {
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
func (b *WorldBase) SpawnEntity(pos component.Position, opts ...SpawnOption) ecs.Entity {
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
	if o.withComps {
		b.EnsureEntityKindComponents(entity)
	}

	b.eng.Log.Log(CatMeshCell, "[%s] spawned entity netID=%d at (%.0f,%.0f)", b.nodeID, nid, pos.X, pos.Y)
	return entity
}

// Init is a no-op default. Override in your game world for custom initialization.
func (b *WorldBase) Init() {}
