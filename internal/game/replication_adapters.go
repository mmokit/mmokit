package game

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
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
	lockedBy map[mmokit.EntityHandle]lockerInfo
}
