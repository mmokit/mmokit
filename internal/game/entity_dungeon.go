package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DungeonBundle is the entity-kind bundle for a dungeon POI.
//
// Only the world-level Dungeon component travels with the entity — chamber
// state (clear/cooldown, mob roster, terminal-boss progress) is kept
// server-side in GameWorld.dungeonChambers keyed by the dungeon's netID.
type DungeonBundle struct {
	Dungeon *gamecomp.Dungeon
}

// SpawnDungeon creates the dungeon marker entity at the given local
// position with the given dungeon-state values. Returns the entity's
// NetID.
//
// The caller is responsible for spawning the walls + chambers + NPCs —
// SpawnDungeon only creates the world-level marker. Procgen
// (Tasks 19-22) wraps this with the geometry + roster setup.
func (gw *GameWorld) SpawnDungeon(x, y float32, d gamecomp.Dungeon) uint32 {
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindDungeon},
		d,
	)
	gw.eng.Log.Log(CatDungeon, "dungeon: spawned netID=%d pos=(%.0f,%.0f) name=%q entrances=%d",
		e.NetID(), x, y, d.Name, d.EntranceCount)
	return e.NetID()
}
