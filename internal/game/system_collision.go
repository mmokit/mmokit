package game

import (
	"math"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// CollisionSystem handles terrain bounce (player-vs-asteroid).
// Actual combat damage is hitscan, handled by AbilitySystem via GameWorld.ApplyDamage.
type CollisionSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	nearby []mmokit.SpatialEntry // reusable scratch buffer
}

func (s *CollisionSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
	s.nearby = make([]mmokit.SpatialEntry, 0, 64)
}

func (s *CollisionSystem) Update(dt float32) {
	gw := s.gw

	// Terrain bounce: only check player entities against nearby terrain.
	// This avoids the O(n²) full-grid scan that was the previous bottleneck.
	gw.Players.ForEach(mmokit.StateActive, func(sess *mmokit.PlayerSession) {
		entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
		if !entity.Alive() {
			return
		}
		if mmokit.Has[mmokit.Ghost](entity) || mmokit.Has[mmokit.Replica](entity) {
			return
		}
		pos := mmokit.Get[mmokit.Position](entity)
		col := mmokit.Get[mmokit.Collider](entity)
		if pos == nil || col == nil {
			return
		}

		// Query nearby entries within the player's bounding radius + margin
		searchRadius := col.Radius + gw.Config.AsteroidMaxRadius
		s.nearby = gw.Spatial.QueryRadius(pos.X, pos.Y, searchRadius, s.nearby[:0])

		for _, terrain := range s.nearby {
			// Ships collide with both world props (asteroids = LayerProp)
			// and static structures (cave walls + stations = LayerStatic).
			// LayerEntity (ships/NPCs) and Layer=0 markers are skipped.
			if terrain.Layer != spatial.LayerProp && terrain.Layer != spatial.LayerStatic {
				continue
			}
			if !gw.stage.ECSWorld().Alive(terrain.Entity) {
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
			if rot := mmokit.Get[mmokit.Rotation](entity); rot != nil {
				rotation = rot.Angle
			}
			playerEntry := mmokit.SpatialEntry{
				Entity:   sess.Entity,
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
	})
}

func (s *CollisionSystem) handleTerrainCollision(player, terrain mmokit.SpatialEntry) {
	gw := s.gw

	playerE := mmokit.EntityFromECS(gw.stage, player.Entity)
	terrainE := mmokit.EntityFromECS(gw.stage, terrain.Entity)
	playerPos := mmokit.Get[mmokit.Position](playerE)
	terrainPos := mmokit.Get[mmokit.Position](terrainE)
	if playerPos == nil || terrainPos == nil {
		return
	}

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

		gw.eng.Log.Log(CatWorldCollision, "terrain bounce: player=%d overlap=%.1f", playerE.NetID(), overlap)
	}

	// Reflect velocity
	vel := mmokit.Get[mmokit.Velocity](playerE)
	if vel == nil {
		return
	}
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
