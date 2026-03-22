package component

import (
	"github.com/mlange-42/ark/ecs"
	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/item"
)

// Collision layers
const (
	LayerPlayer     uint8 = 1
	LayerTerrain    uint8 = 2
)

// Entity types (derived from protobuf enums)
const (
	TypeShip       = uint8(gamepb.EntityType_ENTITY_TYPE_SHIP)
	TypeAsteroid   = uint8(gamepb.EntityType_ENTITY_TYPE_ASTEROID)
	TypeStation    = uint8(gamepb.EntityType_ENTITY_TYPE_STATION)
	TypeLootCrate  = uint8(gamepb.EntityType_ENTITY_TYPE_LOOT_CRATE)
	TypeNPC        = uint8(gamepb.EntityType_ENTITY_TYPE_NPC)
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

// Lifetime tracks remaining time before despawn.
type Lifetime struct {
	Remaining float32 // seconds
}

// Minable marks an entity as a mineable resource.
type Minable struct {
	ResourceType uint8
	Remaining    float32
}

// MiningBeamState holds the state for one mining beam (one weapon slot).
type MiningBeamState struct {
	Rate        float32 // units/sec from equipment (0 = no laser in this slot)
	Range       float32 // max mining distance
	PulseYield  float32 // bonus resource amount for extract pulse
	Active      bool    // beam currently on
	Accumulator float32 // fractional mining accumulation
}

// MiningLaser holds dual-beam mining state driven by equipment.
// Beams[0] corresponds to Weapon1, Beams[1] to Weapon2.
type MiningLaser struct {
	Beams  [2]MiningBeamState
	Target ecs.Entity // shared target (from lock)
}

// Inventory holds collected items with a mass-based capacity limit.
type Inventory struct {
	Items   map[uint32]int32 // itemID -> quantity
	MaxMass float32            // capacity limit (total mass)
}

// ensureMap lazily initializes the Items map.
func (inv *Inventory) ensureMap() {
	if inv.Items == nil {
		inv.Items = make(map[uint32]int32)
	}
}

// TotalMass returns the total mass of all items in this inventory.
func (inv *Inventory) TotalMass() float32 {
	var total float32
	for id, qty := range inv.Items {
		total += float32(qty) * item.MassOf(id)
	}
	return total
}

// RemainingMass returns how much more mass can be added.
func (inv *Inventory) RemainingMass() float32 {
	r := inv.MaxMass - inv.TotalMass()
	if r < 0 {
		return 0
	}
	return r
}

// AddItem adds up to amount of the given item, respecting the mass limit.
// Returns the quantity actually added.
func (inv *Inventory) AddItem(itemID uint32, amount int32) int32 {
	if amount <= 0 {
		return 0
	}
	inv.ensureMap()
	massPerUnit := item.MassOf(itemID)
	remaining := inv.RemainingMass()
	maxByMass := int32(remaining / massPerUnit)
	if amount > maxByMass {
		amount = maxByMass
	}
	if amount <= 0 {
		return 0
	}
	inv.Items[itemID] += amount
	return amount
}

// RemoveItem removes up to amount of the given item.
// Returns the quantity actually removed.
func (inv *Inventory) RemoveItem(itemID uint32, amount int32) int32 {
	if amount <= 0 || inv.Items == nil {
		return 0
	}
	have := inv.Items[itemID]
	if have <= 0 {
		return 0
	}
	if amount > have {
		amount = have
	}
	inv.Items[itemID] -= amount
	if inv.Items[itemID] <= 0 {
		delete(inv.Items, itemID)
	}
	return amount
}

// Clear removes all items and returns the previous contents.
func (inv *Inventory) Clear() map[uint32]int32 {
	old := inv.Items
	inv.Items = nil
	return old
}

// IsEmpty returns true if the inventory contains no items.
func (inv *Inventory) IsEmpty() bool {
	return len(inv.Items) == 0
}

// PlayerConn links a player entity to its network connection.
type PlayerConn struct {
	ConnID uint32
}

// PlayerInput holds the current frame's input for a player.
type PlayerInput struct {
	Sequence         uint32 // for input ack
	JettisonItemID   uint32 // item ID to jettison (0 = none)
	AbilityCast      uint32 // bitmask: bit 0=Q, 1=W, 2=E, 3=R, 4=D, 5=F
	LockTargetNetID  uint32 // lock-on target network ID
}

// LootCrate is a marker for dropped cargo entities.
type LootCrate struct{}

// Station is a marker for trade station entities.
type Station struct{}

// SectorCoord identifies which sector an entity belongs to.
type SectorCoord struct {
	SX, SY int32
}

// MoveTarget holds a click-to-move destination.
type MoveTarget struct {
	X, Y   float32 // destination local coordinates within target sector
	SX, SY int32   // sector of the destination
	Active bool    // whether ship is moving to destination
}

// TargetLock holds EVE-style lock-on state.
type TargetLock struct {
	TargetEntity ecs.Entity // entity being locked/locked onto
	TargetNetID  uint32     // network ID of target
	Progress     float32    // 0.0 to 1.0 (1.0 = locked)
	LockTime     float32    // seconds required to achieve full lock
	Range        float32    // max lock range
	Locked       bool       // true when Progress >= 1.0
}

// Ability slot constants (mapped to keyboard keys).
// Abilities are defined by equipped items, not hardcoded.
const (
	AbilityQ     uint8 = 0 // Weapon1 primary
	AbilityW     uint8 = 1 // Weapon1 secondary
	AbilityE     uint8 = 2 // Weapon2 primary
	AbilityR     uint8 = 3 // Weapon2 secondary
	AbilityD     uint8 = 4 // Shield generator
	AbilityF     uint8 = 5 // Thruster
	AbilityCount       = 6
)

// Equipment holds the item IDs of equipped gear. Zero means empty slot.
type Equipment struct {
	Weapon1  uint32 // item ID → Q + W abilities
	Weapon2  uint32 // item ID → E + R abilities
	Shield   uint32 // item ID → D ability
	Thruster uint32 // item ID → F ability
}

// AbilitySet holds cooldown state for all ability slots.
type AbilitySet struct {
	Cooldowns [AbilityCount]float32 // remaining cooldown per slot (seconds)
}

// StatusType identifies a buff or debuff.
type StatusType uint8

const (
	StatusNone          StatusType = 0
	StatusIonBurn StatusType = 1 // damage over time (Value = DPS)
	StatusFortified     StatusType = 2 // damage reduction (Value = fraction e.g. 0.3)
	StatusAfterburner   StatusType = 3 // speed multiplier (Value = multiplier e.g. 2.5)
	StatusShieldRegen   StatusType = 4 // shield heal over time (Value = shield points per second)
)

// StatusEffect represents a single active buff or debuff.
type StatusEffect struct {
	Type     StatusType
	Duration float32    // remaining seconds
	Value    float32    // effect magnitude
	Source   ecs.Entity // who applied this
}

// MaxStatusEffects is the fixed capacity for concurrent effects per entity.
const MaxStatusEffects = 4

// StatusEffects holds active buffs/debuffs on an entity.
type StatusEffects struct {
	Effects [MaxStatusEffects]StatusEffect
	Count   uint8
}

// Add adds or refreshes a status effect. If the same type already exists, it is overwritten.
func (s *StatusEffects) Add(effect StatusEffect) {
	// Overwrite existing effect of same type
	for i := uint8(0); i < s.Count; i++ {
		if s.Effects[i].Type == effect.Type {
			s.Effects[i] = effect
			return
		}
	}
	// Add new if there's room
	if s.Count < MaxStatusEffects {
		s.Effects[s.Count] = effect
		s.Count++
	}
}

// Remove removes the effect at the given index using swap-remove.
func (s *StatusEffects) Remove(index uint8) {
	if index >= s.Count {
		return
	}
	s.Count--
	s.Effects[index] = s.Effects[s.Count]
	s.Effects[s.Count] = StatusEffect{}
}

// Has returns true if any active effect of the given type exists.
func (s *StatusEffects) Has(t StatusType) bool {
	for i := uint8(0); i < s.Count; i++ {
		if s.Effects[i].Type == t {
			return true
		}
	}
	return false
}

// Get returns a pointer to the first effect of the given type, or nil.
func (s *StatusEffects) Get(t StatusType) *StatusEffect {
	for i := uint8(0); i < s.Count; i++ {
		if s.Effects[i].Type == t {
			return &s.Effects[i]
		}
	}
	return nil
}

// TickDown decrements all durations and removes expired effects.
func (s *StatusEffects) TickDown(dt float32) {
	for i := uint8(0); i < s.Count; {
		s.Effects[i].Duration -= dt
		if s.Effects[i].Duration <= 0 {
			s.Remove(i)
			// Don't increment i; swap-remove moved another element here
		} else {
			i++
		}
	}
}
