package item

import (
	"sort"
	"sync"
)

// ItemCategory classifies items for filtering and gameplay logic.
type ItemCategory uint8

const (
	CategoryCurrency ItemCategory = iota
	CategoryResource
	CategoryEquipment
	CategoryConsumable // future
	CategoryModule     // future
)

// EquipSlot identifies which equipment slot an item can be placed in.
type EquipSlot uint8

const (
	SlotNone     EquipSlot = 0
	SlotWeapon1  EquipSlot = 1 // physical slot: Q + W abilities
	SlotWeapon2  EquipSlot = 2 // physical slot: E + R abilities
	SlotShield   EquipSlot = 3 // D ability
	SlotThruster EquipSlot = 4 // F ability
	SlotWeapon   EquipSlot = 5 // item category: fits weapon1 or weapon2
)

// SlotCompatible returns true if an item with the given EquipSlot can be placed in the target physical slot.
func SlotCompatible(itemSlot, targetSlot EquipSlot) bool {
	if itemSlot == SlotWeapon {
		return targetSlot == SlotWeapon1 || targetSlot == SlotWeapon2
	}
	return itemSlot == targetSlot
}

// AbilityType identifies the behavior of an equipment-granted ability.
type AbilityType uint8

const (
	AbilityTypeNone AbilityType = 0

	// Weapon abilities
	AbilityTypePulseLaser    AbilityType = 1 // hitscan damage
	AbilityTypePulseBarrage  AbilityType = 2 // hitscan damage (burst)
	AbilityTypeRailShot      AbilityType = 3 // hitscan damage (long range)
	AbilityTypePiercingRound AbilityType = 4 // hitscan damage + bonus vs unshielded
	AbilityTypeIonBurn       AbilityType = 5 // DoT debuff
	AbilityTypeIonOverload   AbilityType = 6 // hitscan damage
	AbilityTypePlasmaBolt    AbilityType = 7 // hitscan damage
	AbilityTypePlasmaTorpedo AbilityType = 8 // hitscan damage + bonus vs unshielded

	// PVE v2 — travel-time / projectile weapons.
	AbilityTypePlasmaShot    AbilityType = 9  // travel-time projectile, single target
	AbilityTypeHomingMissile AbilityType = 10 // homing projectile, splash on impact
	AbilityTypeSustainedBeam AbilityType = 11 // channeled hitscan with arc lock (Task 21)
	AbilityTypeMortarShell   AbilityType = 12 // lobbed projectile with telegraphed splash

	// Shield abilities
	AbilityTypeEmergencyShield AbilityType = 20 // restore shield + Fortified buff
	AbilityTypeHardenedShield  AbilityType = 21 // restore shield + stronger Fortified buff

	// Thruster abilities
	AbilityTypeAfterburner AbilityType = 30 // speed multiplier
	AbilityTypeMicroWarp   AbilityType = 31 // speed multiplier (stronger, shorter)

	// Mining abilities
	AbilityTypeMiningBeam   AbilityType = 40 // toggle continuous mining
	AbilityTypeExtractPulse AbilityType = 41 // bonus extraction burst
)

// AbilityParams defines the stats for a single ability granted by equipment.
type AbilityParams struct {
	Type        AbilityType
	Name        string  // display name for the ability bar
	Damage      float32 // direct damage
	BonusDamage float32 // conditional bonus (e.g. vs unshielded)
	Range       float32 // ability range (0 = self-targeted)
	Cooldown    float32 // seconds
	DotDPS      float32 // damage per second for DoT abilities
	DotDuration float32 // duration of DoT

	// Shield ability fields
	ShieldRestore float32 // instant shield points restored
	DmgReduction  float32 // damage reduction fraction (e.g. 0.3 = 30%)
	BuffDuration  float32 // duration of defensive buff

	// Thruster ability fields
	SpeedMult     float32 // speed multiplier
	BoostDuration float32 // duration of speed boost

	// Mining ability fields
	MiningRate  float32 // units/sec for continuous extraction
	MiningRange float32 // max mining distance
	MiningYield float32 // bonus resource amount for extract pulse

	// Projectile / splash / homing fields (PVE v2)
	ProjectileSpeed   float32 // u/s; for projectile-firing abilities
	SplashRadius      float32 // u; 0 = single-target
	SplashDamage      float32 // damage applied at splash radius
	HomingMaxTurnRate float32 // rad/s; 0 = no homing
	RequiresLock      bool    // true = ability is refused (no cooldown) if no active TargetLock

	// Channel-related fields (used by Task 21's SustainedBeam handler).
	ChannelDuration float32 // seconds of max channel
	ChannelTickRate float32 // ticks per second of damage
	BeamHalfArcRad  float32 // ±arc tolerance from caster facing; lose channel if target leaves arc
}

// EquipData defines the abilities and passive stats granted by an equipment item.
type EquipData struct {
	Primary   AbilityParams  // Q for Weapon1, E for Weapon2, D for Shield, F for Thruster
	Secondary *AbilityParams // W for Weapon1, R for Weapon2 (nil for shield/thruster)

	// Passive stat bonuses (applied while equipped)
	ShieldMax       float32 // added to base shield
	ShieldRegenRate float32 // overrides base regen rate (0 = use base)
	ThrustBonus     float32 // added to base thrust
	MaxSpeedBonus   float32 // added to base max speed
}

// Well-known item IDs.
const CreditsItemID uint32 = 1

// IsCurrency returns true if the given item ID is a currency.
func IsCurrency(id uint32) bool {
	def := registry[id]
	return def != nil && def.Category == CategoryCurrency
}

// Starter equipment IDs.
const (
	StarterWeapon1     uint32 = 100 // Pulse Laser Array
	StarterMiningLaser uint32 = 130 // Mining Laser (Weapon2 slot for new players)
	StarterShield      uint32 = 110 // Standard Shield Gen
	StarterThruster    uint32 = 120 // Standard Thruster
)

// ItemDef defines a type of item in the game.
type ItemDef struct {
	ID          uint32
	Name        string
	Category    ItemCategory
	MassPerUnit float32    // mass contribution per unit of quantity
	EquipSlot   EquipSlot  // which equipment slot this fits (SlotNone for non-equipment)
	Equip       *EquipData // ability/stat data (nil for non-equipment items)
	Gaseous     bool       // if true, resource has no terrain collision (e.g. gas clouds)
}

var registry map[uint32]*ItemDef
var byName map[string]*ItemDef
var initOnce sync.Once

// Init populates the item registry. Safe to call multiple times — the
// registry is populated exactly once across the process, guarded by
// sync.Once. Calling Init from every new GameWorld (e.g. on every cell
// split) would otherwise rewrite the global `registry` map concurrently
// with readers on the source cell's game loop (item.MassOf hot path in
// Inventory.TotalMass), tripping Go's concurrent-map-write detector.
func Init() {
	initOnce.Do(doInit)
}

func doInit() {
	registry = make(map[uint32]*ItemDef)
	byName = make(map[string]*ItemDef)

	// --- Currency & Resources ---
	register(&ItemDef{ID: 1, Name: "Credits", Category: CategoryCurrency, MassPerUnit: 0})
	register(&ItemDef{ID: 2, Name: "Ore", Category: CategoryResource, MassPerUnit: 1.0})
	register(&ItemDef{ID: 3, Name: "Crystal", Category: CategoryResource, MassPerUnit: 2.5})
	register(&ItemDef{ID: 4, Name: "Gas", Category: CategoryResource, MassPerUnit: 0.5, Gaseous: true})
	register(&ItemDef{ID: 5, Name: "Metal", Category: CategoryResource, MassPerUnit: 5.0})

	// --- Weapons (SlotWeapon → fits Weapon1 or Weapon2) ---
	register(&ItemDef{
		ID: 100, Name: "Pulse Laser Array", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypePulseLaser, Name: "Pulse Shot",
				Damage: 30.0, Range: 30.0, Cooldown: 2.0,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypePulseBarrage, Name: "Pulse Barrage",
				Damage: 50.0, Range: 50.0, Cooldown: 5.0,
			},
		},
	})
	register(&ItemDef{
		ID: 101, Name: "Railgun System", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeRailShot, Name: "Rail Shot",
				Damage: 35, Range: 33.3, Cooldown: 6.0,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypePiercingRound, Name: "Piercing Round",
				Damage: 50, BonusDamage: 20, Range: 26.7, Cooldown: 10.0,
			},
		},
	})
	register(&ItemDef{
		ID: 105, Name: "Ion Array", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeIonBurn, Name: "Ion Burn",
				Range: 16.7, Cooldown: 8.0, DotDPS: 6.0, DotDuration: 4.0,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypeIonOverload, Name: "Ion Overload",
				Damage: 40, Range: 20.0, Cooldown: 12.0,
			},
		},
	})
	register(&ItemDef{
		ID: 106, Name: "Plasma System", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypePlasmaBolt, Name: "Plasma Bolt",
				Damage: 20, Range: 23.3, Cooldown: 4.0,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypePlasmaTorpedo, Name: "Plasma Torpedo",
				Damage: 60, BonusDamage: 30, Range: 30.0, Cooldown: 20.0,
			},
		},
	})
	// PVE v2 — projectile weapons. Pairs PlasmaShot (primary, fast
	// travel-time bolts) with HomingMissile (secondary, lock-required
	// homing splash munition).
	register(&ItemDef{
		ID: 107, Name: "Plasma Cannon", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypePlasmaShot, Name: "Plasma Shot",
				Damage: 25, Range: 700, Cooldown: 1.5,
				ProjectileSpeed: 700,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypeHomingMissile, Name: "Homing Missile",
				Damage: 80, Range: 1500, Cooldown: 8,
				ProjectileSpeed:   200,
				SplashRadius:      30,
				SplashDamage:      20,
				HomingMaxTurnRate: 2.09,
				RequiresLock:      true,
			},
		},
	})
	// PVE v2 — channeled + lobbed weapons. Pairs SustainedBeam
	// (primary; channeled, Task 21) with MortarShell (secondary; lobbed
	// splash munition with no lock requirement).
	register(&ItemDef{
		ID: 108, Name: "Beam-Mortar Battery", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeSustainedBeam, Name: "Sustained Beam",
				Damage: 4, Range: 500, Cooldown: 4,
				ChannelDuration: 3,
				ChannelTickRate: 10,
				BeamHalfArcRad:  0.5236,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypeMortarShell, Name: "Mortar Shell",
				Damage: 80, Range: 600, Cooldown: 6,
				ProjectileSpeed: 400,
				SplashRadius:    80,
				SplashDamage:    60,
			},
		},
	})

	// --- Mining Lasers (SlotWeapon → fits Weapon1 or Weapon2) ---
	register(&ItemDef{
		ID: 130, Name: "Mining Laser", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeMiningBeam, Name: "Mining Beam",
				MiningRate: 1.0, MiningRange: 10.0, Cooldown: 0,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypeExtractPulse, Name: "Extract Pulse",
				MiningYield: 2.0, MiningRange: 10.0, Cooldown: 3.0,
			},
		},
	})
	register(&ItemDef{
		ID: 131, Name: "Deep Core Mining Laser", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotWeapon,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeMiningBeam, Name: "Mining Beam",
				MiningRate: 2.5, MiningRange: 13.3, Cooldown: 0,
			},
			Secondary: &AbilityParams{
				Type: AbilityTypeExtractPulse, Name: "Extract Pulse",
				MiningYield: 5.0, MiningRange: 13.3, Cooldown: 2.5,
			},
		},
	})

	// --- Shield Generator (SlotShield → D) ---
	register(&ItemDef{
		ID: 110, Name: "Standard Shield Gen", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotShield,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeEmergencyShield, Name: "Emergency Shield",
				Cooldown: 15.0, ShieldRestore: 35, DmgReduction: 0.3, BuffDuration: 5.0,
			},
			ShieldMax: 50, ShieldRegenRate: 1.7,
		},
	})
	register(&ItemDef{
		ID: 111, Name: "Hardened Shield Gen", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotShield,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeHardenedShield, Name: "Hardened Shield",
				Cooldown: 20.0, ShieldRestore: 55, DmgReduction: 0.5, BuffDuration: 6.0,
			},
			ShieldMax: 75, ShieldRegenRate: 1.0,
		},
	})

	// --- Thruster (SlotThruster → F) ---
	register(&ItemDef{
		ID: 120, Name: "Standard Thruster", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotThruster,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeAfterburner, Name: "Afterburner",
				Cooldown: 10.0, SpeedMult: 2.5, BoostDuration: 1.5,
			},
		},
	})
	register(&ItemDef{
		ID: 121, Name: "Micro Warp Drive", Category: CategoryEquipment,
		MassPerUnit: 5.0, EquipSlot: SlotThruster,
		Equip: &EquipData{
			Primary: AbilityParams{
				Type: AbilityTypeMicroWarp, Name: "Micro Warp",
				Cooldown: 18.0, SpeedMult: 4.0, BoostDuration: 0.8,
			},
			ThrustBonus: 3.3, MaxSpeedBonus: 6.7,
		},
	})
}

func register(def *ItemDef) {
	registry[def.ID] = def
	byName[def.Name] = def
}

// Get returns the item definition for the given ID, or nil if not found.
func Get(id uint32) *ItemDef {
	return registry[id]
}

// GetByName returns the item definition for the given name, or nil if not found.
func GetByName(name string) *ItemDef {
	return byName[name]
}

// All returns all registered item definitions.
func All() []*ItemDef {
	items := make([]*ItemDef, 0, len(registry))
	for _, def := range registry {
		items = append(items, def)
	}
	return items
}

// AllEquipment returns all registered equipment item definitions.
func AllEquipment() []*ItemDef {
	items := make([]*ItemDef, 0)
	for _, def := range registry {
		if def.Category == CategoryEquipment {
			items = append(items, def)
		}
	}
	return items
}

// MassOf returns the mass per unit for the given item ID.
// Returns 1.0 as a safe default if the item is not found.
func MassOf(id uint32) float32 {
	if def := registry[id]; def != nil {
		return def.MassPerUnit
	}
	return 1.0
}

// ResourceIDs returns all item IDs with CategoryResource, sorted by ID.
func ResourceIDs() []uint32 {
	ids := make([]uint32, 0)
	for _, def := range registry {
		if def.Category == CategoryResource {
			ids = append(ids, def.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// IsResource returns true if the given item ID is a resource.
func IsResource(id uint32) bool {
	def := registry[id]
	return def != nil && def.Category == CategoryResource
}

// SlotToAbilitySlots returns the primary and secondary ability slot indices for an equipment slot.
// Returns (primary, secondary, hasSecondary).
func SlotToAbilitySlots(slot EquipSlot) (uint8, uint8, bool) {
	switch slot {
	case SlotWeapon1:
		return 0, 1, true // Q, W
	case SlotWeapon2:
		return 2, 3, true // E, R
	case SlotShield:
		return 4, 0, false // D
	case SlotThruster:
		return 5, 0, false // F
	default:
		return 0, 0, false
	}
}

// AbilitySlotToEquipSlot returns which equipment slot controls a given ability slot index.
// Returns (equipSlot, isPrimary).
func AbilitySlotToEquipSlot(abilitySlot uint8) (EquipSlot, bool) {
	switch abilitySlot {
	case 0: // Q
		return SlotWeapon1, true
	case 1: // W
		return SlotWeapon1, false
	case 2: // E
		return SlotWeapon2, true
	case 3: // R
		return SlotWeapon2, false
	case 4: // D
		return SlotShield, true
	case 5: // F
		return SlotThruster, true
	default:
		return SlotNone, false
	}
}
