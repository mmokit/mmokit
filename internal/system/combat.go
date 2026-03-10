package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/gameserver/internal/component"
	"github.com/zenion/gameserver/internal/game"
)

type fireCommand struct {
	owner               ecs.Entity
	x, y, angle         float32
	speed, damage, life float32
}

// CombatSystem handles weapon firing and projectile spawning.
type CombatSystem struct {
	gw       *game.GameWorld
	filter   *ecs.Filter5[component.PlayerInput, component.Weapon, component.Position, component.Rotation, component.NetworkID]
	deferred []fireCommand
}

func NewCombatSystem(gw *game.GameWorld) *CombatSystem {
	return &CombatSystem{
		gw:       gw,
		deferred: make([]fireCommand, 0, 16),
	}
}

func (s *CombatSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter5[component.PlayerInput, component.Weapon, component.Position, component.Rotation, component.NetworkID](gw.ECS)
	}

	s.deferred = s.deferred[:0]

	query := s.filter.Query()
	for query.Next() {
		input, weapon, pos, rot, _ := query.Get()
		entity := query.Entity()

		// Reduce cooldown
		if weapon.CooldownLeft > 0 {
			weapon.CooldownLeft -= dt
		}

		// Collect fire commands (can't spawn during query iteration)
		if input.Fire && weapon.CooldownLeft <= 0 {
			weapon.CooldownLeft = 1.0 / weapon.FireRate
			s.deferred = append(s.deferred, fireCommand{
				owner:  entity,
				x:      pos.X,
				y:      pos.Y,
				angle:  rot.Angle,
				speed:  weapon.Speed,
				damage: weapon.Damage,
				life:   gw.Config.ProjectileLifetime,
			})
		}
	}

	// Spawn projectiles after query is done
	for _, cmd := range s.deferred {
		gw.SpawnProjectile(cmd.owner, cmd.x, cmd.y, cmd.angle, cmd.speed, cmd.damage, cmd.life)
	}
}
