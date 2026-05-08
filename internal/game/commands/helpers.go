package commands

import (
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// gwForStage returns the per-stage *game.GameWorld registered via
// mmokit.AddState. Returns nil only if the stage is nil; callers that get
// nil from a non-nil stage have hit a programmer error (the type wasn't
// registered).
func gwForStage(coord *mmokit.Process, stage *mmokit.Stage) *game.GameWorld {
	_ = coord
	if stage == nil {
		return nil
	}
	return mmokit.State[game.GameWorld](stage)
}
