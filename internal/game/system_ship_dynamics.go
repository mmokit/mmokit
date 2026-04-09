package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ShipDynamicsSystem handles ship movement physics: linear drag, click-to-move
// steering toward MoveTarget, turn-rate-limited rotation, distance-based
// thrust ramp-down on approach, and afterburner speed boost.
//
// This is the game-specific replacement for the old ShipControlSystem. It
// reads MoveTarget and writes Velocity/Rotation; Position is updated by the
// downstream PhysicsSystem.
type ShipDynamicsSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		MT   *mmokit.MoveTarget
		Ship *gamecomp.ShipControl
		Vel  *mmokit.Velocity
		Rot  *mmokit.Rotation
	}]
}

func (s *ShipDynamicsSystem) Init() {
	s.gw = gwFromSystem(s.SystemBase)
	s.entities.Init(s)
}

func (s *ShipDynamicsSystem) Update(dt float32) {
	gw := s.gw

	// Collect docking entities — DockingSystem owns their drag and pull.
	dockingSessions := gw.Players.InState(StateDocking)

	// Frame-rate independent drag: vel *= exp(-drag * dt)
	dragFactor := float32(math.Exp(float64(-gw.Config.ShipDragCoeff * dt)))

	for e, b := range s.entities.All() {
		mt, ship, vel, rot := b.MT, b.Ship, b.Vel, b.Rot

		// Skip docking players — DockingSystem handles their drag and pull.
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

		// Determine effective thrust and max speed (afterburner check).
		thrust := ship.Thrust
		maxSpeed := ship.MaxSpeed
		if gw.C.StatusEffects.HasAll(e) {
			if eff := gw.C.StatusEffects.Get(e).Get(gamecomp.StatusAfterburner); eff != nil {
				thrust *= eff.Value
				maxSpeed *= eff.Value
			}
		}

		// 1. Always apply drag.
		vel.X *= dragFactor
		vel.Y *= dragFactor

		// 2. Dead stop below a small floor to prevent infinite creep.
		speed := float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
		if speed < 0.5 {
			vel.X = 0
			vel.Y = 0
		}

		// 3. If no active move target, let drag handle deceleration.
		if !mt.Active {
			continue
		}

		// Distance to destination (accounting for cross-cell targets).
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

		// Arrival: stop thrusting, let drag coast the ship to rest.
		if dist < gw.Config.MoveArrivalDist {
			mt.Active = false
			continue
		}

		// Turn toward destination with a rate limit for smooth rotation.
		targetAngle := float32(math.Atan2(float64(dy), float64(dx)))
		angleDiff := normalizeAngle(targetAngle - rot.Angle)
		maxTurn := ship.TurnRate * dt
		turnStep := angleDiff
		if turnStep > maxTurn {
			turnStep = maxTurn
		} else if turnStep < -maxTurn {
			turnStep = -maxTurn
		}
		rot.Angle += turnStep

		// Thrust: full when facing target, zero when perpendicular or away.
		alignment := float32(math.Cos(float64(angleDiff)))
		if alignment < 0 {
			alignment = 0
		}

		// Distance factor: ramp down thrust near the destination.
		distFactor := float32(1.0)
		if dist < gw.Config.MoveDecelDist {
			distFactor = dist / gw.Config.MoveDecelDist
		}

		thrustMag := thrust * alignment * distFactor * dt
		vel.X += float32(math.Cos(float64(rot.Angle))) * thrustMag
		vel.Y += float32(math.Sin(float64(rot.Angle))) * thrustMag

		// Max speed clamp — only while afterburner is active (safety).
		// Without a boost, drag naturally limits speed, allowing afterburner
		// speed to bleed off smoothly after the buff expires.
		if gw.C.StatusEffects.HasAll(e) && gw.C.StatusEffects.Get(e).Get(gamecomp.StatusAfterburner) != nil {
			speed = float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
			if speed > maxSpeed {
				scale := maxSpeed / speed
				vel.X *= scale
				vel.Y *= scale
			}
		}
	}
}
