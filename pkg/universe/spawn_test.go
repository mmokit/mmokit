package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

func newTestStage(t *testing.T) *Stage {
	t.Helper()
	eng := engine.New(engine.DefaultConfig(), net.NewConnManager(), logger.New())
	return NewStage(eng, CellID{X: 0, Y: 0}, 100, nil)
}

// TestSpawn_AttachesEveryComponent verifies that Stage.Spawn attaches each
// variadic component value to the entity and returns a usable Entity wrapper.
func TestSpawn_AttachesEveryComponent(t *testing.T) {
	stage := newTestStage(t)

	type marker struct{ Tag uint8 }
	_ = ecs.NewMap1[marker](stage.ECSWorld()) // pre-register

	e := stage.Spawn(
		component.Position{X: 100, Y: 200},
		component.Collider{Radius: 5, Layer: 1},
		marker{Tag: 42},
	)

	if e.NetID() == 0 {
		t.Fatal("expected non-zero netID on spawned entity")
	}
	if !e.Alive() {
		t.Fatal("expected spawned entity to be Alive")
	}
	posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
	if !posMap.HasAll(e.Handle()) {
		t.Fatal("expected Position attached")
	}
	if p := posMap.Get(e.Handle()); p.X != 100 || p.Y != 200 {
		t.Errorf("expected Position{100,200}, got {%v,%v}", p.X, p.Y)
	}
	colMap := ecs.NewMap1[component.Collider](stage.ECSWorld())
	if !colMap.HasAll(e.Handle()) {
		t.Fatal("expected Collider attached")
	}
	if c := colMap.Get(e.Handle()); c.Radius != 5 || c.Layer != 1 {
		t.Errorf("Collider not copied verbatim: %+v", c)
	}
	mkMap := ecs.NewMap1[marker](stage.ECSWorld())
	if !mkMap.HasAll(e.Handle()) || mkMap.Get(e.Handle()).Tag != 42 {
		t.Errorf("marker component missing or wrong: %+v", mkMap.Get(e.Handle()))
	}
}
