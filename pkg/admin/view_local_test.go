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
