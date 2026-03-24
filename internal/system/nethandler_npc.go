package system

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NpcNetHandler handles network serialization for NPC entities.
type NpcNetHandler struct{}

func (h *NpcNetHandler) EntityType() uint8 { return component.TypeNPC }

func (h *NpcNetHandler) HashSnapshot(hasher *SnapshotHasher, ctx *NetworkContext, entry mmokit.SpatialEntry) {
	hashCombat(hasher, ctx.GW, entry.Entity)
}

func (h *NpcNetHandler) Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry mmokit.SpatialEntry) {
	state.TypeData = &gamepb.EntityState_Npc{
		Npc: &gamepb.NpcState{
			Combat: serializeCombat(ctx.GW, entry.Entity),
		},
	}
}
