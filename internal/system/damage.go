package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// DamageSystem processes collisions from the spatial grid and applies damage.
// Also handles shield regeneration.
type DamageSystem struct {
	gw              *game.GameWorld
	collisionMatrix map[uint8]uint8
	shieldFilter    *ecs.Filter1[component.Shield]
}

func NewDamageSystem(gw *game.GameWorld) *DamageSystem {
	return &DamageSystem{
		gw: gw,
		collisionMatrix: map[uint8]uint8{
			component.LayerPlayer:     component.LayerTerrain,
			component.LayerProjectile: component.LayerPlayer | component.LayerTerrain,
		},
	}
}

func (s *DamageSystem) Update(dt float32) {
	gw := s.gw
	gw.Grid.QueryCollisions(s.collisionMatrix, func(a, b spatial.Entry) {
		// Handle projectile hitting something
		s.handleProjectileCollision(a, b)
		s.handleProjectileCollision(b, a)

		// Handle player hitting terrain (bounce off)
		s.handleTerrainCollision(a, b)
		s.handleTerrainCollision(b, a)
	})

	// Shield regeneration
	if s.shieldFilter == nil {
		s.shieldFilter = ecs.NewFilter1[component.Shield](gw.ECS)
	}
	query := s.shieldFilter.Query()
	for query.Next() {
		shield := query.Get()
		if shield.DamageCooldown > 0 {
			shield.DamageCooldown -= dt
			continue
		}
		if shield.Current < shield.Max {
			shield.Current = min(shield.Current+shield.RegenRate*dt, shield.Max)
		}
	}
}

func (s *DamageSystem) handleProjectileCollision(projectile, target spatial.Entry) {
	gw := s.gw
	if projectile.Layer != component.LayerProjectile {
		return
	}
	if !gw.ECS.Alive(projectile.Entity) || !gw.ECS.Alive(target.Entity) {
		return
	}

	proj := gw.ProjectileMap.Get(projectile.Entity)

	// Check owner - don't hit own ship
	if gw.OwnerMap.HasAll(projectile.Entity) {
		owner := gw.OwnerMap.Get(projectile.Entity)
		if owner.Entity == target.Entity {
			return
		}
	}

	// Apply damage to target if it has health
	if gw.HealthMap.HasAll(target.Entity) {
		health := gw.HealthMap.Get(target.Entity)
		damage := proj.Damage
		shieldAbsorbed := float32(0)

		// Shield absorbs damage first
		if gw.ShieldMap.HasAll(target.Entity) {
			shield := gw.ShieldMap.Get(target.Entity)
			shield.DamageCooldown = shield.RegenDelay
			if shield.Current > 0 {
				shieldAbsorbed = min(shield.Current, damage)
				shield.Current -= shieldAbsorbed
				damage -= shieldAbsorbed
			}
		}

		health.Current -= damage

		// Log the hit
		targetNetID := uint32(0)
		if gw.NetworkIDMap.HasAll(target.Entity) {
			targetNetID = gw.NetworkIDMap.Get(target.Entity).ID
		}
		attackerNetID := uint32(0)
		if gw.OwnerMap.HasAll(projectile.Entity) {
			owner := gw.OwnerMap.Get(projectile.Entity)
			if gw.ECS.Alive(owner.Entity) && gw.NetworkIDMap.HasAll(owner.Entity) {
				attackerNetID = gw.NetworkIDMap.Get(owner.Entity).ID
			}
		}
		gw.Log.Log(logger.CatCombat, "hit: attacker=%d -> target=%d damage=%.1f (shield_absorbed=%.1f) hp=%.1f/%.1f",
			attackerNetID, targetNetID, proj.Damage, shieldAbsorbed, health.Current, health.Max)

		if health.Current <= 0 {
			gw.Log.Log(logger.CatKill, "killed: target=%d by attacker=%d", targetNetID, attackerNetID)
			// Use MarkPlayerDeath for players so they get a death notification
			if gw.PlayerConnMap.HasAll(target.Entity) {
				gw.MarkPlayerDeath(target.Entity, attackerNetID)
			} else {
				gw.MarkForRemoval(target.Entity)
			}
		}
	}

	// Remove the projectile
	gw.MarkForRemoval(projectile.Entity)
}

func (s *DamageSystem) handleTerrainCollision(player, terrain spatial.Entry) {
	gw := s.gw
	if player.Layer != component.LayerPlayer || terrain.Layer != component.LayerTerrain {
		return
	}
	if !gw.ECS.Alive(player.Entity) || !gw.ECS.Alive(terrain.Entity) {
		return
	}

	// Bounce: use center-to-center vector as collision normal
	playerPos := gw.PositionMap.Get(player.Entity)
	terrainPos := gw.PositionMap.Get(terrain.Entity)

	dx := playerPos.X - terrainPos.X
	dy := playerPos.Y - terrainPos.Y
	dist := float32(1.0)
	if d := dx*dx + dy*dy; d > 0 {
		dist = sqrt32(d)
	}

	// Normalize
	nx := dx / dist
	ny := dy / dist

	// Estimate overlap using bounding radii
	overlap := player.Radius + terrain.Radius - dist
	if overlap > 0 {
		playerPos.X += nx * overlap
		playerPos.Y += ny * overlap

		playerNetID := uint32(0)
		if gw.NetworkIDMap.HasAll(player.Entity) {
			playerNetID = gw.NetworkIDMap.Get(player.Entity).ID
		}
		gw.Log.Log(logger.CatCollision, "terrain bounce: player=%d overlap=%.1f", playerNetID, overlap)
	}

	// Reflect velocity
	vel := gw.VelocityMap.Get(player.Entity)
	dot := vel.X*nx + vel.Y*ny
	if dot < 0 {
		vel.X -= 2 * dot * nx
		vel.Y -= 2 * dot * ny
		// Dampen
		vel.X *= 0.5
		vel.Y *= 0.5
	}
}

func sqrt32(x float32) float32 {
	if x <= 0 {
		return 0
	}
	guess := x
	for range 4 {
		guess = (guess + x/guess) / 2
	}
	return guess
}
