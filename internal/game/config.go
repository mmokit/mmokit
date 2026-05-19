package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"

	gamepersist "github.com/zenion/mmoserver/internal/persist"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ConfigVersion tracks breaking config changes. Bump this when defaults change
// in a way that is incompatible with saved configs (e.g. unit rescale).
// When the saved version doesn't match, defaults are used and re-saved.
const ConfigVersion = 13

// GameConfig holds all tunable game parameters.
type GameConfig struct {
	Version             int     `json:"version"`
	AoIRadius           float32 `json:"aoiRadius"`
	MaxSpeed            float32 `json:"maxSpeed"`
	ShipThrust          float32 `json:"shipThrust"`
	ShipTurnRate        float32 `json:"shipTurnRate"`  // max angular velocity, rad/s
	ShipTurnAccel       float32 `json:"shipTurnAccel"` // angular acceleration, rad/s^2
	ShipWidth           float32 `json:"shipWidth"`
	ShipHeight          float32 `json:"shipHeight"`
	ShipHealth          float32 `json:"shipHealth"`
	ShipShield          float32 `json:"shipShield"`
	ShieldRegenRate     float32 `json:"shieldRegenRate"`
	ShieldRegenDelay    float32 `json:"shieldRegenDelay"`
	AsteroidMinRadius   float32 `json:"asteroidMinRadius"`
	AsteroidMaxRadius   float32 `json:"asteroidMaxRadius"`
	AsteroidCount       int     `json:"asteroidCount"`
	MaxCargo            float32 `json:"maxCargo"`
	SellRange           float32 `json:"sellRange"`
	StationRadius       float32 `json:"stationRadius"`
	LootCrateRadius     float32 `json:"lootCrateRadius"`
	LootCrateLifetime   float32 `json:"lootCrateLifetime"`
	// RepairCostPerHP is the credit cost to restore one HP at a station.
	// Total cost = (MaxHP − CurrentHP) × RepairCostPerHP, rounded up.
	// 0 disables the repair charge (free repairs).
	RepairCostPerHP     float32 `json:"repairCostPerHp"`
	LootPickupRange     float32 `json:"lootPickupRange"`
	BankMaxMass         float32 `json:"bankMaxMass"` // station bank mass limit
	NpcHealth           float32 `json:"npcHealth"`
	NpcShield           float32 `json:"npcShield"`
	NpcShieldRegenRate  float32 `json:"npcShieldRegenRate"`
	NpcShieldRegenDelay float32 `json:"npcShieldRegenDelay"`
	NpcWidth            float32 `json:"npcWidth"`
	NpcHeight           float32 `json:"npcHeight"`

	// NPC archetypes
	BrawlerHP             float32 `json:"brawler_hp"`
	BrawlerShield         float32 `json:"brawler_shield"`
	BrawlerMaxSpeed       float32 `json:"brawler_max_speed"`
	BrawlerTurnRate       float32 `json:"brawler_turn_rate"`
	BrawlerPreferredRange float32 `json:"brawler_preferred_range"`
	BrawlerWeaponRange    float32 `json:"brawler_weapon_range"`
	BrawlerAggroRadius    float32 `json:"brawler_aggro_radius"`
	BrawlerDamagePerShot  float32 `json:"brawler_damage_per_shot"`
	BrawlerFireRate       float32 `json:"brawler_fire_rate"`

	BrawlerSpecialCooldown   float32 `json:"brawler_special_cooldown"`
	BrawlerSpecialWindupTime float32 `json:"brawler_special_windup_time"`
	BrawlerSpecialLength     float32 `json:"brawler_special_length"`
	BrawlerSpecialHalfWidth  float32 `json:"brawler_special_half_width"`
	BrawlerSpecialDamage     float32 `json:"brawler_special_damage"`

	ArtilleryHP              float32 `json:"artillery_hp"`
	ArtilleryShield          float32 `json:"artillery_shield"`
	ArtilleryMaxSpeed        float32 `json:"artillery_max_speed"`
	ArtilleryTurnRate        float32 `json:"artillery_turn_rate"`
	ArtilleryWeaponRange     float32 `json:"artillery_weapon_range"`
	ArtilleryAggroRadius     float32 `json:"artillery_aggro_radius"`
	ArtilleryAoERadius       float32 `json:"artillery_aoe_radius"`
	ArtilleryAoEDamage       float32 `json:"artillery_aoe_damage"`
	ArtilleryCastTime        float32 `json:"artillery_cast_time"`
	ArtilleryCastCooldown    float32 `json:"artillery_cast_cooldown"`
	ArtilleryInterruptDamage float32 `json:"artillery_interrupt_damage"`

	LancerHP           float32 `json:"lancer_hp"`
	LancerShield       float32 `json:"lancer_shield"`
	LancerMaxSpeed     float32 `json:"lancer_max_speed"`
	LancerTurnRate     float32 `json:"lancer_turn_rate"`
	LancerAggroRadius  float32 `json:"lancer_aggro_radius"`
	LancerLockRange    float32 `json:"lancer_lock_range"`
	LancerLanceRange   float32 `json:"lancer_lance_range"`
	LancerWindupTime   float32 `json:"lancer_windup_time"`
	LancerChargeSpeed  float32 `json:"lancer_charge_speed"`
	LancerChargeTime   float32 `json:"lancer_charge_time"`
	LancerChargeWidth  float32 `json:"lancer_charge_width"`
	LancerChargeDamage float32 `json:"lancer_charge_damage"`
	LancerRecoverTime  float32 `json:"lancer_recover_time"`

	// AI shared
	AggroDeescalationSec float32 `json:"aggro_deescalation_sec"`
	// NPCAttackJitter is the random fraction applied to every NPC attack
	// cooldown (initial spawn + post-cast resets). 0.4 means "actual
	// cooldown = base × (1 ± 0.4)" — sampled fresh each time so multiple
	// NPCs of the same archetype don't fire their abilities in unison.
	NPCAttackJitter float32 `json:"npc_attack_jitter"`

	// AILosRecheckIntervalSec throttles how often an engaging NPC re-tests
	// LOS to its current target. Defaults to 0.5s — frequent enough that a
	// target ducking behind a wall is detected quickly, but cheap enough
	// that we don't raycast every tick.
	AILosRecheckIntervalSec float32 `json:"ai_los_recheck_interval_sec"`
	// AILosLossDropSec is how long LOS must stay continuously blocked before
	// an engaging NPC drops its target and returns to Idle. Defaults to 3.0s
	// so quick obscurations (sweeping past a pillar, brief overlap with a
	// friendly ship) don't break aggro.
	AILosLossDropSec float32 `json:"ai_los_loss_drop_sec"`

	// LockLosLossBreakSec is how long a player's Selection may have its LOS
	// to the selected entity continuously blocked by a LayerStatic collider
	// before SelectionLOSSystem auto-clears the selection. Mirrors the AI
	// LOS-loss latch but on the player side; mining beams and other
	// Selection-driven abilities stop the moment the selection clears.
	LockLosLossBreakSec float32 `json:"lock_los_loss_break_sec"`


	// Docking
	DockTime         float32 `json:"dockTime"`         // seconds to complete docking
	DockRange        float32 `json:"dockRange"`        // max distance to initiate docking
	DockPullStrength float32 `json:"dockPullStrength"` // acceleration toward station during docking
	DockDragCoeff    float32 `json:"dockDragCoeff"`    // exponential drag during docking

	// Click-to-move
	MoveArrivalDist float32 `json:"moveArrivalDist"` // stop thrusting within this distance
	MoveDecelDist   float32 `json:"moveDecelDist"`   // start reducing thrust at this distance
	ShipDragCoeff   float32 `json:"shipDragCoeff"`   // linear drag coefficient (higher = snappier stops)

	// Persistence
	PersistFlushInterval  float32 `json:"persistFlushInterval"`  // seconds between dirty player flushes
	DisconnectGracePeriod float32 `json:"disconnectGracePeriod"` // seconds to keep entity alive after disconnect
	RespawnGraceSec       float32 `json:"respawnGraceSec"`       // seconds player stays on death screen before auto-respawn

	// Marketplace
	MarketTaxPct         float64 `json:"marketTaxPct"`         // transaction tax (default 0.02 = 2%)
	MarketOrderExpiry    float64 `json:"marketOrderExpiry"`    // hours until expiry (default 168 = 7 days)
	MarketMinPrice       int64   `json:"marketMinPrice"`       // min price per unit (default 1)
	MarketMaxOrders      int     `json:"marketMaxOrders"`      // max active orders per player (default 50)
	SettlementCurrencyID uint32  `json:"settlementCurrencyID"` // item ID of marketplace settlement currency

	// Server meshing
	StationCell mmokit.CellCoord `json:"stationCell"` // cell where station spawns and players respawn
	MeshCellsX  uint32           `json:"meshCellsX"`  // number of cells wide
	MeshCellsY  uint32           `json:"meshCellsY"`  // number of cells tall

	// POI
	StationPOIOffsetX float32 `json:"station_poi_offset_x"`
	StationPOIOffsetY float32 `json:"station_poi_offset_y"`

	POIAnchorRadius                float32 `json:"poi_anchor_radius"`
	POILeashRadius                 float32 `json:"poi_leash_radius"`
	POIPerCellProbability          float32 `json:"poi_per_cell_probability"`
	POIBeltClearance               float32 `json:"poi_belt_clearance"`
	POIStationClearance            float32 `json:"poi_station_clearance"`
	POIPlacementMargin             float32 `json:"poi_placement_margin"`
	POIBaseClearFlux               int32   `json:"poi_base_clear_flux"`
	POIPerKillFluxBonus            int32   `json:"poi_per_kill_flux_bonus"`
	POIIntraCellClearance          float32 `json:"poi_intra_cell_clearance"`
	StationCellPOIClearCooldown    int32   `json:"station_cell_poi_clear_cooldown_sec"`
	NonStationCellPOIClearCooldown int32   `json:"non_station_cell_poi_clear_cooldown_sec"`

	// Dungeon procgen
	DungeonAsteroidRadius   float32 `json:"dungeon_asteroid_radius"`
	DungeonChamberCountMin  int     `json:"dungeon_chamber_count_min"`
	DungeonChamberCountMax  int     `json:"dungeon_chamber_count_max"`
	DungeonChamberRadiusMin float32 `json:"dungeon_chamber_radius_min"`
	DungeonChamberRadiusMax float32 `json:"dungeon_chamber_radius_max"`
	DungeonCorridorWidth    float32 `json:"dungeon_corridor_width"`
	DungeonEntranceCount    int     `json:"dungeon_entrance_count"`
	DungeonWallThickness    float32 `json:"dungeon_wall_thickness"`
	DungeonTestsiteCellX    int     `json:"dungeon_testsite_cell_x"`
	DungeonTestsiteCellY    int     `json:"dungeon_testsite_cell_y"`
	DungeonTestsiteOffsetX  float32 `json:"dungeon_testsite_offset_x"`
	DungeonTestsiteOffsetY  float32 `json:"dungeon_testsite_offset_y"`

	// Per-chamber cooldowns (seconds)
	ChamberCooldownMobPack  float32 `json:"chamber_cooldown_mob_pack"`
	ChamberCooldownSideBoss float32 `json:"chamber_cooldown_side_boss"`
	ChamberCooldownTerminal float32 `json:"chamber_cooldown_terminal"`

	// Boss stats
	BossSoloHPMultiplier       float32   `json:"boss_solo_hp_multiplier"`
	BossSoloDmgMultiplier      float32   `json:"boss_solo_dmg_multiplier"`
	BossSoloSpeedMultiplier    float32   `json:"boss_solo_speed_multiplier"`
	BossMainHPMultiplier       float32   `json:"boss_main_hp_multiplier"`
	BossMainDmgMultiplier      float32   `json:"boss_main_dmg_multiplier"`
	BossMainAddSpawnThresholds []float32 `json:"boss_main_add_spawn_thresholds"`

	// EliteStatMultiplier scales HP / DamagePerShot / MaxSpeed for the
	// ArchetypeEliteBrawler / ArchetypeEliteArtillery / ArchetypeEliteLancer
	// variants used by T3 (and beyond) tiered rosters. Applied at
	// archetypeDefaults() time, before any NPCSpawnModifiers (Elite/HPMul/
	// DmgMul/ShieldMul) — so a tiered Elite Lancer in an EliteAnchor roster
	// stacks: base × EliteStatMultiplier × tier StatMultiplier.
	EliteStatMultiplier float32 `json:"elite_stat_multiplier"`

	// Loot
	ChamberMobPackFluxBase      float32 `json:"chamber_mob_pack_flux_base"`
	ChamberSideBossFluxBase     float32 `json:"chamber_side_boss_flux_base"`
	ChamberTerminalBossFluxBase float32 `json:"chamber_terminal_boss_flux_base"`

	// Pathfinding
	NavGridCellSize                float32 `json:"nav_grid_cell_size"`
	PathRepathIntervalSec          float32 `json:"path_repath_interval_sec"`
	PathRepathTargetMovedThreshold float32 `json:"path_repath_target_moved_threshold"`
}

// DefaultGameConfig returns sensible defaults for game balance.
func DefaultGameConfig() GameConfig {
	return GameConfig{
		Version:             ConfigVersion,
		AoIRadius:           500, // bumped from 100 — old value made 80u dungeon asteroid invisible until you were inside it
		MaxSpeed:            68,
		ShipThrust:          20,
		ShipTurnRate:        8.0,
		ShipTurnAccel:       8.0,
		ShipWidth:           2.0, // ship length (forward)
		ShipHeight:          1.0, // ship width (side)
		ShipHealth:          50,
		ShipShield:          0,
		ShieldRegenRate:     1.7,
		ShieldRegenDelay:    2.0,
		AsteroidMinRadius:   0.7,
		AsteroidMaxRadius:   2.0,
		AsteroidCount:       150,
		MaxCargo:            250,
		SellRange:           8.3,
		StationRadius:       5.0,
		LootCrateRadius:     0.4,
		LootCrateLifetime:   60.0,
		RepairCostPerHP:     1.0,
		LootPickupRange:     5.0,
		BankMaxMass:         10000,
		NpcHealth:           100,
		NpcShield:           50,
		NpcShieldRegenRate:  1.0,
		NpcShieldRegenDelay: 3.0,
		NpcWidth:            1.7,
		NpcHeight:           0.83,

		// NPC archetypes — ranges scaled to fit the player's 100u AoI.
		// Aggro radius < AoI ensures the player can always see an engaging
		// NPC; WeaponRange ≤ AoI keeps fire visible on the player's HUD.
		BrawlerHP: 120, BrawlerShield: 60, BrawlerMaxSpeed: 6, BrawlerTurnRate: 1.5,
		BrawlerPreferredRange: 30, BrawlerWeaponRange: 50, BrawlerAggroRadius: 30,
		BrawlerDamagePerShot: 8, BrawlerFireRate: 1.0,

		BrawlerSpecialCooldown:   6.0,
		BrawlerSpecialWindupTime: 0.8,
		BrawlerSpecialLength:     50,
		BrawlerSpecialHalfWidth:  5,
		BrawlerSpecialDamage:     25,

		ArtilleryHP: 80, ArtilleryShield: 40, ArtilleryMaxSpeed: 4, ArtilleryTurnRate: 1.5,
		ArtilleryWeaponRange: 70, ArtilleryAggroRadius: 80,
		ArtilleryAoERadius: 12, ArtilleryAoEDamage: 50,
		ArtilleryCastTime: 3.5, ArtilleryCastCooldown: 3.0, ArtilleryInterruptDamage: 25,

		// Lancer — telegraphed charge: stalk to mid range, freeze with a
		// visible line telegraph, then dash through fast. Heavy damage on
		// contact. Counter-play is sidestep during telegraph + punish
		// during the recovery window. Windup + recover are both stretched
		// so the charge cadence feels readable rather than spammy, and the
		// jitter knob below desyncs multiple Lancers engaging the same
		// player.
		LancerHP: 60, LancerShield: 20, LancerMaxSpeed: 14, LancerTurnRate: 2.5,
		LancerAggroRadius: 60, LancerLockRange: 40, LancerLanceRange: 30,
		LancerWindupTime: 1.5, LancerChargeSpeed: 50, LancerChargeTime: 0.8,
		LancerChargeWidth: 3, LancerChargeDamage: 35, LancerRecoverTime: 2.5,

		AggroDeescalationSec: 6,
		NPCAttackJitter:      0.4,

		AILosRecheckIntervalSec: 0.5,
		AILosLossDropSec:        3.0,
		LockLosLossBreakSec:     1.0,

		// Docking
		DockTime:         3.0,
		DockRange:        13.3,
		DockPullStrength: 25.0,
		DockDragCoeff:    4.0,

		// Click-to-move
		MoveArrivalDist: 2.7,
		MoveDecelDist:   10.0,
		ShipDragCoeff:   1.5,

		// Persistence
		PersistFlushInterval:  15.0, // seconds
		DisconnectGracePeriod: 30.0, // seconds
		RespawnGraceSec:       5.0,  // seconds death screen before auto-respawn

		// Marketplace
		MarketTaxPct:         0.02,
		MarketOrderExpiry:    168, // hours (7 days)
		MarketMinPrice:       1,
		MarketMaxOrders:      50,
		SettlementCurrencyID: 1, // Credits

		// Server meshing
		StationCell: mmokit.CellCoord{CellX: 0, CellY: 0}, // adjacent to the cross-corner mesh-test belt
		MeshCellsX:  3,
		MeshCellsY:  3,

		// POI
		StationPOIOffsetX:              0,
		StationPOIOffsetY:              -200,
		POIAnchorRadius:                30,
		POILeashRadius:                 120,
		POIPerCellProbability:          0.3,
		POIBeltClearance:               40,
		POIStationClearance:            400,
		POIPlacementMargin:             200,
		POIBaseClearFlux:               500,
		POIPerKillFluxBonus:            100,
		POIIntraCellClearance:          1000,
		StationCellPOIClearCooldown:    180,
		NonStationCellPOIClearCooldown: 600,

		// Dungeon procgen — scaled to match the game's actual world scale
		// (ships are ~2u wide, asteroids 0.7-2u, station radius 5, AoI 100u,
		// Brawler aggro 30u). The original spec used 1800u radii which was
		// ~900× too large — the player couldn't see the asteroid at all
		// because AoI queries by entity center and 1800u puts the entity
		// center far past every reasonable AoI.
		DungeonAsteroidRadius:   80,
		DungeonChamberCountMin:  5,
		DungeonChamberCountMax:  8,
		DungeonChamberRadiusMin: 10,
		DungeonChamberRadiusMax: 18,
		DungeonCorridorWidth:    5,
		DungeonEntranceCount:    3,
		DungeonWallThickness:    1.5,
		DungeonTestsiteCellX:    0,
		DungeonTestsiteCellY:    0,
		DungeonTestsiteOffsetX:  0,
		DungeonTestsiteOffsetY:  -580, // asteroid center 580u north of station; entrance (south side, radius 80) ~500u north of station

		// Per-chamber cooldowns
		ChamberCooldownMobPack:  1800,
		ChamberCooldownSideBoss: 2700,
		ChamberCooldownTerminal: 3600,

		// Boss stats
		BossSoloHPMultiplier:       3.0,
		BossSoloDmgMultiplier:      1.5,
		BossSoloSpeedMultiplier:    1.2,
		BossMainHPMultiplier:       10.0,
		BossMainDmgMultiplier:      2.0,
		BossMainAddSpawnThresholds: []float32{0.75, 0.5, 0.25},

		EliteStatMultiplier: 1.3,

		// Loot
		ChamberMobPackFluxBase:      200,
		ChamberSideBossFluxBase:     1500,
		ChamberTerminalBossFluxBase: 6000,

		// Pathfinding
		NavGridCellSize:                30,
		PathRepathIntervalSec:          1.5,
		PathRepathTargetMovedThreshold: 50,
	}
}

// LoadConfig loads the game config via the repository. If no config
// exists yet, returns the defaults and saves them. If the saved
// version doesn't match ConfigVersion, discards the saved config and
// re-saves the defaults.
func LoadConfig(ctx context.Context, repo gamepersist.ConfigRepository) (GameConfig, error) {
	snap, err := repo.Load(ctx)
	if err != nil {
		if errors.Is(err, gamepersist.ErrNotFound) {
			cfg := DefaultGameConfig()
			if saveErr := SaveConfig(ctx, repo, &cfg); saveErr != nil {
				return cfg, fmt.Errorf("save default config: %w", saveErr)
			}
			return cfg, nil
		}
		return GameConfig{}, fmt.Errorf("load config: %w", err)
	}

	// Start from defaults so any new fields added in code get their default values
	cfg := DefaultGameConfig()
	if err := json.Unmarshal(snap.Data, &cfg); err != nil {
		return GameConfig{}, fmt.Errorf("unmarshal config: %w", err)
	}

	// Discard saved config if version doesn't match (e.g. after unit rescale)
	if cfg.Version != ConfigVersion {
		log.Printf("config version mismatch (saved=%d, current=%d) — using defaults", cfg.Version, ConfigVersion)
		cfg = DefaultGameConfig()
		if saveErr := SaveConfig(ctx, repo, &cfg); saveErr != nil {
			return cfg, fmt.Errorf("save upgraded config: %w", saveErr)
		}
	}
	return cfg, nil
}

// SaveConfig persists the game config via the repository synchronously.
func SaveConfig(ctx context.Context, repo gamepersist.ConfigRepository, cfg *GameConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return repo.Save(ctx, &gamepersist.ConfigSnapshot{
		Data:    data,
		Version: int64(cfg.Version),
	})
}

// Fields returns all config field names in declaration order.
func (c *GameConfig) Fields() []string {
	t := reflect.TypeOf(*c)
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		names = append(names, t.Field(i).Name)
	}
	return names
}

// GetField returns a field's value as a string, or an error if not found.
func (c *GameConfig) GetField(name string) (string, error) {
	v := reflect.ValueOf(c).Elem()
	f := v.FieldByNameFunc(func(n string) bool {
		return strings.EqualFold(n, name)
	})
	if !f.IsValid() {
		return "", fmt.Errorf("unknown config field: %s", name)
	}
	return fmt.Sprintf("%v", f.Interface()), nil
}

// SetField sets a config field by name. Supports int, float32, float64, and string fields.
// Array fields like SellPrices are not settable through this interface.
func (c *GameConfig) SetField(name, value string) error {
	v := reflect.ValueOf(c).Elem()
	f := v.FieldByNameFunc(func(n string) bool {
		return strings.EqualFold(n, name)
	})
	if !f.IsValid() {
		return fmt.Errorf("unknown config field: %s", name)
	}
	if !f.CanSet() {
		return fmt.Errorf("cannot set field: %s", name)
	}

	switch f.Kind() {
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int: %s", value)
		}
		f.SetInt(n)
	case reflect.Float32:
		n, err := strconv.ParseFloat(value, 32)
		if err != nil {
			return fmt.Errorf("invalid float: %s", value)
		}
		f.SetFloat(n)
	case reflect.Float64:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float: %s", value)
		}
		f.SetFloat(n)
	case reflect.String:
		f.SetString(value)
	default:
		return fmt.Errorf("unsupported field type: %s (%s)", name, f.Kind())
	}
	return nil
}
