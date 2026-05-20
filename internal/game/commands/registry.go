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
	// gwGetter returns the first locally-hosted GameWorld. The world.*
	// verbs use it for shared WorldRepo / WorldSnapshot access — every
	// per-cell GameWorld points at the same disk-backed repo, so any
	// cell's view is equivalent for read+write of the manifest.
	gwGetter := func() *game.GameWorld {
		for _, c := range coord.Cells {
			if c == nil || c.Stage == nil {
				continue
			}
			if gw := gwForStage(coord, c.Stage); gw != nil {
				return gw
			}
		}
		return nil
	}

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
		func() error { return registerWorldList(reg, coord, gwGetter) },
		func() error { return registerWorldPlace(reg, coord, gwGetter) },
		func() error { return registerWorldMove(reg, coord, gwGetter) },
		func() error { return registerWorldUpdate(reg, coord, gwGetter) },
		func() error { return registerWorldDelete(reg, coord, gwGetter) },
		func() error { return registerWorldReload(reg, coord, gwGetter) },
		func() error { return registerWorldExport(reg, coord, gwGetter) },
	}
	for _, fn := range funcs {
		if err := fn(); err != nil {
			return fmt.Errorf("commands.RegisterAll: %w", err)
		}
	}
	return nil
}
