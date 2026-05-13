package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TargetLockSystem ticks per-slot lock progress and prunes invalid
// slots (target dead / out of AoI / out of range / dormant / leashing).
// Lock acquisition/release is handled by input_handlers (LockTarget /
// UnlockTarget). This system only advances + validates state.
type TargetLockSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		Lock *gamecomp.TargetLock
	}]
}

func (s *TargetLockSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *TargetLockSystem) Update(dt float32) {
	gw := s.gw
	for e, b := range s.entities.Iter {
		lock := b.Lock
		owner := mmokit.EntityFromECS(gw.stage, e)
		ownerPos := mmokit.Get[mmokit.Position](owner)
		if ownerPos == nil {
			// Owner has no Position — drop everything.
			s.clearAll(lock)
			continue
		}

		// Iterate in reverse so we can remove without shifting indices.
		dirty := false
		for i := len(lock.Slots) - 1; i >= 0; i-- {
			slot := &lock.Slots[i]
			drop := false

			// Re-resolve target after potential cell transfer.
			targetE := mmokit.EntityFromECS(gw.stage, slot.TargetEntity)
			if !targetE.Alive() {
				resolved := mmokit.EntityByNetID(gw.stage, slot.TargetNetID)
				if resolved.Alive() {
					slot.TargetEntity = resolved.Handle()
					targetE = resolved
				} else {
					drop = true
				}
			}

			if !drop && mmokit.Has[mmokit.Dormant](targetE) {
				drop = true
			}
			if !drop && mmokit.Has[gamecomp.Leashing](targetE) {
				drop = true
			}

			if !drop {
				tpos := mmokit.Get[mmokit.Position](targetE)
				if tpos == nil {
					drop = true
				} else {
					dx, dy := tpos.X-ownerPos.X, tpos.Y-ownerPos.Y
					dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
					if dist > lock.Range {
						drop = true
					}
				}
			}

			if drop {
				gw.eng.Log.Log(CatCombatLock, "lock: drop slot=%d owner=%d target=%d",
					i, owner.NetID(), slot.TargetNetID)
				droppedNetID := slot.TargetNetID
				lock.Slots = append(lock.Slots[:i], lock.Slots[i+1:]...)
				if lock.ActiveNetID == droppedNetID {
					lock.ActiveNetID = 0 // re-resolved below
				}
				dirty = true
				continue
			}

			// Tick lock progress. Each tick of a not-yet-locked slot
			// mutates Progress, so flag dirty so LockSlotsMsg ships the
			// new value to the owning client every tick — the lock-on-ring
			// animates smoothly off these samples (without the per-tick
			// push it snaps 0%→100% at the moment the slot flips Locked).
			if !slot.Locked {
				slot.Progress += dt / slot.LockTime
				dirty = true
				if slot.Progress >= 1.0 {
					slot.Progress = 1.0
					slot.Locked = true
					gw.eng.Log.Log(CatCombatLock, "lock: LOCKED owner=%d target=%d",
						owner.NetID(), slot.TargetNetID)
					// First newly-locked slot becomes active when none set.
					if lock.ActiveNetID == 0 {
						lock.ActiveNetID = slot.TargetNetID
					}
				}
			}
		}

		// If we dropped the active, fall back.
		if lock.ActiveNetID == 0 && len(lock.Slots) > 0 {
			lock.ActiveNetID = pickActiveFallback(lock)
			if lock.ActiveNetID != 0 {
				dirty = true
			}
		}

		// Push update to owner if they're a player.
		if dirty && mmokit.Has[mmokit.PlayerConn](owner) {
			sendLockSlots(gw, owner)
		}
	}
}

func (s *TargetLockSystem) clearAll(lock *gamecomp.TargetLock) {
	lock.Slots = lock.Slots[:0]
	lock.ActiveNetID = 0
}
