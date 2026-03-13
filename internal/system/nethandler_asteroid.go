package system

import (
	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// AsteroidNetHandler handles network serialization for asteroid entities.
type AsteroidNetHandler struct{}

func (h *AsteroidNetHandler) EntityType() uint8 { return component.TypeAsteroid }

func (h *AsteroidNetHandler) HashSnapshot(hasher *SnapshotHasher, ctx *NetworkContext, entry spatial.Entry) {
	gw := ctx.GW
	if gw.MinableMap.HasAll(entry.Entity) {
		minable := gw.MinableMap.Get(entry.Entity)
		hasher.Uint8(minable.ResourceType)
		hasher.Float32(minable.Remaining)
	}
}

func (h *AsteroidNetHandler) Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry spatial.Entry) {
	gw := ctx.GW
	asteroid := &gamepb.AsteroidState{}
	if gw.MinableMap.HasAll(entry.Entity) {
		minable := gw.MinableMap.Get(entry.Entity)
		asteroid.ResourceType = gamepb.ResourceType(minable.ResourceType)
		asteroid.ResourceRemaining = minable.Remaining
	}
	state.TypeData = &gamepb.EntityState_Asteroid{Asteroid: asteroid}
}
