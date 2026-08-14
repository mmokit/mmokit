package game

import (
	"math"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
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
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindLootCrate},
		mmokit.Collider{Radius: gw.Config.LootCrateRadius},
		gamecomp.Inventory{Items: items, MaxMass: math.MaxFloat32},
		mmokit.Lifetime{Remaining: gw.Config.LootCrateLifetime},
		gamecomp.LootCrate{},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", e.NetID(), x, y)
}
