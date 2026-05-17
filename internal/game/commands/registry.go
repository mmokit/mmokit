package commands

import (
	"fmt"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// RegisterAll registers space-game admin commands. Generic
// player/entity/cell/cluster commands are registered by the engine via
// mmokit.RegisterBuiltins; only game-specific verbs (damage, heal, kill,
// give, currency) live here.
func RegisterAll(reg *cmdsys.Registry, coord *mmokit.Process, playerDB *game.PlayerRepo, cfg *game.GameConfig) error {
	cfgPtr := &cfg
	funcs := []func() error{
		func() error { return registerDamage(reg, coord) },
		func() error { return registerHeal(reg, coord) },
		func() error { return registerKill(reg) },
		func() error { return registerGive(reg, coord) },
		func() error { return registerCurrency(reg, coord, playerDB, cfgPtr) },
		func() error { return registerNPCSpawn(reg, coord) },
		func() error { return registerPOIList(reg, coord) },
		func() error { return registerPOIClear(reg, coord) },
		func() error { return registerPOISpawn(reg, coord) },
		func() error { return registerDungeonList(reg, coord) },
		func() error { return registerDungeonRespawn(reg, coord) },
		func() error { return registerDungeonRegenerate(reg, coord) },
		func() error { return registerDungeonSpawn(reg, coord) },
	}
	for _, fn := range funcs {
		if err := fn(); err != nil {
			return fmt.Errorf("commands.RegisterAll: %w", err)
		}
	}
	return nil
}
