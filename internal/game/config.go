package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/zenion/mmoserver/pkg/persist"
)

const (
	configCollection = "config"
	configKey        = "game"
)

// GameConfig holds all tunable game parameters.
type GameConfig struct {
	WorldWidth         float32    `json:"worldWidth"`
	WorldHeight        float32    `json:"worldHeight"`
	AoIRadius          float32    `json:"aoiRadius"`
	GridCellSize       float32    `json:"gridCellSize"`
	MaxSpeed           float32    `json:"maxSpeed"`
	ShipThrust         float32    `json:"shipThrust"`
	ShipTurnRate       float32    `json:"shipTurnRate"`
	ShipWidth          float32    `json:"shipWidth"`
	ShipHeight         float32    `json:"shipHeight"`
	ShipHealth         float32    `json:"shipHealth"`
	ShipShield         float32    `json:"shipShield"`
	ShieldRegenRate    float32    `json:"shieldRegenRate"`
	ShieldRegenDelay   float32    `json:"shieldRegenDelay"`
	WeaponDamage       float32    `json:"weaponDamage"`
	WeaponSpeed        float32    `json:"weaponSpeed"`
	WeaponFireRate     float32    `json:"weaponFireRate"`
	ProjectileLifetime float32    `json:"projectileLifetime"`
	AsteroidMinRadius  float32    `json:"asteroidMinRadius"`
	AsteroidMaxRadius  float32    `json:"asteroidMaxRadius"`
	AsteroidCount      int        `json:"asteroidCount"`
	MiningRange        float32    `json:"miningRange"`
	MiningRate         float32    `json:"miningRate"`
	MaxCargo           float32    `json:"maxCargo"`
	SellPrices         [4]float64 `json:"sellPrices"`
	SellRange          float32    `json:"sellRange"`
	StationRadius      float32    `json:"stationRadius"`
	LootCrateRadius    float32    `json:"lootCrateRadius"`
	LootCrateLifetime  float32    `json:"lootCrateLifetime"`
	LootPickupRange    float32    `json:"lootPickupRange"`
}

// DefaultGameConfig returns sensible defaults for game balance.
func DefaultGameConfig() GameConfig {
	return GameConfig{
		WorldWidth:         10000,
		WorldHeight:        10000,
		AoIRadius:          1500,
		GridCellSize:       512,
		MaxSpeed:           300,
		ShipThrust:         400,
		ShipTurnRate:       4.0,
		ShipWidth:          60, // ship length (forward)
		ShipHeight:         30, // ship width (side)
		ShipHealth:         100,
		ShipShield:         50,
		ShieldRegenRate:    1.7,
		ShieldRegenDelay:   2.0,
		WeaponDamage:       10,
		WeaponSpeed:        600,
		WeaponFireRate:     3,
		ProjectileLifetime: 2.0,
		AsteroidMinRadius:  20,
		AsteroidMaxRadius:  60,
		AsteroidCount:      200,
		MiningRange:        200,
		MiningRate:         5.0,
		MaxCargo:           100,
		SellPrices:         [4]float64{1.0, 3.0, 2.0, 5.0},
		SellRange:          250,
		StationRadius:      80,
		LootCrateRadius:    12,
		LootCrateLifetime:  60.0,
		LootPickupRange:    60,
	}
}

// LoadConfig loads the game config from the store. If no config exists,
// returns the defaults and saves them to the store.
func LoadConfig(store persist.Store) (GameConfig, error) {
	data, err := store.Get(configCollection, configKey)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			cfg := DefaultGameConfig()
			if saveErr := SaveConfig(store, &cfg); saveErr != nil {
				return cfg, fmt.Errorf("save default config: %w", saveErr)
			}
			return cfg, nil
		}
		return GameConfig{}, fmt.Errorf("load config: %w", err)
	}

	// Start from defaults so any new fields added in code get their default values
	cfg := DefaultGameConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return GameConfig{}, fmt.Errorf("unmarshal config: %w", err)
	}
	return cfg, nil
}

// SaveConfig persists the game config to the store synchronously.
func SaveConfig(store persist.Store, cfg *GameConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return store.Put(configCollection, configKey, data)
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
