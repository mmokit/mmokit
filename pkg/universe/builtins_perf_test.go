package universe

import (
	"context"
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
