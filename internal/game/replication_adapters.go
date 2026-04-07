package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
)

// ---------------------------------------------------------------------------
// Shared per-tick context (rebuilt each tick by OnBeforeTick)
// ---------------------------------------------------------------------------

// lockerInfo tracks the most-progressed entity locking a given target.
type lockerInfo struct {
	netID    uint32
	progress float32
}

// gameNetContext holds precomputed per-tick shared data for handlers.
type gameNetContext struct {
	// lockedBy maps target entity -> most-progressed locker.
	lockedBy map[ecs.Entity]lockerInfo
}

// ---------------------------------------------------------------------------
// Helper: hash common base fields for change detection
// ---------------------------------------------------------------------------

// hashBaseFields hashes common fields for all entity types: relative position,
// velocity, rotation, and locked-by state.
func hashBaseFields(h *system.Hasher, gw *GameWorld, ctx *gameNetContext, viewer *system.ViewerInfo, entry spatial.Entry) {
	relX, relY := system.CellRelativePos(gw.C.CellCoord, viewer.Entity, entry)
	h.Float32(relX)
	h.Float32(relY)

	if gw.C.Velocity.HasAll(entry.Entity) {
		vel := gw.C.Velocity.Get(entry.Entity)
		h.Float32(vel.X)
		h.Float32(vel.Y)
	}
	if gw.C.Rotation.HasAll(entry.Entity) {
		h.Float32(gw.C.Rotation.Get(entry.Entity).Angle)
	}

	// Locked-by state
	if lb, ok := ctx.lockedBy[entry.Entity]; ok {
		h.Uint32(lb.netID)
		h.Float32(lb.progress)
	} else {
		h.Uint32(0)
		h.Float32(0)
	}
}
