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
const ConfigVersion = 5

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

	// AI shared
	AggroDeescalationSec float32 `json:"aggro_deescalation_sec"`

	// Target lock
	LockOnTime         float32 `json:"lockOnTime"`         // seconds to achieve full lock
	LockOnRange        float32 `json:"lockOnRange"`        // max range to maintain lock
	LockMaxSlotsPlayer uint8   `json:"lock_max_slots_player"`
	LockMaxSlotsNPC    uint8   `json:"lock_max_slots_npc"`
	MiningLockTime     float32 `json:"miningLockTime"` // seconds to lock an asteroid

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
	POIAnchorRadius                float32 `json:"poi_anchor_radius"`
	POILeashRadius                 float32 `json:"poi_leash_radius"`
	POIPerCellProbability          float32 `json:"poi_per_cell_probability"`
	POIBeltClearance               float32 `json:"poi_belt_clearance"`
	POIStationClearance            float32 `json:"poi_station_clearance"`
	POIPlacementMargin             float32 `json:"poi_placement_margin"`
	POIBaseClearFlux               int32   `json:"poi_base_clear_flux"`
	POIPerKillFluxBonus            int32   `json:"poi_per_kill_flux_bonus"`
	StationCellPOIClearCooldown    int32   `json:"station_cell_poi_clear_cooldown_sec"`
	NonStationCellPOIClearCooldown int32   `json:"non_station_cell_poi_clear_cooldown_sec"`
}

// DefaultGameConfig returns sensible defaults for game balance.
func DefaultGameConfig() GameConfig {
	return GameConfig{
		Version:             ConfigVersion,
		AoIRadius:           100,
		MaxSpeed:            68,
		ShipThrust:          20,
		ShipTurnRate:        8.0,
		ShipTurnAccel:       8.0,
		ShipWidth:           2.0, // ship length (forward)
		ShipHeight:          1.0, // ship width (side)
		ShipHealth:          100,
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
		LootPickupRange:     3.0,
		BankMaxMass:         10000,
		NpcHealth:           100,
		NpcShield:           50,
		NpcShieldRegenRate:  1.0,
		NpcShieldRegenDelay: 3.0,
		NpcWidth:            1.7,
		NpcHeight:           0.83,

		// NPC archetypes — ranges scaled to fit the player's 100u AoI /
		// 50u LockOnRange. Aggro radius < AoI ensures the player can
		// always see an engaging NPC; WeaponRange ≤ player LockOnRange
		// keeps fire visible on the player's HUD.
		BrawlerHP: 400, BrawlerShield: 200, BrawlerMaxSpeed: 6, BrawlerTurnRate: 1.5,
		BrawlerPreferredRange: 30, BrawlerWeaponRange: 50, BrawlerAggroRadius: 30,
		BrawlerDamagePerShot: 2, BrawlerFireRate: 1.0,

		AggroDeescalationSec: 6,

		// Target lock
		LockOnTime:         2.0,
		LockOnRange:        50,
		LockMaxSlotsPlayer: 4,
		LockMaxSlotsNPC:    1,
		MiningLockTime:     1.5,

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
		POIAnchorRadius:                30,
		POILeashRadius:                 120,
		POIPerCellProbability:          0.3,
		POIBeltClearance:               40,
		POIStationClearance:            400,
		POIPlacementMargin:             200,
		POIBaseClearFlux:               500,
		POIPerKillFluxBonus:            100,
		StationCellPOIClearCooldown:    180,
		NonStationCellPOIClearCooldown: 600,
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
