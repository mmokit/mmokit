package component

import (
	"github.com/mlange-42/ark/ecs"
	gamepb "github.com/zenion/gameserver/gen/go"
)

// Collision layers
const (
	LayerPlayer     uint8 = 1
	LayerTerrain    uint8 = 2
	LayerProjectile uint8 = 4
)

// Entity types (derived from protobuf enums)
const (
	TypeShip       = uint8(gamepb.EntityType_ENTITY_TYPE_SHIP)
	TypeAsteroid   = uint8(gamepb.EntityType_ENTITY_TYPE_ASTEROID)
	TypeProjectile = uint8(gamepb.EntityType_ENTITY_TYPE_PROJECTILE)
	TypeStation    = uint8(gamepb.EntityType_ENTITY_TYPE_STATION)
	TypeLootCrate  = uint8(gamepb.EntityType_ENTITY_TYPE_LOOT_CRATE)
)

// Resource types (derived from protobuf enums)
const (
	ResourceOre     = uint8(gamepb.ResourceType_RESOURCE_TYPE_ORE)
	ResourceCrystal = uint8(gamepb.ResourceType_RESOURCE_TYPE_CRYSTAL)
	ResourceGas     = uint8(gamepb.ResourceType_RESOURCE_TYPE_GAS)
	ResourceMetal   = uint8(gamepb.ResourceType_RESOURCE_TYPE_METAL)
)

// Position in world space.
type Position struct {
	X, Y float32
}

// Velocity in world units per second.
type Velocity struct {
	X, Y float32
}

// Rotation facing direction.
type Rotation struct {
	Angle float32 // radians
}

// Collider defines a collision shape (circle or oriented rectangle).
// Shape constants (ShapeCircle, ShapeRect) are in pkg/spatial.
// For circles, use Radius. For rects, use Width (forward) and Height (side).
// Radius is also used as the bounding radius for broad-phase checks on rects.
type Collider struct {
	Radius float32 // circle radius, or bounding radius for rects
	Width  float32 // rect extent along local X (forward axis)
	Height float32 // rect extent along local Y (side axis)
	Layer  uint8
	Shape  uint8
}

// NetworkID is a stable identifier sent to clients.
type NetworkID struct {
	ID uint32
}

// EntityKind identifies the type of entity for the client.
type EntityKind struct {
	Type uint8
}

// ShipControl holds ship movement parameters.
type ShipControl struct {
	Thrust   float32
	TurnRate float32
	MaxSpeed float32
}

// Health represents hit points.
type Health struct {
	Current float32
	Max     float32
}

// Shield represents shield points with regeneration.
type Shield struct {
	Current      float32
	Max          float32
	RegenRate    float32
	RegenDelay   float32 // seconds after damage before regen starts
	DamageCooldown float32 // time remaining before regen resumes
}

// Weapon represents a ship's weapon.
type Weapon struct {
	Damage       float32
	FireRate     float32 // shots per second
	Speed        float32 // projectile speed
	CooldownLeft float32 // seconds until next shot
}

// Projectile marks an entity as a projectile with damage.
type Projectile struct {
	Damage float32
}

// Owner links an entity (e.g. projectile) to its creator.
type Owner struct {
	Entity ecs.Entity
}

// Lifetime tracks remaining time before despawn.
type Lifetime struct {
	Remaining float32 // seconds
}

// Minable marks an entity as a mineable resource.
type Minable struct {
	ResourceType uint8
	Remaining    float32
}

// MiningLaser represents a ship's mining equipment.
type MiningLaser struct {
	Range  float32
	Rate   float32 // units per second
	Active bool
	Target ecs.Entity
}

// Inventory holds collected resources.
type Inventory struct {
	Resources [4]float32
}

// PlayerConn links a player entity to its network connection.
type PlayerConn struct {
	ConnID uint32
}

// PlayerInput holds the current frame's input for a player.
type PlayerInput struct {
	Thrust      float32 // -1 to 1
	Turn        float32 // -1 to 1
	Fire        bool
	Mine        bool
	Sequence    uint32 // for input ack
	TargetNetID      uint32 // target asteroid network ID from client
	JettisonResource uint8  // resource type to jettison (1-4, 0 = none)
	Sell             bool
}

// LootCrate is a marker for dropped cargo entities.
type LootCrate struct{}

// Station is a marker for trade station entities.
type Station struct{}
