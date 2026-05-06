package mmokit_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestRawWorld_ReturnsECSWorld(t *testing.T) {
	stage, _ := newTestStage(t)
	w := mmokit.RawWorld(stage)
	if w == nil {
		t.Fatal("RawWorld returned nil")
	}
	if w != stage.ECSWorld() {
		t.Fatal("RawWorld should return stage.ECSWorld()")
	}
}
