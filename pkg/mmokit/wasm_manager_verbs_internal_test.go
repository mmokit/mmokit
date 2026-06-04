package mmokit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/universe"

	gamecomp "github.com/zenion/mmoserver/internal/component"
)

// buildShieldWasmForTest compiles the shieldregen example module to a wasip1
// reactor .wasm (c-shared so //go:wasmexport is reachable) and returns its path.
func buildShieldWasmForTest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "shieldregen.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = "../../examples/4node-basic/wasmmods/shieldregen"
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build shield wasm: %v\n%s", err, b)
	}
	return out
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

	return &universe.Cell{
		MeshID: universe.MeshCellID(id),
		Engine: eng,
		Stage:  stage,
		Loop:   gl,
	}
}

// TestWasmVerbs_LoadListUnload exercises the runtime verbs end-to-end on a
// single bare cell: registry registration → load → list → unload, asserting
// both the table results and the live loop reflect each transition.
func TestWasmVerbs_LoadListUnload(t *testing.T) {
	shieldPath := buildShieldWasmForTest(t)

	c0 := newVerbCell(t, "0_0")
	// universe.New gives a fully-wired cmdsys.Registry (no Build required);
	// we then inject our hand-built cell so the verbs have something to act on.
	proc := universe.New(universe.Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1})
	proc.Cells = map[universe.MeshCellID]*universe.Cell{"0_0": c0}

	// Register the wasm system in the per-process registry. The bare proc is
	// never Built, so AddWasmSystem records the entry without auto-loading it
	// into any cell — load is driven entirely by the verb below.
	AddWasmSystem[gamecomp.Shield](proc, shieldPath)
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
