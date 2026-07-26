package game

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/system"
)

// updateShipDynGolden regenerates the fixture instead of asserting against it.
// Driven by `just shipdyn-golden`.
var updateShipDynGolden = flag.Bool("update-shipdyn-golden", false,
	"regenerate web-pixi/src/__tests__/testdata/shipdyn_golden.json from the real systems")

// shipDynGoldenPath is the fixture consumed by
// web-pixi/src/__tests__/prediction-golden.test.ts.
const shipDynGoldenPath = "../../web-pixi/src/__tests__/testdata/shipdyn_golden.json"

// The generator lives here rather than under cmd/ because driving the REAL
// systems needs a real Stage + GameWorld, and the only bootstrap for that
// (newTestGameWorld) is test-scoped. Duplicating the bootstrap in a cmd/ main
// would let it drift from the one production-shaped world the tests use, which
// would defeat the entire point of the golden.

type shipDynGoldenTick struct {
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	VelocityX  float64 `json:"velocityX"`
	VelocityY  float64 `json:"velocityY"`
	Angle      float64 `json:"angle"`
	AngularVel float64 `json:"angularVelocity"`
}

type shipDynGoldenParams struct {
	Thrust               float64 `json:"thrust"`
	MaxSpeed             float64 `json:"maxSpeed"`
	TurnRate             float64 `json:"turnRate"`
	TurnAccel            float64 `json:"turnAccel"`
	DragCoefficient      float64 `json:"dragCoefficient"`
	ArrivalDistance      float64 `json:"arrivalDistance"`
	DecelerationDistance float64 `json:"decelerationDistance"`
	SpeedMultiplier      float64 `json:"speedMultiplier"`
}

type shipDynGoldenMoveTarget struct {
	Active bool    `json:"active"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

type shipDynGoldenCase struct {
	Name       string                  `json:"name"`
	Seed       shipDynGoldenTick       `json:"seed"`
	MoveTarget shipDynGoldenMoveTarget `json:"moveTarget"`
	Params     shipDynGoldenParams     `json:"params"`
	Ticks      []shipDynGoldenTick     `json:"ticks"`
}

type shipDynGoldenManifest struct {
	// Note is a message to whoever finds this file failing.
	Note           string              `json:"note"`
	TickIntervalMs float64             `json:"tickIntervalMs"`
	Cases          []shipDynGoldenCase `json:"cases"`
}

// shipDynScenario describes one scripted world to drive through the real
// systems. Fields left zero take the game-config default.
type shipDynScenario struct {
	name       string
	posX, posY float32
	velX, velY float32
	angle      float32
	angularVel float32
	targetX    float32
	targetY    float32
	noTarget   bool
	// speedMul, when non-zero, installs an Afterburner status effect with that
	// value — the only way to exercise the conditional max-speed clamp, which
	// is skipped entirely when the multiplier is exactly 1.
	speedMul float32
	// dragCoeff overrides GameConfig.ShipDragCoeff.
	dragCoeff float32
	// maxSpeed overrides ShipControl.MaxSpeed.
	maxSpeed float32
}

func shipDynScenarios() []shipDynScenario {
	return []shipDynScenario{
		{
			name: "straight-line-accelerate",
			// Target far along +X with the ship already facing it: pure thrust
			// with no turn, no deceleration ramp, no arrival.
			targetX: 5000, targetY: 0,
		},
		{
			name: "wide-turn",
			// Target at 90 degrees: exercises the velocity-planned turn
			// (sqrt(2*alpha*s) ramp up, cruise, ramp down) and the cos(angleDiff)
			// thrust alignment gate.
			targetX: 0, targetY: 5000,
		},
		{
			name: "crossing-snap",
			// A tiny angular error with a large TurnAccel makes the integration
			// step overshoot, so the crossing branch snaps the angle exactly onto
			// target and freezes angular velocity.
			angle:   0.02,
			targetX: 5000, targetY: 0,
		},
		{
			name: "arrival-radius-stop",
			// Target inside MoveArrivalDist: the target deactivates on the first
			// tick and drag coasts the ship down.
			velX:    40,
			targetX: 10, targetY: 0,
		},
		{
			name: "drag-only-coast-with-angular-bleed",
			// No active target: drag decays velocity, the 0.5 stop floor kills
			// residual creep, and angular velocity bleeds to zero at TurnAccel.
			velX: 30, velY: -12, angularVel: 3.0, noTarget: true,
		},
		{
			name: "afterburner-speed-clamp",
			// speedMul != 1 turns the max-speed clamp on. Start above the cap so
			// the clamp actually bites rather than being a no-op.
			velX: 200, velY: 0, maxSpeed: 60, speedMul: 2.0,
			targetX: 5000, targetY: 0,
		},
	}
}

const shipDynGoldenTicks = 8

// runShipDynScenario drives the REAL ShipDynamicsSystem + PhysicsSystem over a
// scenario for shipDynGoldenTicks fixed 50 ms ticks, in the same order the game
// loop runs them (dynamics writes velocity/rotation, physics integrates
// position downstream).
func runShipDynScenario(t *testing.T, sc shipDynScenario) shipDynGoldenCase {
	t.Helper()
	gw, _ := newTestGameWorld()
	if sc.dragCoeff != 0 {
		gw.Config.ShipDragCoeff = sc.dragCoeff
	}

	w := gw.stage.ECSWorld()
	mapper := ecs.NewMap6[
		mmokit.Position,
		mmokit.Velocity,
		mmokit.Rotation,
		gamecomp.ShipControl,
		mmokit.MoveTarget,
		mmokit.NetworkID,
	](w)

	maxSpeed := gw.Config.MaxSpeed
	if sc.maxSpeed != 0 {
		maxSpeed = sc.maxSpeed
	}

	const netID uint32 = 7100
	handle := mapper.NewEntity(
		&mmokit.Position{X: sc.posX, Y: sc.posY},
		&mmokit.Velocity{X: sc.velX, Y: sc.velY},
		&mmokit.Rotation{Angle: sc.angle},
		&gamecomp.ShipControl{
			Thrust:     gw.Config.ShipThrust,
			TurnRate:   gw.Config.ShipTurnRate,
			TurnAccel:  gw.Config.ShipTurnAccel,
			MaxSpeed:   maxSpeed,
			AngularVel: sc.angularVel,
		},
		&mmokit.MoveTarget{
			Active: !sc.noTarget,
			LocalX: sc.targetX,
			LocalY: sc.targetY,
		},
		&mmokit.NetworkID{ID: netID},
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	entity := mmokit.EntityFromECS(gw.stage, handle)

	speedMul := float32(1)
	if sc.speedMul != 0 {
		speedMul = sc.speedMul
		se := gamecomp.StatusEffects{}
		se.Add(gamecomp.StatusEffect{
			Type:     gamecomp.StatusAfterburner,
			Value:    sc.speedMul,
			Duration: 1e6, // long enough that TickDown never expires it here
		})
		effectsMap := ecs.NewMap1[gamecomp.StatusEffects](w)
		effectsMap.Add(handle, &se)
	}

	dyn := &ShipDynamicsSystem{}
	mmokit.WireSystem(dyn, w, gw.eng, gw.stage)
	dyn.gw = gw

	phys := &system.PhysicsSystem{}
	mmokit.WireSystem(phys, w, gw.eng, gw.stage)

	sample := func() shipDynGoldenTick {
		pos := mmokit.Get[mmokit.Position](entity)
		vel := mmokit.Get[mmokit.Velocity](entity)
		rot := mmokit.Get[mmokit.Rotation](entity)
		ship := mmokit.Get[gamecomp.ShipControl](entity)
		return shipDynGoldenTick{
			X:          float64(pos.X),
			Y:          float64(pos.Y),
			VelocityX:  float64(vel.X),
			VelocityY:  float64(vel.Y),
			Angle:      float64(rot.Angle),
			AngularVel: float64(ship.AngularVel),
		}
	}

	seed := sample()
	const dt = float32(0.05) // 20 Hz
	ticks := make([]shipDynGoldenTick, 0, shipDynGoldenTicks)
	for range shipDynGoldenTicks {
		gw.stage.TickOne(dyn, dt)
		gw.stage.TickOne(phys, dt)
		ticks = append(ticks, sample())
	}

	return shipDynGoldenCase{
		Name: sc.name,
		Seed: seed,
		MoveTarget: shipDynGoldenMoveTarget{
			Active: !sc.noTarget,
			X:      float64(sc.targetX),
			Y:      float64(sc.targetY),
		},
		Params: shipDynGoldenParams{
			Thrust:               float64(gw.Config.ShipThrust),
			MaxSpeed:             float64(maxSpeed),
			TurnRate:             float64(gw.Config.ShipTurnRate),
			TurnAccel:            float64(gw.Config.ShipTurnAccel),
			DragCoefficient:      float64(gw.Config.ShipDragCoeff),
			ArrivalDistance:      float64(gw.Config.MoveArrivalDist),
			DecelerationDistance: float64(gw.Config.MoveDecelDist),
			SpeedMultiplier:      float64(speedMul),
		},
		Ticks: ticks,
	}
}

func buildShipDynGolden(t *testing.T) shipDynGoldenManifest {
	t.Helper()
	m := shipDynGoldenManifest{
		Note: "GENERATED by `just shipdyn-golden` from the real ShipDynamicsSystem + PhysicsSystem. " +
			"Do not hand-edit. Consumed by web-pixi/src/__tests__/prediction-golden.test.ts, which is " +
			"what fails when internal/game/system_ship_dynamics.go changes without web-pixi/src/prediction.ts.",
		TickIntervalMs: 50,
	}
	for _, sc := range shipDynScenarios() {
		m.Cases = append(m.Cases, runShipDynScenario(t, sc))
	}
	return m
}

// TestShipDynamicsGolden regenerates the Go→TS parity fixture under
// -update-shipdyn-golden, and otherwise asserts the generator is deterministic
// and still agrees with the committed fixture.
//
// The fixture is the only thing that ties web-pixi/src/prediction.ts stepShip
// to the server it mirrors. Without it, routine gameplay tuning of ship
// dynamics silently desynchronises the predictor and shows up in production as
// constant rubber-banding rather than as a failing test.
func TestShipDynamicsGolden(t *testing.T) {
	got := buildShipDynGolden(t)

	if *updateShipDynGolden {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal golden: %v", err)
		}
		data = append(data, '\n')
		if err := os.MkdirAll(filepath.Dir(shipDynGoldenPath), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(shipDynGoldenPath, data, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s (%d bytes, %d cases)", shipDynGoldenPath, len(data), len(got.Cases))
		return
	}

	raw, err := os.ReadFile(shipDynGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run `just shipdyn-golden` to create it): %v", err)
	}
	var want shipDynGoldenManifest
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}

	if want.TickIntervalMs != got.TickIntervalMs {
		t.Fatalf("tickIntervalMs = %v, want %v", got.TickIntervalMs, want.TickIntervalMs)
	}
	if len(want.Cases) != len(got.Cases) {
		t.Fatalf("case count = %d, want %d — regenerate with `just shipdyn-golden`",
			len(got.Cases), len(want.Cases))
	}
	for i := range want.Cases {
		wc, gc := want.Cases[i], got.Cases[i]
		if wc.Name != gc.Name {
			t.Fatalf("case %d name = %q, want %q", i, gc.Name, wc.Name)
		}
		if len(wc.Ticks) != len(gc.Ticks) {
			t.Fatalf("%s: tick count = %d, want %d", wc.Name, len(gc.Ticks), len(wc.Ticks))
		}
		for j := range wc.Ticks {
			assertShipDynTick(t, wc.Name, j, gc.Ticks[j], wc.Ticks[j])
		}
	}
}

func assertShipDynTick(t *testing.T, caseName string, idx int, got, want shipDynGoldenTick) {
	t.Helper()
	// Exact: both sides are the same float32 arithmetic round-tripped through
	// JSON as float64, so any difference means the systems changed.
	for _, f := range []struct {
		name       string
		got, want_ float64
	}{
		{"x", got.X, want.X},
		{"y", got.Y, want.Y},
		{"velocityX", got.VelocityX, want.VelocityX},
		{"velocityY", got.VelocityY, want.VelocityY},
		{"angle", got.Angle, want.Angle},
		{"angularVelocity", got.AngularVel, want.AngularVel},
	} {
		if math.Abs(f.got-f.want_) > 1e-9 {
			t.Errorf("%s tick %d: %s = %v, want %v — ship dynamics changed; regenerate with `just shipdyn-golden` AND update web-pixi/src/prediction.ts to match",
				caseName, idx, f.name, f.got, f.want_)
		}
	}
}
