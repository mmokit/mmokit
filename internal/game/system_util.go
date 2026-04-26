package game

import "github.com/zenion/mmoserver/pkg/mmokit"

// gwFromSystem extracts a *GameWorld from the mmokit.GameWorld interface
// returned by SystemBase.GameWorld(). All game systems call this in Init().
func gwFromSystem(base mmokit.SystemBase[*GameWorld]) *GameWorld {
	return base.GameWorld().(*GameWorld)
}
