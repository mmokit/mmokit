package mmokit

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

type wireTestSystem struct {
	engine.SystemBase
	q query.Query[struct {
		Pos *component.Position
	}]
}

func (s *wireTestSystem) Update(dt float32) {}

func TestWireSystem_FullLifecycle(t *testing.T) {
	w := ecs.NewWorld()
	s := &wireTestSystem{}
	WireSystem(s, w, nil, nil) // no engine, no stage

	count := 0
	for range s.q.Iter {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}
