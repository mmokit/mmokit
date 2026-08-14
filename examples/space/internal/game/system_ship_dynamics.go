package game

import (
	"math"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/coords"
	"github.com/zenion/mmokit/pkg/mmokit"
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

// EffectiveSpeedMul returns the combined movement multiplier from all
// active status effects on the entity (Afterburner amplifies, Slow
// attenuates). Returns 1.0 if se is nil. Exported so tests can verify
// the combination directly without simulating motion.
func EffectiveSpeedMul(se *gamecomp.StatusEffects) float32 {
	mul := float32(1.0)
	if se == nil {
		return mul
	}
	if af := se.Get(gamecomp.StatusAfterburner); af != nil {
		mul *= af.Value
	}
	if sc := se.Get(gamecomp.StatusSupercruise); sc != nil {
		mul *= sc.Value
	}
	if sl := se.Get(gamecomp.StatusSlow); sl != nil {
		mul *= sl.Value
	}
	return mul
}

// signf returns -1, 0, or +1 based on the sign of x.
func signf(x float32) float32 {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	default:
		return 0
	}
}

func (s *ShipDynamicsSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *ShipDynamicsSystem) Update(dt float32) {
	gw := s.gw

	// Collect docking entities — DockingSystem owns their drag and pull.
	dockingSessions := gw.Players.InState(StateDocking)

	// Frame-rate independent drag: vel *= exp(-drag * dt)
	dragFactor := float32(math.Exp(float64(-gw.Config.ShipDragCoeff * dt)))

	for e, b := range s.entities.Iter {
		mt, ship, vel, rot := b.MT, b.Ship, b.Vel, b.Rot
		entity := mmokit.EntityFromECS(gw.stage, e)

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

		// Determine effective thrust and max speed.
		// Afterburner amplifies, Slow attenuates — both flow through
		// EffectiveSpeedMul as a single multiplier on thrust + maxSpeed.
		thrust := ship.Thrust
		maxSpeed := ship.MaxSpeed
		se := mmokit.Get[gamecomp.StatusEffects](entity)
		speedMul := EffectiveSpeedMul(se)
		thrust *= speedMul
		maxSpeed *= speedMul

		// 1. Always apply drag.
		vel.X *= dragFactor
		vel.Y *= dragFactor

		// 2. Dead stop below a small floor to prevent infinite creep.
		speed := float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
		if speed < 0.5 {
			vel.X = 0
			vel.Y = 0
		}

		// 3. If no active move target, let drag handle linear deceleration
		// and bleed angular velocity to zero at turn-accel rate.
		if !mt.Active {
			if ship.AngularVel != 0 {
				step := ship.TurnAccel * dt
				if float32(math.Abs(float64(ship.AngularVel))) <= step {
					ship.AngularVel = 0
				} else if ship.AngularVel > 0 {
					ship.AngularVel -= step
				} else {
					ship.AngularVel += step
				}
				rot.Angle += ship.AngularVel * dt
			}
			continue
		}

		// Distance to destination (accounting for cross-cell targets).
		pos := mmokit.Get[mmokit.Position](entity)
		var cellDX, cellDY int32
		if sec := mmokit.Get[mmokit.CellCoord](entity); sec != nil {
			cellDX = mt.CellX - sec.CellX
			cellDY = mt.CellY - sec.CellY
		}
		dx := float32(cellDX)*coords.CellSize + mt.LocalX - pos.X
		dy := float32(cellDY)*coords.CellSize + mt.LocalY - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		// Arrival: stop thrusting, let drag coast the ship to rest.
		if dist < gw.Config.MoveArrivalDist {
			mt.Active = false
			continue
		}

		// Turn toward destination with angular acceleration for a smooth
		// curved-arc entry and exit. The control law is velocity-planned:
		// compute the ideal angular velocity that would exactly brake to
		// rest over the remaining angleDiff (v² = 2·α·s ⇒ v = √(2αs)),
		// clamped to the max turn rate. Then step current ω toward that
		// ideal at ±TurnAccel·dt. This produces smooth ramp-up, cruise at
		// max ω if the turn is big enough, and smooth ramp-down without
		// discrete braking jitter.
		targetAngle := float32(math.Atan2(float64(dy), float64(dx)))
		angleDiff := normalizeAngle(targetAngle - rot.Angle)

		const angEps = float32(0.001)
		absDiff := float32(math.Abs(float64(angleDiff)))

		if absDiff < angEps && float32(math.Abs(float64(ship.AngularVel))) < angEps {
			// Arrived on target — snap exact and kill residual ω.
			rot.Angle = targetAngle
			ship.AngularVel = 0
		} else {
			// Ideal ω magnitude for braking to rest over absDiff.
			desiredMag := float32(math.Sqrt(float64(2 * ship.TurnAccel * absDiff)))
			if desiredMag > ship.TurnRate {
				desiredMag = ship.TurnRate
			}
			desiredOmega := signf(angleDiff) * desiredMag

			// Converge current ω toward desired, rate-limited by TurnAccel.
			delta := desiredOmega - ship.AngularVel
			maxDelta := ship.TurnAccel * dt
			if delta > maxDelta {
				delta = maxDelta
			} else if delta < -maxDelta {
				delta = -maxDelta
			}
			ship.AngularVel += delta

			// Safety clamp to max angular velocity.
			if ship.AngularVel > ship.TurnRate {
				ship.AngularVel = ship.TurnRate
			} else if ship.AngularVel < -ship.TurnRate {
				ship.AngularVel = -ship.TurnRate
			}

			// If this tick's integration step would cross the target,
			// land exactly on the target angle and freeze ω.
			stepAng := ship.AngularVel * dt
			if signf(stepAng) == signf(angleDiff) &&
				float32(math.Abs(float64(stepAng))) >= absDiff {
				rot.Angle = targetAngle
				ship.AngularVel = 0
			} else {
				rot.Angle += stepAng
			}
		}

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

		// Max speed clamp — only while a movement-status is active
		// (afterburner boost or slow debuff). Without a status effect,
		// drag naturally limits speed, allowing afterburner-built speed
		// to bleed off smoothly after the buff expires. Slow needs the
		// clamp to bring an already-fast ship under its reduced cap
		// without waiting for drag.
		if speedMul != 1.0 {
			speed = float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
			if speed > maxSpeed {
				scale := maxSpeed / speed
				vel.X *= scale
				vel.Y *= scale
			}
		}
	}
}
