package component

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/examples/space/internal/item"
)

// Collision layer constants live in pkg/spatial — use spatial.LayerStatic /
// spatial.LayerProp / spatial.LayerEntity. The old LayerPlayer/LayerTerrain
// pair was removed because LayerPlayer=1 numerically collided with
// spatial.LayerStatic (1<<0), causing hasLOSOnGrid to treat every ship /
// NPC collider as a sight-blocker — NPCs never aggro'd because the
// caster's own collider always returned a hit at the ray origin.

// Entity kinds — wire byte that identifies an entity's kind on the
// replication channel. Names match the second arg passed to
// mmokit.RegisterKind in internal/game/entity_kinds.go; sdkgen emits a
// matching TypeScript const block from the same kind registry, so the
// authoritative source of truth is the RegisterKind call sites.
const (
	KindShip          uint8 = iota // 0
	KindAsteroid                   // 1
	KindStation                    // 2
	KindLootCrate                  // 3
	KindNPC                        // 4
	KindPOI                        // 5
	KindAoEMarker                  // 6
	KindProjectile                 // 7
	KindLineTelegraph              // 8
	KindDungeon                    // 9
	KindDungeonWall                // 10
	KindDecoration                 // 11
)

// Decoration is the marker component for hand-placed visual-only props
// (wrecks, beacons, signs). Kind selects the sprite family and Variant
// picks a specific look within that family; both are sent once at
// visibility-enter via the "initial" encoding (they never change).
type Decoration struct {
	Kind    string `net:"initial"`
	Variant string `net:"initial"`
}

// PlacedID tags an entity with the world-manifest id that spawned it.
// Used by world.* cmdsys verbs to despawn / re-spawn placed entities
// cleanly without position-matching heuristics.
//
// The component IS serialized across cell-transfer so the ID survives
// when belt asteroids / dungeon walls / POI NPCs migrate to neighboring
// cells. The ID field has no `net:"..."` tag so the component is NOT
// sent to clients on the replication wire — it's server-side bookkeeping.
//
// Cascade behavior: SpawnPOI tags both the POI marker and every roster
// NPC with the POI's PlacedID; SpawnDungeonAt tags the dungeon marker
// and every wall with the dungeon's PlacedID; SpawnBelt tags the
// asteroids it scatters. One cluster-wide DespawnPlacedByID(id) sweep
// removes the whole subtree wherever its members ended up.
type PlacedID struct {
	ID string
}

// Health represents hit points.
//
// LastDamagedByNetID and DeathFired are server-side only (no net:"..." tag,
// so not replicated to clients) but they ride along the cell-to-cell transfer
// codec so kill attribution survives boundary handoff and the death observer
// stays idempotent across cells.
//
// Stored as a NetID rather than mmokit.Entity because the reflect-marshal
// codec used for cell transfer (pkg/universe/reflect_marshal.go) skips
// ecs.Entity fields and rejects mmokit.Entity (which embeds *Stage).
// uint32 is reflect-friendly; the killer is re-resolved via
// mmokit.EntityByNetID(stage, h.LastDamagedByNetID) at observer-fire time.
type Health struct {
	Current            float32 `net:"f32"`
	Max                float32 `net:"f32"`
	LastDamagedByNetID uint32  // not replicated; serialized in transfer codec
	DeathFired         bool    // observer idempotence flag; serialized in transfer codec
}

// Shield represents shield points with regeneration.
type Shield struct {
	Current        float32 `net:"f32"`
	Max            float32 `net:"f32"`
	RegenRate      float32 // not replicated
	RegenDelay     float32 // seconds after damage before regen starts
	DamageCooldown float32 // time remaining before regen resumes
}

// LockedBy is a replicated "who is locking me" marker. The NetworkSystem
// populates it each tick from the reverse lock map so clients can render a
// warning ring on any entity currently being target-locked. Zero LockerNetID
// means nobody is currently locking this entity.
//
// Field names are prefixed with "Locker" to avoid colliding with the
// hardcoded netID field on every generated entity interface (cmd/sdkgen
// writes netID: number before processing bindings, and the collision
// resolver in auto_replicator.go doesn't know about hardcoded fields).
type LockedBy struct {
	LockerNetID    uint32  `net:"u32"`
	LockerProgress float32 `net:"qnorm"`
}

// ShipControl holds ship movement parameters.
//
// TurnRate is the maximum angular velocity (rad/s). TurnAccel is the
// angular acceleration (rad/s^2) used to ramp AngularVel up and down —
// this is what gives ships their "curved arc" turning feel instead of the
// old constant-rate snap. AngularVel is runtime state, reset naturally
// when the ship comes to rest.
type ShipControl struct {
	Thrust     float32
	TurnRate   float32 // max angular velocity, rad/s
	TurnAccel  float32 // angular acceleration, rad/s^2
	MaxSpeed   float32
	AngularVel float32 // current angular velocity, rad/s (runtime)
}

// Minable marks an entity as a mineable resource.
type Minable struct {
	ItemID    uint32  `net:"u32"`
	Remaining float32 `net:"f32"`
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

// ActiveMining is a lean replicated game-state component describing whether
// each of a ship's mining beams is currently active and what asteroid it is
// targeting. MiningSystem / AbilitySystem write this on state change.
// MiningLaser remains LocalOnly because it carries an ecs.Entity target ref
// and per-beam timers/cooldowns the client doesn't need.
//
// The target field is named MiningTargetNetID (not TargetNetID) to keep it
// unambiguous on the generated client entity interface.
type ActiveMining struct {
	Beam0Active       bool   `net:"bool"`
	Beam1Active       bool   `net:"bool"`
	MiningTargetNetID uint32 `net:"u32"`
}

// Inventory holds collected items with a mass-based capacity limit.
type Inventory struct {
	Items   map[uint32]int32 // itemID -> quantity
	MaxMass float32          // capacity limit (total mass)
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

// PlayerInput holds the current frame's input for a player.
type PlayerInput struct {
	Sequence       uint32 // for input ack
	JettisonItemID uint32 // item ID to jettison (0 = none)
	AbilityCast    uint32 // bitmask: bit 0=Q, 1=W, 2=E, 3=R, 4=D, 5=F

	// PVE v3: cursor world coords from the most recent CastAbility press.
	// AbilitySystem reads these when dispatching skillshot abilities and
	// resets them to 0 each tick after dispatch (matching the
	// AbilityCast bitmask reset pattern). The entire PlayerInput
	// component is local-only (tagged on the ship bundle); these fields
	// are never serialized over the wire.
	LastCastAimX float32
	LastCastAimY float32
}

// LootCrate is a marker for dropped cargo entities.
type LootCrate struct{}

// Station is the marker component for trade station entities. Name is
// the operator-facing identifier sourced from world/stations.json and
// survives transfer alongside the rest of the component.
type Station struct {
	Name string `mmokit:"local"`
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
	Weapon1  uint32 // item ID -> Q + W abilities
	Weapon2  uint32 // item ID -> E + R abilities
	Shield   uint32 // item ID -> D ability
	Thruster uint32 // item ID -> F ability
}

// AbilitySet holds cooldown state for all ability slots.
type AbilitySet struct {
	Cooldowns [AbilityCount]float32 // remaining cooldown per slot (seconds)
}

// StatusType identifies a buff or debuff.
type StatusType uint8

const (
	StatusNone        StatusType = 0
	StatusIonBurn     StatusType = 1 // damage over time (Value = DPS)
	StatusFortified   StatusType = 2 // damage reduction (Value = fraction e.g. 0.3)
	StatusAfterburner StatusType = 3 // speed multiplier (Value = multiplier e.g. 2.5)
	StatusShieldRegen StatusType = 4 // shield heal over time (Value = shield points per second)
	StatusSlow        StatusType = 5 // movement multiplier (Value < 1.0 attenuates speed)
	StatusSilence     StatusType = 6 // disables ability casts (Value unused)
	StatusSupercruise StatusType = 7 // speed multiplier while in active supercruise (Value = multiplier e.g. 2.5)
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
	for i := uint8(0); i < s.Count; i++ {
		if s.Effects[i].Type == effect.Type {
			s.Effects[i] = effect
			return
		}
	}
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
		} else {
			i++
		}
	}
}

// SupercruisePhase identifies the player's supercruise state.
type SupercruisePhase uint8

const (
	SupercruiseIdle       SupercruisePhase = 0
	SupercruiseChanneling SupercruisePhase = 1
	SupercruiseActive     SupercruisePhase = 2
)

// Supercruise tracks the state machine for the Z-bound travel-mode toggle.
// Phase transitions are driven by SupercruiseSystem (tick) and verb_damage.go
// (damage drains BufferHP; combat involvement stamps LockoutRemaining).
type Supercruise struct {
	Phase            SupercruisePhase `net:"u8"`
	BufferHP         float32          `net:"f32"` // remaining damage buffer (Active phase only)
	BufferMax        float32          `net:"f32"` // snapshot at Channeling→Active transition
	ChannelRemaining float32          `net:"f32"` // seconds left in channel (Channeling phase only)
	LockoutRemaining float32          `net:"f32"` // seconds until Z press is accepted again
}

// Leashing marks an NPC currently returning to its anchor under the
// leash mechanic. Leashing entities are invulnerable (ApplyDamage skips
// them), untargetable, and move at 2× speed toward their anchor.
// Removed when the NPC reaches its anchor — at that point HP and Shield
// are restored to Max and the NPC re-enters Idle.
type Leashing struct{}

// NPCAI is the runtime AI state on each NPC entity. Archetype is wire-
// replicated (clients pick sprites per archetype). State is sent as a
// thin signal so clients can visually distinguish Leash mode etc.
// LastDamageBy fields drive the target-switching rule. All numeric
// tunables are captured at spawn time from the active GameConfig.
type NPCAI struct {
	Archetype            uint8 `net:"initial,u8"`
	State                uint8 `net:"u8"`
	MaxSpeed             float32
	TurnRate             float32
	PreferredRange       float32
	WeaponRange          float32
	AggroRadius          float32
	LockRange            float32 // PVE v2: required proximity to transition Approach → Acquire
	MotionPolicy         uint8
	DamagePerShot        float32
	FireRate             float32
	FireCooldown         float32 // seconds remaining before next shot
	LastDamageByNetID    uint32
	LastDamageAtSec      float32
	LastCombatActivityAt float32

	// PVE v2: Artillery cast state. Non-zero CastTimeRemaining means an
	// AoEMarker is in flight; CastingMarkerNetID is its netID so interrupts
	// can despawn it. CastDamageAccum tracks cumulative damage taken since
	// cast start (for interrupt threshold). CastCooldown is the inter-cast
	// gap (ticks down each frame between casts).
	CastTimeRemaining  float32
	CastDamageAccum    float32
	CastingMarkerNetID uint32
	CastCooldown       float32

	// PVE v2: Lancer windup + charge tracking. WindupRemaining > 0 means a
	// telegraph is in flight at LineTelegraphNetID; ChargeRemaining > 0
	// means the lancer is actively dashing in ChargeDirAngle direction.
	// RecoverRemaining > 0 means the lancer just finished a charge and is
	// vulnerable. ChargeHit ensures one-shot-per-charge damage application.
	WindupRemaining    float32
	ChargeRemaining    float32
	RecoverRemaining   float32
	ChargeDirAngle     float32 // radians; locked at windup-end
	LineTelegraphNetID uint32
	ChargeHit          bool

	// PVE v3: Brawler special-attack state. SpecialCooldown ticks down each
	// frame between specials; SpecialWindup > 0 means a telegraph is in
	// flight; SpecialDirAngle is the locked aim direction; SpecialTelegraphNetID
	// is the in-flight LineTelegraph entity netID so we can despawn it on death.
	SpecialCooldown       float32
	SpecialWindup         float32
	SpecialDirAngle       float32
	SpecialTelegraphNetID uint32

	// PVE-tier (Support): countdown between heal casts. Independent of
	// SpecialCooldown so Support doesn't interfere with Brawler/Disruptor
	// state. Defaults to 0 on spawn — first heal fires as soon as a
	// target appears.
	HealCooldown float32

	// TargetNetID — the NPC's current engage target. Updated each tick by
	// the closest-enemy logic in tickEngage; consumed by dispatch methods
	// (tickEngage hitscan, tickCast for Artillery, etc.).
	TargetNetID uint32

	// PVE v2 (dungeon POI): periodic LOS recheck in Engage. LastLOSCheckAt
	// is the stage-time of the last LOS raycast — used to throttle the
	// recheck at AILosRecheckIntervalSec cadence. LOSLostAt is the stage-
	// time at which LOS first went blocked (0 if currently clear); when
	// (now - LOSLostAt) >= AILosLossDropSec the NPC drops the target and
	// returns to Idle. Both are local-only state (no net:"..." tag) but
	// ride the transfer codec so an NPC mid-recheck handed off across a
	// cell boundary keeps its timers.
	LastLOSCheckAt float32
	LOSLostAt      float32

	// PVE v3 (BossGuardian): bitmask of HP-fraction thresholds (indices
	// into Config.BossMainAddSpawnThresholds) that have already triggered
	// their escort-add spawn. Bit i set = threshold i has fired this life.
	// Cleared implicitly when the NPC is destroyed (each new spawn gets a
	// fresh NPCAI). Only consulted for ArchetypeBossGuardian.
	AddSpawnThresholdsHit uint8
}

// DungeonAnchor links an NPC (or chest) back to its owning POI/dungeon
// for leash + roster tracking. DungeonNetID is the network ID of the
// owning POI/dungeon entity. ChamberID identifies which chamber inside
// a multi-chamber dungeon the entity belongs to; combat POIs (treated
// as a degenerate single-chamber dungeon per spec §6.7) leave it 0.
type DungeonAnchor struct {
	DungeonNetID uint32
	ChamberID    uint16
}

// POI status — wire-replicated so clients can tint the marker.
const (
	POIStatusActive   uint8 = 0
	POIStatusCleared  uint8 = 1
	POIStatusCooldown uint8 = 2
)

// POI types — extensible. v1 only has Combat.
const (
	POITypeCombat uint8 = 0
)

// POI is a first-class entity component for points-of-interest. The
// marker is replicated to clients via Position + EntityKind + Status;
// AnchorRadius/LeashRadius/RosterDefIdx/ClearedAt are server-local.
type POI struct {
	Type         uint8 `net:"initial,u8"`
	Status       uint8 `net:"u8"`
	AnchorRadius float32
	LeashRadius  float32
	Tier         uint8 `net:"initial"` // 1..3, immutable per POI
	ClearedAt    int64 // unix nanos (server time, not ClusterClock)
	RosterDefIdx uint16
}

// PilotName stores the player's display name for network replication.
type PilotName struct {
	Name string `net:"initial"`
}

// Dungeon is the marker + state component for a dungeon POI (asteroid-cave
// system). One per dungeon entity (KindDungeon). The chamber state is
// tracked server-side in GameWorld.dungeonChambers — only the world-level
// info travels with the entity.
//
// Name and EntranceMask are the wire fields (initial-only — fixed at
// spawn). EntranceMask is a 16-bit perimeter bitmap: bit i is 1 if slot i
// of the 16-slot ring is a wall (occluded), 0 if it's an entrance gap.
// The client uses this to render entrance markers at the exact positions
// the server actually carved gaps in the perimeter wall — replaces the
// previously hardcoded south/NE/NW assumption that drifted out of sync
// with pickEntranceAngles.
//
// The remaining geometry (outer radius, entrance positions) are local-only
// because the client doesn't need them at the dungeon-marker level: the
// outer wall + entrance gaps are materialized as KindDungeonWall entities
// which clients render directly from their replicated Position/Rotation/
// Width/Height. EntranceCount + EntranceX/Y0..2 stay on the server for
// AI pathfinding and chamber assignment, and Seed reproduces the procgen
// layout after cross-cell transfer.
//
// (The current codec only supports string/u8/u16/u32/bool as `initial,...`
// encodings — see pkg/system/field_writers.go::initialWriterFor — so any
// f32 fields that need to reach the client must do so via snapshot
// encoding, a separate event, or be derived from sibling entities.)
type Dungeon struct {
	Name          string  `net:"initial"`
	EntranceMask  uint16  `net:"initial,u16"`
	OuterRadius   float32 `mmokit:"local"`
	EntranceCount uint8   `mmokit:"local"`
	EntranceX0    float32 `mmokit:"local"`
	EntranceY0    float32 `mmokit:"local"`
	EntranceX1    float32 `mmokit:"local"`
	EntranceY1    float32 `mmokit:"local"`
	EntranceX2    float32 `mmokit:"local"`
	EntranceY2    float32 `mmokit:"local"`
	Seed          uint64  `mmokit:"local"`
}

// DungeonWall is the marker component for a static rectangular cave wall.
//
// The wall's geometry (width/height) is carried by the entity's Collider,
// which the engine already replicates via the per-entity QSize binding
// (radius+width+height as quantized snapshot fields). The orientation
// comes from the entity's Rotation component, attached via a per-kind
// QAngle extra binding. The DungeonWall component itself is intentionally
// empty + local-only — it exists as a marker so server-side systems can
// distinguish "this is a wall" from other static colliders, and so the
// kind shows up in the SDK's entity-type enum.
//
// Width/Height are kept here (local-only) for ergonomic server access
// without going through the Collider component.
type DungeonWall struct {
	Width  float32 `mmokit:"local"`
	Height float32 `mmokit:"local"`
}

// Pathing caches the NavGrid-derived waypoint list for an NPC under
// MotionPathfind. Local-only — never replicated. The waypoint slices
// are reused across repaths (truncate-and-append) to avoid per-tick
// allocations.
type Pathing struct {
	WaypointsX   []float32 `mmokit:"local"`
	WaypointsY   []float32 `mmokit:"local"`
	CurrentIdx   int       `mmokit:"local"`
	TargetX      float32   `mmokit:"local"`
	TargetY      float32   `mmokit:"local"`
	RepathAt     float32   `mmokit:"local"`
	DungeonNetID uint32    `mmokit:"local"`
}

// Wander tags an entity for random wandering movement (load testing).
type Wander struct {
	Speed       float32 // base movement speed
	Timer       float32 // countdown until next direction change
	Interval    float32 // base seconds between direction changes
	TargetAngle float32 // heading to steer toward (radians)
	TurnRate    float32 // radians per second
}
