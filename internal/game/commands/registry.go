package commands

import (
	"fmt"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// RegisterAll registers all game admin commands into the given cmdsys.Registry.
// cfg is a pointer to the GameConfig value so command handlers always read the current value.
func RegisterAll(reg *cmdsys.Registry, coord *mmokit.Process, playerDB *game.PlayerRepo, cfg *game.GameConfig) error {
	resolver := NewResolver(coord)

	// Wrap cfg as a double-pointer for the few handlers that need it.
	cfgPtr := &cfg

	funcs := []func() error{
		func() error { return registerPlayers(reg, resolver, playerDB, coord, cfgPtr) },
		func() error { return registerPlayerDetail(reg, resolver, playerDB, coord) },
		func() error { return registerDamage(reg, resolver) },
		func() error { return registerKill(reg, resolver) },
		func() error { return registerHeal(reg, resolver) },
		func() error { return registerKick(reg, resolver) },
		func() error { return registerTp(reg, resolver) },
		func() error { return registerGive(reg, resolver) },
		func() error { return registerCurrency(reg, resolver, playerDB, cfgPtr) },
		func() error { return registerSay(reg, resolver) },
		func() error { return registerNPCs(reg, resolver) },
		func() error { return registerTpTo(reg, resolver) },
		func() error { return registerSpawnNPCs(reg, resolver) },
	}

	for _, fn := range funcs {
		if err := fn(); err != nil {
			return fmt.Errorf("commands.RegisterAll: %w", err)
		}
	}
	return nil
}
