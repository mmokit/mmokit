package system

import (
	"math"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// CollisionSystem handles terrain bounce (player-vs-asteroid).
// Actual combat damage is hitscan, handled by AbilitySystem via GameWorld.ApplyDamage.
type CollisionSystem struct {
	gw     *game.GameWorld
	nearby []mmokit.SpatialEntry // reusable scratch buffer
}

func NewCollisionSystem(gw *game.GameWorld) *CollisionSystem {
	return &CollisionSystem{
		gw:     gw,
		nearby: make([]mmokit.SpatialEntry, 0, 64),
	}
}

func (s *CollisionSystem) Name() string { return "Collision" }

func (s *CollisionSystem) Update(dt float32) {
	gw := s.gw

	// Terrain bounce: only check player entities against nearby terrain.
	// This avoids the O(n²) full-grid scan that was the previous bottleneck.
	for _, entity := range gw.Players.Entities {
		if !gw.ECS.Alive(entity) {
			continue
		}
		if gw.C.Ghost.HasAll(entity) || gw.C.Replica.HasAll(entity) {
			continue
		}
		if !gw.C.Position.HasAll(entity) || !gw.C.Collider.HasAll(entity) {
			continue
		}
		pos := gw.C.Position.Get(entity)
		col := gw.C.Collider.Get(entity)

		// Query nearby entries within the player's bounding radius + margin
		searchRadius := col.Radius + gw.Config.AsteroidMaxRadius
		s.nearby = gw.Grid.QueryRadius(pos.X, pos.Y, searchRadius, s.nearby[:0])

		for _, terrain := range s.nearby {
			if terrain.Layer != component.LayerTerrain {
				continue
			}
			if !gw.ECS.Alive(terrain.Entity) {
				continue
			}

			// Broad-phase bounding circle check
			dx := pos.X - terrain.X
			dy := pos.Y - terrain.Y
			dist2 := dx*dx + dy*dy
			maxDist := col.Radius + terrain.Radius
			if dist2 > maxDist*maxDist {
				continue
			}

			var rotation float32
			if gw.C.Rotation.HasAll(entity) {
				rotation = gw.C.Rotation.Get(entity).Angle
			}
			playerEntry := mmokit.SpatialEntry{
				Entity:   entity,
				X:        pos.X,
				Y:        pos.Y,
				Radius:   col.Radius,
				Width:    col.Width,
				Height:   col.Height,
				Rotation: rotation,
				Layer:    col.Layer,
				Shape:    col.Shape,
			}
			s.handleTerrainCollision(playerEntry, terrain)
		}
	}
}

func (s *CollisionSystem) handleTerrainCollision(player, terrain mmokit.SpatialEntry) {
	gw := s.gw

	playerPos := gw.C.Position.Get(player.Entity)
	terrainPos := gw.C.Position.Get(terrain.Entity)

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
		if gw.C.NetworkID.HasAll(player.Entity) {
			playerNetID = gw.C.NetworkID.Get(player.Entity).ID
		}
		gw.Log.Log(game.CatCollision, "terrain bounce: player=%d overlap=%.1f", playerNetID, overlap)
	}

	// Reflect velocity
	vel := gw.C.Velocity.Get(player.Entity)
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
	return float32(math.Sqrt(float64(x)))
}
