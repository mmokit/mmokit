package system

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ShipNetHandler handles network serialization for player ship entities.
type ShipNetHandler struct {
	gw *game.GameWorld
}

func (h *ShipNetHandler) EntityType() uint8 { return component.TypeShip }

func (h *ShipNetHandler) HashSnapshot(hasher *SnapshotHasher, ctx *NetworkContext, entry spatial.Entry) {
	gw := ctx.GW

	// Combat state
	hashCombat(hasher, gw, entry.Entity)

	// Pilot name (stable, but hash it for new-entity detection)
	if gw.C.PlayerConn.HasAll(entry.Entity) {
		connID := gw.C.PlayerConn.Get(entry.Entity).ConnID
		if username, ok := gw.Players.Usernames[connID]; ok {
			// Hash username length + first few bytes for cheap identity
			hasher.Uint32(uint32(len(username)))
		}
	}

	// Mining state
	if gw.C.MiningLaser.HasAll(entry.Entity) {
		laser := gw.C.MiningLaser.Get(entry.Entity)
		hasher.Bool(laser.Beams[0].Active)
		hasher.Bool(laser.Beams[1].Active)
		if (laser.Beams[0].Active || laser.Beams[1].Active) &&
			gw.ECS.Alive(laser.Target) && gw.C.NetworkID.HasAll(laser.Target) {
			hasher.Uint32(gw.C.NetworkID.Get(laser.Target).ID)
		} else {
			hasher.Uint32(0)
		}
	}
}

func (h *ShipNetHandler) Serialize(state *gamepb.EntityState, ctx *NetworkContext, entry spatial.Entry) {
	gw := ctx.GW

	ship := &gamepb.ShipState{
		Combat: serializeCombat(gw, entry.Entity),
	}

	// Pilot name
	if gw.C.PlayerConn.HasAll(entry.Entity) {
		connID := gw.C.PlayerConn.Get(entry.Entity).ConnID
		if username, ok := gw.Players.Usernames[connID]; ok {
			ship.PilotName = username
		}
	}

	// Mining state
	if gw.C.MiningLaser.HasAll(entry.Entity) {
		laser := gw.C.MiningLaser.Get(entry.Entity)
		anyActive := laser.Beams[0].Active || laser.Beams[1].Active
		ship.MiningActive = anyActive
		var mask uint32
		if laser.Beams[0].Active {
			mask |= 1
		}
		if laser.Beams[1].Active {
			mask |= 2
		}
		ship.MiningBeamMask = mask
		if anyActive && gw.ECS.Alive(laser.Target) && gw.C.NetworkID.HasAll(laser.Target) {
			ship.MiningTargetId = gw.C.NetworkID.Get(laser.Target).ID
		}
	}

	state.TypeData = &gamepb.EntityState_Ship{Ship: ship}
}
