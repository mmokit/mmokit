package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// SpawnLootCrate creates a loot crate entity with the given cargo.
func (gw *GameWorld) SpawnLootCrate(x, y float32, items map[uint32]int32) {
	entity := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.TypeLootCrate),
		mmokit.WithCollider(gw.Config.LootCrateRadius),
		mmokit.WithComponents(),
	)

	*gw.C.Inventory.Get(entity) = gamecomp.Inventory{Items: items, MaxMass: math.MaxFloat32}
	gw.C.Lifetime.Get(entity).Remaining = gw.Config.LootCrateLifetime

	netID := gw.C.NetworkID.Get(entity).ID
	gw.eng.Log.Log(CatPlayerSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
