package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NPCAISystem drives the per-NPC state machine each tick. Acquires
// targets, applies motion policy in Engage, manages leash return, and
// fires weapons. Runs AFTER TargetLockSystem so locks are progress-ticked
// before the AI consults them.
//
// Time is tracked locally via elapsedSec (monotonic dt accumulation) —
// only the deltas matter for deescalation, and the Stage/Process layer
// doesn't expose a game-time accessor. ClusterClock.Now() exists but is
// uint64 ms and serves a different purpose (cross-host stamping).
type NPCAISystem struct {
	mmokit.SystemBase
	gw         *GameWorld
	elapsedSec float32
	entities   mmokit.Query[struct {
		AI   *gamecomp.NPCAI
		Pos  *mmokit.Position
		Vel  *mmokit.Velocity
		Rot  *mmokit.Rotation
		Lock *gamecomp.TargetLock
	}]
}

func (s *NPCAISystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *NPCAISystem) Update(dt float32) {
	gw := s.gw
	s.elapsedSec += dt
	now := s.elapsedSec

	for e, b := range s.entities.Iter {
		ai, pos, vel, rot, lock := b.AI, b.Pos, b.Vel, b.Rot, b.Lock
		self := mmokit.EntityFromECS(gw.stage, e)

		// Leashing entities are driven below; skip state-driven logic here.
		if mmokit.Has[gamecomp.Leashing](self) {
			s.tickLeash(self, ai, pos, vel, rot, dt)
			continue
		}

		switch ai.State {
		case AIStateIdle:
			s.tickIdle(self, ai, pos, vel, lock, now, dt)
		case AIStateAcquire:
			s.tickAcquire(self, ai, pos, rot, lock, now, dt)
		case AIStateEngage:
			s.tickEngage(self, ai, pos, vel, rot, lock, now, dt)
		}
	}
}

func (s *NPCAISystem) tickIdle(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, lock *gamecomp.TargetLock,
	now, dt float32,
) {
	vel.X, vel.Y = 0, 0

	target := s.findNearestEnemy(self, pos, ai.AggroRadius)
	if target == (ecs.Entity{}) {
		return
	}

	tEnt := mmokit.EntityFromECS(s.gw.stage, target)
	tNetID := tEnt.NetID()
	lock.Slots = lock.Slots[:0]
	lock.Slots = append(lock.Slots, gamecomp.LockSlot{
		TargetNetID:  tNetID,
		TargetEntity: target,
		Progress:     0,
		LockTime:     s.gw.Config.LockOnTime * 0.8, // NPCs lock a bit faster
	})
	lock.ActiveNetID = tNetID
	ai.State = AIStateAcquire
	ai.LastCombatActivityAt = now
	s.gw.eng.Log.Log(CatNPCAI, "ai: %d Idle→Acquire target=%d",
		self.NetID(), tNetID)
}

func (s *NPCAISystem) tickAcquire(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, rot *mmokit.Rotation, lock *gamecomp.TargetLock,
	now, dt float32,
) {
	if len(lock.Slots) == 0 || lock.ActiveNetID == 0 {
		ai.State = AIStateIdle
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Acquire→Idle (no target)", self.NetID())
		return
	}
	target := mmokit.EntityByNetID(s.gw.stage, lock.ActiveNetID)
	if !target.Alive() {
		ai.State = AIStateIdle
		return
	}
	tpos := mmokit.Get[mmokit.Position](target)
	if tpos == nil {
		ai.State = AIStateIdle
		return
	}
	desired := float32(math.Atan2(float64(tpos.Y-pos.Y), float64(tpos.X-pos.X)))
	if turnTowards(rot, desired, ai.TurnRate, dt) {
		ai.State = AIStateEngage
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Acquire→Engage", self.NetID())
	}
}

func (s *NPCAISystem) tickEngage(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, rot *mmokit.Rotation,
	lock *gamecomp.TargetLock, now, dt float32,
) {
	if lock.ActiveNetID == 0 {
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	target := mmokit.EntityByNetID(s.gw.stage, lock.ActiveNetID)
	if !target.Alive() || mmokit.Has[mmokit.Dormant](target) {
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	if now-ai.LastCombatActivityAt > s.gw.Config.AggroDeescalationSec {
		ai.State = AIStateIdle
		lock.Slots = lock.Slots[:0]
		lock.ActiveNetID = 0
		vel.X, vel.Y = 0, 0
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Engage→Idle (deescalation)", self.NetID())
		return
	}
	tpos := mmokit.Get[mmokit.Position](target)
	if tpos == nil {
		ai.State = AIStateIdle
		return
	}

	// Target switching: if a damage source is closer than current target,
	// drop the lock and re-acquire on the attacker next tick. Stamped by
	// ApplyDamage in verb_damage.go; consumed (and cleared) here.
	if ai.LastDamageByNetID != 0 && ai.LastDamageByNetID != lock.ActiveNetID {
		attacker := mmokit.EntityByNetID(s.gw.stage, ai.LastDamageByNetID)
		if attacker.Alive() && !mmokit.Has[mmokit.Dormant](attacker) && !mmokit.Has[gamecomp.Leashing](attacker) {
			if apos := mmokit.Get[mmokit.Position](attacker); apos != nil {
				adx, ady := apos.X-pos.X, apos.Y-pos.Y
				adist2 := adx*adx + ady*ady
				cdx, cdy := tpos.X-pos.X, tpos.Y-pos.Y
				cdist2 := cdx*cdx + cdy*cdy
				if adist2 < cdist2 {
					attackerNetID := ai.LastDamageByNetID
					lock.Slots = lock.Slots[:0]
					lock.Slots = append(lock.Slots, gamecomp.LockSlot{
						TargetNetID:  attackerNetID,
						TargetEntity: attacker.Handle(),
						Progress:     0,
						LockTime:     s.gw.Config.LockOnTime * 0.8,
					})
					lock.ActiveNetID = attackerNetID
					ai.LastDamageByNetID = 0 // consume
					ai.State = AIStateAcquire
					ai.LastCombatActivityAt = now
					s.gw.eng.Log.Log(CatNPCAI, "ai: %d target-switch to %d (closer attacker)",
						self.NetID(), attackerNetID)
					return
				}
			}
		}
		ai.LastDamageByNetID = 0 // consumed (no switch)
	}

	dx, dy := tpos.X-pos.X, tpos.Y-pos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

	desired := float32(math.Atan2(float64(dy), float64(dx)))
	turnTowards(rot, desired, ai.TurnRate, dt)

	s.applyMotion(ai, vel, pos, tpos, dist, dx, dy)

	if dist <= ai.WeaponRange && angleDelta(rot.Angle, desired) < 0.26 /* ~15° */ {
		ai.FireCooldown -= dt
		if ai.FireCooldown <= 0 && ai.FireRate > 0 {
			ai.FireCooldown = 1.0 / ai.FireRate
			dealt := s.gw.ApplyDamage(target, ai.DamagePerShot, self.NetID())
			if dealt > 0 {
				ai.LastCombatActivityAt = now
			}
		}
	}
}

func (s *NPCAISystem) applyMotion(ai *gamecomp.NPCAI, vel *mmokit.Velocity,
	pos *mmokit.Position, tpos *mmokit.Position, dist, dx, dy float32,
) {
	const tolerance = 50.0
	if dist < 1e-3 {
		vel.X, vel.Y = 0, 0
		return
	}
	ux, uy := dx/dist, dy/dist
	switch ai.MotionPolicy {
	case MotionCharge:
		vel.X, vel.Y = ux*ai.MaxSpeed, uy*ai.MaxSpeed
	case MotionHoldRange:
		if dist < ai.PreferredRange-tolerance {
			vel.X, vel.Y = -ux*ai.MaxSpeed, -uy*ai.MaxSpeed
		} else if dist > ai.PreferredRange+tolerance {
			vel.X, vel.Y = ux*ai.MaxSpeed, uy*ai.MaxSpeed
		} else {
			vel.X, vel.Y = 0, 0
		}
	case MotionEncircle:
		tx, ty := -uy, ux
		radial := float32(0)
		if dist < ai.PreferredRange-tolerance {
			radial = -1
		} else if dist > ai.PreferredRange+tolerance {
			radial = 1
		}
		vel.X = (tx + ux*radial) * ai.MaxSpeed
		vel.Y = (ty + uy*radial) * ai.MaxSpeed
	}
}

func (s *NPCAISystem) tickLeash(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, rot *mmokit.Rotation, dt float32,
) {
	anchor := mmokit.Get[gamecomp.POIAnchor](self)
	if anchor == nil || anchor.POINetID == 0 {
		// No anchor (test NPC) — clear leash immediately.
		removeLeashing(self)
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	poiE := mmokit.EntityByNetID(s.gw.stage, anchor.POINetID)
	if !poiE.Alive() {
		removeLeashing(self)
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	poiPos := mmokit.Get[mmokit.Position](poiE)
	poi := mmokit.Get[gamecomp.POI](poiE)
	if poiPos == nil || poi == nil {
		return
	}
	dx, dy := poiPos.X-pos.X, poiPos.Y-pos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	if dist < poi.AnchorRadius {
		removeLeashing(self)
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		if h := mmokit.Get[gamecomp.Health](self); h != nil {
			h.Current = h.Max
		}
		if sh := mmokit.Get[gamecomp.Shield](self); sh != nil {
			sh.Current = sh.Max
		}
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Leash→Idle (arrived)", self.NetID())
		return
	}
	ux, uy := dx/dist, dy/dist
	vel.X, vel.Y = ux*ai.MaxSpeed*2, uy*ai.MaxSpeed*2
	desired := float32(math.Atan2(float64(uy), float64(ux)))
	turnTowards(rot, desired, ai.TurnRate*2, dt)
}

func (s *NPCAISystem) findNearestEnemy(self mmokit.Entity,
	pos *mmokit.Position, radius float32,
) ecs.Entity {
	var best ecs.Entity
	bestDist2 := radius * radius
	world := s.gw.stage.ECSWorld()
	filter := ecs.NewFilter2[mmokit.Position, mmokit.EntityKind](world)
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		entE := q.Entity()
		if entE == self.Handle() {
			continue
		}
		ppos, kind := q.Get()
		if kind.Type != gamecomp.KindShip {
			continue
		}
		e := mmokit.EntityFromECS(s.gw.stage, entE)
		if mmokit.Has[mmokit.Dormant](e) {
			continue
		}
		if h := mmokit.Get[gamecomp.Health](e); h == nil || h.Current <= 0 {
			continue
		}
		dx, dy := ppos.X-pos.X, ppos.Y-pos.Y
		d2 := dx*dx + dy*dy
		if d2 < bestDist2 {
			bestDist2 = d2
			best = entE
		}
	}
	return best
}

// removeLeashing strips the Leashing tag via raw ECS. mmokit has no
// component-removal primitive (only Get/Has/Set); the dormant-clear path
// in internal/game/hooks.go follows the same pattern.
func removeLeashing(e mmokit.Entity) {
	h := e.Handle()
	if h == (ecs.Entity{}) || e.Stage() == nil {
		return
	}
	w := e.Stage().ECSWorld()
	leashMap := ecs.NewMap1[gamecomp.Leashing](w)
	if leashMap.HasAll(h) {
		leashMap.Remove(h)
	}
}

// turnTowards rotates rot.Angle toward desired by up to turnRate*dt.
// Returns true if the rotation is within ±15° of desired after the step.
func turnTowards(rot *mmokit.Rotation, desired, turnRate, dt float32) bool {
	diff := normalizeAngle(desired - rot.Angle)
	maxTurn := turnRate * dt
	if diff > maxTurn {
		diff = maxTurn
	} else if diff < -maxTurn {
		diff = -maxTurn
	}
	rot.Angle += diff
	return angleDelta(rot.Angle, desired) < 0.26
}

func angleDelta(a, b float32) float32 {
	return float32(math.Abs(float64(normalizeAngle(a - b))))
}
