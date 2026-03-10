package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
)

type projectileMappers struct {
	base  *ecs.Map8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.Projectile, component.Lifetime]
	owner *ecs.Map1[component.Owner]
}

func initProjectileEntity(gw *GameWorld) {
	gw.projectileMappers = &projectileMappers{
		base:  ecs.NewMap8[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind, component.Projectile, component.Lifetime](gw.ECS),
		owner: ecs.NewMap1[component.Owner](gw.ECS),
	}

	gw.Registry.Register(EntityDef{
		Name:        "projectile",
		Description: "weapon projectile",
		EntityType:  component.TypeProjectile,
		Spawnable:   false,
	})
}

// SpawnProjectile creates a new projectile entity.
func (gw *GameWorld) SpawnProjectile(owner ecs.Entity, x, y, angle, speed, damage, lifetime float32) {
	m := gw.projectileMappers
	netID := gw.NextNetID()
	vx := float32(math.Cos(float64(angle))) * speed
	vy := float32(math.Sin(float64(angle))) * speed

	entity := m.base.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{X: vx, Y: vy},
		&component.Rotation{Angle: angle},
		&component.Collider{Radius: 4, Layer: component.LayerProjectile},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeProjectile},
		&component.Projectile{Damage: damage},
		&component.Lifetime{Remaining: lifetime},
	)

	m.owner.Add(entity, &component.Owner{Entity: owner})
}
