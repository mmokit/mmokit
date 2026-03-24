package system

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// StationNetHandler handles network serialization for station entities.
// Stations have no mutable type-specific state.
type StationNetHandler struct{}

func (h *StationNetHandler) EntityType() uint8 { return component.TypeStation }

func (h *StationNetHandler) HashSnapshot(hasher *SnapshotHasher, ctx *NetworkContext, entry spatial.Entry) {
	// No mutable type-specific state to hash.
}

func (h *StationNetHandler) Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry spatial.Entry) {
	state.TypeData = &gamepb.EntityState_Station{Station: &gamepb.StationState{}}
}
