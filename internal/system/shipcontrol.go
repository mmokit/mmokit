package system

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ShipControlSystem steers ships toward their click-to-move destination
// or in the direction specified by direction-vector input.
type ShipControlSystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	entities mmokit.Query[struct {
		MT    *mmokit.MoveTarget
		Ship  *gamecomp.ShipControl
		Vel   *mmokit.Velocity
		Rot   *mmokit.Rotation
		Input *gamecomp.PlayerInput `ecs:"optional"`
	}]
}

func (s *ShipControlSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.entities.Init(s)
}

func (s *ShipControlSystem) Update(dt float32) {
	gw := s.gw

	// Collect docking entities — DockingSystem owns their drag and pull
	dockingSessions := gw.Players.InState(game.StateDocking)

	// Frame-rate independent drag: vel *= exp(-drag * dt)
	dragFactor := float32(math.Exp(float64(-gw.Config.ShipDragCoeff * dt)))

	for e, b := range s.entities.All() {
		mt, ship, vel, rot := b.MT, b.Ship, b.Vel, b.Rot

		// Skip docking players — DockingSystem handles their drag and pull
		isDocking := false
		for _, sess := range dockingSessions {
			if sess.Entity == e {
				isDocking = true
				break
			}
		}
		if isDocking {
			continue
		}

		// Determine effective thrust and max speed (Afterburner check)
		thrust := ship.Thrust
		maxSpeed := ship.MaxSpeed
		if gw.C.StatusEffects.HasAll(e) {
			se := gw.C.StatusEffects.Get(e)
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

		// 3. Check for direction-vector mode
		var dirInput *gamecomp.PlayerInput
		if b.Input != nil && b.Input.DirActive {
			dirInput = b.Input
		}

		if dirInput != nil {
			// DIRECTION MODE: thrust in input direction
			targetAngle := float32(math.Atan2(float64(dirInput.DirY), float64(dirInput.DirX)))
			angleDiff := normalizeAngle(targetAngle - rot.Angle)
			maxTurn := ship.TurnRate * dt
			turnStep := angleDiff
			if turnStep > maxTurn {
				turnStep = maxTurn
			} else if turnStep < -maxTurn {
				turnStep = -maxTurn
			}
			rot.Angle += turnStep

			// Thrust based on alignment only (no distance factor)
			alignment := float32(math.Cos(float64(angleDiff)))
			if alignment < 0 {
				alignment = 0
			}
			thrustMag := thrust * alignment * dt
			vel.X += float32(math.Cos(float64(rot.Angle))) * thrustMag
			vel.Y += float32(math.Sin(float64(rot.Angle))) * thrustMag

			// Always clamp max speed in direction mode (no natural stopping point)
			speed = float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
			if speed > maxSpeed {
				scale := maxSpeed / speed
				vel.X *= scale
				vel.Y *= scale
			}
		} else if mt.Active {
			// DESTINATION MODE: steer toward click-to-move target (existing logic)

			// Distance to destination (accounting for cross-cell targets)
			pos := gw.C.Position.Get(e)
			var cellDX, cellDY int32
			if gw.C.CellCoord.HasAll(e) {
				sec := gw.C.CellCoord.Get(e)
				cellDX = mt.CellX - sec.CellX
				cellDY = mt.CellY - sec.CellY
			}
			dx := float32(cellDX)*coords.CellSize + mt.X - pos.X
			dy := float32(cellDY)*coords.CellSize + mt.Y - pos.Y
			dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

			// Arrival: stop thrusting, let drag coast the ship to rest
			if dist < gw.Config.MoveArrivalDist {
				mt.Active = false
				continue
			}

			// Turn toward destination
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

			// Compute thrust
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

			// Max speed clamp — only while afterburner is active (safety).
			// When no boost is active, drag naturally limits speed, allowing
			// afterburner speed to bleed off smoothly after the buff expires.
			if gw.C.StatusEffects.HasAll(e) {
				if eff := gw.C.StatusEffects.Get(e).Get(gamecomp.StatusAfterburner); eff != nil {
					speed = float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
					if speed > maxSpeed {
						scale := maxSpeed / speed
						vel.X *= scale
						vel.Y *= scale
					}
				}
			}
		}
		// else: idle — drag handles deceleration (already applied above)
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
