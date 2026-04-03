package main

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// CollisionSystem detects snake head-to-body and head-to-head collisions.
// It queries the spatial grid for nearby LayerSnakeHead and LayerSnakeBody
// entries, so it correctly detects collisions with long snake bodies even
// when the other snake's head is far away.
type CollisionSystem struct {
	mmokit.SystemBase
	gw     *SlitherWorld
	filter *ecs.Filter4[mmokit.Position, mmokit.Rotation, SnakeState, mmokit.NetworkID]
	buf    []mmokit.SpatialEntry
}

func (s *CollisionSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.filter = ecs.NewFilter4[mmokit.Position, mmokit.Rotation, SnakeState, mmokit.NetworkID](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *CollisionSystem) Update(dt float32) {
	gw := s.gw

	headRadius := gw.Cfg.HeadCollisionRadius
	bodyRadius := gw.Cfg.BodyCollisionRadius
	headKillDist := (headRadius * 2) * (headRadius * 2)
	bodyKillDist := (headRadius + bodyRadius) * (headRadius + bodyRadius)

	type snakeInfo struct {
		entity ecs.Entity
		posX   float32
		posY   float32
		angle  float32
		netID  uint32
		mass   float32
	}
	var snakes []snakeInfo

	query := s.filter.Query()
	for query.Next() {
		pos, rot, state, netID := query.Get()
		snakes = append(snakes, snakeInfo{
			entity: query.Entity(),
			posX:   pos.X,
			posY:   pos.Y,
			angle:  rot.Angle,
			netID:  netID.ID,
			mass:   state.Mass,
		})
	}

	killed := make(map[ecs.Entity]bool)

	for _, snake := range snakes {
		if killed[snake.entity] {
			continue
		}

		// Query nearby snake heads and body segments
		queryRadius := headRadius + bodyRadius + 50
		s.buf = gw.Spatial.QueryRadius(snake.posX, snake.posY, queryRadius, s.buf[:0])

		for _, entry := range s.buf {
			if entry.Entity == snake.entity {
				continue // skip self (head and own body segments)
			}
			if !gw.ECSWorld().Alive(entry.Entity) {
				continue
			}

			dx := snake.posX - entry.X
			dy := snake.posY - entry.Y
			distSq := dx*dx + dy*dy

			if entry.Layer == LayerSnakeHead {
				// Head-to-head collision — the snake whose head is further
				// "in front" along its direction of travel survives.
				if distSq < headKillDist {
					killerNetID := uint32(0)
					var otherAngle float32
					var otherMass float32
					if gw.NetworkIDMap().HasAll(entry.Entity) {
						killerNetID = gw.NetworkIDMap().Get(entry.Entity).ID
					}
					if gw.SnakeStateMap.HasAll(entry.Entity) {
						otherMass = gw.SnakeStateMap.Get(entry.Entity).Mass
					}
					if gw.RotationMap.HasAll(entry.Entity) {
						otherAngle = gw.RotationMap.Get(entry.Entity).Angle
					}

					// Project each head forward along its direction.
					// The midpoint between the two heads is the collision point.
					// The snake whose forward vector points more toward the
					// collision point (i.e. is "ahead") wins.
					midX := (snake.posX + entry.X) * 0.5
					midY := (snake.posY + entry.Y) * 0.5

					// Dot product of (mid - head) with forward direction
					aDot := (midX-snake.posX)*float32(math.Cos(float64(snake.angle))) +
						(midY-snake.posY)*float32(math.Sin(float64(snake.angle)))
					bDot := (midX-entry.X)*float32(math.Cos(float64(otherAngle))) +
						(midY-entry.Y)*float32(math.Sin(float64(otherAngle)))

					// If the two snakes are heading nearly opposite (head-on),
					// the larger snake wins. Otherwise the one further "in front" wins.
					// Compute angle between travel directions: 180° = perfect head-on.
					angleDiff := math.Abs(math.Atan2(
						math.Sin(float64(snake.angle)-float64(otherAngle)),
						math.Cos(float64(snake.angle)-float64(otherAngle)),
					))
					const headOnThreshold = math.Pi * 0.75 // within 135° of opposite = head-on

					var aWins bool
					if math.Pi-angleDiff < math.Pi-headOnThreshold {
						// Near head-on — larger snake survives
						aWins = snake.mass >= otherMass
					} else {
						// Clear angle difference — the one further ahead wins
						aWins = aDot > bDot
					}

					if aWins {
						// Snake A wins — Snake B dies
						if !killed[entry.Entity] {
							gw.PendingKills = append(gw.PendingKills, KillInfo{
								Victim: entry.Entity, Killer: snake.entity,
								VictimNet: killerNetID, KillerNet: snake.netID,
								Mass: otherMass, PosX: entry.X, PosY: entry.Y,
								HasKiller: true,
							})
							killed[entry.Entity] = true
						}
					} else {
						// Snake B was ahead — Snake A dies
						if !killed[snake.entity] {
							gw.PendingKills = append(gw.PendingKills, KillInfo{
								Victim: snake.entity, Killer: entry.Entity,
								VictimNet: snake.netID, KillerNet: killerNetID,
								Mass: snake.mass, PosX: snake.posX, PosY: snake.posY,
								HasKiller: true,
							})
							killed[snake.entity] = true
						}
					}

					gw.Engine().Log.Log(CatSnakeCollision, "head-to-head: snake=%d vs snake=%d (aDot=%.2f bDot=%.2f)", snake.netID, killerNetID, aDot, bDot)
					break
				}
			} else if entry.Layer == LayerSnakeBody {
				// Head-to-body collision (spatial grid checkpoint hit)
				// Do a fine-grained check against actual body segments near this checkpoint
				if distSq < bodyKillDist {
					killerNetID := uint32(0)
					if gw.NetworkIDMap().HasAll(entry.Entity) {
						killerNetID = gw.NetworkIDMap().Get(entry.Entity).ID
					}
					gw.PendingKills = append(gw.PendingKills, KillInfo{
						Victim: snake.entity, Killer: entry.Entity,
						VictimNet: snake.netID, KillerNet: killerNetID,
						Mass: snake.mass, PosX: snake.posX, PosY: snake.posY,
						HasKiller: true,
					})
					killed[snake.entity] = true
					gw.Engine().Log.Log(CatSnakeCollision, "head-to-body: snake=%d hit snake=%d", snake.netID, killerNetID)
					break
				}
			}
		}
	}
}
