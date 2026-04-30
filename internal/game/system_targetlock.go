package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TargetLockSystem manages EVE-style lock-on targeting.
type TargetLockSystem struct {
	mmokit.SystemBase[*GameWorld]
	entities mmokit.Query[struct {
		Input *gamecomp.PlayerInput
		Lock  *gamecomp.TargetLock
	}]
}

func (s *TargetLockSystem) Update(dt float32) {
	gw := s.World()

	for e, b := range s.entities.Iter {
		input, lock := b.Input, b.Lock

		// Player cleared target or set to 0
		if input.LockTargetNetID == 0 {
			if lock.TargetNetID != 0 {
				gw.eng.Log.Log(CatCombatLock, "lock: player cleared lock (was targeting %d)", lock.TargetNetID)
			}
			s.breakLock(lock)
			continue
		}

		// Player switched targets
		if input.LockTargetNetID != lock.TargetNetID {
			gw.eng.Log.Log(CatCombatLock, "lock: new target netID=%d (was %d)", input.LockTargetNetID, lock.TargetNetID)
			lock.TargetNetID = input.LockTargetNetID
			lock.Progress = 0
			lock.Locked = false

			// Resolve net ID to entity
			target, ok := gw.NetIDToEntity[input.LockTargetNetID]
			if !ok || !gw.eng.ECS.Alive(target) {
				gw.eng.Log.Log(CatCombatLock, "lock: BREAK - netID=%d not found in NetIDToEntity (ok=%v)", input.LockTargetNetID, ok)
				s.breakLock(lock)
				continue
			}

			// Dormant entities (e.g. docked players parked at a station) are
			// not lockable — they're "inside" the station from a gameplay
			// perspective and AoI replication doesn't broadcast them anyway.
			if gw.C.Dormant.HasAll(target) {
				gw.eng.Log.Log(CatCombatLock, "lock: BREAK - target netID=%d is dormant", input.LockTargetNetID)
				s.breakLock(lock)
				continue
			}

			// Only lock onto ships, NPCs, and asteroids
			if gw.C.EntityKind.HasAll(target) {
				kind := gw.C.EntityKind.Get(target).Type
				if kind != gamecomp.TypeShip && kind != gamecomp.TypeNPC && kind != gamecomp.TypeAsteroid {
					gw.eng.Log.Log(CatCombatLock, "lock: BREAK - target type %d not lockable", kind)
					s.breakLock(lock)
					continue
				}
				// Asteroids lock faster
				if kind == gamecomp.TypeAsteroid {
					lock.LockTime = gw.Config.MiningLockTime
				} else {
					lock.LockTime = gw.Config.LockOnTime
				}
			}

			lock.TargetEntity = target
			gw.eng.Log.Log(CatCombatLock, "lock: started locking netID=%d", input.LockTargetNetID)
		}

		// Validate lock target is still valid — re-resolve from NetID if needed (e.g. after cell transfer)
		if !gw.eng.ECS.Alive(lock.TargetEntity) {
			resolved, ok := gw.NetIDToEntity[lock.TargetNetID]
			if ok && gw.eng.ECS.Alive(resolved) {
				lock.TargetEntity = resolved
				gw.eng.Log.Log(CatCombatLock, "lock: re-resolved netID=%d after transfer", lock.TargetNetID)
			} else {
				gw.eng.Log.Log(CatCombatLock, "lock: BREAK - target entity no longer alive (netID=%d)", lock.TargetNetID)
				s.breakLock(lock)
				continue
			}
		}

		// Target became Dormant mid-lock (e.g. enemy player just docked).
		if gw.C.Dormant.HasAll(lock.TargetEntity) {
			gw.eng.Log.Log(CatCombatLock, "lock: BREAK - target netID=%d went dormant", lock.TargetNetID)
			s.breakLock(lock)
			continue
		}

		// Check range
		if !gw.C.Position.HasAll(e) || !gw.C.Position.HasAll(lock.TargetEntity) {
			gw.eng.Log.Log(CatCombatLock, "lock: BREAK - missing position component")
			s.breakLock(lock)
			continue
		}
		pos := gw.C.Position.Get(e)
		targetPos := gw.C.Position.Get(lock.TargetEntity)
		dx := targetPos.X - pos.X
		dy := targetPos.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		if dist > lock.Range {
			gw.eng.Log.Log(CatCombatLock, "lock: BREAK - out of range (dist=%.0f, max=%.0f)", dist, lock.Range)
			s.breakLock(lock)
			continue
		}

		// Tick lock progress
		if !lock.Locked {
			lock.Progress += dt / lock.LockTime
			if lock.Progress >= 1.0 {
				lock.Progress = 1.0
				lock.Locked = true
				gw.eng.Log.Log(CatCombatLock, "lock: LOCKED on netID=%d", lock.TargetNetID)
			}
		}
	}
}

func (s *TargetLockSystem) breakLock(lock *gamecomp.TargetLock) {
	lock.TargetNetID = 0
	lock.Progress = 0
	lock.Locked = false
	lock.TargetEntity = ecs.Entity{}
}
