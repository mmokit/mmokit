package game

import (
	"math"

	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// CollisionSystem handles terrain bounce (player-vs-asteroid).
// Actual combat damage is hitscan, handled by AbilitySystem via GameWorld.ApplyDamage.
type CollisionSystem struct {
	mmokit.SystemBase
	gw     *GameWorld
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

		// Query nearby entries within the player's bounding radius +
		// margin. The margin must cover the LARGEST possible terrain
		// bounding radius — dungeon walls have bounding radius up to
		// roughly half their length (corridor walls can be ~30u, perimeter
		// segments ~25u). Using AsteroidMaxRadius alone (2u) was sized for
		// regular asteroids and missed every wall in the spatial query.
		terrainMargin := gw.Config.AsteroidMaxRadius
		if dungeonMargin := gw.Config.DungeonAsteroidRadius * 0.5; dungeonMargin > terrainMargin {
			terrainMargin = dungeonMargin
		}
		searchRadius := col.Radius + terrainMargin
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

	// Resolve contact: closest point on the terrain surface to the player
	// center. For circles that's center+radius*direction; for OBB rects we
	// rotate the player into the rect's local frame, clamp to the box, and
	// rotate back. Either way we recover an outward normal + overlap.
	var contactX, contactY, nx, ny, overlap float32
	if terrain.Shape == spatial.ShapeRect {
		contactX, contactY, nx, ny, overlap = obbContact(
			playerPos.X, playerPos.Y, player.Radius,
			terrainPos.X, terrainPos.Y, terrain.Width, terrain.Height, terrain.Rotation,
		)
	} else {
		contactX, contactY, nx, ny, overlap = circleContact(
			playerPos.X, playerPos.Y, player.Radius,
			terrainPos.X, terrainPos.Y, terrain.Radius,
		)
	}
	_ = contactX
	_ = contactY
	if overlap <= 0 {
		return
	}

	playerPos.X += nx * overlap
	playerPos.Y += ny * overlap
	gw.eng.Log.Log(CatWorldCollision, "terrain bounce: player=%d overlap=%.2f shape=%d", playerE.NetID(), overlap, terrain.Shape)

	// Reflect velocity along the contact normal.
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

// circleContact returns the contact point, outward normal, and overlap
// for a circle-vs-circle penetration. overlap <= 0 means no contact.
func circleContact(px, py, pr, cx, cy, cr float32) (contactX, contactY, nx, ny, overlap float32) {
	dx := px - cx
	dy := py - cy
	d2 := dx*dx + dy*dy
	sum := pr + cr
	if d2 >= sum*sum {
		return 0, 0, 0, 0, 0
	}
	d := sqrt32(d2)
	if d < 1e-6 {
		// Coincident centers — push along +X by convention.
		return cx + cr, cy, 1, 0, sum
	}
	nx, ny = dx/d, dy/d
	contactX = cx + nx*cr
	contactY = cy + ny*cr
	overlap = sum - d
	return
}

// obbContact returns the contact point, outward normal, and overlap
// for a circle-vs-oriented-rectangle penetration. overlap <= 0 means
// no contact. The "outward" normal points from the rect surface toward
// the player, so caller can push the player out by overlap*normal.
//
// Algorithm: transform the player center into the rect's local frame,
// clamp to the rect's extents to find the closest surface point, then
// compute the gap (player.radius - distance to closest point). When
// the player center is inside the rect, the closest *surface* point
// lies on the nearest edge — we pick whichever extent (X or Y) has
// the smaller penetration and push along that axis.
func obbContact(px, py, pr, rx, ry, rw, rh, rrot float32) (contactX, contactY, nx, ny, overlap float32) {
	// Transform player into local frame (inverse rotation).
	cosA := float32(math.Cos(float64(-rrot)))
	sinA := float32(math.Sin(float64(-rrot)))
	dx := px - rx
	dy := py - ry
	lpx := cosA*dx - sinA*dy
	lpy := sinA*dx + cosA*dy

	hw := rw / 2
	hh := rh / 2

	// Closest point in local frame: clamp to [-hw, hw] × [-hh, hh].
	clx := lpx
	switch {
	case clx > hw:
		clx = hw
	case clx < -hw:
		clx = -hw
	}
	cly := lpy
	switch {
	case cly > hh:
		cly = hh
	case cly < -hh:
		cly = -hh
	}

	outside := lpx > hw || lpx < -hw || lpy > hh || lpy < -hh
	if outside {
		ddx := lpx - clx
		ddy := lpy - cly
		d2 := ddx*ddx + ddy*ddy
		if d2 >= pr*pr {
			return 0, 0, 0, 0, 0
		}
		d := sqrt32(d2)
		var lnx, lny float32
		if d < 1e-6 {
			// On the edge — push perpendicular to the nearest axis.
			if math.Abs(float64(hw-lpx)) < math.Abs(float64(hh-lpy)) {
				lnx, lny = 1, 0
				if lpx < 0 {
					lnx = -1
				}
			} else {
				lnx, lny = 0, 1
				if lpy < 0 {
					lny = -1
				}
			}
		} else {
			lnx, lny = ddx/d, ddy/d
		}
		overlap = pr - d
		// Local contact point on the rect surface.
		lcx, lcy := clx, cly
		// Rotate normal + contact back into world frame.
		cosB := float32(math.Cos(float64(rrot)))
		sinB := float32(math.Sin(float64(rrot)))
		nx = cosB*lnx - sinB*lny
		ny = sinB*lnx + cosB*lny
		contactX = rx + cosB*lcx - sinB*lcy
		contactY = ry + sinB*lcx + cosB*lcy
		return
	}

	// Player center is *inside* the rect. Push out along the nearest
	// edge — the axis with the smaller penetration wins.
	penX := hw - float32(math.Abs(float64(lpx)))
	penY := hh - float32(math.Abs(float64(lpy)))
	var lnx, lny, lcx, lcy float32
	if penX < penY {
		if lpx >= 0 {
			lnx, lcx = 1, hw
		} else {
			lnx, lcx = -1, -hw
		}
		lcy = lpy
		overlap = pr + penX
	} else {
		if lpy >= 0 {
			lny, lcy = 1, hh
		} else {
			lny, lcy = -1, -hh
		}
		lcx = lpx
		overlap = pr + penY
	}
	cosB := float32(math.Cos(float64(rrot)))
	sinB := float32(math.Sin(float64(rrot)))
	nx = cosB*lnx - sinB*lny
	ny = sinB*lnx + cosB*lny
	contactX = rx + cosB*lcx - sinB*lcy
	contactY = ry + sinB*lcx + cosB*lcy
	return
}

func sqrt32(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}
