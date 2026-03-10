package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/logger"
)

// TargetLockSystem manages EVE-style lock-on targeting.
type TargetLockSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter2[component.PlayerInput, component.TargetLock]
}

func NewTargetLockSystem(gw *game.GameWorld) *TargetLockSystem {
	return &TargetLockSystem{gw: gw}
}

func (s *TargetLockSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter2[component.PlayerInput, component.TargetLock](gw.ECS)
	}

	query := s.filter.Query()
	for query.Next() {
		input, lock := query.Get()
		entity := query.Entity()

		// Player cleared target or set to 0
		if input.LockTargetNetID == 0 {
			if lock.TargetNetID != 0 {
				gw.Log.Log(logger.CatCombat, "lock: player cleared lock (was targeting %d)", lock.TargetNetID)
			}
			s.breakLock(lock)
			continue
		}

		// Player switched targets
		if input.LockTargetNetID != lock.TargetNetID {
			gw.Log.Log(logger.CatCombat, "lock: new target netID=%d (was %d)", input.LockTargetNetID, lock.TargetNetID)
			lock.TargetNetID = input.LockTargetNetID
			lock.Progress = 0
			lock.Locked = false

			// Resolve net ID to entity
			target, ok := gw.NetIDToEntity[input.LockTargetNetID]
			if !ok || !gw.ECS.Alive(target) {
				gw.Log.Log(logger.CatCombat, "lock: BREAK - netID=%d not found in NetIDToEntity (ok=%v)", input.LockTargetNetID, ok)
				s.breakLock(lock)
				continue
			}

			// Only lock onto ships and NPCs
			if gw.EntityKindMap.HasAll(target) {
				kind := gw.EntityKindMap.Get(target).Type
				if kind != component.TypeShip && kind != component.TypeNPC {
					gw.Log.Log(logger.CatCombat, "lock: BREAK - target type %d not lockable", kind)
					s.breakLock(lock)
					continue
				}
			}

			lock.TargetEntity = target
			gw.Log.Log(logger.CatCombat, "lock: started locking netID=%d", input.LockTargetNetID)
		}

		// Validate lock target is still valid
		if !gw.ECS.Alive(lock.TargetEntity) {
			gw.Log.Log(logger.CatCombat, "lock: BREAK - target entity no longer alive")
			s.breakLock(lock)
			continue
		}

		// Check range
		if !gw.PositionMap.HasAll(entity) || !gw.PositionMap.HasAll(lock.TargetEntity) {
			gw.Log.Log(logger.CatCombat, "lock: BREAK - missing position component")
			s.breakLock(lock)
			continue
		}
		pos := gw.PositionMap.Get(entity)
		targetPos := gw.PositionMap.Get(lock.TargetEntity)
		dx := targetPos.X - pos.X
		dy := targetPos.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		if dist > lock.Range {
			gw.Log.Log(logger.CatCombat, "lock: BREAK - out of range (dist=%.0f, max=%.0f)", dist, lock.Range)
			s.breakLock(lock)
			continue
		}

		// Tick lock progress
		if !lock.Locked {
			lock.Progress += dt / lock.LockTime
			if lock.Progress >= 1.0 {
				lock.Progress = 1.0
				lock.Locked = true
				gw.Log.Log(logger.CatCombat, "lock: LOCKED on netID=%d", lock.TargetNetID)
			}
		}
	}
}

func (s *TargetLockSystem) breakLock(lock *component.TargetLock) {
	lock.TargetNetID = 0
	lock.Progress = 0
	lock.Locked = false
	lock.TargetEntity = ecs.Entity{}
}
