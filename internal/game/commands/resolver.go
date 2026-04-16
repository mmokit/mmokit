package commands

import (
	"context"
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

// ExecOnLoop runs fn on gw's game loop and returns its result. Delegates to
// engine.RunOnLoop, which detects on-loop reentrance and runs fn inline when
// the caller is already the loop goroutine — preventing the nested-schedule
// deadlock that used to freeze the sim for 5 seconds whenever a cmdsys
// handler (already running on the loop) tried to post back to it.
func ExecOnLoop(gw *game.GameWorld, fn func() (any, error)) (any, error) {
	var val any
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := gw.Engine().RunOnLoop(ctx, func() error {
		v, e := fn()
		val = v
		return e
	})
	return val, err
}
