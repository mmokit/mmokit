package system

import "github.com/zenion/mmoserver/internal/game"

// gwProvider is implemented by adapters that wrap *game.GameWorld (e.g. gameWorldAdapter).
type gwProvider interface {
	GW() *game.GameWorld
}

// unwrapGW extracts a *game.GameWorld from the value returned by SystemBase.GameWorld().
// It handles both the direct *game.GameWorld case and adapter wrappers that implement GW().
func unwrapGW(v any) *game.GameWorld {
	if gw, ok := v.(*game.GameWorld); ok {
		return gw
	}
	if p, ok := v.(gwProvider); ok {
		return p.GW()
	}
	panic("system: GameWorld is neither *game.GameWorld nor gwProvider")
}
