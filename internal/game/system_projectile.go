package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ProjectileSystem moves Projectile entities, performs swept collision
// against the spatial grid, applies impact damage, and on impact
// optionally spawns an AoEMarker for splash. Runs BEFORE AoESystem so
// splash markers spawned this tick resolve same-tick.
//
// Homing: if Spec.TargetNetID != 0 AND the target is alive, the velocity
// vector is rotated toward the target each tick capped by Spec.MaxTurnRate
// (rad/s). If the target is gone, TargetNetID is zeroed and the projectile
// continues straight.
//
// Missile acceleration: if Spec.Type == ProjectileTypeMissile, speed
// ramps from initial up to a cruise speed (~350 u/s) at +150 u/s².
type ProjectileSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	nearby   []mmokit.SpatialEntry
	entities mmokit.Query[struct {
		Pos  *mmokit.Position
		Vel  *mmokit.Velocity
		Life *mmokit.Lifetime
		Spec *gamecomp.ProjectileSpec
	}]
}

const (
	projectileMissileCruiseSpeed float32 = 350
	projectileMissileAccel       float32 = 150 // u/s²
	projectileHitRadius          float32 = 4   // collision query radius
)

func (s *ProjectileSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *ProjectileSystem) Update(dt float32) {
	gw := s.gw

	for entity, b := range s.entities.Iter {
		proj := mmokit.EntityFromECS(gw.stage, entity)
		pos, vel, spec := b.Pos, b.Vel, b.Spec

		// Homing
		if spec.TargetNetID != 0 && spec.MaxTurnRate > 0 {
			target := mmokit.EntityByNetID(gw.stage, spec.TargetNetID)
			if !target.Alive() {
				spec.TargetNetID = 0
			} else if tpos := mmokit.Get[mmokit.Position](target); tpos != nil {
				s.steerToward(vel, pos, tpos, spec.MaxTurnRate, dt)
			}
		}

		// Missile acceleration
		if spec.Type == gamecomp.ProjectileTypeMissile {
			speed := float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
			if speed > 1e-3 && speed < projectileMissileCruiseSpeed {
				newSpeed := speed + projectileMissileAccel*dt
				if newSpeed > projectileMissileCruiseSpeed {
					newSpeed = projectileMissileCruiseSpeed
				}
				ratio := newSpeed / speed
				vel.X *= ratio
				vel.Y *= ratio
			}
		}

		// Move
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt

		// Collision query
		s.nearby = gw.Spatial.QueryRadius(pos.X, pos.Y, projectileHitRadius, s.nearby[:0])
		var victim mmokit.Entity
		for _, entry := range s.nearby {
			e := mmokit.EntityFromECS(gw.stage, entry.Entity)
			if !e.Alive() {
				continue
			}
			if e.NetID() == spec.OwnerNetID {
				continue
			}
			// Only collide with NPCs and players.
			if !mmokit.Has[gamecomp.NPCAI](e) && !mmokit.Has[mmokit.PlayerConn](e) {
				continue
			}
			victim = e
			break
		}

		if victim.Alive() {
			gw.ApplyDamage(victim, spec.Damage, spec.OwnerNetID)
			if spec.SplashRadius > 0 {
				mask := factionMaskFromOwner(gw, spec.OwnerNetID)
				gw.SpawnAoEMarker(pos.X, pos.Y, 0, spec.SplashRadius, spec.SplashDamage, spec.OwnerNetID, mask)
			}
			gw.eng.Log.Log(CatCombatHit, "projectile: hit netID=%d dmg=%.0f splash=%.0f",
				victim.NetID(), spec.Damage, spec.SplashRadius)
			s.Commands().Despawn(proj.Handle())
			continue
		}

		// Lifetime expiry (no hit)
		if b.Life.Remaining <= 0 {
			s.Commands().Despawn(proj.Handle())
		}
	}
}

// steerToward rotates `vel` toward the heading from `pos` to `tpos`,
// capped by maxTurnRate (rad/s). Preserves speed.
func (s *ProjectileSystem) steerToward(
	vel *mmokit.Velocity, pos *mmokit.Position, tpos *mmokit.Position,
	maxTurnRate, dt float32,
) {
	speed := float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
	if speed < 1e-3 {
		return
	}
	curHeading := float32(math.Atan2(float64(vel.Y), float64(vel.X)))
	desired := float32(math.Atan2(float64(tpos.Y-pos.Y), float64(tpos.X-pos.X)))
	delta := desired - curHeading
	for delta > math.Pi {
		delta -= 2 * math.Pi
	}
	for delta < -math.Pi {
		delta += 2 * math.Pi
	}
	maxDelta := maxTurnRate * dt
	if delta > maxDelta {
		delta = maxDelta
	} else if delta < -maxDelta {
		delta = -maxDelta
	}
	newHeading := curHeading + delta
	vel.X = speed * float32(math.Cos(float64(newHeading)))
	vel.Y = speed * float32(math.Sin(float64(newHeading)))
}

// factionMaskFromOwner returns the AoE FactionMask appropriate for a
// projectile owned by the given netID. Player-owned projectile splash
// damages NPCs; NPC-owned splash damages players.
func factionMaskFromOwner(gw *GameWorld, ownerNetID uint32) uint8 {
	if ownerNetID == 0 {
		return FactionMaskAll
	}
	owner := mmokit.EntityByNetID(gw.stage, ownerNetID)
	if owner.Alive() && mmokit.Has[mmokit.PlayerConn](owner) {
		return FactionMaskNPC
	}
	return FactionMaskPlayer
}
