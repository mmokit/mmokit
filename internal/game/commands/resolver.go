package commands

import (
	"fmt"
	"time"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Resolver provides helpers for game command handlers to locate the local GameWorld
// for a player or cell and schedule work on the game loop.
type Resolver struct {
	coord *mmokit.Coordinator
}

// NewResolver creates a Resolver backed by the given coordinator.
func NewResolver(coord *mmokit.Coordinator) *Resolver {
	return &Resolver{coord: coord}
}

// GameWorldForUser returns the local GameWorld hosting the named player, or nil
// if the player is not on a local cell. Command handlers trust that the dispatcher
// has already routed the request to the correct host.
func (r *Resolver) GameWorldForUser(username string) *game.GameWorld {
	for _, cell := range r.coord.Cells {
		gw := game.UnwrapGameWorld(cell.World)
		if gw == nil {
			continue
		}
		if gw.Players.ByUsername(username) != nil {
			return gw
		}
	}
	return nil
}

// AnyLocalWorld returns the first available local GameWorld, or nil if none.
func (r *Resolver) AnyLocalWorld() *game.GameWorld {
	for _, cell := range r.coord.Cells {
		if gw := game.UnwrapGameWorld(cell.World); gw != nil {
			return gw
		}
	}
	return nil
}

// AllLocalWorlds returns all local GameWorlds.
func (r *Resolver) AllLocalWorlds() []*game.GameWorld {
	var out []*game.GameWorld
	for _, cell := range r.coord.Cells {
		if gw := game.UnwrapGameWorld(cell.World); gw != nil {
			out = append(out, gw)
		}
	}
	return out
}

// ExecOnLoop posts fn to the game loop of gw and blocks for the result with a 5s timeout.
func ExecOnLoop(gw *game.GameWorld, fn func() (any, error)) (any, error) {
	type res struct {
		val any
		err error
	}
	ch := make(chan res, 1)
	eng := gw.Engine()
	eng.PendingAdminCmds <- func() {
		v, err := fn()
		ch <- res{v, err}
	}
	select {
	case r := <-ch:
		return r.val, r.err
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("game loop not responding (timeout)")
	}
}
