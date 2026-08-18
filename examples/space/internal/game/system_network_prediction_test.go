package game

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
)

type movementSeedClock struct{ stamp uint64 }

func (c movementSeedClock) Now() uint64                           { return c.stamp }
func (c movementSeedClock) TickTime(tickIntervalMs uint64) uint64 { return c.stamp }

func TestNetworkSystemBuildMovementStateIsWorldAbsoluteAndComplete(t *testing.T) {
	gw, _ := newTestGameWorld()
	mapper := ecs.NewMap5[
		mmokit.Position,
		mmokit.NetworkID,
		mmokit.CellCoord,
		mmokit.Velocity,
		mmokit.Rotation,
	](gw.stage.ECSWorld())
	handle := mapper.NewEntity(
		&mmokit.Position{X: 25, Y: 40},
		&mmokit.NetworkID{ID: 91, Epoch: 9},
		&mmokit.CellCoord{CellX: 2, CellY: 3},
		&mmokit.Velocity{X: 12, Y: -4},
		&mmokit.Rotation{Angle: 0.75},
	)
	entity := mmokit.EntityFromECS(gw.stage, handle)
	mmokit.Set(entity, gamecomp.ShipControl{
		Thrust: 20, TurnRate: 3, TurnAccel: 5, MaxSpeed: 80, AngularVel: -0.4,
	})
	mmokit.Set(entity, mmokit.MoveTarget{
		LocalX: 10, LocalY: 20, CellX: 4, CellY: 5, Active: true, Sequence: 44,
	})
	effects := gamecomp.StatusEffects{}
	effects.Add(gamecomp.StatusEffect{Type: gamecomp.StatusAfterburner, Duration: 2, Value: 2.5})
	mmokit.Set(entity, effects)

	gw.eng.Tick = 123
	ns := &NetworkSystem{gw: gw, clock: movementSeedClock{stamp: 9_876_500}}
	got := ns.buildMovementState(entity, 7, 5)

	if !got.Valid || got.PredictionTicks != 5 || got.Tick != 123 || got.ProducedAtMs != 9_876_500 {
		t.Fatalf("seed identity/timeline = %+v", got)
	}
	if got.EntityNetID != 91 || got.StreamEpoch != 7 || got.ProcessedSequence != 44 {
		t.Fatalf("seed authority/sequence = %+v", got)
	}
	if got.WorldX != 2*gw.stage.CellSize()+25 || got.WorldY != 3*gw.stage.CellSize()+40 {
		t.Fatalf("world position = (%g,%g)", got.WorldX, got.WorldY)
	}
	if got.TargetX != 4*gw.stage.CellSize()+10 || got.TargetY != 5*gw.stage.CellSize()+20 || !got.TargetActive {
		t.Fatalf("world target = (%g,%g active=%v)", got.TargetX, got.TargetY, got.TargetActive)
	}
	if got.VelocityX != 12 || got.VelocityY != -4 || got.Angle != 0.75 || got.AngularVelocity != -0.4 {
		t.Fatalf("kinematic state = %+v", got)
	}
	if got.Thrust != 20 || got.TurnRate != 3 || got.TurnAccel != 5 || got.MaxSpeed != 80 || got.SpeedMultiplier != 2.5 {
		t.Fatalf("movement parameters = %+v", got)
	}
	if got.TickIntervalMs != 50 || got.DragCoefficient != gw.Config.ShipDragCoeff ||
		got.ArrivalDistance != gw.Config.MoveArrivalDist || got.DecelerationDist != gw.Config.MoveDecelDist {
		t.Fatalf("simulation constants = %+v", got)
	}
}

func TestNetworkSystemBuildMovementStateRequiresAuthoritativeComponents(t *testing.T) {
	gw, _ := newTestGameWorld()
	handle := ecs.NewMap2[mmokit.Position, mmokit.NetworkID](gw.stage.ECSWorld()).NewEntity(
		&mmokit.Position{},
		&mmokit.NetworkID{ID: 1001},
	)
	ns := &NetworkSystem{gw: gw}
	if got := ns.buildMovementState(mmokit.EntityFromECS(gw.stage, handle), 0, 5); got.Valid {
		t.Fatalf("incomplete entity produced a valid movement seed: %+v", got)
	}
}

func TestMovementPredictionTicksRespectModeAndFiniteStatusExpiry(t *testing.T) {
	gw, _ := newTestGameWorld()
	handle := ecs.NewMap2[mmokit.Position, mmokit.NetworkID](gw.stage.ECSWorld()).NewEntity(
		&mmokit.Position{},
		&mmokit.NetworkID{ID: 1002},
	)
	entity := mmokit.EntityFromECS(gw.stage, handle)

	if got := movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs); got != 5 {
		t.Fatalf("ordinary active movement ticks = %d, want 5", got)
	}
	if movementPredictionTicks(StateDead, entity, 50, defaultPredictionHorizonMs) != 0 ||
		movementPredictionTicks(StateDocking, entity, 50, defaultPredictionHorizonMs) != 0 ||
		movementPredictionTicks(StateDocked, entity, 50, defaultPredictionHorizonMs) != 0 {
		t.Fatal("non-active lifecycle states must remain authoritative")
	}

	mmokit.Set(entity, gamecomp.Supercruise{Phase: gamecomp.SupercruiseChanneling})
	if movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs) != 0 {
		t.Fatal("supercruise channeling must remain authoritative")
	}
	mmokit.Get[gamecomp.Supercruise](entity).Phase = gamecomp.SupercruiseActive
	if movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs) != 5 {
		t.Fatal("active supercruise can use the seeded speed multiplier")
	}
	if got := movementPredictionTicks(mmokit.StateActive, entity, 16, defaultPredictionHorizonMs); got != 16 {
		t.Fatalf("non-20Hz horizon ticks = %d, want 16", got)
	}

	effects := gamecomp.StatusEffects{}
	effects.Add(gamecomp.StatusEffect{Type: gamecomp.StatusAfterburner, Duration: 0, Value: 2})
	mmokit.Set(entity, effects)
	if got := movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs); got != 1 {
		t.Fatalf("zero-duration surviving status prediction ticks = %d, want 1", got)
	}
	mmokit.Get[gamecomp.StatusEffects](entity).Effects[0].Duration = 0.05
	if got := movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs); got != 1 {
		t.Fatalf("one-tick status prediction ticks = %d, want 1", got)
	}
	mmokit.Get[gamecomp.StatusEffects](entity).Effects[0].Duration = 0.051
	if got := movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs); got != 2 {
		t.Fatalf("two-tick status prediction ticks = %d, want 2", got)
	}
	if got := movementPredictionTicks(mmokit.StateActive, entity, 16, defaultPredictionHorizonMs); got != 4 {
		t.Fatalf("non-20Hz status prediction ticks = %d, want 4", got)
	}

	effects = *mmokit.Get[gamecomp.StatusEffects](entity)
	effects.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSlow, Duration: 0.01, Value: 0.5})
	mmokit.Set(entity, effects)
	if got := movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs); got != 1 {
		t.Fatalf("stacked modifier minimum prediction ticks = %d, want 1", got)
	}

	effects = gamecomp.StatusEffects{}
	effects.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 0.051, Value: 2.5})
	mmokit.Set(entity, effects)
	if got := movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs); got != 2 {
		t.Fatalf("finite supercruise prediction ticks = %d, want 2", got)
	}
	mmokit.Get[gamecomp.StatusEffects](entity).Effects[0].Value = float32(math.NaN())
	if got := movementPredictionTicks(mmokit.StateActive, entity, 50, defaultPredictionHorizonMs); got != 0 {
		t.Fatalf("non-finite multiplier prediction ticks = %d, want 0", got)
	}
}

// defaultPredictionHorizonMs mirrors GameConfig.MovementPredictionHorizonMs's
// default, so the cases above keep asserting the shipped behaviour.
const defaultPredictionHorizonMs uint64 = 250

// TestMovementPredictionTicks_ConfigurableHorizon covers
// GameConfig.MovementPredictionHorizonMs, including the operator kill switch.
func TestMovementPredictionTicks_ConfigurableHorizon(t *testing.T) {
	gw, _ := newTestGameWorld()
	handle := ecs.NewMap2[mmokit.Position, mmokit.NetworkID](gw.stage.ECSWorld()).NewEntity(
		&mmokit.Position{},
		&mmokit.NetworkID{ID: 1003},
	)
	entity := mmokit.EntityFromECS(gw.stage, handle)

	cases := []struct {
		name           string
		horizonMs      uint64
		tickIntervalMs uint64
		want           uint8
	}{
		{"default 250ms at 20Hz", 250, 50, 5},
		{"lowered to 100ms at 20Hz", 100, 50, 2},
		{"exactly one tick", 50, 50, 1},
		{"below one tick still authorizes one", 20, 50, 1},
		{"raised to 500ms at 20Hz", 500, 50, 10},
		// Zero is the kill switch. It must NOT underflow the
		// 1 + (h-1)/tick expression, which on uint64 would otherwise
		// authorize an enormous replay window instead of none.
		{"zero disables prediction", 0, 50, 0},
		{"zero disables prediction at any tick rate", 0, 16, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := movementPredictionTicks(mmokit.StateActive, entity, tc.tickIntervalMs, tc.horizonMs)
			if got != tc.want {
				t.Fatalf("movementPredictionTicks(horizon=%d, tick=%d) = %d, want %d",
					tc.horizonMs, tc.tickIntervalMs, got, tc.want)
			}
		})
	}
}

// TestDefaultGameConfig_PredictionHorizon pins the shipped default so a
// silent change to it shows up as a failing test rather than as a behaviour
// change nobody noticed.
func TestDefaultGameConfig_PredictionHorizon(t *testing.T) {
	if got := DefaultGameConfig().MovementPredictionHorizonMs; got != 250 {
		t.Fatalf("default MovementPredictionHorizonMs = %d, want 250", got)
	}
}
