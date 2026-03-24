package system

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// AsteroidNetHandler handles network serialization for asteroid entities.
type AsteroidNetHandler struct{}

func (h *AsteroidNetHandler) EntityType() uint8 { return component.TypeAsteroid }

func (h *AsteroidNetHandler) HashSnapshot(hasher *SnapshotHasher, ctx *NetworkContext, entry mmokit.SpatialEntry) {
	gw := ctx.GW
	if gw.C.Minable.HasAll(entry.Entity) {
		minable := gw.C.Minable.Get(entry.Entity)
		hasher.Uint32(minable.ItemID)
		hasher.Float32(minable.Remaining)
	}
}

func (h *AsteroidNetHandler) Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry mmokit.SpatialEntry) {
	gw := ctx.GW
	asteroid := &gamepb.AsteroidState{}
	if gw.C.Minable.HasAll(entry.Entity) {
		minable := gw.C.Minable.Get(entry.Entity)
		asteroid.ItemId = minable.ItemID
		asteroid.ResourceRemaining = minable.Remaining
	}
	state.TypeData = &gamepb.EntityState_Asteroid{Asteroid: asteroid}
}
