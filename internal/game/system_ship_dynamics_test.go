package game

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// shipDynamicsTestFixture sets up a minimal world + initialized
// ShipDynamicsSystem plus a single ship entity positioned at the origin
// facing +X (angle=0) at rest with an inactive MoveTarget.
type shipDynamicsTestFixture struct {
	gw     *GameWorld
	sys    *ShipDynamicsSystem
	entity mmokit.Entity
}

func newShipDynamicsFixture(t *testing.T) *shipDynamicsTestFixture {
	t.Helper()
	gw, _ := newTestGameWorld()

	// Disable linear drag so velocity assertions are not confounded. The
	// dynamics system still runs drag but at coeff=0 it's a no-op.
	gw.Config.ShipDragCoeff = 0

	w := gw.Stage.ECSWorld()
	mapper := ecs.NewMap6[
		mmokit.Position,
		mmokit.Velocity,
		mmokit.Rotation,
		gamecomp.ShipControl,
		mmokit.MoveTarget,
		mmokit.NetworkID,
	](w)

	const netID uint32 = 9000
	handle := mapper.NewEntity(
		&mmokit.Position{X: 0, Y: 0},
		&mmokit.Velocity{X: 0, Y: 0},
		&mmokit.Rotation{Angle: 0},
		&gamecomp.ShipControl{
			Thrust:    gw.Config.ShipThrust,
			TurnRate:  gw.Config.ShipTurnRate,
			TurnAccel: gw.Config.ShipTurnAccel,
			MaxSpeed:  gw.Config.MaxSpeed,
		},
		&mmokit.MoveTarget{Active: false},
		&mmokit.NetworkID{ID: netID},
	)
	gw.Stage.RegisterLiveNetID(netID, handle)
	entity := mmokit.EntityFromECS(gw.Stage, handle)

	sys := &ShipDynamicsSystem{}
	mmokit.WireSystem(sys, w, gw.eng, gw)

	return &shipDynamicsTestFixture{gw: gw, sys: sys, entity: entity}
}

// setTarget activates the MoveTarget at the given world-relative offset
// from the entity's current position, expressed in local-cell coords
// (CellX/Y = 0 so LocalX/Y are absolute in the test frame).
func (f *shipDynamicsTestFixture) setTarget(localX, localY float32) {
	mt := mmokit.Get[mmokit.MoveTarget](f.entity)
	mt.Active = true
	mt.CellX = 0
	mt.CellY = 0
	mt.LocalX = localX
	mt.LocalY = localY
}

func (f *shipDynamicsTestFixture) ship() *gamecomp.ShipControl {
	return mmokit.Get[gamecomp.ShipControl](f.entity)
}

func (f *shipDynamicsTestFixture) rot() *mmokit.Rotation {
	return mmokit.Get[mmokit.Rotation](f.entity)
}

// runTicks advances the system by n ticks of dt seconds each.
func (f *shipDynamicsTestFixture) runTicks(dt float32, n int) {
	for range n {
		f.sys.Update(dt)
	}
}

// TestShipTurn_RampUpFromRest verifies that starting from angVel=0 with a
// target far enough to require full-rate braking distance, the angular
// velocity ramps up at TurnAccel (not instantly to TurnRate like the old
// constant-rate system).
func TestShipTurn_RampUpFromRest(t *testing.T) {
	f := newShipDynamicsFixture(t)

	// Target straight up (+Y), angleDiff = +pi/2.
	f.setTarget(0, 1000)

	const dt = float32(0.05) // 20 Hz
	ship := f.ship()
	turnAccel := ship.TurnAccel // 8.0

	// After one tick, angVel should be turnAccel*dt (0.4), not the old
	// system's turnRate clamp (0.2).
	f.sys.Update(dt)
	expectedAfter1 := turnAccel * dt
	if diff := math.Abs(float64(ship.AngularVel - expectedAfter1)); diff > 1e-5 {
		t.Fatalf("after 1 tick: AngularVel = %f, want %f", ship.AngularVel, expectedAfter1)
	}

	// After another tick it should still be accelerating (no instant snap).
	prev := ship.AngularVel
	f.sys.Update(dt)
	if ship.AngularVel <= prev {
		t.Fatalf("after 2 ticks: AngularVel = %f should be > %f (still accelerating)",
			ship.AngularVel, prev)
	}
	// And the delta should match α·dt (constant-acceleration).
	if diff := math.Abs(float64((ship.AngularVel - prev) - turnAccel*dt)); diff > 1e-5 {
		t.Errorf("tick-2 delta = %f, want %f", ship.AngularVel-prev, turnAccel*dt)
	}
}

// TestShipTurn_ReachesMaxOnLargeTurn verifies that for a turn large enough
// to have a cruise phase (|angleDiff| > 2·brakeDist(maxOmega)), AngularVel
// actually reaches TurnRate during the cruise.
func TestShipTurn_ReachesMaxOnLargeTurn(t *testing.T) {
	f := newShipDynamicsFixture(t)
	ship := f.ship()
	// Override TurnRate so a cruise phase is feasible within the
	// normalized angleDiff range [-π, π]. With TurnAccel=8, brakeDist at
	// peak ω is TurnRate²/(2·TurnAccel); for a cruise phase we need
	// absDiff > 2·brakeDist, i.e. TurnRate² < TurnAccel·absDiff. At
	// absDiff=3 and TurnAccel=8 we need TurnRate < √24 ≈ 4.9. Use 4.0 so
	// brakeDist = 16/16 = 1 rad and the full cruise math (0.5s ramp → 1s
	// cruise at ω=4 → 0.5s brake) fits inside a 3 rad turn.
	ship.TurnRate = 4
	// Target at angle 3 rad from +X: (cos(3), sin(3)) ≈ (-0.99, 0.14).
	f.setTarget(-990, 141)

	const dt = float32(0.05)
	// Run long enough to reach cruise: accel phase ~0.5s, then cruise.
	var peak float32
	for range 30 {
		f.sys.Update(dt)
		if ship.AngularVel > peak {
			peak = ship.AngularVel
		}
	}
	if peak < ship.TurnRate-1e-4 {
		t.Errorf("peak AngularVel = %f, want >= TurnRate (%f)", peak, ship.TurnRate)
	}
}

// TestShipTurn_StopsOnTargetWithoutOvershoot verifies that the ship
// decelerates cleanly and comes to rest exactly on the target heading.
func TestShipTurn_StopsOnTargetWithoutOvershoot(t *testing.T) {
	f := newShipDynamicsFixture(t)
	// Target at +pi/4 radians (upper-right).
	f.setTarget(100, 100)

	const dt = float32(0.05)
	// Plenty of ticks to ramp up, cruise, brake, and settle.
	f.runTicks(dt, 200)

	ship := f.ship()
	rot := f.rot()
	targetAngle := float32(math.Pi / 4)
	angleDiff := float32(math.Abs(float64(normalizeAngle(rot.Angle - targetAngle))))

	if angleDiff > 0.01 {
		t.Errorf("final angle diff = %f rad, want < 0.01", angleDiff)
	}
	if math.Abs(float64(ship.AngularVel)) > 0.05 {
		t.Errorf("final AngularVel = %f, want ~0", ship.AngularVel)
	}
}

// TestShipTurn_DirectionReversal verifies that if the target flips to the
// opposite side mid-turn, the angular velocity decelerates through zero
// and reverses sign.
func TestShipTurn_DirectionReversal(t *testing.T) {
	f := newShipDynamicsFixture(t)

	// Turn left first (target at +pi/2).
	f.setTarget(0, 1000)
	const dt = float32(0.05)
	f.runTicks(dt, 5) // let it spin up a bit

	ship := f.ship()
	if ship.AngularVel <= 0 {
		t.Fatalf("setup: expected positive AngularVel, got %f", ship.AngularVel)
	}
	initial := ship.AngularVel

	// Flip target to -pi/2 (fully opposite direction).
	f.setTarget(0, -1000)

	// One tick of braking must reduce positive angVel.
	f.sys.Update(dt)
	if ship.AngularVel >= initial {
		t.Errorf("after reversal tick: AngularVel = %f, expected < %f", ship.AngularVel, initial)
	}

	// Enough ticks that angVel must have crossed zero and become negative.
	f.runTicks(dt, 20)
	if ship.AngularVel >= 0 {
		t.Errorf("after reversal: AngularVel = %f, expected negative", ship.AngularVel)
	}
}

// TestShipTurn_InactiveTargetBleedOff verifies that with no active move
// target, a nonzero AngularVel bleeds to zero at the TurnAccel rate.
func TestShipTurn_InactiveTargetBleedOff(t *testing.T) {
	f := newShipDynamicsFixture(t)

	// Preload AngularVel at max with no active target.
	ship := f.ship()
	ship.AngularVel = ship.TurnRate

	const dt = float32(0.05)
	// Expected bleed-off time = TurnRate / TurnAccel seconds.
	bleedTicks := int(math.Ceil(float64(ship.TurnRate/ship.TurnAccel/dt))) + 1
	f.runTicks(dt, bleedTicks)

	if math.Abs(float64(ship.AngularVel)) > 1e-5 {
		t.Errorf("after bleed: AngularVel = %f, want 0", ship.AngularVel)
	}
}

// TestShipTurn_AlreadyFacingTarget verifies no NaN / jitter when the
// target is already directly ahead.
func TestShipTurn_AlreadyFacingTarget(t *testing.T) {
	f := newShipDynamicsFixture(t)

	// Target directly ahead (+X), ship facing +X.
	f.setTarget(1000, 0)

	const dt = float32(0.05)
	f.runTicks(dt, 10)

	ship := f.ship()
	rot := f.rot()
	if math.IsNaN(float64(rot.Angle)) || math.IsNaN(float64(ship.AngularVel)) {
		t.Fatal("NaN in angle or AngularVel")
	}
	if math.Abs(float64(ship.AngularVel)) > 0.05 {
		t.Errorf("AngularVel = %f, want ~0 (already facing target)", ship.AngularVel)
	}
	if math.Abs(float64(rot.Angle)) > 0.01 {
		t.Errorf("rot.Angle = %f, want ~0", rot.Angle)
	}
}
