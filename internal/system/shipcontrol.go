package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ShipControlSystem steers ships toward their click-to-move destination.
type ShipControlSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter4[mmokit.MoveTarget, gamecomp.ShipControl, mmokit.Velocity, mmokit.Rotation]
}

func NewShipControlSystem(gw *game.GameWorld) *ShipControlSystem {
	return &ShipControlSystem{gw: gw}
}

func (s *ShipControlSystem) Name() string { return "ShipControl" }

func (s *ShipControlSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter4[mmokit.MoveTarget, gamecomp.ShipControl, mmokit.Velocity, mmokit.Rotation](gw.ECS).Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
	}

	// Frame-rate independent drag: vel *= exp(-drag * dt)
	dragFactor := float32(math.Exp(float64(-gw.Config.ShipDragCoeff * dt)))

	query := s.filter.Query()
	for query.Next() {
		mt, ship, vel, rot := query.Get()
		entity := query.Entity()

		// Determine effective thrust and max speed (Afterburner check)
		thrust := ship.Thrust
		maxSpeed := ship.MaxSpeed
		if gw.C.StatusEffects.HasAll(entity) {
			se := gw.C.StatusEffects.Get(entity)
			if eff := se.Get(gamecomp.StatusAfterburner); eff != nil {
				thrust *= eff.Value
				maxSpeed *= eff.Value
			}
		}

		// 1. Always apply drag
		vel.X *= dragFactor
		vel.Y *= dragFactor

		// 2. Dead stop to prevent infinite creep
		speed := float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
		if speed < 0.5 {
			vel.X = 0
			vel.Y = 0
		}

		// If no active move target, drag handles deceleration — done
		if !mt.Active {
			continue
		}

		// 3. Distance to destination (accounting for cross-sector targets)
		pos := gw.C.Position.Get(entity)
		var sectorDX, sectorDY int32
		if gw.C.SectorCoord.HasAll(entity) {
			sec := gw.C.SectorCoord.Get(entity)
			sectorDX = mt.SX - sec.SX
			sectorDY = mt.SY - sec.SY
		}
		dx := float32(sectorDX)*coords.SectorSize + mt.X - pos.X
		dy := float32(sectorDY)*coords.SectorSize + mt.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		// Arrival: stop thrusting, let drag coast the ship to rest
		if dist < gw.Config.MoveArrivalDist {
			mt.Active = false
			continue
		}

		// 4. Turn toward destination
		targetAngle := float32(math.Atan2(float64(dy), float64(dx)))
		angleDiff := normalizeAngle(targetAngle - rot.Angle)
		turnStep := angleDiff
		maxTurn := ship.TurnRate * dt
		if turnStep > maxTurn {
			turnStep = maxTurn
		} else if turnStep < -maxTurn {
			turnStep = -maxTurn
		}
		rot.Angle += turnStep

		// 5. Compute thrust
		// Alignment: full thrust when facing target, zero when perpendicular/away
		alignment := float32(math.Cos(float64(angleDiff)))
		if alignment < 0 {
			alignment = 0
		}

		// Distance factor: ramp down thrust near destination
		distFactor := float32(1.0)
		if dist < gw.Config.MoveDecelDist {
			distFactor = dist / gw.Config.MoveDecelDist
		}

		thrustMag := thrust * alignment * distFactor * dt
		vel.X += float32(math.Cos(float64(rot.Angle))) * thrustMag
		vel.Y += float32(math.Sin(float64(rot.Angle))) * thrustMag

		// 6. Max speed clamp — only while afterburner is active (safety).
		// When no boost is active, drag naturally limits speed, allowing
		// afterburner speed to bleed off smoothly after the buff expires.
		if gw.C.StatusEffects.HasAll(entity) {
			if eff := gw.C.StatusEffects.Get(entity).Get(gamecomp.StatusAfterburner); eff != nil {
				speed = float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
				if speed > maxSpeed {
					scale := maxSpeed / speed
					vel.X *= scale
					vel.Y *= scale
				}
			}
		}
	}
}

// normalizeAngle wraps an angle to [-pi, pi].
func normalizeAngle(a float32) float32 {
	for a > math.Pi {
		a -= 2 * math.Pi
	}
	for a < -math.Pi {
		a += 2 * math.Pi
	}
	return a
}
