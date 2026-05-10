package admin

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/universe"
)

func TestLocalClusterView_Cluster(t *testing.T) {
	t.Parallel()
	p := newTestProcessForView(t)
	v := NewLocalClusterView(p)
	c := v.Cluster()

	if c.Now.IsZero() {
		t.Fatalf("Now is zero")
	}
	if c.HostCount < 1 {
		t.Fatalf("expected >=1 host, got %d", c.HostCount)
	}
	if c.CellCount != 4 {
		t.Fatalf("expected 4 cells in 2x2 fixture, got %d", c.CellCount)
	}
}

func TestLocalClusterView_Cell_NotFound(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	if _, err := v.Cell("does_not_exist"); err != ErrCellNotFound {
		t.Fatalf("expected ErrCellNotFound, got %v", err)
	}
}

func TestLocalClusterView_Hosts(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	hosts := v.Hosts()
	if len(hosts) == 0 {
		t.Fatalf("expected >=1 host, got 0")
	}
	for _, h := range hosts {
		if h.ID == "" {
			t.Fatalf("host missing ID: %+v", h)
		}
		if h.State == "" {
			t.Fatalf("host %s missing State", h.ID)
		}
	}
}

func TestLocalClusterView_Gateways(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	// Default config (Modes unset) resolves to the "all" preset
	// (coordinator+host+gateway), so the embedded in-process gateway is
	// always present in the fixture.
	gws := v.Gateways()
	if len(gws) == 0 {
		t.Fatalf("expected >=1 gateway, got 0")
	}
}

func TestLocalClusterView_Players_EmptyOnFreshFixture(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	if got := v.Players(PlayerFilter{}); len(got) != 0 {
		t.Fatalf("expected 0 players in fresh fixture, got %d", len(got))
	}
}

func TestLocalClusterView_Perf_NotFound(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	_, err := v.Perf("cell_99_99")
	if err != ErrCellNotFound {
		t.Fatalf("expected ErrCellNotFound, got %v", err)
	}
}

func TestLocalClusterView_Perf_HappyPath(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	got, err := v.Perf("cell_0_0")
	if err != nil {
		t.Fatalf("Perf(cell_0_0): %v", err)
	}
	if got.CellID != "cell_0_0" {
		t.Fatalf("CellID = %q, want cell_0_0", got.CellID)
	}
}

// newTestProcessForView spins up a minimal headless coordinator with a 2x2
// grid so view tests can read live state without a full game wiring.
// HTTPPort is set to -1 to keep the listener disabled so parallel tests
// don't fight over :8080.
func newTestProcessForView(t *testing.T) *universe.Process {
	t.Helper()
	cfg := universe.Config{
		Headless: true,
		CellsX:   2,
		CellsY:   2,
		HTTPPort: -1,
	}
	p := universe.New(cfg)
	p.Build()
	t.Cleanup(func() { p.Shutdown() })
	return p
}
