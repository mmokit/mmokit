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

func TestGameLoop_SystemByName(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, nil, Hooks{})
	n := 0
	want := &liveCountSys{n: &n}
	gl.AddSystemLive("counter", want)

	got, ok := gl.SystemByName("counter")
	if !ok || got != want {
		t.Fatalf("SystemByName(counter) = %v,%v want %v,true", got, ok, want)
	}
	if _, ok := gl.SystemByName("missing"); ok {
		t.Fatal("SystemByName(missing) returned ok=true")
	}
}

func TestGameLoop_ReplaceSystemLive_PreservesOrder(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, nil, Hooks{})
	a, b, c := &liveCountSys{n: new(int)}, &liveCountSys{n: new(int)}, &liveCountSys{n: new(int)}
	gl.AddSystemLive("a", a)
	gl.AddSystemLive("b", b)
	gl.AddSystemLive("c", c)

	repl := &liveCountSys{n: new(int)}
	if !gl.ReplaceSystemLive("b", repl) {
		t.Fatal("ReplaceSystemLive(b) returned false")
	}
	if gl.systems[0] != a || gl.systems[1] != repl || gl.systems[2] != c {
		t.Fatalf("order not preserved: %v", gl.systems)
	}
	if gl.systemNames[1] != "b" {
		t.Fatalf("name at slot 1 = %q want b", gl.systemNames[1])
	}
	if gl.ReplaceSystemLive("missing", repl) {
		t.Fatal("ReplaceSystemLive(missing) returned true")
	}
}
