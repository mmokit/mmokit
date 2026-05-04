package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// LootCrateBundle is the entity-kind component bundle for loot crates.
// LootCrate marker is local-only — clients identify loot crates via the
// EntityKind; only the contained inventory + lifetime need to replicate.
type LootCrateBundle struct {
	Inventory *gamecomp.Inventory
	Lifetime  *mmokit.Lifetime
	LootCrate *gamecomp.LootCrate `mmokit:"local"`
}

// SpawnLootCrate creates a loot crate entity with the given cargo.
func (gw *GameWorld) SpawnLootCrate(x, y float32, items map[uint32]int32) {
	handle := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.TypeLootCrate),
		mmokit.WithCollider(gw.Config.LootCrateRadius),
		mmokit.WithComponents(),
	)
	entity := mmokit.EntityFromECS(gw.Stage, handle)

	mmokit.Set(entity, gamecomp.Inventory{Items: items, MaxMass: math.MaxFloat32})
	if lt := mmokit.Get[mmokit.Lifetime](entity); lt != nil {
		lt.Remaining = gw.Config.LootCrateLifetime
	}

	gw.eng.Log.Log(CatPlayerSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", entity.NetID(), x, y)
}
