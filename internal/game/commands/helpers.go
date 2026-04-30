package commands

import (
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// gwForStage finds the *game.GameWorld whose Stage pointer matches the given
// stage. Returns nil if the stage is not hosted by a local cell or the cell's
// world is not a *GameWorld.
func gwForStage(coord *mmokit.Process, stage *mmokit.Stage) *game.GameWorld {
	for _, cell := range coord.Cells {
		if cell.Stage == stage {
			return game.UnwrapGameWorld(cell.World)
		}
	}
	return nil
}
