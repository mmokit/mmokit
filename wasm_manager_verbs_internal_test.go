package mmokit

import (
	"context"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/spatial"
	"github.com/mmokit/mmokit/pkg/universe"

	"github.com/mmokit/mmokit/internal/wasmfixtures"
	"github.com/mmokit/mmokit/internal/wasmfixtures/podcomp"
)

// buildShieldWasmForTest compiles the shieldregen fixture module to a wasip1
// reactor .wasm and returns its path.
func buildShieldWasmForTest(t *testing.T) string {
	t.Helper()
	return wasmfixtures.Build(t, "internal/wasmfixtures/shieldregen")
}

// newVerbCell builds one cell with a real Stage + Engine and a running
// GameLoop. The verbs schedule work via CmdOnLoop → engine.RunOnLoop, which
// blocks until the loop goroutine drains the queue — so the loop must actually
// tick. The loop is stopped via t.Cleanup when the test ends.
func newVerbCell(t *testing.T, id string) *universe.Cell {
	t.Helper()
	log := logger.New()
	eng := engine.New(engine.Config{TickRate: 20}, net.NewConnManager(), log)
	stage := universe.NewStage(eng, universe.CellID{}, 1000, nil)
	stage.SetSpatialGrid(spatial.NewHashGrid(coords.CellSize / 10))
	gl := engine.NewGameLoop(eng, nil, nil, engine.Hooks{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		gl.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	c := universe.NewCell(universe.MeshCellID(id), universe.CellID{})
	c.Engine = eng
	c.Stage = stage
	c.Loop = gl
	return c
}

// TestWasmVerbs_LoadListUnload exercises the runtime verbs end-to-end on a
// single bare cell: registry registration → load → list → unload, asserting
// both the table results and the live loop reflect each transition.
func TestWasmVerbs_LoadListUnload(t *testing.T) {
	shieldPath := buildShieldWasmForTest(t)

	c0 := newVerbCell(t, "cell_0_0")
	// universe.New gives a fully-wired cmdsys.Registry (no Build required);
	// we then inject our hand-built cell so the verbs have something to act on.
	// Use the canonical mesh key ("cell_0_0") so --cell filters resolve as in prod.
	proc := universe.New(universe.Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1})
	proc.Cells = map[universe.MeshCellID]*universe.Cell{"cell_0_0": c0}

	// Register the wasm system in the per-process registry. The bare proc is
	// never Built, so AddWasmSystem records the entry without auto-loading it
	// into any cell — load is driven entirely by the verb below.
	AddWasmSystem[podcomp.Shield](proc, shieldPath)
	if err := registerWasmVerbs(proc); err != nil {
		t.Fatalf("registerWasmVerbs: %v", err)
	}
	reg := proc.CmdRegistry()

	call := func(verb string, args any) wasmOpResult {
		cmd, ok := reg.Lookup(verb)
		if !ok {
			t.Fatalf("verb %q not registered", verb)
		}
		if cmd.Route != cmdsys.RouteAllHosts {
			t.Fatalf("%s route = %v want RouteAllHosts", verb, cmd.Route)
		}
		res, err := cmd.Handler(context.Background(), &cmdsys.Env{}, args)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		return res.(wasmOpResult)
	}

	// Before load: list reports the system as unloaded on the cell.
	if got := call("wasm.list", wasmListArgs{}); len(got.Rows) != 1 || got.Rows[0].Status != "unloaded" {
		t.Fatalf("pre-load list rows=%+v", got.Rows)
	}

	// load → present on the loop.
	if got := call("wasm.load", wasmNameArgs{Name: "shieldregen"}); len(got.Rows) != 1 || got.Rows[0].Status != "loaded" {
		t.Fatalf("load rows=%+v", got.Rows)
	}
	if _, ok := c0.Loop.SystemByName("shieldregen"); !ok {
		t.Fatal("system not on loop after load")
	}

	// idempotent load → already-loaded, no duplicate on the loop.
	if got := call("wasm.load", wasmNameArgs{Name: "shieldregen"}); got.Rows[0].Status != "already-loaded" {
		t.Fatalf("second load rows=%+v", got.Rows)
	}

	// list → loaded.
	if got := call("wasm.list", wasmListArgs{}); len(got.Rows) != 1 || got.Rows[0].Status != "loaded" {
		t.Fatalf("list rows=%+v", got.Rows)
	}

	// unknown-name load → clear error.
	if cmd, _ := reg.Lookup("wasm.load"); true {
		if _, err := cmd.Handler(context.Background(), &cmdsys.Env{}, wasmNameArgs{Name: "nope"}); err == nil {
			t.Fatal("expected error loading unknown wasm system")
		}
	}

	// unload → gone from the loop.
	if got := call("wasm.unload", wasmNameArgs{Name: "shieldregen"}); got.Rows[0].Status != "unloaded" {
		t.Fatalf("unload rows=%+v", got.Rows)
	}
	if _, ok := c0.Loop.SystemByName("shieldregen"); ok {
		t.Fatal("system still on loop after unload")
	}

	// unload again → not-loaded.
	if got := call("wasm.unload", wasmNameArgs{Name: "shieldregen"}); got.Rows[0].Status != "not-loaded" {
		t.Fatalf("second unload rows=%+v", got.Rows)
	}
}

// TestWasmVerbs_Swap_PreservesState covers the swap handler's snapshot → restore
// → ReplaceSystemLive → Close orchestration: after a swap, the freshly-built
// replacement instance must continue from the outgoing instance's internal tick
// counter (a broken restore would reset it to ~0). The loop runs live, so we
// poll wasm.list (which reads the counter safely on the loop goroutine) until it
// passes a threshold, then assert continuity across the swap.
func TestWasmVerbs_Swap_PreservesState(t *testing.T) {
	shieldPath := buildShieldWasmForTest(t)

	c0 := newVerbCell(t, "cell_0_0")
	proc := universe.New(universe.Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1})
	proc.Cells = map[universe.MeshCellID]*universe.Cell{"cell_0_0": c0}

	AddWasmSystem[podcomp.Shield](proc, shieldPath)
	if err := registerWasmVerbs(proc); err != nil {
		t.Fatalf("registerWasmVerbs: %v", err)
	}
	reg := proc.CmdRegistry()
	call := func(verb string, args any) wasmOpResult {
		cmd, ok := reg.Lookup(verb)
		if !ok {
			t.Fatalf("verb %q not registered", verb)
		}
		res, err := cmd.Handler(context.Background(), &cmdsys.Env{}, args)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		return res.(wasmOpResult)
	}

	// Spawn a Shield entity (on the loop goroutine) so the system has work each
	// tick. The adapter skips the module call when zero entities match T, which
	// would otherwise freeze the module's internal tick counter at 0.
	if err := c0.Engine.RunOnLoop(context.Background(), func() error {
		c0.Stage.Spawn(Position{}, podcomp.Shield{Current: 0, Max: 1e9, RegenRate: 1})
		return nil
	}); err != nil {
		t.Fatalf("spawn shield: %v", err)
	}

	if got := call("wasm.load", wasmNameArgs{Name: "shieldregen"}); got.Rows[0].Status != "loaded" {
		t.Fatalf("load rows=%+v", got.Rows)
	}

	// Wait until the live module's tick counter advances comfortably past a
	// threshold so a reset-to-zero after swap is unambiguous.
	const threshold = 10
	deadline := time.Now().Add(5 * time.Second)
	var ticks uint64
	for {
		rows := call("wasm.list", wasmListArgs{}).Rows
		if len(rows) == 1 {
			ticks = rows[0].Ticks
		}
		if ticks >= threshold {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("module tick counter only reached %d before deadline (loop not ticking?)", ticks)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Swap. The returned Ticks is the snapshot captured at swap time (on-loop),
	// so it must be >= threshold.
	swapRow := call("wasm.swap", wasmNameArgs{Name: "shieldregen"}).Rows[0]
	if swapRow.Status != "swapped" {
		t.Fatalf("swap status=%q want swapped", swapRow.Status)
	}
	if swapRow.Ticks < threshold {
		t.Fatalf("swap snapshot ticks=%d < threshold %d", swapRow.Ticks, threshold)
	}

	// The replacement must continue from the preserved counter, never reset.
	after := call("wasm.list", wasmListArgs{}).Rows[0].Ticks
	if after < swapRow.Ticks {
		t.Fatalf("post-swap ticks=%d < swap ticks=%d — state NOT preserved across swap", after, swapRow.Ticks)
	}
	if _, ok := c0.Loop.SystemByName("shieldregen"); !ok {
		t.Fatal("system missing from loop after swap")
	}
}
