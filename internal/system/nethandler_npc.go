package system

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// NpcNetHandler handles network serialization for NPC entities.
type NpcNetHandler struct{}

func (h *NpcNetHandler) EntityType() uint8 { return component.TypeNPC }

func (h *NpcNetHandler) HashSnapshot(hasher *SnapshotHasher, ctx *NetworkContext, entry spatial.Entry) {
	hashCombat(hasher, ctx.GW, entry.Entity)
}

func (h *NpcNetHandler) Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry spatial.Entry) {
	state.TypeData = &gamepb.EntityState_Npc{
		Npc: &gamepb.NpcState{
			Combat: serializeCombat(ctx.GW, entry.Entity),
		},
	}
}
