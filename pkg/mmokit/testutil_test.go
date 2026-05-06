package mmokit_test

import (
	"reflect"
	"testing"
	"time"

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

// loopbackBridge is a minimal in-process Bridge that records cross-cell
// actions and replays them into a destination stage's HandleEngineAction
// when the test calls drain(). All other Bridge methods are no-ops — only
// SendAction is exercised by typed-message routing.
type loopbackBridge struct {
	routes  map[pkguniverse.MeshCellID]*pkguniverse.Stage
	pending []routedAction
}

type routedAction struct {
	dest   *pkguniverse.Stage
	action *pkguniverse.CrossCellAction
}

func (b *loopbackBridge) PreTick()                                                    {}
func (b *loopbackBridge) PostSystems()                                                {}
func (b *loopbackBridge) CellOwner(pkguniverse.CellID) string                         { return "" }
func (b *loopbackBridge) CellOwnerAtPos(float32, float32) string                      { return "" }
func (b *loopbackBridge) OnPlayerTransfer(uint32, pkguniverse.MeshCellID)             {}
func (b *loopbackBridge) RelayChatToOtherCells(string, string)                        {}
func (b *loopbackBridge) RequestRespawn(uint32, string)                               {}
func (b *loopbackBridge) SendBorderFrame(pkguniverse.MeshCellID, pkguniverse.MeshCellID, []byte) {
}
func (b *loopbackBridge) SendHandoff(pkguniverse.MeshCellID, *pkguniverse.HandoffPayload) bool {
	return true
}
func (b *loopbackBridge) SendForwardInput(pkguniverse.MeshCellID, *pkguniverse.ForwardInputPayload) {
}

func (b *loopbackBridge) SendAction(dest pkguniverse.MeshCellID, action *pkguniverse.CrossCellAction) {
	if s, ok := b.routes[dest]; ok {
		b.pending = append(b.pending, routedAction{dest: s, action: action})
	}
}

func (b *loopbackBridge) drain(_ time.Duration) {
	pending := b.pending
	b.pending = nil
	for _, r := range pending {
		r.dest.HandleEngineAction(r.action)
	}
}

// newTwoCellLoopback creates two stages connected by a shared loopback
// bridge so cross-cell SendAction calls land on the destination's
// HandleEngineAction. Returns (cellA, cellB, drain).
func newTwoCellLoopback(t *testing.T) (*pkguniverse.Stage, *pkguniverse.Stage, func(time.Duration)) {
	t.Helper()
	log := logger.New()

	cmA := net.NewConnManager()
	engA := engine.New(engine.Config{TickRate: 20}, cmA, log)
	stageA := pkguniverse.NewStage(engA, pkguniverse.CellID{X: 0, Y: 0}, 1000, nil)
	stageA.SetSpatialGrid(spatial.NewHashGrid(coords.CellSize / 10))

	cmB := net.NewConnManager()
	engB := engine.New(engine.Config{TickRate: 20}, cmB, log)
	stageB := pkguniverse.NewStage(engB, pkguniverse.CellID{X: 1, Y: 0}, 1000, nil)
	stageB.SetSpatialGrid(spatial.NewHashGrid(coords.CellSize / 10))

	bridge := &loopbackBridge{
		routes: map[pkguniverse.MeshCellID]*pkguniverse.Stage{
			stageA.CellID(): stageA,
			stageB.CellID(): stageB,
		},
	}
	stageA.SetBridge(bridge)
	stageB.SetBridge(bridge)

	return stageA, stageB, bridge.drain
}

// runTicks drives the registered tick callbacks N times manually. Used by
// tests that want to exercise OnWorldTick / OnTick / OnTickEach without
// spinning up the full engine loop.
func runTicks(t *testing.T, stage *pkguniverse.Stage, n int) {
	t.Helper()
	const dt = float32(1.0 / 20.0)
	for range n {
		for _, fn := range stage.TickCallbacks() {
			fn(dt)
		}
	}
}

// pushBorderReplicaTo creates a Replica entity on dest pointing back at
// (source.CellID, netID). Used to simulate a border replica without
// running the full ApplyBorderFrame codec; the replica is registered in
// dest's NetID index as PresenceReplica so LookupNetID resolves it.
func pushBorderReplicaTo(t *testing.T, source, dest *pkguniverse.Stage, netID uint32) {
	t.Helper()
	w := dest.ECSWorld()
	mapper := ecs.NewMap3[component.Position, component.NetworkID, component.Replica](w)
	h := mapper.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.NetworkID{ID: netID},
		&component.Replica{
			SourceCellID: string(source.CellID()),
			SourceNetID:  netID,
		},
	)
	dest.RegisterReplicaNetID(netID, h)
}
