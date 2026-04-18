package universe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// distributedFixture spins up one coord-role *Coordinator (Mode=coordinator,
// ControlListen=127.0.0.1:0) plus N host-role *Coordinators (Mode=host,
// CoordinatorAddr=<coord addr>), connected via real gRPC MeshControl —
// the same wire path production uses. Layout seeding lands in Task 4.
type distributedFixture struct {
	coord *Coordinator
	hosts map[string]*Coordinator // hostID -> host-role *Coordinator
	order []string                // declared host order (for HostIDs())
}

func newDistributedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	coords.SetCellSize(cfg.CellSize)

	// 1. Coord-role process on an ephemeral port.
	coord := NewCoordinator(Config{
		CellsX:        cfg.CellsX,
		CellsY:        cfg.CellsY,
		CellSize:      cfg.CellSize,
		Mode:          "coordinator",
		ControlListen: "127.0.0.1:0",
		Headless:      true,
		ConnManager:   net.NewConnManager(),
		Logger:        logger.New(),
		LoginHandler:  func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	// 2. One host-role process per host ID, each dialing the coord.
	hosts := make(map[string]*Coordinator, len(cfg.HostIDs))
	for _, hid := range cfg.HostIDs {
		host := NewCoordinator(Config{
			CellsX:          cfg.CellsX,
			CellsY:          cfg.CellsY,
			CellSize:        cfg.CellSize,
			Mode:            "host",
			CoordinatorAddr: coordAddr,
			HostID:          hid,
			Headless:        true,
			ConnManager:     net.NewConnManager(),
			Logger:          logger.New(),
			LoginHandler:    func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
		})
		host.SetWorld(func(base *WorldBase) GameWorld { return base })
		host.Build()
		t.Cleanup(host.Shutdown)
		hosts[hid] = host
	}

	// 3. Wait until every host has reached the coord's hostRegistry. The
	// settle loop may still be running; that's fine — Task 4 bypasses it
	// before seeding layout.
	regDeadline := time.Now().Add(3 * time.Second)
	for _, hid := range cfg.HostIDs {
		for {
			if rh := coord.hostRegistry.Get(hid); rh != nil {
				break
			}
			if time.Now().After(regDeadline) {
				t.Fatalf("distributedFixture: host %q failed to register within 3s", hid)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	// 4. Bypass the 5s settle window — our tests seed layout explicitly.
	// Flip settled before dispatching so the settle loop's first rebalance
	// is a no-op rather than a rendezvous placement that would stomp our
	// seeding. firstRegistered is already true (set by onHostRegistered
	// during step 3), so the settle loop has already picked up the clock.
	ae := coord.assignmentEngine
	ae.mu.Lock()
	ae.settled = true
	ae.mu.Unlock()

	// 5. Drive CellAssign for every (cell, host) in the declared layout.
	// dispatchCellAssign sends NetIDRangeGrant + CellAssign over MeshControl;
	// the host executor creates the cell and responds with CellReady,
	// which updates hostRegistry.OwnedCells on the coord side.
	for _, cellKey := range sortedKeys(cfg.Layout) {
		hostID := cfg.Layout[cellKey]
		ae.dispatchCellAssign(hostID, cellKey)
	}

	// 6. Wait for every (cell, host) pair to actually land on the host
	// (not just the registry — dispatchCellAssign marks OwnedCells at
	// dispatch time, which races ahead of the host's async cell creation).
	// Checking the host-side *Cell is the authoritative signal. Deadline is
	// generous; per-cell createNode + CellReady roundtrip is well under
	// 500ms in practice.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer seedCancel()
	for _, cellKey := range sortedKeys(cfg.Layout) {
		hostID := cfg.Layout[cellKey]
		if err := waitForCellOnHost(seedCtx, hosts, hostID, cellKey); err != nil {
			t.Fatalf("distributedFixture: seed %s -> %s: %v", cellKey, hostID, err)
		}
	}

	return &distributedFixture{
		coord: coord,
		hosts: hosts,
		order: append([]string(nil), cfg.HostIDs...),
	}
}

// waitForCellOnHost polls the host-role Coordinator's own Hosts map
// until cellKey is present as an in-process *Cell, or ctx expires.
// Host-side presence is the authoritative "cell is alive here" signal —
// the coord's hostRegistry marks OwnedCells at dispatch time (before
// the CellReady roundtrip), so a registry-only poll would race.
func waitForCellOnHost(ctx context.Context, hosts map[string]*Coordinator, hostID, cellKey string) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if h, ok := hosts[hostID]; ok {
			if lh := h.localHost(); lh != nil && lh.CellByID(cellKey) != nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cell %s not present on host %s before deadline", cellKey, hostID)
		case <-tick.C:
		}
	}
}

func (f *distributedFixture) Coord() *Coordinator { return f.coord }
func (f *distributedFixture) HostIDs() []string   { return f.order }

func (f *distributedFixture) CellOwner(cellKey string) string {
	return f.coord.HostForCellID(cellKey)
}

// HostOwnsCell reaches into the host-role Coordinator's local Host.
// Each host-role *Coordinator has exactly one local Host (itself).
func (f *distributedFixture) HostOwnsCell(hostID, cellKey string) bool {
	return f.CellOn(hostID, cellKey) != nil
}

func (f *distributedFixture) CellOn(hostID, cellKey string) *Cell {
	host, ok := f.hosts[hostID]
	if !ok {
		return nil
	}
	lh := host.localHost()
	if lh == nil {
		return nil
	}
	return lh.CellByID(cellKey)
}

// WaitForCellOwner polls host-side state — not just the coord's registry —
// because dispatchCellAssign updates hostRegistry.OwnedCells at dispatch
// time (before the host has actually created the cell via CellReady).
// Waiting on the host-side *Cell is the authoritative "cell is alive
// here" signal and avoids the race that tripped early migrate tests.
func (f *distributedFixture) WaitForCellOwner(ctx context.Context, cellKey, hostID string) error {
	if err := waitForCellOnHost(ctx, f.hosts, hostID, cellKey); err != nil {
		return fmt.Errorf("distributedFixture: %w", err)
	}
	return nil
}
