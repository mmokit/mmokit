package universe

import (
	"log"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// SpawnOption configures optional components when spawning an entity via WorldBase.SpawnEntity.
type SpawnOption func(*spawnOpts)

type spawnOpts struct {
	velX, velY   float32
	rotation     float32
	collider     component.Collider
	entityType   uint8
	hasVel       bool
	hasRot       bool
	hasCollider  bool
	hasKind      bool
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
	eng       *engine.Engine
	sector    coords.SectorCoord
	nodeID    string
	aoiRadius float32
	bridge    NodeBridge

	replicaNetIDs map[uint32]ecs.Entity
	replRegistry  *ReplicationRegistry

	// Component mappers for core components
	posMap      *ecs.Map1[component.Position]
	velMap      *ecs.Map1[component.Velocity]
	rotMap      *ecs.Map1[component.Rotation]
	netIDMap    *ecs.Map1[component.NetworkID]
	kindMap     *ecs.Map1[component.EntityKind]
	colliderMap *ecs.Map1[component.Collider]
	sectorMap   *ecs.Map1[component.SectorCoord]
	ghostMap    *ecs.Map1[component.Ghost]
	replicaMap  *ecs.Map1[component.Replica]
	cooldownMap *ecs.Map1[component.TransferCooldown]
	playerMap   *ecs.Map1[component.PlayerConn]

	spawner *ecs.Map6[component.Position, component.Velocity, component.NetworkID, component.EntityKind, component.Collider, component.SectorCoord]

	// Replica creation mapper (includes Rotation for full-fidelity replicas)
	replicaCreator *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
}

// NewWorldBase creates a WorldBase for use within a NodeFactory.
// Typically called by the Coordinator; games that need manual setup can call this directly.
func NewWorldBase(eng *engine.Engine, sector coords.SectorCoord, aoiRadius float32, replRegistry *ReplicationRegistry) WorldBase {
	w := eng.ECS
	if replRegistry == nil {
		replRegistry = NewReplicationRegistry()
	}

	nodeID := SectorID(sector)

	base := WorldBase{
		eng:           eng,
		sector:        sector,
		nodeID:        nodeID,
		aoiRadius:     aoiRadius,
		bridge:        NoopNodeBridge{},
		replicaNetIDs: make(map[uint32]ecs.Entity),
		replRegistry:  replRegistry,

		posMap:      ecs.NewMap1[component.Position](w),
		velMap:      ecs.NewMap1[component.Velocity](w),
		rotMap:      ecs.NewMap1[component.Rotation](w),
		netIDMap:    ecs.NewMap1[component.NetworkID](w),
		kindMap:     ecs.NewMap1[component.EntityKind](w),
		colliderMap: ecs.NewMap1[component.Collider](w),
		sectorMap:   ecs.NewMap1[component.SectorCoord](w),
		ghostMap:    ecs.NewMap1[component.Ghost](w),
		replicaMap:  ecs.NewMap1[component.Replica](w),
		cooldownMap: ecs.NewMap1[component.TransferCooldown](w),
		playerMap:   ecs.NewMap1[component.PlayerConn](w),

		spawner:        ecs.NewMap6[component.Position, component.Velocity, component.NetworkID, component.EntityKind, component.Collider, component.SectorCoord](w),
		replicaCreator: ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](w),
	}

	// Wire GetNetID so the engine can track removed network IDs
	eng.GetNetID = func(e ecs.Entity) (uint32, bool) {
		if base.netIDMap.HasAll(e) {
			return base.netIDMap.Get(e).ID, true
		}
		return 0, false
	}

	return base
}

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

// Engine returns the underlying engine.
func (b *WorldBase) Engine() *engine.Engine { return b.eng }

// Bridge returns the node bridge for inter-node communication.
func (b *WorldBase) Bridge() NodeBridge { return b.bridge }

// Sector returns this node's sector coordinates.
func (b *WorldBase) Sector() coords.SectorCoord { return b.sector }

// NodeID returns this node's unique identifier (e.g., "node_0_0").
func (b *WorldBase) NodeID() string { return b.nodeID }

// ReplicaNetIDs returns the map tracking replica entities by network ID.
func (b *WorldBase) ReplicaNetIDs() map[uint32]ecs.Entity { return b.replicaNetIDs }

// ReplicationRegistry returns the registry used for replica scanning.
func (b *WorldBase) ReplicationRegistry() *ReplicationRegistry { return b.replRegistry }

// SetReplicationRegistry replaces the replication registry (e.g., to inject
// a game-specific registry built with game component mappers).
func (b *WorldBase) SetReplicationRegistry(reg *ReplicationRegistry) { b.replRegistry = reg }

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

// SectorCoordMap returns the SectorCoord component mapper.
func (b *WorldBase) SectorCoordMap() *ecs.Map1[component.SectorCoord] { return b.sectorMap }

// ---------------------------------------------------------------------------
// GameWorld interface — default implementations
// ---------------------------------------------------------------------------

func (b *WorldBase) ECSWorld() *ecs.World        { return b.eng.ECS }
func (b *WorldBase) GetAoIRadius() float32        { return b.aoiRadius }
func (b *WorldBase) SetBridge(bridge NodeBridge)   { b.bridge = bridge }
func (b *WorldBase) MarkForRemoval(e ecs.Entity)   { b.eng.MarkForRemoval(e) }
func (b *WorldBase) Hooks() engine.Hooks           { return engine.Hooks{} }
func (b *WorldBase) Shutdown()                     {}
func (b *WorldBase) DispatchChat(string, string)   {}
func (b *WorldBase) RegisterPendingLogin(uint32, string) {}
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
	if b.sectorMap.HasAll(entity) {
		sec := b.sectorMap.Get(entity)
		f.SectorX, f.SectorY = sec.SX, sec.SY
	}
	if b.playerMap.HasAll(entity) {
		f.ConnID = b.playerMap.Get(entity).ConnID
	}

	return f
}

// SerializeEntity encodes an entity's core components for cross-node transfer.
func (b *WorldBase) SerializeEntity(entity ecs.Entity) ([]byte, error) {
	return MarshalTransferFrame(b.SerializeEntityCore(entity))
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
		&component.SectorCoord{SX: frame.SectorX, SY: frame.SectorY},
	)

	if frame.Rotation != 0 {
		b.rotMap.Add(entity, &component.Rotation{Angle: frame.Rotation})
	}
	if frame.ConnID != 0 {
		b.playerMap.Add(entity, &component.PlayerConn{ConnID: frame.ConnID})
	}

	b.cooldownMap.Add(entity, &component.TransferCooldown{Remaining: 20})

	log.Printf("[%s] transfer received: netID=%d at (%.0f,%.0f)", b.nodeID, frame.NetworkID, frame.PosX, frame.PosY)
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

func (b *WorldBase) ScanBorderEntities(neighbors map[string]NeighborInfo) map[string][][]byte {
	return ScanBorderWithRegistry(
		b.eng.ECS,
		b.replRegistry,
		b.sector,
		coords.SectorSize,
		b.aoiRadius,
		neighbors,
	)
}

func (b *WorldBase) ApplyReplicas(snapshots [][]byte, sourceNodeID string) {
	ApplyReplicasWithRegistry(
		snapshots, sourceNodeID,
		b.sector, coords.SectorSize,
		b.replRegistry, b,
	)
}

// --- ReplicaApplyContext implementation ---

func (b *WorldBase) FindReplica(netID uint32) (ecs.Entity, bool) {
	if e, ok := b.replicaNetIDs[netID]; ok && b.eng.ECS.Alive(e) {
		return e, true
	}
	return ecs.Entity{}, false
}

func (b *WorldBase) CreateReplica(frame *ReplicaFrame, localX, localY float32, sourceNodeID string) ecs.Entity {
	collider := component.Collider{}
	for _, cs := range frame.Components {
		if cs.ID == 0 {
			collider = UnmarshalCollider(cs.Data)
			break
		}
	}

	entity := b.replicaCreator.NewEntity(
		&component.Position{X: localX, Y: localY},
		&component.Velocity{},
		&component.Rotation{},
		&collider,
		&component.NetworkID{ID: frame.NetworkID},
		&component.EntityKind{Type: frame.EntityType},
	)

	b.sectorMap.Add(entity, &component.SectorCoord{SX: b.sector.SX, SY: b.sector.SY})
	b.replicaMap.Add(entity, &component.Replica{
		SourceNodeID: sourceNodeID,
		SourceNetID:  frame.NetworkID,
		TTL:          30,
	})

	b.replicaNetIDs[frame.NetworkID] = entity
	log.Printf("[%s] replica created: netID=%d from %s at (%.0f,%.0f)", b.nodeID, frame.NetworkID, sourceNodeID, localX, localY)
	return entity
}

func (b *WorldBase) UpdateReplicaBase(entity ecs.Entity, localX, localY float32) {
	if b.posMap.HasAll(entity) {
		pos := b.posMap.Get(entity)
		pos.X = localX
		pos.Y = localY
	}
	if b.replicaMap.HasAll(entity) {
		b.replicaMap.Get(entity).TTL = 30
	}
}

// ---------------------------------------------------------------------------
// Lifecycle management (ghost, replica, cooldown TTLs)
// ---------------------------------------------------------------------------

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
		if b.eng.ECS.Alive(e) {
			b.eng.ECS.RemoveEntity(e)
		}
		delete(b.replicaNetIDs, netID)
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
	filter := ecs.NewFilter2[component.NetworkID, component.Ghost](b.eng.ECS)
	query := filter.Query()
	for query.Next() {
		nid, _ := query.Get()
		if nid.ID == netID {
			entity := query.Entity()
			query.Close()
			b.eng.MarkForRemoval(entity)
			log.Printf("[%s] ghost removed: netID=%d (arrival confirmed)", b.nodeID, netID)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Convenience spawn
// ---------------------------------------------------------------------------

// SpawnEntity creates a new entity with Position, Velocity, NetworkID,
// EntityKind, Collider, and SectorCoord. Use SpawnOptions to configure
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
		&component.SectorCoord{SX: b.sector.SX, SY: b.sector.SY},
	)

	if o.hasRot {
		b.rotMap.Add(entity, &component.Rotation{Angle: o.rotation})
	}

	log.Printf("[%s] spawned entity netID=%d at (%.0f,%.0f)", b.nodeID, nid, pos.X, pos.Y)
	return entity
}
