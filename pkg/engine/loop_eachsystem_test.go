package engine

import "testing"

func TestEachSystem(t *testing.T) {
	// NewGameLoop eagerly dereferences the *Engine (eng.Perf = ...), so it
	// panics on nil. This is a package-internal test, so construct the
	// GameLoop directly via a struct literal — EachSystem only reads the
	// systems/systemNames slices and never runs the loop.
	gl := &GameLoop{
		systems:     []System{&SystemBase{}, &SystemBase{}},
		systemNames: []string{"A", "B"},
	}
	var got []string
	gl.EachSystem(func(name string, _ System) { got = append(got, name) })
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("EachSystem order/names wrong: %v", got)
	}
}
