package engine

import "testing"

type liveCountSys struct {
	SystemBase
	n *int
}

func (s *liveCountSys) Update(dt float32) { *s.n++ }

func TestGameLoop_AddRemoveSystemLive(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, nil, Hooks{})

	n := 0
	gl.AddSystemLive("counter", &liveCountSys{n: &n})
	if len(gl.systems) != 1 || len(gl.sysTimings) != 1 {
		t.Fatalf("after add: systems=%d timings=%d", len(gl.systems), len(gl.sysTimings))
	}

	if !gl.RemoveSystemLive("counter") {
		t.Fatal("RemoveSystemLive returned false")
	}
	if len(gl.systems) != 0 || len(gl.sysTimings) != 0 {
		t.Fatalf("after remove: systems=%d timings=%d", len(gl.systems), len(gl.sysTimings))
	}

	if gl.RemoveSystemLive("counter") {
		t.Fatal("RemoveSystemLive of absent system returned true")
	}
}
