package game

import (
	"math"

	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/item"
	"github.com/mmokit/mmokit/pkg/mmokit"
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
	projectileHitRadius float32 = 4 // collision query radius
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
			victimNetID := victim.NetID()

			// Skip already-pierced victims so the same target can't take
			// multiple hits from one line shot.
			alreadyHit := false
			for _, id := range spec.PiercedNetIDs {
				if id == victimNetID {
					alreadyHit = true
					break
				}
			}
			if !alreadyHit {
				caster := mmokit.EntityByNetID(gw.stage, spec.OwnerNetID)
				if caster.Alive() {
					// gw.Damage emits a Damage broadcast (Plan F Phase 2 auto-broadcast),
					// so AoI viewers see the impact and render the proper VFX via
					// examples/space/web/src/effects/ability-effects.ts case 9/10/12.
					gw.Damage(caster, victim, spec.Damage, 0, 0 /*slot*/, projectileAbilityType(spec.Type))
				} else {
					// Owner gone — apply damage anyway, just no broadcast attribution.
					gw.ApplyDamage(victim, spec.Damage, spec.OwnerNetID)
				}
				if spec.SplashRadius > 0 {
					// Capture splash params; defer the spawn so it runs after
					// the entities.Iter query closes — Stage.Spawn requires an
					// unlocked world. The projectile's AbilityType maps the
					// splash hit to the same VFX/sound as the direct impact.
					ix, iy := pos.X, pos.Y
					splashR, splashDmg := spec.SplashRadius, spec.SplashDamage
					owner := spec.OwnerNetID
					mask := factionMaskFromOwner(gw, owner)
					abilityType := projectileAbilityType(spec.Type)
					s.Commands().Defer(func() {
						gw.SpawnAoEMarker(ix, iy, 0, splashR, splashDmg, owner, mask, abilityType)
					})
				}
				gw.eng.Log.Log(CatCombatHit, "projectile: hit netID=%d dmg=%.0f pierce=%d",
					victimNetID, spec.Damage, spec.PierceCount)

				if spec.PierceCount > 0 {
					// Record + continue. Append to first free slot in PiercedNetIDs.
					for i := range spec.PiercedNetIDs {
						if spec.PiercedNetIDs[i] == 0 {
							spec.PiercedNetIDs[i] = victimNetID
							break
						}
					}
					spec.PierceCount--
					// Don't despawn — the projectile keeps flying.
					continue
				}

				s.Commands().Despawn(proj.Handle())
				continue
			}
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

// projectileAbilityType maps a ProjectileType to the AbilityType that
// owns it, so the per-hit Damage broadcast carries the right abilityType
// for the client's ability-effects.ts dispatch (cases 9/10/12).
func projectileAbilityType(projType uint8) uint8 {
	switch projType {
	case gamecomp.ProjectileTypePlasma:
		return uint8(item.AbilityTypePlasmaShot)
	case gamecomp.ProjectileTypeMissile:
		return uint8(item.AbilityTypeHomingMissile)
	case gamecomp.ProjectileTypeMortar:
		return uint8(item.AbilityTypeMortarShell)
	case gamecomp.ProjectileTypePulse:
		return uint8(item.AbilityTypePulseLaser)
	case gamecomp.ProjectileTypeRail:
		return uint8(item.AbilityTypeRailShot)
	case gamecomp.ProjectileTypeIon:
		return uint8(item.AbilityTypeIonOverload)
	}
	return 0
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
