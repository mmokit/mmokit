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
	CatMeshNode     = "mesh:node"     // node start/stop/shutdown, coordinator lifecycle
	CatMeshAction   = "mesh:action"   // cross-node action dispatch and results
	CatMeshMsg      = "mesh:msg"      // inter-node message routing
	CatNetConn      = "net:conn"      // connection lifecycle (WebSocket/UDP)
	CatNetTransport = "net:transport" // transport-level: UDP errors, buffer full, timeouts
	CatEngineLoop   = "engine:loop"   // game loop start/stop
)

// MeshCategories lists all framework log categories.
var MeshCategories = []string{
	CatMeshTransfer, CatMeshReplica, CatMeshProxy, CatMeshDormancy,
	CatMeshNode, CatMeshAction, CatMeshMsg,
	CatNetConn, CatNetTransport,
	CatEngineLoop,
}

// StartupCategories are always enabled so server lifecycle is visible.
var StartupCategories = []string{CatMeshNode, CatEngineLoop, CatNetConn}

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
	bridge      NodeBridge
	spatialGrid *spatial.HashGrid

	coord     *Coordinator // set by Coordinator.createNode after world factory
	fromSplit bool         // true if created during a cell split (skip initial entity spawning)

	replicaNetIDs    map[uint32]ecs.Entity
	highestSeenEpoch map[uint32]uint32 // per-netID: highest epoch seen from border frames
	replRegistry     *ReplicationRegistry
	velScale         float32 // max velocity for qvel quantization

	entityKinds map[uint8]*EntityKindDef // registered via RegisterEntityKind

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

	nodeID := MeshNodeID(cell)

	base := WorldBase{
		eng:              eng,
		cell:             cell,
		nodeID:           nodeID,
		aoiRadius:        aoiRadius,
		bridge:           NoopNodeBridge{},
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

// Bridge returns the node bridge for inter-node communication.
func (b *WorldBase) Bridge() NodeBridge { return b.bridge }

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

// NodeID returns this node's unique identifier (e.g., "node_0_0").
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

// SendCellTopology sends the current cell topology to a specific client.
// Delegates to the Coordinator if available.
func (b *WorldBase) SendCellTopology(connID uint32) {
	if b.coord != nil {
		b.coord.SendCellTopology(connID)
	}
}

// PreSerialize calls the pre-serialize hook if registered.
func (b *WorldBase) PreSerialize(entity ecs.Entity, dx, dy float32) {
	if b.onPreSerialize != nil {
		b.onPreSerialize(entity, dx, dy)
	}
}

// PostSerialize calls the post-serialize hook if registered.
func (b *WorldBase) PostSerialize(entity ecs.Entity, dx, dy float32) {
	if b.onPostSerialize != nil {
		b.onPostSerialize(entity, dx, dy)
	}
}

// GhostMap returns the Ghost component mapper (used by BoundarySystem).
func (b *WorldBase) GhostMap() *ecs.Map1[component.Ghost] { return b.ghostMap }

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
func (b *WorldBase) SetBridge(bridge NodeBridge)                          { b.bridge = bridge }
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
	b.nodeID = MeshNodeID(cell)

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
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy]())
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

// ---------------------------------------------------------------------------
// Replication
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// Border frame apply (new path, replaces ApplyProxySummaries)
// ---------------------------------------------------------------------------

// ApplyBorderFrame applies each entry in a decoded border frame, creating or
// updating a replica entity for the entity described. Entries carry world-space
// position plus quantized velocity, radius, and entity kind. Stale epochs
// (frame entry epoch < highest seen epoch for that netID) are dropped silently.
//
// This replaces the legacy ApplyProxySummaries / ApplyReplicas path.
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
		// padding [16:18]
		vx := dequantizeVelI16(qvx, 2000)
		vy := dequantizeVelI16(qvy, 2000)

		localX := worldX - recvCellX
		localY := worldY - recvCellY

		b.upsertBorderReplica(entry.NetID.ID, entry.NetID.Epoch, uint8(entry.Kind), localX, localY, radius, vx, vy, sourceNodeID)
	}
}

// upsertBorderReplica is the single entry point for creating or updating a
// replica entity from a border frame. Tracks the highest-seen epoch per netID
// so stale frames are dropped trivially.
func (b *WorldBase) upsertBorderReplica(
	netID uint32, epoch uint32, kind uint8,
	localX, localY, radius, vx, vy float32,
	sourceNodeID string,
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

func (b *WorldBase) TickGhosts() {
	filter := ecs.NewFilter1[component.Ghost](b.eng.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		ghost := query.Get()
		ghost.TTL--
		if ghost.TTL <= 0 {
			expired = append(expired, query.Entity())
		}
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

func (b *WorldBase) RemoveGhostByNetID(netID uint32) {
	// Mark the ghost as confirmed rather than removing immediately. The ghost
	// stays visible until a replica with the same NetworkID arrives, preventing
	// a 1-tick gap where the entity disappears between ghost removal and
	// replica creation. TickGhosts handles final removal when TTL expires.
	filter := ecs.NewFilter2[component.Ghost, component.NetworkID](b.eng.ECS)
	query := filter.Query()
	for query.Next() {
		ghost, nid := query.Get()
		if nid.ID == netID {
			query.Close()
			ghost.Confirmed = true
			b.eng.Log.Log(CatMeshTransfer, "[%s] ghost confirmed: netID=%d (awaiting replica replacement)", b.nodeID, netID)
			return
		}
	}
}

// removeConfirmedGhost removes a confirmed ghost entity matching the given netID.
// Called when a replacement replica arrives, ensuring zero visual gap.
func (b *WorldBase) removeConfirmedGhost(netID uint32) {
	filter := ecs.NewFilter2[component.Ghost, component.NetworkID](b.eng.ECS)
	query := filter.Query()
	for query.Next() {
		ghost, nid := query.Get()
		if nid.ID == netID && ghost.Confirmed {
			entity := query.Entity()
			query.Close()
			b.eng.Log.Log(CatMeshTransfer, "[%s] confirmed ghost replaced by replica: netID=%d", b.nodeID, netID)
			b.eng.MarkForRemoval(entity)
			return
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

	b.eng.Log.Log(CatMeshNode, "[%s] spawned entity netID=%d at (%.0f,%.0f)", b.nodeID, nid, pos.X, pos.Y)
	return entity
}

// Init is a no-op default. Override in your game world for custom initialization.
func (b *WorldBase) Init() {}
