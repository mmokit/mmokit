package game

import (
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
)

// ShipNetHandler handles network serialization for player ship entities.
type ShipNetHandler struct {
	gw  *GameWorld
	ctx *gameNetContext
}

func (h *ShipNetHandler) EntityType() uint8 { return component.TypeShip }

func (h *ShipNetHandler) Hash(hasher *system.Hasher, viewer *system.ViewerInfo, entry spatial.Entry) {
	gw := h.gw

	// Base fields (position, velocity, rotation, locked-by)
	hashBaseFields(hasher, gw, h.ctx, viewer, entry)

	// Combat state
	hashCombat(hasher, gw, entry.Entity)

	// Pilot name
	if gw.C.PlayerConn.HasAll(entry.Entity) {
		connID := gw.C.PlayerConn.Get(entry.Entity).ConnID
		if sess := gw.Players.ByConnID(connID); sess != nil && sess.Username != "" {
			hasher.Uint32(uint32(len(sess.Username)))
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

func (h *ShipNetHandler) Snapshot(w *quantize.SnapshotWriter, viewer *system.ViewerInfo, entry spatial.Entry) {
	gw := h.gw

	// Base fields (21 bytes)
	snapshotBaseFields(w, gw, h.ctx, viewer, entry)

	// Health + shield raw values (8 bytes)
	snapshotCombat(w, gw, entry.Entity)

	// Mining flags packed into uint8: bit0 = beam0 active, bit1 = beam1 active
	var flags uint8
	var miningTargetID uint32
	if gw.C.MiningLaser.HasAll(entry.Entity) {
		laser := gw.C.MiningLaser.Get(entry.Entity)
		if laser.Beams[0].Active {
			flags |= 1
		}
		if laser.Beams[1].Active {
			flags |= 2
		}
		if (flags != 0) && gw.ECS.Alive(laser.Target) && gw.C.NetworkID.HasAll(laser.Target) {
			miningTargetID = gw.C.NetworkID.Get(laser.Target).ID
		}
	}
	w.Uint8(flags)
	w.Uint32(miningTargetID)

	// Combat target ID (from CombatState if present — placeholder 0 for now)
	var combatTargetID uint32
	w.Uint32(combatTargetID)
}

// SnapshotLayout returns field sizes for delta encoding.
// Base (10 fields): relX(4), relY(4), vx(2), vy(2), rotation(2), radius(2), width(2), height(2), lockedByID(4), lockedByProgress(1)
// Ship-specific (7 fields): healthCur(2), healthMax(2), shieldCur(2), shieldMax(2), flags(1), miningTargetID(4), combatTargetID(4)
func (h *ShipNetHandler) SnapshotLayout() []int {
	return []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, 2, 2, 2, 2, 1, 4, 4}
}

func (h *ShipNetHandler) InitialData(viewer *system.ViewerInfo, entry spatial.Entry) []byte {
	gw := h.gw
	var pilotName string
	if gw.C.PlayerConn.HasAll(entry.Entity) {
		connID := gw.C.PlayerConn.Get(entry.Entity).ConnID
		if sess := gw.Players.ByConnID(connID); sess != nil {
			pilotName = sess.Username
		}
	}
	return encodeLengthPrefixedString(pilotName)
}
