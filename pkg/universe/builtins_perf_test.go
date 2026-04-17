package universe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

// newTestCoordWithCell creates a minimal *Coordinator containing one cell
// owned by one host. Used by perf worker handler tests.
func newTestCoordWithCell(t *testing.T, cellID, hostID string) *Coordinator {
	t.Helper()
	parsed, err := ParseCellID(cellID)
	if err != nil {
		t.Fatalf("ParseCellID(%q): %v", cellID, err)
	}
	eng := engine.New(engine.Config{TickRate: 20}, nil, nil)
	eng.Perf = engine.NewTickProfile([]string{"S1"})
	eng.Perf.Record([]time.Duration{3 * time.Millisecond}, 7*time.Millisecond)
	cell := &Cell{ID: cellID, Engine: eng}

	host := &Host{
		ID:    hostID,
		Cells: map[CellID]*Cell{parsed: cell},
	}
	return &Coordinator{
		Cells: map[string]*Cell{cellID: cell},
		Hosts: map[string]*Host{hostID: host},
	}
}

func TestPerfSnapshotHandlerReturnsOneRowPerCell(t *testing.T) {
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	reg := cmdsys.NewRegistry()
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd, ok := reg.Lookup("perf.snapshot")
	if !ok {
		t.Fatal("perf.snapshot not registered")
	}
	if cmd.Route != cmdsys.RouteAllHosts {
		t.Errorf("Route = %v, want RouteAllHosts", cmd.Route)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, perfSnapshotArgs{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out, ok := res.(perfSnapshotResult)
	if !ok {
		t.Fatalf("result type = %T, want perfSnapshotResult", res)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(out.Rows))
	}
	if out.Rows[0].CellID != "0_0" || out.Rows[0].HostID != "host-a" {
		t.Errorf("row = %+v", out.Rows[0])
	}
	if out.Rows[0].Tick.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1", out.Rows[0].Tick.SampleCount)
	}
}

func TestPerfSnapshotHandlerFiltersCellID(t *testing.T) {
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	// Add a second cell to host-a.
	parsed, _ := ParseCellID("0_1")
	eng := engine.New(engine.Config{TickRate: 20}, nil, nil)
	eng.Perf = engine.NewTickProfile([]string{"S1"})
	eng.Perf.Record([]time.Duration{2 * time.Millisecond}, 5*time.Millisecond)
	cell := &Cell{ID: "0_1", Engine: eng}
	coord.Cells["0_1"] = cell
	coord.Hosts["host-a"].Cells[parsed] = cell

	reg := cmdsys.NewRegistry()
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd, _ := reg.Lookup("perf.snapshot")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := cmd.Handler(ctx, &cmdsys.Env{}, perfSnapshotArgs{CellID: "0_1"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := res.(perfSnapshotResult)
	if len(out.Rows) != 1 {
		t.Fatalf("filtered rows = %d, want 1", len(out.Rows))
	}
	if out.Rows[0].CellID != "0_1" {
		t.Errorf("filtered snap CellID = %q, want %q", out.Rows[0].CellID, "0_1")
	}
}

func TestPerfResetHandlerClearsTickProfile(t *testing.T) {
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	// Precondition: one sample exists.
	for _, cell := range coord.Cells {
		if cell.Engine.Perf.Stats().SampleCount != 1 {
			t.Fatalf("precondition failed: SampleCount != 1")
		}
	}

	reg := cmdsys.NewRegistry()
	if err := registerPerfResetWorker(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd, _ := reg.Lookup("perf.reset")
	if cmd.Route != cmdsys.RouteAllHosts {
		t.Errorf("Route = %v, want RouteAllHosts", cmd.Route)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := cmd.Handler(ctx, &cmdsys.Env{}, perfResetArgs{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out, ok := res.(perfResetResult)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if out.CellsReset != 1 {
		t.Errorf("CellsReset = %d, want 1", out.CellsReset)
	}
	for _, cell := range coord.Cells {
		if cell.Engine.Perf.Stats().SampleCount != 0 {
			t.Errorf("postcondition failed: SampleCount = %d, want 0",
				cell.Engine.Perf.Stats().SampleCount)
		}
	}
}

func TestPerfResetHandlerFiltersCellID(t *testing.T) {
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	// Add a second cell; the filter should leave its profile untouched.
	parsed, _ := ParseCellID("0_1")
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile([]string{"S1"}),
	}
	eng.Perf.Record([]time.Duration{2 * time.Millisecond}, 5*time.Millisecond)
	cell := &Cell{ID: "0_1", Engine: eng}
	coord.Cells["0_1"] = cell
	coord.Hosts["host-a"].Cells[parsed] = cell

	reg := cmdsys.NewRegistry()
	if err := registerPerfResetWorker(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd, _ := reg.Lookup("perf.reset")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := cmd.Handler(ctx, &cmdsys.Env{}, perfResetArgs{CellID: "0_0"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// 0_0 should be cleared; 0_1 should still have 1 sample.
	if coord.Cells["0_0"].Engine.Perf.Stats().SampleCount != 0 {
		t.Errorf("0_0 not reset")
	}
	if coord.Cells["0_1"].Engine.Perf.Stats().SampleCount != 1 {
		t.Errorf("0_1 reset unexpectedly")
	}
}

// ── frontend helpers ──────────────────────────────────────────────────────────

// addCellToCoord extends a test Coordinator with another cell on the named
// host. The host is created if it doesn't already exist.
func addCellToCoord(t *testing.T, coord *Coordinator, cellID, hostID string) {
	t.Helper()
	parsed, err := ParseCellID(cellID)
	if err != nil {
		t.Fatalf("ParseCellID(%q): %v", cellID, err)
	}
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile([]string{"S1"}),
	}
	eng.Perf.Record([]time.Duration{2 * time.Millisecond}, 5*time.Millisecond)
	cell := &Cell{ID: cellID, Engine: eng}
	coord.Cells[cellID] = cell

	host, ok := coord.Hosts[hostID]
	if !ok {
		host = &Host{ID: hostID, Cells: map[CellID]*Cell{}}
		coord.Hosts[hostID] = host
	}
	host.Cells[parsed] = cell
}

// allLocalResolver resolves every route kind to a single local target.
// Used in perf frontend tests where all cells are colocated in-process.
type allLocalResolver struct{}

func (allLocalResolver) Resolve(_ cmdsys.RouteKind, _ string, _ any) ([]cmdsys.Target, error) {
	return []cmdsys.Target{{Kind: cmdsys.RouteLocal, ID: "local"}}, nil
}

// newTestDispatcher constructs a Dispatcher that routes every command locally.
// All route kinds (RouteLocal, RouteAllHosts, etc.) resolve to in-process
// execution — appropriate for unit tests where all hosts/cells are colocated.
func newTestDispatcher(t *testing.T, reg *cmdsys.Registry, _ *Coordinator) *cmdsys.Dispatcher {
	t.Helper()
	return cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry: reg,
		Resolver: allLocalResolver{},
	})
}

// ── frontend tests ────────────────────────────────────────────────────────────

func TestPerfFrontendFiltersByHostAndCell(t *testing.T) {
	// Two hosts, three cells total.
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	addCellToCoord(t, coord, "0_1", "host-a")
	addCellToCoord(t, coord, "1_0", "host-b")

	reg := cmdsys.NewRegistry()
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		t.Fatalf("register snapshot: %v", err)
	}
	if err := registerPerfResetWorker(reg, coord); err != nil {
		t.Fatalf("register reset: %v", err)
	}
	coord.dispatcher = newTestDispatcher(t, reg, coord)
	if err := registerPerfFrontend(reg, coord); err != nil {
		t.Fatalf("register frontend: %v", err)
	}
	cmd, _ := reg.Lookup("perf")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// No args: aggregate view — should contain all three cell IDs.
	res, err := cmd.Handler(ctx, &cmdsys.Env{Caller: cmdsys.NewOperatorIdentity("test-op")}, perfArgs{})
	if err != nil {
		t.Fatalf("perf no-args: %v", err)
	}
	out := res.(perfResult).Output
	for _, want := range []string{"0_0", "0_1", "1_0"} {
		if !strings.Contains(out, want) {
			t.Errorf("aggregate output missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Host filter — only host-a's two cells.
	res, err = cmd.Handler(ctx, &cmdsys.Env{Caller: cmdsys.NewOperatorIdentity("test-op")}, perfArgs{HostID: "host-a"})
	if err != nil {
		t.Fatalf("perf --host host-a: %v", err)
	}
	out = res.(perfResult).Output
	if !strings.Contains(out, "0_0") || !strings.Contains(out, "0_1") {
		t.Errorf("host-a filter should include 0_0 and 0_1\n--- output ---\n%s", out)
	}
	if strings.Contains(out, "1_0") {
		t.Errorf("host-a filter should not include 1_0\n--- output ---\n%s", out)
	}

	// Cell filter — only 0_0.
	res, err = cmd.Handler(ctx, &cmdsys.Env{Caller: cmdsys.NewOperatorIdentity("test-op")}, perfArgs{CellID: "0_0"})
	if err != nil {
		t.Fatalf("perf --cell 0_0: %v", err)
	}
	out = res.(perfResult).Output
	if !strings.Contains(out, "0_0") {
		t.Errorf("cell filter should include 0_0:\n%s", out)
	}
	if strings.Contains(out, "0_1") || strings.Contains(out, "1_0") {
		t.Errorf("cell filter should exclude other cells:\n%s", out)
	}
}

func TestPerfFrontendRegistersWhenCoordHasNoCells(t *testing.T) {
	coord := &Coordinator{
		Cells: map[string]*Cell{},
		Hosts: map[string]*Host{},
	}
	reg := cmdsys.NewRegistry()
	coord.dispatcher = newTestDispatcher(t, reg, coord)

	if err := registerPerfBuiltins(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Lookup("perf"); !ok {
		t.Error("perf verb missing")
	}
	if _, ok := reg.Lookup("perf.snapshot"); !ok {
		t.Error("perf.snapshot verb missing")
	}
	if _, ok := reg.Lookup("perf.reset"); !ok {
		t.Error("perf.reset verb missing")
	}
}

func TestPerfFrontendResetSub(t *testing.T) {
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	addCellToCoord(t, coord, "0_1", "host-a")
	reg := cmdsys.NewRegistry()
	coord.dispatcher = newTestDispatcher(t, reg, coord)
	if err := registerPerfBuiltins(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd, _ := reg.Lookup("perf")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := cmd.Handler(ctx, &cmdsys.Env{Caller: cmdsys.NewOperatorIdentity("test-op")}, perfArgs{Sub: "reset"})
	if err != nil {
		t.Fatalf("perf reset: %v", err)
	}
	out := res.(perfResult).Output
	if !strings.Contains(out, "reset") {
		t.Errorf("output should mention reset: %s", out)
	}
	for _, cell := range coord.Cells {
		if cell.Engine.Perf.Stats().SampleCount != 0 {
			t.Errorf("profile not reset for cell %s", cell.ID)
		}
	}
}
