package mmokit_test

import (
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/spatial"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// newTestStage spins up a single-cell Stage with the minimum wiring needed
// for mmokit tests: ECS, spatial grid, NetID index. No game world, no bridge.
func newTestStage(t *testing.T) (*pkguniverse.Stage, *engine.Engine) {
	t.Helper()
	log := logger.New()
	cm := net.NewConnManager()
	eng := engine.New(engine.Config{TickRate: 20}, cm, log)
	stage := pkguniverse.NewStage(eng, pkguniverse.CellID{}, 1000, nil)
	stage.SetSpatialGrid(spatial.NewHashGrid(coords.CellSize / 10))
	return stage, eng
}

// spawnTestEntity creates a bare entity on the stage with NetworkID=netID
// and Position=(0,0). Returns the ECS handle. Registers the netID in the
// stage's index so LookupNetID succeeds.
func spawnTestEntity(t *testing.T, stage *pkguniverse.Stage, netID uint32) ecs.Entity {
	t.Helper()
	w := stage.ECSWorld()
	mapper := ecs.NewMap2[component.Position, component.NetworkID](w)
	h := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.NetworkID{ID: netID},
	)
	stage.RegisterLiveNetID(netID, h)
	return h
}

// testKindID is the kind ID used by Spawn-related tests.
const testKindID mmokit.KindID = 99

// registerTestKind registers a single-component entity kind (testKindHealth)
// on the stage so Spawn(stage, testKindID, ...) can attach it.
func registerTestKind(t *testing.T, stage *pkguniverse.Stage) {
	t.Helper()
	def := pkguniverse.EntityKindDef{Kind: uint8(testKindID), Name: "TestKind"}
	w := stage.ECSWorld()
	pkguniverse.KindComponentByID(
		&def, w,
		ecs.ComponentID[testKindHealth](w),
		reflect.TypeFor[testKindHealth](),
		false,
	)
	stage.RegisterEntityKind(def)
}
