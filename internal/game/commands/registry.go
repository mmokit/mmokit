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
		func() error { return registerKill(reg, coord) },
		func() error { return registerGive(reg, coord) },
		func() error { return registerCurrency(reg, coord, playerDB, cfgPtr) },
	}
	for _, fn := range funcs {
		if err := fn(); err != nil {
			return fmt.Errorf("commands.RegisterAll: %w", err)
		}
	}
	return nil
}
